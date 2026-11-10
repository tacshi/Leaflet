package mobile

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"leaflet/internal/doc"
)

func TestBlockTypes(t *testing.T) {
	var got blockTypes
	if err := json.Unmarshal([]byte(BlockTypes()), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Text) != len(doc.TextTypes) || !slices.Contains(got.Text, "callout") || slices.Contains(got.Text, "image") {
		t.Errorf("text %v", got.Text)
	}
	if len(got.Nestable) != len(doc.NestableTypes) || !slices.Contains(got.Nestable, "toggle_list") || slices.Contains(got.Nestable, "paragraph") {
		t.Errorf("nestable %v", got.Nestable)
	}
}

func TestEnterAsJSON(t *testing.T) {
	var got struct {
		Action string         `json:"action"`
		Type   string         `json:"type"`
		Data   map[string]any `json:"data"`
		Unfold bool           `json:"unfold"`
	}
	if err := json.Unmarshal([]byte(Enter("todo_list", false, false, false, false, false, false)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != "split" || got.Type != "todo_list" || got.Data["checked"] != false {
		t.Errorf("todo: %+v", got)
	}
	_ = json.Unmarshal([]byte(Enter("heading", true, false, false, false, false, false)), &got)
	if got.Action != "split" || got.Type != "paragraph" || !got.Unfold {
		t.Errorf("folded heading: %+v", got)
	}
	if a := Backspace("paragraph", false, true, true, "toggle_list"); a != "outdent" {
		t.Errorf("Backspace = %s", a)
	}
	if a := Backspace("paragraph", false, false, false, ""); a != "nothing" {
		t.Errorf("Backspace at the page's start = %s", a)
	}
	if a := Delete("paragraph", "image"); a != "selectAfter" {
		t.Errorf("Delete = %s", a)
	}
}

func TestCodeOffsetsAreUTF16(t *testing.T) {
	var enter codeEnter
	_ = json.Unmarshal([]byte(CodeEnter("😀\n\n", 4)), &enter)
	if !enter.Leave || enter.Keep != 2 {
		t.Errorf("CodeEnter: %+v", enter)
	}
	var line codeEnter
	_ = json.Unmarshal([]byte(CodeEnter("  😀x", 5)), &line)
	if line.Leave || line.Insert != "\n  " {
		t.Errorf("CodeEnter in a line: %+v", line)
	}
	var got indented
	if err := json.Unmarshal([]byte(IndentCode("😀a\nb", 0, 5, false)), &got); err != nil {
		t.Fatal(err)
	}
	if got != (indented{Code: "  😀a\n  b", Anchor: 2, Focus: 9}) {
		t.Errorf("IndentCode: %+v", got)
	}
	if IndentCode("a", 0, 1, true) != "" {
		t.Error("nothing to outdent changes nothing")
	}
}

func TestFolded(t *testing.T) {
	blocks := "toggle_list,0,0,1\nparagraph,1,0,0\nheading,0,2,1\nheading,0,3,0\nheading,0,1,0\n"
	if got := Folded(blocks); got != "01010" {
		t.Errorf("Folded = %q", got)
	}
	if Folded("") != "" {
		t.Error("no blocks")
	}
}

func TestLinkTarget(t *testing.T) {
	for href, want := range map[string]string{
		"leaflet://page/abc":       "leaflet://page/abc",
		"leaflet://page/abc#b1":    "leaflet://page/abc#b1",
		" https://example.com ":    "https://example.com",
		"HTTPS://example.com":      "HTTPS://example.com",
		"mailto:me@example.com":    "mailto:me@example.com",
		"leaflet://page/":          "",
		"file:///etc/passwd":       "",
		"shortcuts://run-shortcut": "",
		"javascript:alert(1)":      "",
	} {
		if got := LinkTarget(href); got != want {
			t.Errorf("LinkTarget(%q) = %q, want %q", href, got, want)
		}
	}
}

func TestTableText(t *testing.T) {
	table, _ := doc.TableNode([][]string{{"**A**", "B"}, {"", ""}}, "")
	raw, _ := json.Marshal(table)
	text, err := TableText(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	_ = json.Unmarshal([]byte(text), &rows)
	if !reflect.DeepEqual(rows, [][]string{{"A", "B"}, {"", ""}}) {
		t.Errorf("TableText = %v", rows)
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 26, 20, 0, 0, 0, time.Local)
	var tomorrow, morning due
	_ = json.Unmarshal([]byte(Due("en", "2026-10-27", now.UnixMilli())), &tomorrow)
	if tomorrow != (due{Label: "Tomorrow"}) {
		t.Errorf("tomorrow: %+v", tomorrow)
	}
	_ = json.Unmarshal([]byte(Due("ja", "2026-10-26T09:00", now.UnixMilli())), &morning)
	got := morning
	if got.Label == "" || got.Label == "Today 9:00 AM" || !got.Overdue || !got.Today {
		t.Errorf("this morning: %+v", got)
	}
	if Due("en", "soon", now.UnixMilli()) != "" {
		t.Error("a date not read")
	}
}

func TestPagesOpenLifted(t *testing.T) {
	c, _ := open(t)
	raw, _ := c.CreatePage(`{"title":"Plan"}`)
	var row pageRow
	_ = json.Unmarshal([]byte(raw), &row)
	a := openEditor(t, c, row.ID)
	p := paragraph("p", "Text")
	p.Children = []*doc.Node{paragraph("kid", "Under it")}
	if _, err := c.EditPage(row.ID, a.Editor, doc.FromBlocks([]*doc.Node{p}).Encode(), a.Rev); err != nil {
		t.Fatal(err)
	}
	b := openEditor(t, c, row.ID)
	var ids []string
	for _, n := range b.Content.Children {
		ids = append(ids, n.ID)
	}
	if !reflect.DeepEqual(ids, []string{"p", "kid"}) || len(b.Content.Children[0].Children) != 0 {
		t.Errorf("opened as %v", ids)
	}
}
