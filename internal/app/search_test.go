package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/store"
)

// ⌘P lists matches in MyGo's list: the field's Down moves the choice,
// Enter opens it, and a click opens the row clicked.
func TestSearchList(t *testing.T) {
	a := templatesApp(t)
	var ids []string
	for _, title := range []string{"Plan A", "Plan B", "Plan C"} {
		p, err := a.st.CreatePage(store.NewPageOptions{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	a.reloadAll()
	a.openSearch()
	tt := ui.NewTester(func(c *ui.Context) { a.searchDialog(c) }, 900, 700)
	tt.Frame()
	tt.Type("plan")
	tt.Frame()
	if len(a.dialogs.hits) != 3 || a.dialogs.hitIndex != 0 {
		t.Fatalf("hits %+v, chosen %d", a.dialogs.hits, a.dialogs.hitIndex)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	if a.dialogs.searchOpen || a.route.ID != a.dialogs.hits[1].PageID {
		t.Fatalf("Enter opened %v, search open %v", a.route, a.dialogs.searchOpen)
	}
	a.openSearch()
	tt.Frame()
	tt.Type("plan")
	tt.Frame()
	if err := tt.Click("Plan C"); err != nil {
		t.Fatal(err)
	}
	if a.route.ID != ids[2] {
		t.Errorf("a click opened %v", a.route)
	}
}

// selectBox is MyGo's Select over the options' names: choosing a name
// sets the option it shows.
func TestSelectBox(t *testing.T) {
	value := "en"
	names := map[string]string{"en": "English", "fr": "French"}
	tt := ui.NewTester(func(c *ui.Context) {
		selectBox(c, "lang", &value, []string{"en", "fr"}, func(s string) string { return names[s] }, 200)
	}, 400, 300)
	tt.Frame()
	if err := tt.Click("English"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("French"); err != nil {
		t.Fatalf("%v in %v", err, tt.Texts())
	}
	tt.Frame()
	if value != "fr" {
		t.Errorf("value %q", value)
	}
}
