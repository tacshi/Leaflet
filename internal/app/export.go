package app

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// writeExport writes every page to file, and returns how many pages it
// left out: those this device does not keep and could not download. It
// runs off the main thread, so it is given the folders and files as they
// were when it started.
func (a *App) writeExport(file string, allFolders []store.Folder, allFiles []store.File) (int, error) {
	pages, err := a.st.Pages()
	if err != nil {
		return 0, err
	}
	templates, err := a.st.Templates()
	if err != nil {
		return 0, err
	}
	folders := map[string]string{}
	for _, fo := range allFolders {
		folders[fo.ID] = fo.Name
	}
	// Templates go in a folder of their own.
	const templatesFolder = "\x00templates"
	folders[templatesFolder] = locales.ExportTemplatesFolder()
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
		if a.st.Content(p.ID) != nil {
			continue
		}
		var c *doc.Content
		a.sync.Hold(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c, err = a.sync.RefreshContent(ctx, p.ID)
		})
		if err != nil || c == nil {
			missing++
		}
	}
	f, err := os.Create(file) // #nosec G304 -- the file the user picked to export to.
	if err != nil {
		return 0, err
	}
	localPath, readFile := ownFiles(a.st.Dir, allFiles)
	// Recordings' audio goes in files/ too, from a folder of the export's
	// own that goes once the zip is written.
	audio, err := os.MkdirTemp(a.st.Dir, "export-")
	if err != nil {
		_ = f.Close()
		_ = os.Remove(file)
		return 0, err
	}
	defer func() { _ = os.RemoveAll(audio) }()
	content := func(id string) *doc.Content { return a.withAudio(a.withTranscripts(a.st.Content(id)), audio) }
	err = exportZip(f, pages, folders, content, localPath, readFile)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	// A zip cut short is no export: it goes.
	if err != nil {
		_ = os.Remove(file)
		return 0, err
	}
	return missing, nil
}

// withTranscripts is a page's content to export: a copy, each recording's
// transcript after its block as lines of text, as Insert transcript puts
// them. The block alone names the recording, and its words are the
// recording's, kept apart from the page.
func (a *App) withTranscripts(c *doc.Content) *doc.Content {
	if c == nil {
		return nil
	}
	c = c.Copy()
	var recs []*doc.Node
	c.Walk(func(n, _ *doc.Node) {
		if n.Type == "recording" {
			recs = append(recs, n)
		}
	})
	for _, n := range recs {
		rec, err := a.st.Recording(n.Str("recording_id"))
		if err != nil {
			continue
		}
		var lines []*doc.Node
		for _, s := range recording.Tidy(rec.Transcript) {
			l := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
			l.SetDelta(doc.Delta{{Text: fmtDuration(time.Duration(s.StartMs)*time.Millisecond) + "  ", Attrs: doc.Attrs{Bold: true}}, {Text: s.Text}})
			lines = append(lines, l)
		}
		if len(lines) > 0 {
			_ = c.Insert(doc.Position{After: n.ID}, lines...)
		}
	}
	return c
}

// withAudio is withTranscripts for a zip: each recording's audio follows
// its block as a file, named for the recording, which the zip takes into
// files/. Audio not on this device downloads, into dir; a recording whose
// audio can't come has its block and transcript alone.
func (a *App) withAudio(c *doc.Content, dir string) *doc.Content {
	if c == nil || a.rec == nil {
		return c
	}
	var recs []*doc.Node
	c.Walk(func(n, _ *doc.Node) {
		if n.Type == "recording" {
			recs = append(recs, n)
		}
	})
	for _, n := range recs {
		rec, err := a.st.Recording(n.Str("recording_id"))
		if err != nil {
			continue
		}
		title := n.Str("title")
		if strings.TrimSpace(title) == "" {
			title = locales.ExportRecording()
		}
		name := safeName(title) + ".wav"
		// A folder each, so recordings of one title keep their names.
		path := filepath.Join(dir, rec.ID, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			continue
		}
		if _, err := os.Stat(rec.LocalPath); rec.LocalPath != "" && err == nil {
			// The audio as it is, under the recording's name, without
			// copying it: a link where the folder allows one.
			if os.Link(rec.LocalPath, path) != nil && copyAudio(rec.LocalPath, path) != nil {
				continue
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			data, err := a.rec.Audio(ctx, rec)
			cancel()
			if err != nil || os.WriteFile(path, data, 0o600) != nil {
				continue
			}
		}
		file := &doc.Node{ID: doc.NewBlockID(), Type: "file", Data: map[string]any{"url": "file://" + path, "name": name}}
		_ = c.Insert(doc.Position{After: n.ID}, file)
	}
	return c
}

// copyAudio copies a recording's audio to path.
func copyAudio(from, path string) error {
	in, err := os.Open(from) // #nosec G304 -- a recording's audio in the app's own folder.
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(path) // #nosec G304 -- a file in the export's own folder.
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ownFiles returns where each of files is on this device ("" when it is
// not), and a reader of the app's own files only: those the store keeps,
// or in its data folder dir. A block's link to a file elsewhere, as from
// another device or person, is no reason to read it.
func ownFiles(dir string, files []store.File) (localPath func(fileID string) string, read func(path string) ([]byte, error)) {
	known := map[string]string{}
	owned := map[string]bool{}
	for _, f := range files {
		known[f.ID] = f.LocalPath
		owned[f.LocalPath] = f.LocalPath != ""
	}
	localPath = func(fileID string) string { return known[fileID] }
	read = func(path string) ([]byte, error) {
		if !owned[path] && !inFolder(dir, path) {
			return nil, errNotOwnFile
		}
		return os.ReadFile(path) // #nosec G304 -- a file of the app's own.
	}
	return localPath, read
}

// exportZip writes the pages as Markdown files in a zip: a folder is a
// directory, and so is a page with subpages, which go in it beside the
// page's own file. Pictures kept on this device go in files/, and links to
// them and to subpages are relative, so the zip reads anywhere. localPath
// is where a file is on this device, or "" when it is not.
func exportZip(w io.Writer, pages []store.Page, folders map[string]string, content func(id string) *doc.Content,
	localPath func(fileID string) string, readFile func(string) ([]byte, error)) error {
	byID := map[string]store.Page{}
	for _, p := range pages {
		byID[p.ID] = p
	}
	// paths are each page's path in the zip, without ".md".
	paths := map[string]string{}
	used := map[string]bool{}
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
		} else if name, ok := folders[p.FolderID]; ok {
			dir = safeName(name) + "/"
		}
		base := dir + safeName(p.DisplayTitle())
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		used[strings.ToLower(name)] = true
		paths[id] = name
		return name
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
	// A page's file, or the directory of its subpages, may be in files/
	// too, in a folder of that name: a file there takes another name.
	for _, p := range paths {
		usedFiles[strings.ToLower(p)], usedFiles[strings.ToLower(p)+".md"] = true, true
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
				base := safeName(filepath.Base(local))
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
