package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A client's text is kept fit to sync, in whichever tool it comes: a NUL
// would have the server refuse the page for good.
func TestCleanArgs(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "1.0.0")
	made := session(t, s, call(1, "create_page", map[string]any{"title": "Ti\x00tle", "markdown": "one\x00two\n"}))
	out, failed := result(t, made[1])
	if failed {
		t.Fatalf("create_page: %v", out)
	}
	id, _ := out["page_id"].(string)
	folder := session(t, s, call(1, "create_folder", map[string]any{"name": "Fol\x00der"}))
	if out, failed := result(t, folder[1]); failed {
		t.Fatalf("create_folder: %v", out)
	}
	for name, args := range map[string]map[string]any{
		"write_markdown": {"page_id": id, "position": map[string]any{"at": "end"}, "markdown": "three\x00four"},
		"set_page_tags":  {"page_id": id, "tags": []any{"ta\x00g"}},
	} {
		if out, failed := result(t, session(t, s, call(1, name, args))[1]); failed {
			t.Fatalf("%s: %v", name, out)
		}
	}
	p, err := st.Page(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Ti�tle" || len(p.Tags) != 1 || p.Tags[0] != "ta�g" {
		t.Errorf("title %q, tags %q", p.Title, p.Tags)
	}
	folders, err := st.Folders()
	if err != nil || len(folders) != 1 || folders[0].Name != "Fol�der" {
		t.Errorf("folders %+v, %v", folders, err)
	}
	row, err := st.ContentRow(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{row.Content, row.PlainText} {
		if strings.Contains(text, `\u0000`) || strings.ContainsRune(text, 0) {
			t.Errorf("kept a NUL: %q", text)
		}
	}
	if !strings.Contains(row.PlainText, "one�two") || !strings.Contains(row.PlainText, "three�four") {
		t.Errorf("text %q", row.PlainText)
	}
}

// The command line calls the tools directly, with bytes as they were
// piped in, which no JSON has cleaned on the way.
func TestCleanArgsOfCall(t *testing.T) {
	st := newStore(t)
	s := NewServer(st, "1.0.0")
	out := s.Call("create_page", map[string]any{"title": "Caf\xe9", "markdown": "caf\xe9\n"})
	id, ok := out["page_id"].(string)
	if !ok {
		t.Fatalf("create_page: %v", out)
	}
	p, err := st.Page(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Caf�" {
		t.Errorf("title %q", p.Title)
	}
	row, err := st.ContentRow(id)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(row.PlainText) || !strings.Contains(row.PlainText, "caf�") {
		t.Errorf("text %q", row.PlainText)
	}
}

// Everything but text comes through as it is, nested as it was.
func TestCleanArgKeepsTheRest(t *testing.T) {
	got, _ := cleanArg(map[string]any{"n": 1.5, "b": true, "none": nil, "list": []any{"a\x00", map[string]any{"s": "\xff"}}}).(map[string]any)
	list, _ := got["list"].([]any)
	inner, _ := list[1].(map[string]any)
	if got["n"] != 1.5 || got["b"] != true || got["none"] != nil || list[0] != "a�" || inner["s"] != "�" {
		t.Errorf("cleanArg = %v", got)
	}
	if clean, ok := cleanArg(map[string]any(nil)).(map[string]any); !ok || clean == nil {
		t.Errorf("no arguments = %#v", clean)
	}
}
