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
	kinds := map[string]int{}
	var sub string
	for _, n := range st.Content(welcome).All() {
		kinds[n.Type]++
		if n.Type == "subpage" {
			sub = n.Str("page_id")
		}
	}
	if kinds["embed"] != 3 || kinds["chart"] != 3 || kinds["table"] != 2 || kinds["image"] != 1 || kinds["subpage"] != 1 ||
		kinds["code"] != 1 || kinds["video"] != 1 {
		t.Errorf("blocks %v", kinds)
	}
	if got := strings.Join(headings(st.Content(welcome)), ", "); got != "Try it, Writing, Code, Tables, Images, Charts, Video, Interactive embeds, More blocks, Working with agents, Shortcuts, Subpages, Links between pages" {
		t.Errorf("sections %s", got)
	}
	if p, err := st.Page(sub); err != nil || p.ParentID != welcome {
		t.Errorf("subpage %q: parent %q, %v", sub, p.ParentID, err)
	}
	if got := linkedPage(st.Content(welcome)); got != sub {
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

// linkedPage is the page the guide's last block mentions.
func linkedPage(c *doc.Content) string {
	last := c.Children[len(c.Children)-1]
	for _, r := range last.Delta() {
		if id, ok := doc.PageLinkID(r.Attrs.Href); ok {
			return id
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

func TestUpdateWelcome(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	var blocks []*doc.Node
	for _, s := range []string{
		"Type **/** on an empty line to add a heading, list, table, chart, or embed",
		"Press **Record** to add a recording with its transcript",
		"Connect a model in **Settings › Models**: a cloud provider, or a local one such as Ollama",
		"My own step",
	} {
		n, _ := doc.SpecToNode(map[string]any{"type": "todo_list", "text": s}, "", "Leaflet")
		blocks = append(blocks, n)
	}
	review, _ := doc.SpecToNode(map[string]any{"type": "numbered_list", "text": "Review its edits, and **Undo run** if you don't like them"}, "", "Leaflet")
	blocks = append(blocks, review)
	closing, _ := doc.SpecToNode(map[string]any{"type": "paragraph", "text": "Make a new page with the pencil at the top of the sidebar, and nest pages inside pages from the **+** beside them."}, "", "Leaflet")
	blocks = append(blocks, closing)
	p, err := st.CreatePage(store.NewPageOptions{Title: welcomeTitle, Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("welcome.created", "true"); err != nil {
		t.Fatal(err)
	}
	a := &App{st: st}
	if err := a.updateWelcome(); err != nil {
		t.Fatal(err)
	}
	var got []string
	st.Content(p.ID).Walk(func(n, _ *doc.Node) {
		if n.Type == "todo_list" {
			got = append(got, n.Delta().ToMarkdown())
		}
	})
	want := []string{welcomeSlashTodo, welcomeRecordTodo, welcomeProviderTodo, "My own step"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q", got)
	}
	if n := st.Content(p.ID).Find(review.ID); n == nil || n.Delta().ToMarkdown() != welcomeReviewStep {
		t.Errorf("review step not renamed")
	}
	// The image section comes after the steps, with the picture as a file
	// of the page.
	var image *doc.Node
	st.Content(p.ID).Walk(func(n, _ *doc.Node) {
		if n.Type == "image" {
			image = n
		}
	})
	if image == nil {
		t.Fatal("no image added")
	}
	path := strings.TrimPrefix(image.Str("url"), "file://")
	if data, err := os.ReadFile(path); err != nil || len(data) != len(welcomeImage) { // #nosec G304 -- the image file the store just added.
		t.Errorf("image file %s: %v", path, err)
	}
	// The Subpages section takes the place of the old closing line, and
	// links to its page close the guide.
	if n := st.Content(p.ID).Find(closing.ID); n != nil {
		t.Error("the old closing line stayed")
	}
	if linkedPage(st.Content(p.ID)) == "" {
		t.Error("the guide does not end linking to the subpage")
	}
	// A second run adds no second image or subpage.
	if err := st.SetValue("welcome.version", "2"); err != nil {
		t.Fatal(err)
	}
	if err := a.updateWelcome(); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	st.Content(p.ID).Walk(func(n, _ *doc.Node) { kinds[n.Type]++ })
	if kinds["image"] != 1 || kinds["subpage"] != 1 || kinds["code"] != 1 || kinds["video"] != 1 {
		t.Errorf("blocks after a second run %v", kinds)
	}
	// The new sections go before Subpages, which stays last.
	if got := strings.Join(headings(st.Content(p.ID)), ", "); got != "Code, Images, Video, More blocks, Working with agents, Shortcuts, Subpages, Links between pages" {
		t.Errorf("sections %s", got)
	}
	if pages, _ := st.Pages(); len(pages) != 2 {
		t.Errorf("%d pages after a second run", len(pages))
	}
	if v, _ := st.Value("welcome.version"); v != welcomeVersion {
		t.Errorf("version %q", v)
	}
}

func TestUpdateWelcomeEmbedsTheme(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	embed := func(src string) *doc.Node {
		n, _ := doc.SpecToNode(map[string]any{"type": "embed", "embed": map[string]any{"title": "x", "html": src, "height": 200.0}}, "", "Leaflet")
		return n
	}
	// As version 4 made it, and one the user wrote.
	old := embed(`<html><head><style>html,body{margin:0;background:#fff;color:#16181d;font:14px sans-serif}</style></head></html>`)
	mine := embed(`<p>mine</p>`)
	p, err := st.CreatePage(store.NewPageOptions{Title: welcomeTitle, Content: doc.FromBlocks([]*doc.Node{old, mine})})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("welcome.created", "true"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("welcome.version", "4"); err != nil {
		t.Fatal(err)
	}
	a := &App{st: st}
	if err := a.updateWelcome(); err != nil {
		t.Fatal(err)
	}
	c := st.Content(p.ID)
	if got := c.Find(old.ID).Str("html"); got != savingsEmbed {
		t.Error("the guide's embed kept its light-only HTML")
	}
	if got := c.Find(mine.ID).Str("html"); got != `<p>mine</p>` {
		t.Errorf("the user's embed changed: %q", got)
	}
}

func TestUpdateWelcomeFromVersion9(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	a := &App{st: st}
	// A guide as version 9 made it: the steps, sections and table rows
	// version 10 rewrote, without the table text and the links.
	if err := a.createWelcome(); err != nil {
		t.Fatal(err)
	}
	id, _ := st.Value("welcome.page")
	c := st.Content(id)
	var tableText, links string
	for _, n := range c.All() {
		switch n.Delta().ToMarkdown() {
		case welcomeMentionTodo:
			n.SetDelta(doc.FromMarkdown("Type **@** to ask an agent to write here, or to mention someone"))
		case welcomeTabKeys:
			n.SetDelta(doc.FromMarkdown("Nest or lift a list item, or indent code"))
		case welcomeTableText:
			tableText = n.ID
		}
		if n.Type == "heading" && n.Delta().Plain() == welcomeLinksTitle {
			links = n.ID
		}
	}
	c.Remove(tableText)
	for c.Children[len(c.Children)-1].ID != links {
		c.Remove(c.Children[len(c.Children)-1].ID)
	}
	c.Remove(links)
	if _, _, err := st.SaveContent(id, c); err != nil {
		t.Fatal(err)
	}
	if err := st.SetValue("welcome.version", "9"); err != nil {
		t.Fatal(err)
	}

	for range 2 { // a second run adds nothing more
		if err := a.updateWelcome(); err != nil {
			t.Fatal(err)
		}
		_ = st.SetValue("welcome.version", "9")
	}
	c = st.Content(id)
	var found []string
	for _, n := range c.All() {
		switch md := n.Delta().ToMarkdown(); md {
		case welcomeMentionTodo, welcomeTabKeys, welcomeTableText:
			found = append(found, md)
		}
	}
	if len(found) != 3 {
		t.Errorf("found %q", found)
	}
	for i, n := range c.Children {
		if n.Type == "heading" && n.Delta().Plain() == "Tables" && c.Children[i+1].Delta().ToMarkdown() != welcomeTableText {
			t.Error("the table text is not under Tables")
		}
	}
	if got := strings.Count(strings.Join(headings(c), ","), welcomeLinksTitle); got != 1 {
		t.Errorf("%d link sections", got)
	}
	if linkedPage(c) == "" {
		t.Error("the guide does not end linking to the subpage")
	}
}
