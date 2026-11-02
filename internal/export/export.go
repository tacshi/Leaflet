// Package export writes the user's pages out of Leaflet, as Settings ›
// Account › Export all does on the desktop and the phone alike: every page
// a Markdown file in a zip, with the pictures, files and recordings kept
// on this device.
package export

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

// Source is what an export reads: the store, sync, to download the pages
// this device does not keep, and recordings, for audio not on it. Rec may
// be nil, and the zip then takes no audio.
type Source struct {
	Store *store.Store
	Sync  *syncsvc.Service
	Rec   *recording.Controller
}

// All writes every page, templates in a folder of their own, to file, and
// returns how many pages it left out: those this device does not keep and
// could not download. It runs off the main thread, so it is given the
// folders and files as they were when it started. A zip cut short goes.
func All(file string, src Source, allFolders []store.Folder, allFiles []store.File) (int, error) {
	st := src.Store
	pages, err := st.Pages()
	if err != nil {
		return 0, err
	}
	templates, err := st.Templates()
	if err != nil {
		return 0, err
	}
	// Templates go in a folder of their own.
	const templatesFolder = "\x00templates"
	folders := append(slices.Clip(allFolders), store.Folder{ID: templatesFolder, Name: locales.ExportTemplatesFolder()})
	for _, t := range templates {
		if t.ParentID == "" {
			t.FolderID = templatesFolder
		}
		pages = append(pages, t)
	}
	// A page this device does not keep comes down first, as when it is
	// opened.
	missing := 0
	for _, p := range pages {
		if st.Content(p.ID) != nil {
			continue
		}
		var c *doc.Content
		src.Sync.Hold(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c, err = src.Sync.RefreshContent(ctx, p.ID)
		})
		if err != nil || c == nil {
			missing++
		}
	}
	f, err := os.Create(file) // #nosec G304 -- the file the user picked to export to.
	if err != nil {
		return 0, err
	}
	localPath, readFile := OwnFiles(st.Dir, allFiles)
	// Recordings' audio goes in files/ too, from a folder of the export's
	// own that goes once the zip is written.
	audio, err := os.MkdirTemp(st.Dir, "export-")
	if err != nil {
		_ = f.Close()
		_ = os.Remove(file)
		return 0, err
	}
	defer func() { _ = os.RemoveAll(audio) }()
	content := func(id string) *doc.Content {
		return withAudio(src, WithTranscripts(st, st.Content(id)), audio)
	}
	err = Zip(f, pages, folders, content, localPath, readFile)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(file)
		return 0, err
	}
	return missing, nil
}

// WithTranscripts is a page's content to export: a copy, each recording's
// transcript after its block as lines of text, as Insert transcript puts
// them. The block alone names the recording, and its words are the
// recording's, kept apart from the page.
func WithTranscripts(st *store.Store, c *doc.Content) *doc.Content {
	if c == nil {
		return nil
	}
	c = c.Copy()
	for _, n := range recordings(c) {
		rec, err := st.Recording(n.Str("recording_id"))
		if err != nil {
			continue
		}
		var lines []*doc.Node
		for _, s := range recording.Tidy(rec.Transcript) {
			l := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
			l.SetDelta(doc.Delta{{Text: clock(time.Duration(s.StartMs)*time.Millisecond) + "  ", Attrs: doc.Attrs{Bold: true}}, {Text: s.Text}})
			lines = append(lines, l)
		}
		if len(lines) > 0 {
			_ = c.Insert(doc.Position{After: n.ID}, lines...)
		}
	}
	return c
}

func recordings(c *doc.Content) []*doc.Node {
	var recs []*doc.Node
	c.Walk(func(n, _ *doc.Node) {
		if n.Type == "recording" {
			recs = append(recs, n)
		}
	})
	return recs
}

// clock says a time into a recording as its transcript does: 1:05, or
// 1:02:05.
func clock(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// withAudio is WithTranscripts for a zip: each recording's audio follows
// its block as a file, named for the recording, which the zip takes into
// files/. Audio not on this device downloads, into dir; a recording whose
// audio can't come has its block and transcript alone.
func withAudio(src Source, c *doc.Content, dir string) *doc.Content {
	if c == nil || src.Rec == nil {
		return c
	}
	for _, n := range recordings(c) {
		rec, err := src.Store.Recording(n.Str("recording_id"))
		if err != nil {
			continue
		}
		title := n.Str("title")
		if strings.TrimSpace(title) == "" {
			title = locales.ExportRecording()
		}
		name := SafeName(title) + ".wav"
		// A folder each, so recordings of one title keep their names.
		path := filepath.Join(dir, rec.ID, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			continue
		}
		if putAudio(src, rec, path) != nil {
			continue
		}
		file := &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{"url": "file://" + path, "name": name}}
		_ = c.Insert(doc.Position{After: n.ID}, file)
	}
	return c
}

// putAudio puts rec's audio at path. Another block of the recording, as
// one copied with its page, may have put it there already, and the zip
// then takes it once.
func putAudio(src Source, rec store.Recording, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if _, err := os.Stat(rec.LocalPath); rec.LocalPath != "" && err == nil {
		// The audio as it is, under the recording's name, without
		// copying it: a link where the folder allows one.
		if os.Link(rec.LocalPath, path) == nil {
			return nil
		}
		return copyAudio(rec.LocalPath, path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	data, err := src.Rec.Audio(ctx, rec)
	if err != nil {
		return err
	}
	// Audio cut short goes, or another block would take it as it is.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// copyAudio copies a recording's audio to a new file at path. A file
// there already is left as it is: it may be a link to the recording's own
// audio, which writing over would empty. A copy cut short goes.
func copyAudio(from, path string) error {
	in, err := os.Open(from) // #nosec G304 -- a recording's audio in the app's own folder.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a file in the export's own folder.
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// ErrNotOwnFile is for a file outside the app's data folder.
var ErrNotOwnFile = errors.New("not a file of the app's")

// OwnFiles returns where each of files is on this device ("" when it is
// not), and a reader of the app's own files only: those the store keeps,
// or in its data folder dir. A block's link to a file elsewhere, as from
// another device or person, is no reason to read it.
func OwnFiles(dir string, files []store.File) (localPath func(fileID string) string, read func(path string) ([]byte, error)) {
	known := map[string]string{}
	owned := map[string]bool{}
	for _, f := range files {
		known[f.ID] = f.LocalPath
		owned[f.LocalPath] = f.LocalPath != ""
	}
	localPath = func(fileID string) string { return known[fileID] }
	read = func(path string) ([]byte, error) {
		if !owned[path] && !InFolder(dir, path) {
			return nil, ErrNotOwnFile
		}
		return os.ReadFile(path) // #nosec G304 -- a file of the app's own.
	}
	return localPath, read
}

// InFolder reports whether path is in folder dir, links followed.
func InFolder(dir, path string) bool {
	if dir == "" || !filepath.IsAbs(path) {
		return false
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// SafeName is s as a file's name: no character a file system keeps out,
// short enough for any of them, and never "", "." or ".." or a name
// Windows keeps for a device.
func SafeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsControl(r) {
			return '-'
		}
		return r
	}, s)
	if strings.TrimSpace(s) == "" {
		return locales.CommonUntitled()
	}
	// "." and ".." name the folder itself and the one above it.
	if strings.Trim(s, ".") == "" {
		return strings.ReplaceAll(s, ".", "-")
	}
	// File systems take names of up to 255 bytes; a name here leaves room
	// for the " (2)" and ".md" that may follow it, and keeps its extension.
	const maxName = 200
	if len(s) > maxName {
		// An extension is short, and of letters and digits alone: what
		// follows the last dot of "Notes v2.Next steps" is no extension.
		ext := path.Ext(s)
		if len(ext) > 16 || strings.ContainsFunc(ext[min(len(ext), 1):], func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
		}) {
			ext = ""
		}
		cut := maxName - len(ext)
		for !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + ext
	}
	// Windows drops dots and spaces at a name's end.
	if s = strings.TrimRight(s, ". "); s == "" {
		return locales.CommonUntitled()
	}
	// Nor does it take a device's name, with an extension or without.
	if stem, rest, dot := strings.Cut(s, "."); windowsDevice.MatchString(strings.TrimRight(stem, " ")) {
		s = stem + "-"
		if dot {
			s += "." + rest
		}
	}
	return s
}

// windowsDevice matches the names Windows keeps for its devices.
var windowsDevice = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9¹²³]|lpt[0-9¹²³])$`)

// Zip writes the pages as Markdown files in a zip: a folder is a
// directory, in the directory of the folder it is in, and so is a page
// with subpages, which go in it beside the page's own file. Pictures kept
// on this device go in files/, and links to them and to subpages are
// relative, so the zip reads anywhere. localPath is where a file is on
// this device, or "" when it is not.
func Zip(w io.Writer, pages []store.Page, folders []store.Folder, content func(id string) *doc.Content,
	localPath func(fileID string) string, readFile func(string) ([]byte, error)) error {
	byID := map[string]store.Page{}
	for _, p := range pages {
		byID[p.ID] = p
	}
	// used are the paths taken in the zip, in lower case, as file systems
	// that ignore case take them: a folder's, a page's own and that of
	// the directory of its subpages. One taken already gets a number.
	used := map[string]bool{}
	claim := func(base string) string {
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		used[strings.ToLower(name)] = true
		return name
	}
	// folderPaths are each folder's directory in the zip, each of its
	// names safe on its own: two folders of one name in different folders
	// stay apart, and so do two in the same one.
	folderByID := map[string]store.Folder{}
	for _, fo := range folders {
		folderByID[fo.ID] = fo
	}
	folderPaths := map[string]string{}
	var folderPath func(id string, seen map[string]bool) string
	folderPath = func(id string, seen map[string]bool) string {
		if p, ok := folderPaths[id]; ok {
			return p
		}
		fo := folderByID[id]
		seen[id] = true
		dir := ""
		if _, ok := folderByID[fo.ParentFolderID]; ok && !seen[fo.ParentFolderID] {
			dir = folderPath(fo.ParentFolderID, seen) + "/"
		}
		folderPaths[id] = claim(dir + SafeName(fo.Name))
		return folderPaths[id]
	}
	for _, fo := range folders {
		folderPath(fo.ID, map[string]bool{})
	}
	// paths are each page's path in the zip, without ".md".
	paths := map[string]string{}
	var pathOf func(id string, seen map[string]bool) string
	pathOf = func(id string, seen map[string]bool) string {
		if p, ok := paths[id]; ok {
			return p
		}
		p := byID[id]
		seen[id] = true
		dir := ""
		if _, ok := byID[p.ParentID]; ok && !seen[p.ParentID] {
			dir = pathOf(p.ParentID, seen) + "/"
		} else if dirOf, ok := folderPaths[p.FolderID]; ok {
			dir = dirOf + "/"
		}
		paths[id] = claim(dir + SafeName(p.DisplayTitle()))
		return paths[id]
	}
	for _, p := range pages {
		pathOf(p.ID, map[string]bool{})
	}

	z := zip.NewWriter(w)
	create := func(name string, at time.Time) (io.Writer, error) {
		// Every name stays inside the folder the zip opens into.
		if name != path.Clean(name) || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return nil, fmt.Errorf("export: unsafe name %q", name)
		}
		if at.IsZero() {
			at = time.Now()
		}
		return z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at})
	}
	// files are the pictures written so far, by their URL on this device.
	files := map[string]string{}
	usedFiles := map[string]bool{}
	// A page's file, or the directory of its subpages or of a folder,
	// may be in files/ too, in a folder of that name: a file there takes
	// another name.
	for _, p := range paths {
		usedFiles[strings.ToLower(p)], usedFiles[strings.ToLower(p)+".md"] = true, true
	}
	for _, p := range folderPaths {
		usedFiles[strings.ToLower(p)] = true
	}
	for _, p := range pages {
		c := content(p.ID)
		if c == nil {
			continue
		}
		own := paths[p.ID]
		rel := func(to string) string { return (&url.URL{Path: relPath(path.Dir(own), to)}).String() }
		md := doc.PageMarkdown(p.DisplayTitle(), c, func(id string) string {
			if q, ok := byID[id]; ok {
				return q.DisplayTitle()
			}
			return locales.CommonUntitled()
		})
		var fail error
		c.Walk(func(n, _ *doc.Node) {
			if n.Type != "image" && n.Type != "file" || fail != nil {
				return
			}
			src := n.Str("url")
			local, ok := strings.CutPrefix(src, "file://")
			if !ok {
				return
			}
			// An uploaded file's url is a path on the device that uploaded
			// it: this device keeps its copy elsewhere.
			if p := localPath(n.Str("file_id")); p != "" {
				local = p
			}
			name, done := files[src]
			if !done {
				data, err := readFile(local)
				if err != nil {
					return // gone from this device: the link stays as it was
				}
				base := SafeName(filepath.Base(local))
				name = "files/" + base
				for n := 2; usedFiles[strings.ToLower(name)]; n++ {
					ext := path.Ext(base)
					name = fmt.Sprintf("files/%s (%d)%s", strings.TrimSuffix(base, ext), n, ext)
				}
				usedFiles[strings.ToLower(name)] = true
				fw, err := create(name, p.UpdatedAt)
				if err != nil {
					fail = err
					return
				}
				if _, err := fw.Write(data); err != nil {
					fail = err
					return
				}
				files[src] = name
			}
			md = strings.ReplaceAll(md, "("+doc.LinkDestination(src)+")", "("+rel(name)+")")
		})
		if fail != nil {
			return fail
		}
		// Links to pages, of subpage blocks and page mentions alike, and
		// links to their blocks, go to the pages' files.
		for id, to := range paths {
			md = strings.ReplaceAll(md, "("+doc.PageLink(id)+")", "("+rel(to+".md")+")")
			md = strings.ReplaceAll(md, "("+doc.PageLink(id)+"#", "("+rel(to+".md")+"#")
		}
		fw, err := create(own+".md", p.UpdatedAt)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, md); err != nil {
			return err
		}
	}
	return z.Close()
}

// relPath is the path of file to from directory dir, both in the zip.
func relPath(dir, to string) string {
	var from []string
	if dir != "." {
		from = strings.Split(dir, "/")
	}
	parts := strings.Split(to, "/")
	for len(from) > 0 && len(parts) > 1 && from[0] == parts[0] {
		from, parts = from[1:], parts[1:]
	}
	return strings.Repeat("../", len(from)) + strings.Join(parts, "/")
}
