package app

import (
	"os"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

func TestWelcomePage(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	a := &App{st: st}
	if err := a.createWelcome(); err != nil {
		t.Fatal(err)
	}
	// The guide and the example subpage inside it.
	pages, _ := st.Pages()
	if len(pages) != 2 {
		t.Fatalf("%d pages", len(pages))
	}
	welcome, _ := st.Value("welcome.page")
	c := st.Content(welcome)
	kinds := map[string]int{}
	var sub string
	var image *doc.Node
	for _, n := range c.All() {
		kinds[n.Type]++
		switch n.Type {
		case "subpage":
			sub = n.Str("page_id")
		case "image":
			image = n
		}
	}
	for typ, want := range map[string]int{"embed": 3, "chart": 3, "table": 2, "image": 1, "subpage": 1, "code": 1, "video": 1,
		"callout": 1, "toggle_list": 1, "math_equation": 1, "diagram": 1, "quote": 1} {
		if kinds[typ] != want {
			t.Errorf("%d %s blocks, want %d", kinds[typ], typ, want)
		}
	}
	if got := strings.Join(headings(c), ", "); got != "Try it, Writing, Blocks, Pictures, video and charts, Interactive embeds, To-dos and recordings, Agents, Organizing pages, Working together, Shortcuts" {
		t.Errorf("sections %s", got)
	}
	// The picture is a file of the page.
	path := strings.TrimPrefix(image.Str("url"), "file://")
	if data, err := os.ReadFile(path); err != nil || len(data) != len(welcomeImage) { // #nosec G304 -- the image file the store just added.
		t.Errorf("image file %s: %v", path, err)
	}
	if p, err := st.Page(sub); err != nil || p.ParentID != welcome {
		t.Errorf("subpage %q: parent %q, %v", sub, p.ParentID, err)
	}
	if got := linkedPage(c); got != sub {
		t.Errorf("the guide links to %q, want the subpage %q", got, sub)
	}
	// A second start makes no more pages.
	if err := a.createWelcome(); err != nil {
		t.Fatal(err)
	}
	if pages, _ := st.Pages(); len(pages) != 2 {
		t.Errorf("%d pages after a second start", len(pages))
	}
}

// linkedPage is the page a block of the guide mentions.
func linkedPage(c *doc.Content) string {
	for _, n := range c.All() {
		for _, r := range n.Delta() {
			if id, ok := doc.PageLinkID(r.Attrs.Href); ok {
				return id
			}
		}
	}
	return ""
}

func headings(c *doc.Content) []string {
	var out []string
	for _, n := range c.Children {
		if n.Type == "heading" {
			out = append(out, n.Delta().Plain())
		}
	}
	return out
}
