package mcp

import (
	"bytes"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileThere(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pngData(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pageWith(t *testing.T, st *store.Store, md string) (store.Page, []string) {
	t.Helper()
	nodes := doc.MarkdownBlocks(md)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Quiz", Content: doc.FromBlocks(nodes)})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range st.Content(p.ID).Children {
		ids = append(ids, n.ID)
	}
	return p, ids
}

func liveFiles(t *testing.T, st *store.Store) []store.File {
	t.Helper()
	files, err := st.Files()
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func TestUploadFiles(t *testing.T) {
	st := newStore(t)
	p, paras := pageWith(t, st, "First question.\n\nSecond question.")
	dir := t.TempDir()
	one, two := writeFile(t, dir, "one.png", pngData(t)), writeFile(t, dir, "two.png", pngData(t))
	pdf := writeFile(t, dir, "rules.pdf", []byte("%PDF-1.4\n%%EOF\n"))
	s := NewServer(st, "0.1.0")
	got := session(t, s, initialize, call(2, "upload_files", map[string]any{"page_id": p.ID, "files": []any{
		map[string]any{"path": one, "alt": "The first answer"},
		map[string]any{"path": pdf},
		map[string]any{"path": fileURL(two), "position": map[string]any{"after": paras[0]}},
	}}))
	out, failed := result(t, got[2])
	if failed {
		t.Fatalf("upload_files %v", out)
	}
	ids := toStrings(out["inserted_ids"])
	blocks := st.Content(p.ID).Children
	var order []string
	for _, n := range blocks {
		order = append(order, n.ID)
	}
	if want := []string{paras[0], ids[2], paras[1], ids[0], ids[1]}; !slices.Equal(order, want) {
		t.Fatalf("blocks %v, want %v", order, want)
	}
	files := liveFiles(t, st)
	if len(files) != 3 {
		t.Fatalf("files %+v", files)
	}
	byID := map[string]store.File{}
	for _, f := range files {
		byID[f.ID] = f
		if f.PageID != p.ID || f.LocalPath == "" || f.LocalPath == one || f.LocalPath == two || f.LocalPath == pdf {
			t.Errorf("file %+v is not a copy kept for the page", f)
		}
	}
	first, file, second := blocks[3], blocks[4], blocks[1]
	if f := byID[first.Str("file_id")]; first.Type != "image" || f.Name != "one.png" || first.Str("url") != store.FileURL(f.ID, f.Name) || first.Str("alt") != "The first answer" {
		t.Errorf("first picture %+v", first)
	}
	if second.Type != "image" || second.Str("alt") != "two.png" || byID[second.Str("file_id")].Name != "two.png" {
		t.Errorf("second picture %+v", second)
	}
	if f := byID[file.Str("file_id")]; file.Type != "file" || file.Str("name") != "rules.pdf" || f.MimeType != "application/pdf" {
		t.Errorf("file %+v", file)
	}
	if history, _ := st.History(p.ID); len(history) != 1 || core.HistoryLabel(history[0]) != locales.DialogHistoryBeforeAgent("Claude Code") || strings.Contains(history[0].Content, "file_id") {
		t.Errorf("history %+v", history)
	}

	got = session(t, s, call(3, "read_page", map[string]any{"page_id": p.ID}))
	read, _ := result(t, got[3])
	var url string
	for _, b := range read["blocks"].([]any) {
		if b := b.(map[string]any); b["id"] == ids[0] {
			url = b["image"].(map[string]any)["url"].(string)
		}
	}
	got = session(t, s, call(4, "replace_block", map[string]any{"page_id": p.ID, "id": ids[0],
		"block": map[string]any{"type": "image", "image": map[string]any{"url": url, "alt": "Give way to the right"}}}))
	if out, failed := result(t, got[4]); failed {
		t.Fatalf("replace_block of an uploaded picture %v", out)
	}
	if n := st.Content(p.ID).Find(ids[0]); n.Str("alt") != "Give way to the right" || n.Str("file_id") != first.Str("file_id") || n.Str("url") != url {
		t.Errorf("picture after a new alt %+v", n)
	}
}

func TestUploadRefusals(t *testing.T) {
	st := newStore(t)
	p, _ := pageWith(t, st, "A question.")
	dir := t.TempDir()
	shot := writeFile(t, dir, "shot.png", pngData(t))
	key := writeFile(t, dir, "id_ed25519", []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"))
	env := writeFile(t, dir, ".env", []byte("TOKEN=secret\n"))
	s := NewServer(st, "0.1.0")
	for name, c := range map[string]struct {
		files []any
		want  string
	}{
		"a key":            {[]any{shot, key}, "not a picture"},
		"settings":         {[]any{map[string]any{"path": env}}, "not a picture"},
		"a relative path":  {[]any{"shot.png"}, "not an absolute path"},
		"a folder":         {[]any{dir}, "is a folder"},
		"no file":          {[]any{filepath.Join(dir, "gone.png")}, "No file at"},
		"another machine":  {[]any{"file://server/share/shot.png"}, "not a file on this computer"},
		"no path":          {[]any{map[string]any{"alt": "A picture"}}, "needs its path"},
		"no files":         {[]any{}, "non-empty list"},
		"no block there":   {[]any{shot, map[string]any{"path": shot, "position": map[string]any{"after": "nope"}}}, "files[1].position: No block"},
		"too many at once": {slices.Repeat([]any{shot}, MaxUploads+1), "At most 100 files"},
	} {
		got := session(t, s, call(2, "upload_files", map[string]any{"page_id": p.ID, "files": c.files}))
		out, failed := result(t, got[2])
		if msg, _ := out["error"].(string); !failed || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %v, want an error with %q", name, out, c.want)
		}
	}
	if files := liveFiles(t, st); len(files) != 0 {
		t.Errorf("refused calls kept files %+v", files)
	}
	if n := len(st.Content(p.ID).Children); n != 1 {
		t.Errorf("the page has %d blocks", n)
	}
	if history, _ := st.History(p.ID); len(history) != 0 {
		t.Errorf("refused calls left history %+v", history)
	}
	link := filepath.Join(dir, "disguised.png")
	if err := os.Symlink(key, link); err == nil {
		got := session(t, s, call(2, "upload_files", map[string]any{"page_id": p.ID, "files": []any{link}}))
		out, failed := result(t, got[2])
		if msg, _ := out["error"].(string); !failed || !strings.Contains(msg, "not a picture") {
			t.Errorf("symlink to key: %v", out)
		}
		if files := liveFiles(t, st); len(files) != 0 {
			t.Errorf("symlink refusal kept files %+v", files)
		}
	}
}

func TestFileURLPath(t *testing.T) {
	for _, c := range []struct {
		raw     string
		windows bool
		want    string
		wantErr bool
	}{
		{"file:///tmp/two.png", false, "/tmp/two.png", false},
		{"file://localhost/tmp/two.png", false, "/tmp/two.png", false},
		{"file:///C:/Users/me/My%20Docs/two.png", true, `C:\Users\me\My Docs\two.png`, false},
		{"file://localhost/C:/Users/me/two.png", true, `C:\Users\me\two.png`, false},
		{"file://server/share/two.png", false, "", true},
		{"file://server/share/two.png", true, "", true},
	} {
		got, err := fileURLPath(c.raw, c.windows)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("fileURLPath(%q, win=%v) = (%q, %v), want (%q, err=%v)", c.raw, c.windows, got, err, c.want, c.wantErr)
		}
	}
	if rest, ok := cutHomePrefix(`~\Pictures\a.png`, true); !ok || rest != `Pictures\a.png` {
		t.Errorf("cutHomePrefix windows: (%q, %v)", rest, ok)
	}
	if _, ok := cutHomePrefix(`~\Pictures\a.png`, false); ok {
		t.Error("cutHomePrefix unix matched backslash")
	}
}

func TestUploadIntoAPageOthersHave(t *testing.T) {
	st := newStore(t)
	p, _ := pageWith(t, st, "The team's notes.")
	if err := st.SetMembers(p.ID, []store.Member{{UserID: "sam", Role: "editor"}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	shot := writeFile(t, dir, "shot.png", pngData(t))
	notes := writeFile(t, dir, "notes.txt", []byte("The user's own notes.\n"))
	got := session(t, NewServer(st, "0.1.0"), initialize, call(2, "upload_files", map[string]any{"page_id": p.ID, "files": []any{shot}}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "Other people may have this page") {
		t.Errorf("uploaded into a page others have: %v", out)
	}
	if files := liveFiles(t, st); len(files) != 0 {
		t.Errorf("files %+v", files)
	}
	// The user's own command adds any file, as the app's Upload does.
	out := NewServer(st, "0.1.0").Call("upload_files", map[string]any{"page_id": p.ID, "files": []any{shot, notes}})
	if out["error"] != nil {
		t.Fatalf("the user's own command: %v", out)
	}
	blocks := st.Content(p.ID).Children
	if len(blocks) != 3 || blocks[1].Type != "image" || blocks[2].Type != "file" || blocks[2].Str("name") != "notes.txt" {
		t.Errorf("blocks %+v", blocks)
	}
}

func TestUploadToAPageToView(t *testing.T) {
	st := newStore(t)
	p, err := st.CreatePage(store.NewPageOptions{Title: "Theirs", SpaceID: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembers(p.ID, []store.Member{{PageID: p.ID, UserID: st.Session().UserID, Role: "viewer"}}); err != nil {
		t.Fatal(err)
	}
	shot := writeFile(t, t.TempDir(), "shot.png", pngData(t))
	out := NewServer(st, "0.1.0").Call("upload_files", map[string]any{"page_id": p.ID, "files": []any{shot}})
	if msg, _ := out["error"].(string); !strings.Contains(msg, "to view, not to edit") {
		t.Errorf("uploaded to a page shared to view: %v", out)
	}
	if files := liveFiles(t, st); len(files) != 0 {
		t.Errorf("files %+v", files)
	}
}

func TestUploadCopiesOnceAndTakesOffWhatThePageDidNotKeep(t *testing.T) {
	for _, keeps := range []bool{true, false} {
		st := newStore(t)
		p, _ := pageWith(t, st, "Notes.")
		shot := writeFile(t, t.TempDir(), "shot.png", pngData(t))
		saves := 0
		was := beforeSave
		beforeSave = func(id string) {
			if saves++; keeps && saves > 1 {
				return
			}
			if _, err := st.DB().Exec(`UPDATE page_contents SET content = content || ' ' WHERE page_id = ?`, id); err != nil {
				t.Error(err)
			}
		}
		out := NewServer(st, "0.1.0").Call("upload_files", map[string]any{"page_id": p.ID, "files": []any{shot}})
		beforeSave = was
		files := liveFiles(t, st)
		all, _ := st.AllFiles()
		if keeps {
			blocks := st.Content(p.ID).Children
			if out["error"] != nil || saves != 2 || len(all) != 1 || len(files) != 1 || len(blocks) != 2 || blocks[1].Str("file_id") != files[0].ID {
				t.Errorf("saved again: %v, %d saves, files %+v of %d, blocks %+v", out, saves, files, len(all), blocks)
			}
			continue
		}
		if out["error"] == nil || len(all) != 1 || len(files) != 0 || len(st.Content(p.ID).Children) != 1 {
			t.Errorf("never saved: %v, files %+v of %d", out, files, len(all))
		}
		if copied := filepath.Join(st.Dir, "files", all[0].ID, "shot.png"); fileThere(copied) {
			t.Errorf("the copy at %s is left", copied)
		}
	}
}

func TestLocalPictureIsPointedToUpload(t *testing.T) {
	st := newStore(t)
	p, _ := pageWith(t, st, "Notes.")
	got := session(t, NewServer(st, "0.1.0"), call(2, "insert_blocks", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"},
		"blocks": []any{map[string]any{"type": "image", "image": map[string]any{"url": "/Users/me/shot.png"}}}}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "use upload_files") {
		t.Errorf("a picture on this computer: %v", out)
	}
}

func TestUploadIntoAPagePublicHere(t *testing.T) {
	st := newStore(t)
	p, _ := pageWith(t, st, "Shared by link.")
	if err := st.KeepServedLink(p.ID); err != nil {
		t.Fatal(err)
	}
	shot := writeFile(t, t.TempDir(), "shot.png", pngData(t))
	got := session(t, NewServer(st, "0.1.0"), initialize, call(2, "upload_files", map[string]any{"page_id": p.ID, "files": []any{shot}}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "public link") {
		t.Errorf("uploaded into a page with a public link: %v", out)
	}
	if files := liveFiles(t, st); len(files) != 0 {
		t.Errorf("files %+v", files)
	}
	if err := st.EndServedLink(p.ID); err != nil {
		t.Fatal(err)
	}
	got = session(t, NewServer(st, "0.1.0"), initialize, call(3, "upload_files", map[string]any{"page_id": p.ID, "files": []any{shot}}))
	if out, failed := result(t, got[3]); failed {
		t.Errorf("uploaded once the link stopped: %v", out)
	}
}

func TestMoveIntoAPagePublicHere(t *testing.T) {
	st := newStore(t)
	secret, err := st.CreatePage(store.NewPageOptions{Title: "Secret"})
	if err != nil {
		t.Fatal(err)
	}
	public, err := st.CreatePage(store.NewPageOptions{Title: "Public"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.KeepServedLink(public.ID); err != nil {
		t.Fatal(err)
	}
	got := session(t, NewServer(st, "0.1.0"), initialize, call(2, "move_page", map[string]any{"page_id": secret.ID, "parent_id": public.ID}))
	if out, failed := result(t, got[2]); !failed || !strings.Contains(out["error"].(string), "public link") {
		t.Errorf("moved into a page with a public link: %v", out)
	}
	if p, _ := st.Page(secret.ID); p.ParentID != "" {
		t.Errorf("the page is inside %q", p.ParentID)
	}
	if out := NewServer(st, "0.1.0").Call("move_page", map[string]any{"page_id": secret.ID, "parent_id": public.ID}); out["error"] != nil {
		t.Errorf("the user's own command: %v", out)
	}
}

func TestUploadAndMoveWithLinksElsewhere(t *testing.T) {
	st := newStore(t)
	if _, err := st.LoadSession(&store.User{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("account", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue(syncsvc.OwnMembersKey, "1"); err != nil {
		t.Fatal(err)
	}
	p, _ := pageWith(t, st, "Notes.")
	linked, _ := pageWith(t, st, "Shared by link on another computer.")
	secret, err := st.CreatePage(store.NewPageOptions{Title: "Secret"})
	if err != nil {
		t.Fatal(err)
	}
	shot := writeFile(t, t.TempDir(), "shot.png", pngData(t))
	upload := func(id int, pageID string) (map[string]any, bool) {
		got := session(t, NewServer(st, "0.1.0"), initialize, call(id, "upload_files", map[string]any{"page_id": pageID, "files": []any{shot}}))
		return result(t, got[float64(id)])
	}
	move := func(id int) (map[string]any, bool) {
		got := session(t, NewServer(st, "0.1.0"), initialize, call(id, "move_page", map[string]any{"page_id": secret.ID, "parent_id": linked.ID}))
		return result(t, got[float64(id)])
	}

	// Signed in, with nothing the app read of the other computers' links.
	if out, failed := upload(2, p.ID); !failed || !strings.Contains(out["error"].(string), "cannot tell") {
		t.Errorf("uploaded with the links unknown: %v", out)
	}
	if out := NewServer(st, "0.1.0").Call("upload_files", map[string]any{"page_id": p.ID, "files": []any{shot}}); out["error"] != nil {
		t.Errorf("the user's own command with the links unknown: %v", out)
	}

	if err := st.KeepPublicLinks(map[string]time.Time{linked.ID: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if out, failed := upload(3, p.ID); failed {
		t.Errorf("uploaded to a page with no public link: %v", out)
	}
	if out, failed := upload(4, linked.ID); !failed || !strings.Contains(out["error"].(string), "public link") {
		t.Errorf("uploaded to a page another computer links: %v", out)
	}
	if out, failed := move(5); !failed || !strings.Contains(out["error"].(string), "public link") {
		t.Errorf("moved into a page another computer links: %v", out)
	}
	if err := st.KeepPublicLinks(nil); err != nil {
		t.Fatal(err)
	}
	if out, failed := move(6); failed {
		t.Errorf("moved once the link stopped: %v", out)
	}
}

// On Windows, a path of another computer's share, as page text could name,
// is opened over the network with the user's sign-in: only a drive's paths
// are taken.
func TestOnlyThisComputersDrives(t *testing.T) {
	for path, want := range map[string]bool{
		`C:\Users\me\shot.png`:        true,
		`d:/Pictures/shot.png`:        true,
		`\\host\share\shot.png`:       false,
		`//host/share/shot.png`:       false,
		`\\?\UNC\host\share\shot.png`: false,
		`\\?\C:\Users\me\shot.png`:    false,
		`\\.\pipe\x`:                  false,
		`\??\UNC\host\share\shot.png`: false,
		`C:shot.png`:                  false,
		`\Users\me\shot.png`:          false,
	} {
		if got := onThisComputer(path, true); got != want {
			t.Errorf("onThisComputer(%q) = %v", path, got)
		}
	}
	if p, err := fileURLPath("file:////host/share/shot.png", true); err != nil || onThisComputer(p, true) {
		t.Errorf("file:////host/share/shot.png is %q, %v", p, err)
	}
	if !onThisComputer("//host/share/shot.png", false) {
		t.Error("a path elsewhere than Windows is refused")
	}
}
