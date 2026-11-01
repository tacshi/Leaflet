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
	if err := c.EditPage(page.ID, pg.Editor, doc.FromBlocks([]*doc.Node{paragraph("a", "Hi")}).Encode(), pg.Rev); err != nil {
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
	if !OpensMenu("字") || OpensMenu("a") {
		t.Error("OpensMenu disagrees with the desktop")
	}
}
