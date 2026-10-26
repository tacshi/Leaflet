package app

import (
	"archive/zip"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

func (a *App) writeExport(file string) error {
	pages, err := a.st.Pages()
	if err != nil {
		return err
	}
	f, err := os.Create(file) // #nosec G304 -- the file the user picked to export to.
	if err != nil {
		return err
	}
	folders := map[string]string{}
	for _, fo := range a.folders {
		folders[fo.ID] = fo.Name
	}
	if err := exportZip(f, pages, folders, a.st.Content, os.ReadFile); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// exportZip writes the pages as Markdown files in a zip: a folder is a
// directory, and so is a page with subpages, which go in it beside the
// page's own file. Pictures kept on this device go in files/, and links to
// them and to subpages are relative, so the zip reads anywhere.
func exportZip(w io.Writer, pages []store.Page, folders map[string]string, content func(id string) *doc.Content, readFile func(string) ([]byte, error)) error {
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
		if at.IsZero() {
			at = time.Now()
		}
		return z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at})
	}
	// files are the pictures written so far, by their URL on this device.
	files := map[string]string{}
	usedFiles := map[string]bool{}
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
			return "Untitled"
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
			name, done := files[src]
			if !done {
				data, err := readFile(local)
				if err != nil {
					return // gone from this device: the link stays as it was
				}
				base := path.Base(local)
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
			md = strings.ReplaceAll(md, "("+src+")", "("+rel(name)+")")
		})
		if fail != nil {
			return fail
		}
		c.Walk(func(n, _ *doc.Node) {
			if n.Type != "subpage" {
				return
			}
			id := n.Str("page_id")
			if to, ok := paths[id]; ok {
				md = strings.ReplaceAll(md, "(leaflet://page/"+id+")", "("+rel(to+".md")+")")
			}
		})
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
