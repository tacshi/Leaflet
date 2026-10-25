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
	defer st.Close()
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
	if kinds["embed"] != 3 || kinds["chart"] != 3 || kinds["table"] != 1 || kinds["image"] != 1 || kinds["subpage"] != 1 {
		t.Errorf("blocks %v", kinds)
	}
	if p, err := st.Page(sub); err != nil || p.ParentID != welcome {
		t.Errorf("subpage %q: parent %q, %v", sub, p.ParentID, err)
	}
	// A second start makes no more pages.
	a.createWelcome()
	if pages, _ := st.Pages(); len(pages) != 2 {
		t.Errorf("%d pages after a second start", len(pages))
	}
}

func TestUpdateWelcome(t *testing.T) {
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	var blocks []*doc.Node
	for _, s := range []string{
		"Press **Record** to add a recording with its transcript",
		"Connect a model in **Settings › Models**: a cloud provider, or a local one such as Ollama",
		"My own step",
	} {
		n, _ := doc.SpecToNode(map[string]any{"type": "todo_list", "text": s}, "", "Leaflet")
		blocks = append(blocks, n)
	}
	closing, _ := doc.SpecToNode(map[string]any{"type": "paragraph", "text": "Make a new page with the pencil at the top of the sidebar, and nest pages inside pages from the **+** beside them."}, "", "Leaflet")
	blocks = append(blocks, closing)
	p, err := st.CreatePage(store.NewPageOptions{Title: welcomeTitle, Content: doc.FromBlocks(blocks)})
	if err != nil {
		t.Fatal(err)
	}
	st.SetValue("welcome.created", "true")
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
	want := []string{welcomeRecordTodo, welcomeProviderTodo, "My own step"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q", got)
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
	if data, err := os.ReadFile(path); err != nil || len(data) != len(welcomeImage) {
		t.Errorf("image file %s: %v", path, err)
	}
	// The Subpages section takes the place of the old closing line.
	if n := st.Content(p.ID).Find(closing.ID); n != nil {
		t.Error("the old closing line stayed")
	}
	last := st.Content(p.ID).Children[len(st.Content(p.ID).Children)-1]
	if last.Type != "subpage" {
		t.Errorf("last block %s", last.Type)
	}
	// A second run adds no second image or subpage.
	st.SetValue("welcome.version", "2")
	a.updateWelcome()
	kinds := map[string]int{}
	st.Content(p.ID).Walk(func(n, _ *doc.Node) { kinds[n.Type]++ })
	if kinds["image"] != 1 || kinds["subpage"] != 1 {
		t.Errorf("blocks after a second run %v", kinds)
	}
	if pages, _ := st.Pages(); len(pages) != 2 {
		t.Errorf("%d pages after a second run", len(pages))
	}
	if v, _ := st.Value("welcome.version"); v != welcomeVersion {
		t.Errorf("version %q", v)
	}
}
