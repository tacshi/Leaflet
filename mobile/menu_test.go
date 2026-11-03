package mobile

import (
	"encoding/json"
	"slices"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/menu"
)

func menuItems(t *testing.T, c *Core, pageID, trigger, query string) []string {
	t.Helper()
	raw, err := c.MenuItems(pageID, trigger)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err = FilterMenu(raw, query); err != nil {
		t.Fatal(err)
	}
	var items []menu.Item
	_ = json.Unmarshal([]byte(raw), &items)
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	return keys
}

func TestMenus(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	other := create(t, c, `{"title":"Groceries"}`)

	slash := menuItems(t, c, page.ID, "/", "")
	if !slices.Contains(slash, "callout") || !slices.Contains(slash, "starter:meeting") || !slices.Contains(slash, "embed") {
		t.Errorf("/ offers %v", slash)
	}
	if got := menuItems(t, c, page.ID, "/", "katex"); !slices.Equal(got, []string{"math_equation"}) {
		t.Errorf(`"katex" finds %v`, got)
	}

	mention := menuItems(t, c, page.ID, "@", "")
	if !slices.Contains(mention, "page:"+other.ID) || slices.Contains(mention, "page:"+page.ID) {
		t.Errorf("@ offers %v", mention)
	}

	raw, _ := MenuBlock("todo_list", "")
	var turn menuBlock
	_ = json.Unmarshal([]byte(raw), &turn)
	if turn.Type != "todo_list" || turn.Data["checked"] != false {
		t.Errorf("a to-do turns into %+v", turn)
	}
	if raw, _ := MenuBlock("subpage", ""); raw != "" {
		t.Errorf("a subpage is the app's to make, not %s", raw)
	}
}

func TestInsertTemplate(t *testing.T) {
	c, _ := open(t)
	page := create(t, c, `{"title":"Plan"}`)
	raw, _ := c.OpenPage(page.ID)
	var pg openedPage
	_ = json.Unmarshal([]byte(raw), &pg)
	if _, err := c.EditPage(page.ID, pg.Editor, doc.FromBlocks([]*doc.Node{paragraph("a", "Hi")}).Encode(), pg.Rev); err != nil {
		t.Fatal(err)
	}
	title, err := c.InsertTemplate(page.ID, "a", "starter:meeting")
	if err != nil || title != "" {
		t.Fatalf("inserted %q, %v", title, err)
	}
	got := content(t, c, page.ID)
	if len(got.Children) < 3 || got.Children[0].ID != "a" {
		t.Fatalf("the template's blocks don't follow the paragraph: %s", texts(got))
	}
}

func TestLinkAndEmbedBlocks(t *testing.T) {
	if _, err := LinkBlock("video", "https://example.com/"); err == nil {
		t.Error("a page that is no video made a video")
	}
	if _, err := LinkBlock("image", "file:///etc/hosts"); err == nil {
		t.Error("a file on the device made an image")
	}
	raw, err := LinkBlock("link_preview", " https://go.dev/ ")
	var n doc.Node
	_ = json.Unmarshal([]byte(raw), &n)
	if err != nil || n.Type != "link_preview" || n.Str("url") != "https://go.dev/" {
		t.Errorf("link card %+v, %v", n, err)
	}
	raw, err = EmbedBlock("Counter", EmbedStart(), 200, "Ann")
	n = doc.Node{}
	_ = json.Unmarshal([]byte(raw), &n)
	if err != nil || n.Type != "embed" || n.Int("height", 0) != 200 || n.Str("author") != "Ann" {
		t.Errorf("embed %+v, %v", n.Data, err)
	}
}

func TestShortcuts(t *testing.T) {
	if got := MarkdownShortcut("paragraph", "[x] "); got != `{"type":"todo_list","data":{"checked":true}}` {
		t.Errorf("[x] makes %s", got)
	}
	if got := MarkdownShortcut("heading", "# "); got != "" {
		t.Errorf("a heading's # makes %s", got)
	}
	if got := InlineShortcut("😀 **hi**", "[]"); got != `{"close":7,"end":9,"format":"bold","inner":5,"open":3}` {
		t.Errorf("bold is %s", got)
	}
	if got := InlineShortcut("*a* b*", "[[0,3]]"); got != "" {
		t.Errorf("a pair in a mention made %s", got)
	}
	// The menus open as the desktop's: after a space or Chinese, in full
	// width too, "、" only at a block's start or after a space, and not
	// inside a word.
	for _, c := range []struct{ typed, before, want string }{
		{"/", "", "/"}, {"/", " ", "/"}, {"/", "字", "/"}, {"/", "a", ""},
		{"＠", "字", "@"}, {"／", "", "/"}, {"、", "", "/"}, {"、", " ", "/"}, {"、", "字", ""},
		{"-", "", ""}, {"//", "", ""},
	} {
		if got := Menu(c.typed, c.before); got != c.want {
			t.Errorf("Menu(%q, %q) = %q, want %q", c.typed, c.before, got, c.want)
		}
	}
	if MenuKind("＠") != "@" || MenuKind("、") != "/" || MenuKind("a") != "" {
		t.Error("MenuKind disagrees with the desktop")
	}
}

// Text pasted reads as the desktop's editor reads it: lines as Markdown
// blocks, a link on an empty line as its video or picture, one line that
// is a link as that link, and other text as it is.
func TestPasteText(t *testing.T) {
	read := func(text string, empty bool) pasted {
		t.Helper()
		raw, err := PasteText(text, empty)
		if err != nil {
			t.Fatal(err)
		}
		var p pasted
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := read("- one\r\n- two", false); len(p.Blocks) != 2 || p.Blocks[0].Type != "bulleted_list" || p.Blocks[1].Delta().Plain() != "two" {
		t.Errorf("lines: %+v", p.Blocks)
	}
	if p := read("https://youtu.be/dQw4w9WgXcQ", true); p.Media == nil || p.Media.Type != "video" {
		t.Errorf("a video's link on an empty line: %+v", p)
	}
	if p := read("https://example.com/a.png", true); p.Media == nil || p.Media.Type != "image" {
		t.Errorf("a picture's link on an empty line: %+v", p)
	}
	if p := read("https://example.com/a.png", false); p.Media != nil || p.Link != "https://example.com/a.png" {
		t.Errorf("a picture's link in a line: %+v", p)
	}
	if p := read("just words", true); p.Blocks != nil || p.Media != nil || p.Link != "" {
		t.Errorf("words: %+v", p)
	}
}

// Enter after a fence makes a code block in its language, or a diagram,
// or math; after other text, nothing.
func TestFenceEnter(t *testing.T) {
	read := func(line string) fence {
		t.Helper()
		raw, err := FenceEnter(line)
		if err != nil {
			t.Fatal(err)
		}
		var f fence
		_ = json.Unmarshal([]byte(raw), &f)
		return f
	}
	if f := read("```go"); !f.Code || f.Language != "go" {
		t.Errorf("```go: %+v", f)
	}
	if f := read("```mermaid"); f.Block == nil || f.Block.Type != "diagram" {
		t.Errorf("```mermaid: %+v", f)
	}
	if f := read("$$"); f.Block == nil || f.Block.Type != "math_equation" {
		t.Errorf("$$: %+v", f)
	}
	if f := read("words"); f.Code || f.Block != nil {
		t.Errorf("words: %+v", f)
	}
}
