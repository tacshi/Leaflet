package menu

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"leaflet/internal/store"
	"leaflet/locales"
)

func keys(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

func TestSlashFindsByKeywordAndTemplates(t *testing.T) {
	all := Slash([]Item{{Key: "starter:meeting", Label: "Meeting notes"}})
	if got := keys(Filter(all, "admon")); !slices.Equal(got, []string{"callout"}) {
		t.Errorf(`"admon" finds %v, want the callout`, got)
	}
	if got := keys(Filter(all, "Meet")); !slices.Equal(got, []string{"starter:meeting"}) {
		t.Errorf(`"Meet" finds %v, want the template`, got)
	}
	// Every template, for "template" or the start of it.
	if got := keys(Filter(all, "temp")); !slices.Contains(got, "starter:meeting") {
		t.Errorf(`"temp" finds %v, want the template among them`, got)
	}
	if got := Filter(all, ""); len(got) != len(all) {
		t.Errorf("nothing typed keeps %d of %d items", len(got), len(all))
	}
}

func TestMention(t *testing.T) {
	now := time.Now()
	var pages []store.Page
	for i := range 10 {
		pages = append(pages, store.Page{ID: fmt.Sprint("p", i), Title: fmt.Sprint("Page ", i), UpdatedAt: now.Add(time.Duration(i) * time.Minute)})
	}
	pages = append(pages, store.Page{ID: "t", Title: "Template", Template: true, UpdatedAt: now.Add(time.Hour)})
	m := Mentions{
		PageID: "p9",
		Pages:  pages,
		Members: []store.Member{
			{UserID: "me", DisplayName: "Me", Role: "owner", Accepted: true},
			{UserID: "ann", DisplayName: "Ann", Role: "editor", Accepted: true},
			{UserID: "invite", Email: "bo@example.com", Role: "editor"},
		},
		UserID: "me",
	}
	got := keys(Filter(Mention(m), ""))
	want := []string{"connect", "page:p8", "page:p7", "page:p6", "page:p5", "page:p4", "page:p3", "page:p2", "page:p1", "user:ann"}
	if !slices.Equal(got, want) {
		t.Errorf("@ offers %v, want %v", got, want)
	}
	if got := keys(Filter(Mention(m), "ann")); !slices.Equal(got, []string{"user:ann"}) {
		t.Errorf(`"ann" finds %v, want Ann alone, without the way to add an agent`, got)
	}
	m.Agents = []Agent{{ProviderID: "claude", Name: "Claude", Model: "opus"}}
	if got := keys(Filter(Mention(m), "")); got[0] != "agent:claude" || slices.Contains(got, "connect") {
		t.Errorf("with an agent, @ offers %v", got)
	}
}

// A page the @ menu offers says where it is: in its parent page, else its
// folder, else nothing at the top of the space.
func TestMentionPlace(t *testing.T) {
	m := Mentions{
		Pages: []store.Page{
			{ID: "trip", Title: "Trip", FolderID: "f"},
			{ID: "packing", Title: "Packing", ParentID: "trip", FolderID: "f"},
			{ID: "inbox", Title: "Inbox"},
		},
		Folders: []store.Folder{{ID: "f", Name: "Travel"}},
	}
	places := map[string]string{}
	for _, it := range Mention(m) {
		places[it.Key] = it.Hint
	}
	if places["page:packing"] != "Trip" || places["page:trip"] != "Travel" || places["page:inbox"] != "" {
		t.Errorf("places %v", places)
	}
}

func TestTurnAndBlock(t *testing.T) {
	if typ, level, ok := Turn("h2"); typ != "heading" || level != 2 || !ok {
		t.Errorf("h2 turns into %q level %d (%v)", typ, level, ok)
	}
	if _, _, ok := Turn("chart"); ok {
		t.Error("a chart is no text block to turn into")
	}
	n, ok := Block("chart", "Ann")
	if !ok || n.Type != "chart" || n.Str("author") != "Ann" {
		t.Errorf("chart block is %+v (%v)", n, ok)
	}
	if n, _ := Block("table", ""); n.Int("colsLen", 0) != 3 {
		t.Errorf("table has %d columns, want 3", n.Int("colsLen", 0))
	}
	if _, ok := Block("subpage", ""); ok {
		t.Error("a subpage is made by the app, not as a block here")
	}
}

// What is typed finds the items whose labels start with it first, for
// Enter to take, each group whole; full-width letters find what ASCII
// does.
func TestFilterRanks(t *testing.T) {
	all := Slash(nil)
	for q, want := range map[string]string{"h": "h1", "c": "callout", "chec": "todo_list", "pla": "paragraph", "ｈ２": "h2"} {
		if got := Filter(all, q); len(got) == 0 || got[0].Key != want {
			t.Errorf("%q first finds %v, want %s", q, keys(got), want)
		}
	}
	// The groups stay whole.
	got := Filter(all, "l")
	seen := map[string]bool{}
	for i, it := range got {
		if seen[it.Group] && got[i-1].Group != it.Group {
			t.Fatalf("group %s split: %v", it.Group, keys(got))
		}
		seen[it.Group] = true
	}
}

// A video's share link not followed to its video, as when offline, says
// so, not that it is no video's link; its video's own link makes one.
func TestLinkBlockOfAShareLink(t *testing.T) {
	_, err := LinkBlock("video", "https://b23.tv/xuajGqj")
	if err == nil || err.Error() != locales.DialogLinkNotFollowed() {
		t.Errorf("a share link not followed: %v", err)
	}
	if n, err := LinkBlock("video", " https://www.bilibili.com/video/BV1bY4G6pEEg "); err != nil || n.Str("url") != "https://www.bilibili.com/video/BV1bY4G6pEEg" {
		t.Errorf("the video's own link: %+v, %v", n, err)
	}
}

// Items folded once filter as Filter does, query after query, full-width
// letters too, their labels folded only once a query needs them.
func TestFoldedFiltersAsFilterDoes(t *testing.T) {
	var pages []store.Page
	now := time.Now()
	for i := range 40 {
		pages = append(pages, store.Page{ID: fmt.Sprint("p", i), Title: fmt.Sprint("Meeting ", i, " ＡＢＣ"), UpdatedAt: now.Add(time.Duration(i) * time.Minute)})
	}
	items := Mention(Mentions{Pages: pages, Agents: []Agent{{ProviderID: "a", Name: "Claude"}}})
	f := Fold(items)
	if got := keys(f.Filter("")); !slices.Equal(got, keys(Filter(items, ""))) || f.labels != nil {
		t.Errorf("nothing typed: %v, labels folded %v", got, f.labels != nil)
	}
	for _, q := range []string{"mee", "abc", "ＭＥＥ", "cla", "3", "zzz", " ting "} {
		if got, want := keys(f.Filter(q)), keys(Filter(items, q)); !slices.Equal(got, want) {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
}

// Filtering a long @ menu again folds no label again.
func BenchmarkFoldedMention(b *testing.B) {
	var pages []store.Page
	now := time.Now()
	for i := range 5000 {
		pages = append(pages, store.Page{ID: fmt.Sprintf("p%05d", i), Title: fmt.Sprintf("Meeting notes %d about planning", i), UpdatedAt: now.Add(-time.Duration(i*7919%5000) * time.Minute)})
	}
	f := Fold(Mention(Mentions{Pages: pages, PageID: "p00001"}))
	for b.Loop() {
		_ = f.Filter("pla")
	}
}
