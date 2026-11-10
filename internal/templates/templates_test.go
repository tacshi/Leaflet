package templates

import (
	"slices"
	"testing"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	return st
}

func paragraph(text string) *doc.Node {
	n := &doc.Node{Type: "paragraph"}
	n.SetDelta(doc.FromMarkdown(text))
	return n
}

func template(t *testing.T, st *store.Store, o store.NewPageOptions) store.Page {
	t.Helper()
	o.Template = true
	p, err := st.CreatePage(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func choices(t *testing.T, st *store.Store) ([]Choice, []store.Page) {
	t.Helper()
	all, err := st.Templates()
	if err != nil {
		t.Fatal(err)
	}
	return Choices(all, DeletedStarters(st)), all
}

func keys(choices []Choice) []string {
	var out []string
	for _, c := range choices {
		out = append(out, c.Key())
	}
	return out
}

func TestChoices(t *testing.T) {
	st := openStore(t)
	zine := template(t, st, store.NewPageOptions{Title: "zine"})
	template(t, st, store.NewPageOptions{ParentID: zine.ID, Title: "Issue"})
	meeting := template(t, st, store.NewPageOptions{Title: "Meeting notes"})
	if err := SetStarterDeleted(st, "todo", true); err != nil {
		t.Fatal(err)
	}
	all, _ := choices(t, st)
	want := []string{"template:" + meeting.ID, "template:" + zine.ID, "starter:project", "starter:journal"}
	if got := keys(all); !slices.Equal(got, want) {
		t.Errorf("choices %v, want %v", got, want)
	}
	if c, ok := Find(all, "starter:journal"); !ok || c.Icon() == "" || c.Hint(nil) == "" {
		t.Errorf("journal %+v", c)
	}

	if err := SetStarterDeleted(st, "todo", false); err != nil {
		t.Fatal(err)
	}
	all, _ = choices(t, st)
	if _, ok := Find(all, "starter:todo"); !ok {
		t.Error("the starter brought back is no choice")
	}
}

func TestOwn(t *testing.T) {
	st := openStore(t)
	all, _ := choices(t, st)
	journal, _ := Find(all, "starter:journal")
	p, err := Own(st, journal)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Template || p.Title != journal.Name() || len(st.Content(p.ID).Children) == 0 {
		t.Fatalf("template %+v", p)
	}
	all, _ = choices(t, st)
	if _, ok := Find(all, "starter:journal"); ok {
		t.Error("the starter shows beside the user's own")
	}
	if own, err := Own(st, all[0]); err != nil || own.ID != p.ID {
		t.Errorf("the user's own template is %+v, %v", own, err)
	}
	if _, err := st.DeletePage(p.ID); err != nil {
		t.Fatal(err)
	}
	all, _ = choices(t, st)
	if _, ok := Find(all, "starter:journal"); ok {
		t.Error("deleting the template brought the starter back")
	}
}

func TestHint(t *testing.T) {
	st := openStore(t)
	prd := template(t, st, store.NewPageOptions{Title: "PRD", Content: doc.FromBlocks([]*doc.Node{paragraph(""), paragraph("Problem"), paragraph("Goals")})})
	kit := template(t, st, store.NewPageOptions{Title: "Kit"})
	for _, title := range []string{"FAQ", "Notes"} {
		template(t, st, store.NewPageOptions{ParentID: kit.ID, Title: title})
	}
	empty := template(t, st, store.NewPageOptions{Title: "Empty"})
	_, all := choices(t, st)
	subpages := Subpages(all)
	for _, c := range []struct {
		page store.Page
		want string
	}{
		{prd, "Problem"},
		{kit, locales.TemplateHintSubpages(2)},
		{empty, locales.TemplateHintEmpty()},
	} {
		if got := Hint(st, c.page, subpages); got != c.want {
			t.Errorf("%s: hint %q, want %q", c.page.Title, got, c.want)
		}
	}
}

func TestItems(t *testing.T) {
	st := openStore(t)
	kit := template(t, st, store.NewPageOptions{Title: "Kit"})
	all, _ := choices(t, st)
	hint := func(store.Page) string { return "hint" }
	items := Items(all, "", hint)
	if len(items) != len(all) || items[0].Key != "template:"+kit.ID || items[0].Hint != "hint" || items[0].Icon != "layout-template" {
		t.Fatalf("items %+v", items)
	}
	for _, it := range Items(all, kit.ID, hint) {
		if it.Key == "template:"+kit.ID {
			t.Error("a template offers itself")
		}
	}
}

func TestDefault(t *testing.T) {
	st := openStore(t)
	parent, _ := st.CreatePage(store.NewPageOptions{Title: "Diary"})
	f, err := st.CreateFolder("Meetings")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := choices(t, st)
	if _, ok := Default(st, all, parent.ID, ""); ok {
		t.Error("a default where none is set")
	}

	journal, _ := Find(all, "starter:journal")
	msg, err := SetDefault(st, &journal, "Diary", func(id string) error { return st.SetPageDefaultTemplate(parent.ID, id) })
	if err != nil || msg != locales.TemplateDefaultSet("Diary", "Journal") {
		t.Fatalf("set %q, %v", msg, err)
	}
	all, _ = choices(t, st)
	got, ok := Default(st, all, parent.ID, "")
	if !ok || got.Page == nil || got.Title() != "Journal" {
		t.Fatalf("default %+v", got)
	}
	if _, err := SetDefault(st, &got, "Meetings", func(id string) error { return st.SetFolderDefaultTemplate(f, id) }); err != nil {
		t.Fatal(err)
	}
	if d, ok := Default(st, all, "", f.ID); !ok || d.Key() != got.Key() {
		t.Errorf("the folder's default %+v", d)
	}

	msg, err = SetDefault(st, nil, "Diary", func(id string) error { return st.SetPageDefaultTemplate(parent.ID, id) })
	if err != nil || msg != locales.TemplateDefaultCleared("Diary") {
		t.Fatalf("cleared %q, %v", msg, err)
	}
	if _, ok := Default(st, all, parent.ID, ""); ok {
		t.Error("a default once cleared")
	}

	if _, err := st.DeletePage(got.Page.ID); err != nil {
		t.Fatal(err)
	}
	all, _ = choices(t, st)
	if _, ok := Default(st, all, "", f.ID); ok {
		t.Error("a deleted template is a default")
	}
}

func TestBlocks(t *testing.T) {
	st := openStore(t)
	kit := template(t, st, store.NewPageOptions{Title: "Kit", Content: doc.FromBlocks([]*doc.Node{paragraph("Checklist")})})
	template(t, st, store.NewPageOptions{ParentID: kit.ID, Title: "FAQ"})
	page, _ := st.CreatePage(store.NewPageOptions{})
	all, _ := choices(t, st)

	meeting, _ := Find(all, "starter:meeting")
	if blocks, err := Blocks(st, meeting, page.ID); err != nil || len(blocks) == 0 {
		t.Errorf("the starter gave %d blocks, %v", len(blocks), err)
	}
	blocks, err := Blocks(st, all[0], page.ID)
	if err != nil || len(blocks) != 1 || blocks[0].Delta().Plain() != "Checklist" {
		t.Fatalf("the template gave %v, %v", blocks, err)
	}
	kids, _ := st.Children(page.ID, "")
	if len(kids) != 1 || kids[0].Title != "FAQ" || kids[0].Template {
		t.Errorf("subpages %+v", kids)
	}
}

func TestRetitles(t *testing.T) {
	named := Choice{Page: &store.Page{Title: "PRD"}}
	if !named.Retitles(" ") || named.Retitles("Mine") {
		t.Error("a template's name went to the wrong page")
	}
	if (Choice{Page: &store.Page{}}).Retitles("") {
		t.Error("a template of no name retitles a page")
	}
}
