package mcp

import (
	"errors"
	"fmt"
	"log"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// MaxUploads is how many files one upload_files call adds at most.
const MaxUploads = 100

type upload struct {
	path, alt string
	position  any
}

// uploadFiles adds files on this computer to a page, as the app's Upload
// does, each as its block: a picture for an image, a file card otherwise. A
// file goes after the one before it in the call, or where its position says;
// the first without one at the end of the page. The files are copied once the
// page takes their positions, and taken off again when it does not keep them.
func (t *pageTools) uploadFiles(args map[string]any) (map[string]any, error) {
	st := t.s.st
	p, err := t.page(args, true)
	if err != nil {
		return nil, err
	}
	// An agent may be steered by what it reads, and a file of the user's put
	// in a page other people have, or anyone with its public link, would be
	// theirs to open. Another of the user's computers may serve the link,
	// which the app checks while it runs: without its check, no file goes in.
	if t.app {
		linked, known := st.PublicLink(p.ID)
		switch {
		case agents.OthersHave(st, p) || linked:
			return nil, toolErr("Other people may have this page, as those it is shared with or anyone with its public link: a file added to it would be theirs to open. The user can add it themselves, in the app.")
		case !known:
			return nil, toolErr("Leaflet cannot tell now whether another of the user's computers serves a public link of this page: the app checks while it runs, online. The user can open Leaflet on this computer and ask again, or add the file themselves, in the app.")
		}
	}
	files, err := t.uploads(args["files"])
	if err != nil {
		return nil, err
	}
	var added []store.File
	out, err := t.onPage("upload_files", args, func(x *agents.Executor) map[string]any {
		return agents.ToolAnswer(placeUploads(x, st, p.ID, files, &added))
	})
	if err != nil || out["error"] != nil {
		for _, f := range added {
			if err := st.DeleteFile(f); err != nil {
				log.Printf("mcp: file %s: %v", f.Name, err)
			}
		}
	}
	return out, err
}

// placeUploads puts files in the page x edits, copying them the first time
// it is called: a page that changed while it was saved has it called again,
// with the files copied already.
func placeUploads(x *agents.Executor, st *store.Store, pageID string, files []upload, added *[]store.File) (map[string]any, error) {
	at := make([]*doc.Position, len(files))
	for i, u := range files {
		if u.position == nil {
			continue
		}
		pos, err := x.Position(u.position)
		if te := (*doc.ToolError)(nil); errors.As(err, &te) {
			return nil, toolErr(fmt.Sprintf("files[%d].position: %s", i, te.Message))
		} else if err != nil {
			return nil, err
		}
		at[i] = &pos
	}
	for _, u := range files[len(*added):] {
		f, err := st.AddFile(u.path, "", pageID, mime.TypeByExtension(strings.ToLower(filepath.Ext(u.path))))
		if err != nil {
			return nil, toolErr("Could not copy " + u.path + ": " + err.Error())
		}
		*added = append(*added, f)
	}
	ids := []string{}
	for i, f := range *added {
		n := store.FileBlock(f)
		if alt := strings.TrimSpace(files[i].alt); alt != "" && n.Type == "image" {
			n.Set("alt", alt)
		}
		pos := doc.Position{At: "end"}
		switch {
		case at[i] != nil:
			pos = *at[i]
		case i > 0:
			pos = doc.Position{After: ids[i-1]}
		}
		placed, err := x.Insert(pos, []*doc.Node{n})
		if err != nil {
			return nil, err
		}
		ids = append(ids, placed...)
	}
	return map[string]any{"inserted_ids": ids}, nil
}

// uploads is the files a call names, each a file on this computer. An agent
// outside the app adds pictures, audio, video and PDFs only: not the keys,
// settings and databases it could otherwise copy into a page that syncs.
func (t *pageTools) uploads(raw any) ([]upload, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, toolErr("files must be a non-empty list of files, each with its path.")
	}
	if len(list) > MaxUploads {
		return nil, toolErr(fmt.Sprintf("At most %d files a call.", MaxUploads))
	}
	out := make([]upload, len(list))
	for i, v := range list {
		var u upload
		switch v := v.(type) {
		case string:
			u.path = v
		case map[string]any:
			u.path, _ = v["path"].(string)
			u.alt, _ = v["alt"].(string)
			u.position = v["position"]
		default:
			return nil, toolErr(fmt.Sprintf("files[%d] must be an object with the file's path.", i))
		}
		path, err := localFile(u.path)
		if err != nil {
			return nil, err
		}
		if t.app && !mediaFile(path) {
			return nil, toolErr(filepath.Base(path) + " is not a picture, an audio or video file, or a PDF, the files an agent adds. The user can add it themselves, in the app.")
		}
		u.path = path
		out[i] = u
	}
	return out, nil
}

// localFile is the file at path, an absolute path, one starting with ~/, or a
// file:// URL: a file, not a folder.
func localFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", toolErr("Each file needs its path.")
	}
	if strings.HasPrefix(path, "file://") {
		u, err := url.Parse(path)
		if err != nil || u.Host != "" && u.Host != "localhost" {
			return "", toolErr(path + " is not a file on this computer.")
		}
		path = u.Path
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(path) {
		return "", toolErr(path + " is not an absolute path: give the file's whole path.")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return "", toolErr("No file at " + path + ".")
	case err != nil:
		return "", toolErr("Could not open " + path + ": " + err.Error())
	case info.IsDir():
		return "", toolErr(path + " is a folder: give the files in it, each with its path.")
	case !info.Mode().IsRegular():
		return "", toolErr(path + " is not a file.")
	}
	return path, nil
}

func mediaFile(path string) bool {
	name := filepath.Base(path)
	return doc.ImageExts[strings.ToLower(filepath.Ext(name))] || doc.FileMediaType(name, "") != ""
}
