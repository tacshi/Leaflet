package app

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
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
	// A comment from Leaflet on the paragraph about comments.
	threads, _ := st.Threads(welcome)
	if len(threads) != 1 || threads[0].Root.AuthorKind != "agent" || threads[0].Root.AuthorName != "Leaflet" {
		t.Fatalf("threads %+v", threads)
	}
	if b := c.Find(threads[0].Root.BlockID); b == nil || !strings.HasPrefix(b.Delta().Plain(), "Select text and choose Comment") {
		t.Errorf("the comment is on %v", b)
	}
	// The guide and its meeting carry a tag each, as does the template.
	if p, _ := st.Page(welcome); !slices.Equal(p.Tags, []string{"guide"}) {
		t.Errorf("the guide's tags %q", p.Tags)
	}
	if p, _ := st.Page(sub); !slices.Equal(p.Tags, []string{"meetings"}) {
		t.Errorf("the subpage's tags %q", p.Tags)
	}
	if !strings.Contains(c.PlainText(), "In search, #meetings keeps to the pages tagged meetings") {
		t.Error("the guide does not say how tags work")
	}
	// The subpage's meeting is a template too, which starts at its agenda.
	templates, _ := st.Templates()
	if len(templates) != 1 || templates[0].Title != "Weekly team sync" || !slices.Equal(templates[0].Tags, []string{"meetings"}) {
		t.Fatalf("templates %+v", templates)
	}
	if first := st.Content(templates[0].ID).Children[0]; first.Delta().Plain() != "Agenda" {
		t.Errorf("the template starts with %q", first.Delta().Plain())
	}
	// A second start makes no more pages.
	if err := a.createWelcome(); err != nil {
		t.Fatal(err)
	}
	if pages, _ := st.Pages(); len(pages) != 2 {
		t.Errorf("%d pages after a second start", len(pages))
	}
}

// TestWelcomeTranslated checks the guide and its subpage say every text in
// Chinese and Japanese: the same blocks as in English, none of whose texts
// is left in English but the keys of shortcuts.
func TestWelcomeTranslated(t *testing.T) {
	build := func(m locales.Messages) []*doc.Node {
		blocks, _, err := welcomeBlocks(m, store.File{ID: "image", LocalPath: "/dawn.jpg"}, "sub")
		if err != nil {
			t.Fatal(err)
		}
		return append(doc.FromBlocks(blocks).All(), welcomeSubpage(m).All()...)
	}
	en := build(locales.In("en"))
	for _, lang := range []string{"zh-Hans", "ja"} {
		m := locales.In(lang)
		if m.WelcomeTitle() == locales.In("en").WelcomeTitle() || m.WelcomeImageName() == locales.In("en").WelcomeImageName() {
			t.Errorf("%s: the title or the picture's name is in English", lang)
		}
		got := build(m)
		if len(got) != len(en) {
			t.Fatalf("%s: %d blocks, want %d", lang, len(got), len(en))
		}
		for i, n := range got {
			e := en[i]
			if n.Type != e.Type {
				t.Fatalf("%s: block %d is %s, want %s", lang, i, n.Type, e.Type)
			}
			for _, r := range n.Delta() {
				if strings.Contains(r.Text, "**") {
					t.Errorf("%s: unformatted Markdown in %q", lang, n.Delta().Plain())
				}
			}
			text, english := n.Delta().Plain(), e.Delta().Plain()
			if text != "" && text == english && !strings.ContainsAny(text, "⌘⇧") {
				t.Errorf("%s: %s block %q is in English", lang, n.Type, text)
			}
			// Charts, embeds, the picture and the diagram keep their texts
			// in their data.
			var data, englishData []byte
			for _, k := range []string{"title", "labels", "series", "caption", "html", "alt", "source"} {
				if n.Type == "video" {
					break
				}
				if v, ok := n.Data[k]; ok {
					data, _ = json.Marshal(v)
					englishData, _ = json.Marshal(e.Data[k])
					if string(data) == string(englishData) && string(data) != `""` {
						t.Errorf("%s: the %s of a %s block is in English", lang, k, n.Type)
					}
				}
			}
		}
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
