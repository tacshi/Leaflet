// Package export writes the user's pages out of Leaflet, as Settings ›
// Account › Export all does on the desktop and the phone alike: every page
// a Markdown file in a zip, with the pictures, files and recordings kept
// on this device.
package export

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// Source is what an export reads: the store, sync, to download the pages
// this device does not keep up to date, and recordings, for audio not on
// it. Rec may be nil, and the zip then takes no audio. Kept says which
// pages this device keeps up to date.
type Source struct {
	Store *store.Store
	Sync  Syncer
	Rec   *recording.Controller
	Kept  Keeper
}

// Syncer brings a page's content down as the other devices have it, as
// *sync.Service does.
type Syncer interface {
	// Hold runs fn between sync's passes.
	Hold(fn func())
	// CanSync reports whether there is somewhere to bring content from now.
	CanSync() bool
	// RefreshContent brings a page's content down and returns it.
	RefreshContent(ctx context.Context, pageID string) (*doc.Content, error)
}

// Keeper says which pages this device keeps as the other devices have
// them, up to date as they change elsewhere, as it was when the export
// started: the export runs off the app's main thread, where it is known.
type Keeper struct {
	// SyncOn is there being something to sync with: without it, every
	// page on this device is as it is anywhere.
	SyncOn bool
	// Pulled are the pages sync brings down as they change, as they are
	// open or served by a public link.
	Pulled []string
	// AllOffline keeps every page on this device.
	AllOffline bool
}

// Keeps reports whether this device keeps page p's content up to date: an
// export takes it as it is here, and brings any other page down first.
func (k Keeper) Keeps(p store.Page) bool {
	return !k.SyncOn || slices.Contains(k.Pulled, p.ID) || p.KeepOffline || k.AllOffline
}

// errCantSync is a page an export could not bring down, as there is
// nowhere to bring it from now.
var errCantSync = errors.New("export: nowhere to bring the page from")

// Report is how an export of every page went.
type Report struct {
	// Missing counts the pages left out: those this device does not have
	// and could not download.
	Missing int
	// Stale counts the pages that could not be brought up to date, which
	// went as this device last had them, perhaps without what changed
	// elsewhere since.
	Stale int
}

// All writes every page, templates in a folder of their own, to file, and
// reports the pages it left out and those it could not bring up to date.
// It runs off the main thread, so it is given the folders and files as
// they were when it started. A zip cut short leaves what was at file as
// it was.
func All(file string, src Source, allFolders []store.Folder, allFiles []store.File) (Report, error) {
	var report Report
	st := src.Store
	pages, err := st.Pages()
	if err != nil {
		return report, err
	}
	templates, err := st.Templates()
	if err != nil {
		return report, err
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
	// A page this device does not keep up to date comes down first, as
	// when it is exported alone: the copy here may be one from before it
	// changed elsewhere. One that can't come goes as this device has it.
	fresh := map[string]*doc.Content{}
	for _, p := range pages {
		cached := st.Content(p.ID)
		if cached != nil && src.Kept.Keeps(p) {
			continue
		}
		var c *doc.Content
		err := errCantSync
		if src.Sync.CanSync() {
			src.Sync.Hold(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				c, err = src.Sync.RefreshContent(ctx, p.ID)
			})
		}
		switch {
		case err == nil && c != nil:
			fresh[p.ID] = c
		case cached != nil:
			report.Stale++
		default:
			report.Missing++
		}
	}
	localPath, open := OwnFiles(allFiles)
	// Recordings' audio goes in files/ too, from a folder of the export's
	// own that goes once the zip is written.
	audio, err := os.MkdirTemp(st.Dir, "export-")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = os.RemoveAll(audio) }()
	content := func(id string) *doc.Content {
		c, ok := fresh[id]
		if !ok {
			c = st.Content(id)
		}
		return withAudio(src, id, WithTranscripts(st, id, c), audio)
	}
	openFile := func(path string) (io.ReadCloser, error) {
		if InFolder(audio, path) {
			return openRegular(path)
		}
		return open(path)
	}
	if err := Write(file, func(w io.Writer) error { return Zip(w, pages, folders, content, localPath, openFile) }); err != nil {
		return Report{}, err
	}
	return report, nil
}

// WithTranscripts is page pageID's content c to export: a copy, each of
// its recordings' transcript after its block as lines of text, as Insert
// transcript puts them. The block alone names the recording, and its words
// are the recording's, kept apart from the page. A block of another page's
// recording, as one pasted from it, keeps its words to that page.
func WithTranscripts(st *store.Store, pageID string, c *doc.Content) *doc.Content {
	if c == nil {
		return nil
	}
	c = c.Copy()
	for _, n := range recordings(c) {
		rec, err := st.Recording(n.Str("recording_id"))
		if err != nil || rec.PageID != pageID {
			continue
		}
		if lines := recording.TranscriptBlocks(rec); len(lines) > 0 {
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

// Moments is the Link of a page of content exported alone as Markdown: a
// link to a moment of one of its blocks can't play there, so a video's
// goes to its site at that time, and a recording's or a file's, which have
// no address of their own, is its words alone. Other links stay.
func Moments(c *doc.Content) func(string) string {
	return func(href string) string {
		at, ok := doc.LinkMoment(href)
		if !ok || c == nil {
			return href
		}
		n := c.Find(doc.LinkBlock(href))
		if n == nil {
			return href
		}
		switch n.Type {
		case "video":
			if v, ok := doc.ParseVideoURL(n.Str("url")); ok {
				return v.At(at).WatchURL()
			}
		case "recording", "file":
			return ""
		}
		return href
	}
}

// Save writes what r gives to path, the file the user chose, as Write
// does.
func Save(path string, r io.Reader) error {
	return Write(path, func(w io.Writer) error {
		_, err := io.Copy(w, r)
		return err
	})
}

// Write has write fill the file at path, the file the user chose: a file
// beside it first, which then takes its place, so a write cut short leaves
// what was at path as it was. A file it replaces keeps its permissions; a
// new one has those of any new file the user makes.
func Write(path string, write func(w io.Writer) error) (err error) {
	perm := fs.FileMode(0o666)
	old, statErr := os.Stat(path)
	if statErr == nil {
		perm = old.Mode().Perm()
	}
	f, err := createBeside(path, perm)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// The process's umask may have taken permissions the file replaced
	// had.
	if statErr == nil {
		if err = os.Chmod(f.Name(), perm); err != nil {
			return err
		}
	}
	return os.Rename(f.Name(), path)
}

// createBeside creates a new file of its own in path's folder, with perm
// as the umask leaves it, for Write to fill. Beside path, it takes its
// place in one step.
func createBeside(path string, perm fs.FileMode) (*os.File, error) {
	for {
		name := filepath.Join(filepath.Dir(path), ".leaflet-"+rand.Text())
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm) // #nosec G302 G304 -- a file beside the one the user picked, with its permissions.
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
}

// audioOf names, in a file block withAudio adds, its recording's block.
const audioOf = "audio_of"

// withAudio is WithTranscripts for a zip: each of page pageID's
// recordings' audio follows its block as a file, named for the recording,
// which the zip takes into files/. Audio not on this device downloads,
// into dir; a recording whose audio can't come has its block and
// transcript alone, and so does another page's recording.
func withAudio(src Source, pageID string, c *doc.Content, dir string) *doc.Content {
	if c == nil || src.Rec == nil {
		return c
	}
	for _, n := range recordings(c) {
		rec, err := src.Store.Recording(n.Str("recording_id"))
		if err != nil || rec.PageID != pageID {
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
		file := &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{"url": "file://" + path, "name": name, audioOf: n.ID}}
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
	audio, err := src.Rec.OpenAudio(ctx, rec)
	if err != nil {
		return err
	}
	defer func() { _ = audio.Close() }()
	// Written as it comes, as a long recording is hundreds of megabytes.
	// Audio cut short goes, or another block would take it as it is.
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- a file in the export's own folder.
	if err != nil {
		return err
	}
	_, err = io.Copy(out, audio)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
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

// ErrNotOwnFile is for a file that is not one of the app's own.
var ErrNotOwnFile = errors.New("not a file of the app's")

// OwnFiles returns where each of files is on this device ("" when it is
// not), and an opener of those files alone. A block's link to any other
// file, as from another device or person, is no reason to read it, even
// one in the app's own folder, as its database: it could go out in an
// export or a page shared by link.
func OwnFiles(files []store.File) (localPath func(fileID string) string, open func(path string) (io.ReadCloser, error)) {
	known := map[string]string{}
	owned := map[string]bool{}
	for _, f := range files {
		known[f.ID] = f.LocalPath
		owned[f.LocalPath] = f.LocalPath != ""
	}
	localPath = func(fileID string) string { return known[fileID] }
	open = func(path string) (io.ReadCloser, error) {
		if !owned[path] {
			return nil, ErrNotOwnFile
		}
		return openRegular(path)
	}
	return localPath, open
}

// openRegular opens a file to read, if it is a file and not a folder or
// a device.
func openRegular(path string) (*os.File, error) {
	f, err := os.Open(path) // #nosec G304 -- a file of the app's own, as its callers check.
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrNotOwnFile
	}
	return f, nil
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
// with subpages, which go in it beside the page's own file. Pictures and
// files kept on this device go in files/, and links to them and to pages
// are relative, so the zip reads anywhere. localPath is where a file is
// on this device, or "" when it is not; open opens one there.
func Zip(w io.Writer, pages []store.Page, folders []store.Folder, content func(id string) *doc.Content,
	localPath func(fileID string) string, open func(string) (io.ReadCloser, error)) error {
	byID := map[string]store.Page{}
	for _, p := range pages {
		byID[p.ID] = p
	}
	// used are the paths taken in the zip, in lower case, as file systems
	// that ignore case take them: of files and directories alike, so no
	// file has a directory's name. A folder or a page takes its path, for
	// a directory, and that with ".md", for a page's file, and a picture
	// its own; one taken already gets a number.
	used := map[string]bool{}
	free := func(name string) bool { return !used[strings.ToLower(name)] && !used[strings.ToLower(name)+".md"] }
	claim := func(base string) string {
		name := base
		for n := 2; !free(name); n++ {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		used[strings.ToLower(name)], used[strings.ToLower(name)+".md"] = true, true
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
	// files are the pictures and files written so far, by their URL on
	// this device.
	files := map[string]string{}
	// videos are the exported pages' videos, and media the addresses of
	// the audio that plays their recordings and files, by page and block,
	// which links to moments of them go to.
	videos := map[string]map[string]doc.Video{}
	media := map[string]map[string]string{}
	// All the files go in first: a page may link to a moment of a file of
	// a page after it.
	for _, p := range pages {
		c := content(p.ID)
		if c == nil {
			continue
		}
		videos[p.ID], media[p.ID] = map[string]doc.Video{}, map[string]string{}
		var fail error
		c.Walk(func(n, _ *doc.Node) {
			if v, ok := doc.ParseVideoURL(n.Str("url")); ok && n.Type == "video" {
				videos[p.ID][n.ID] = v
			}
			if n.Type != "image" && n.Type != "file" || fail != nil {
				return
			}
			src := n.Str("url")
			if n.Type == "file" {
				block := n.Str(audioOf)
				if block == "" {
					block = n.ID
				}
				media[p.ID][block] = src
			}
			local, ok := strings.CutPrefix(src, "file://")
			if _, done := files[src]; done || !ok {
				return
			}
			// An uploaded file's url is a path on the device that uploaded
			// it: this device keeps its copy elsewhere.
			if p := localPath(n.Str("file_id")); p != "" {
				local = p
			}
			name, err := putFile(z, open, local, used, p.UpdatedAt)
			switch {
			case errors.Is(err, errNotRead):
				// Gone from this device, or not the app's: the link stays as
				// it was.
			case err != nil:
				fail = err
			default:
				files[src] = name
			}
		})
		if fail != nil {
			return fail
		}
	}
	for _, p := range pages {
		c := content(p.ID)
		if c == nil {
			continue
		}
		own := paths[p.ID]
		rel := func(to string) string { return (&url.URL{Path: relPath(path.Dir(own), to)}).String() }
		// Links to pages, of subpage blocks and page mentions alike, and
		// links to their blocks, go to the pages' files.
		md := doc.PageMarkdown(p.DisplayTitle(), c, doc.MarkdownOptions{
			Lang: i18n.Locale(),
			PageTitle: func(id string) string {
				if q, ok := byID[id]; ok {
					return q.DisplayTitle()
				}
				return locales.CommonUntitled()
			},
			Link: func(href string) string {
				if name, ok := files[href]; ok {
					return rel(name)
				}
				if id, ok := doc.PageLinkID(href); ok && paths[id] != "" {
					// A moment of a video plays on its site, from that time,
					// and of a recording or a file from its file in the zip.
					if at, ok := doc.LinkMoment(href); ok {
						block := doc.LinkBlock(href)
						if v, ok := videos[id][block]; ok {
							return v.At(at).WatchURL()
						}
						if name, ok := files[media[id][block]]; ok {
							return rel(name) + "#t=" + strconv.Itoa(at)
						}
					}
					if block := doc.LinkBlock(href); block != "" {
						return rel(paths[id]+".md") + "#" + block
					}
					return rel(paths[id] + ".md")
				}
				return href
			},
		})
		fw, err := create(z, own+".md", p.UpdatedAt)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, md); err != nil {
			return err
		}
	}
	return z.Close()
}

// errNotRead is a file putFile could not read, which the zip goes
// without.
var errNotRead = errors.New("export: file not read")

// putFile copies a file kept on this device into the zip's files/, under
// its own name or, where that is taken, one with a number, and returns
// the name.
func putFile(z *zip.Writer, open func(string) (io.ReadCloser, error), local string, used map[string]bool, at time.Time) (string, error) {
	in, err := open(local)
	if err != nil {
		return "", errors.Join(errNotRead, err)
	}
	defer func() { _ = in.Close() }()
	base := SafeName(filepath.Base(local))
	name := "files/" + base
	for n := 2; used[strings.ToLower(name)]; n++ {
		ext := path.Ext(base)
		name = fmt.Sprintf("files/%s (%d)%s", strings.TrimSuffix(base, ext), n, ext)
	}
	used[strings.ToLower(name)] = true
	fw, err := create(z, name, at)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, in); err != nil {
		return "", err
	}
	return name, nil
}

// create adds a file to the zip, modified at, or now for a zero time.
// Every name stays inside the folder the zip opens into.
func create(z *zip.Writer, name string, at time.Time) (io.Writer, error) {
	if name != path.Clean(name) || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
		return nil, fmt.Errorf("export: unsafe name %q", name)
	}
	if at.IsZero() {
		at = time.Now()
	}
	return z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at})
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
