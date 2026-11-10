package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	"leaflet/locales"
)

func (a *App) hasSubpageLink(parent, id string) bool {
	c := a.st.Content(parent)
	if ed := a.openEditor(parent); ed != nil {
		c = ed.Content
	}
	if c == nil {
		return false
	}
	_, ok := c.SubpagePlace(id)
	return ok
}

func TestSidebarTree(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	f, err := a.st.CreateFolder("Product")
	if err != nil {
		t.Fatal(err)
	}
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", FolderID: f.ID})
	faq, _ := a.st.CreatePage(store.NewPageOptions{Title: "FAQ", ParentID: prd.ID})
	notes, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	if !tt.HasText("Product") || !tt.HasText("Notes") || tt.HasText("PRD") {
		t.Fatalf("closed folder shows %v", tt.Texts())
	}
	if tt.HasText("My space") {
		t.Errorf("the tree has a My space header: %v", tt.Texts())
	}
	if err := tt.Click("Product"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("PRD") || tt.HasText("FAQ") {
		t.Fatalf("open folder shows %v", tt.Texts())
	}
	clickOn(t, tt, "Notes")
	if a.route.Kind != "page" || a.route.ID != notes.ID {
		t.Errorf("route %+v", a.route)
	}
	a.openPage(faq.ID)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("FAQ") {
		t.Errorf("the open page's row does not show: %v", tt.Texts())
	}
	if a.treeRow < 0 || a.treeState.Item(a.treeRow) != "page:"+faq.ID {
		t.Errorf("chosen row %d", a.treeRow)
	}
}

func TestSidebarDragPages(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	var ids []string
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		p, _ := a.st.CreatePage(store.NewPageOptions{Title: title})
		ids = append(ids, p.ID)
	}
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	order := func() (out []string) {
		for _, p := range a.library().Top {
			out = append(out, p.Title)
		}
		return out
	}
	drag := func(from, onto string, dy float32) {
		t.Helper()
		f, ok1 := tt.Find(from)
		o, ok2 := tt.Find(onto)
		if !ok1 || !ok2 {
			t.Fatalf("no %s or %s in %v", from, onto, tt.Texts())
		}
		x, y := f.X+f.W/2, f.Y+f.H/2
		ty := o.Y + o.H/2 + dy
		tt.Press(x, y)
		for i := 1; i <= 4; i++ {
			tt.Move(x, y+(ty-y)*float32(i)/4)
		}
		tt.Release(x, ty)
		a.reloadAll()
		tt.Frame()
	}
	drag("Gamma", "Alpha", -10)
	if got := fmt.Sprint(order()); got != "[Gamma Alpha Beta]" {
		t.Fatalf("order %s", got)
	}
	drag("Gamma", "Beta", 10)
	if got := fmt.Sprint(order()); got != "[Alpha Beta Gamma]" {
		t.Fatalf("order %s", got)
	}
	drag("Alpha", "Beta", 0)
	if p := a.pageByID(ids[0]); p == nil || p.ParentID != ids[1] {
		t.Fatalf("Alpha is under %q", p.ParentID)
	}
	if got := fmt.Sprint(order()); got != "[Beta Gamma]" {
		t.Fatalf("order %s", got)
	}
	if !a.hasSubpageLink(ids[1], ids[0]) {
		t.Error("Beta does not link to Alpha inside it")
	}
	tt.Frame()
	drag("Beta", "Alpha", 0)
	if p := a.pageByID(ids[1]); p == nil || p.ParentID != "" {
		t.Fatalf("Beta moved under %q", p.ParentID)
	}
	if err := a.pageCore().MovePage(ids[0], "", func() error { return a.st.MovePage(ids[0], "", "", "") }); err != nil {
		t.Fatal(err)
	}
	if a.hasSubpageLink(ids[1], ids[0]) {
		t.Error("Beta still links to Alpha, moved out")
	}
	a.reloadAll()
	tt.Frame()
	clickOn(t, tt, "Gamma")
	if a.route.Kind != "page" || a.route.ID != ids[2] {
		t.Errorf("a click on a draggable row opens %+v", a.route)
	}
}

func TestSidebarDragBesideAPageWhoseParentIsGone(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	alpha, _ := a.st.CreatePage(store.NewPageOptions{Title: "Alpha"})
	parent, _ := a.st.CreatePage(store.NewPageOptions{Title: "Parent"})
	orphan, _ := a.st.CreatePage(store.NewPageOptions{Title: "Orphan", ParentID: parent.ID})
	if _, err := a.st.DB().Exec(`UPDATE pages SET deleted_at = ? WHERE id = ?`, time.Now().UnixMilli(), parent.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	top := func() (out []string) {
		for _, p := range a.library().Top {
			out = append(out, p.Title)
		}
		return out
	}
	if got := top(); len(got) != 2 || !slices.Contains(got, "Orphan") {
		t.Fatalf("top level %v", got)
	}
	drag := func(onto string, dy float32) {
		t.Helper()
		f, ok1 := tt.Find("Alpha")
		o, ok2 := tt.Find(onto)
		if !ok1 || !ok2 {
			t.Fatalf("no Alpha or %s in %v", onto, tt.Texts())
		}
		x, y := f.X+f.W/2, f.Y+f.H/2
		ty := o.Y + o.H/2 + dy
		tt.Press(x, y)
		for i := 1; i <= 4; i++ {
			tt.Move(x, y+(ty-y)*float32(i)/4)
		}
		tt.Release(x, ty)
		a.reloadAll()
		tt.Frame()
	}
	drag("Orphan", -10)
	if p := a.pageByID(alpha.ID); p == nil || p.ParentID != "" || fmt.Sprint(top()) != "[Alpha Orphan]" || len(a.toasts) != 0 {
		t.Fatalf("before: top level %v, toasts %+v", top(), a.toasts)
	}
	drag("Orphan", 10)
	if p := a.pageByID(alpha.ID); p == nil || p.ParentID != "" || fmt.Sprint(top()) != "[Orphan Alpha]" || len(a.toasts) != 0 {
		t.Errorf("after: top level %v, toasts %+v", top(), a.toasts)
	}
	if p := a.pageByID(orphan.ID); p == nil || p.ParentID != parent.ID {
		t.Error("the page beside moved")
	}
}

func TestSidebarDragOut(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	welcome, _ := a.st.CreatePage(store.NewPageOptions{Title: "Welcome"})
	reversi, _ := a.st.CreatePage(store.NewPageOptions{Title: "Reversi", ParentID: welcome.ID})
	sync, _ := a.st.CreatePage(store.NewPageOptions{Title: "Sync", ParentID: welcome.ID})
	a.reloadAll()
	a.treeOpen("page", welcome.ID)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	drag := func(from string, tx, ty float32) {
		t.Helper()
		f, ok := tt.Find(from)
		if !ok {
			t.Fatalf("no %s in %v", from, tt.Texts())
		}
		x, y := f.X+f.W/2, f.Y+f.H/2
		tt.Press(x, y)
		for i := 1; i <= 4; i++ {
			tt.Move(x+(tx-x)*float32(i)/4, y+(ty-y)*float32(i)/4)
		}
		tt.Release(tx, ty)
		a.reloadAll()
		tt.Frame()
	}
	s, _ := tt.Find("Sync")
	drag("Reversi", s.X+12, s.Y+s.H/2+10)
	if p := a.pageByID(reversi.ID); p == nil || p.ParentID != "" {
		t.Fatalf("Reversi is under %q", p.ParentID)
	}
	var top []string
	for _, p := range a.library().Top {
		top = append(top, p.Title)
	}
	if fmt.Sprint(top) != "[Welcome Reversi]" {
		t.Fatalf("top level %v", top)
	}
	drag("Sync", 130, 450)
	if p := a.pageByID(sync.ID); p == nil || p.ParentID != "" {
		t.Fatalf("Sync is under %q", p.ParentID)
	}
	if a.hasSubpageLink(welcome.ID, sync.ID) {
		t.Error("Welcome still links to Sync")
	}
}

func TestMoveOffersWhatADragCould(t *testing.T) {
	a := templatesApp(t)
	mine, _ := a.st.CreatePage(store.NewPageOptions{Title: "Mine"})
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreateFolder("Work"); err != nil {
		t.Fatal(err)
	}
	const theirs, space = "00000000-0000-0000-0000-0000000000bb", "00000000-0000-0000-0000-0000000000cc"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	for _, p := range []map[string]any{
		{"id": theirs, "space_id": space, "title": "Theirs", "sort_key": "V", "created_at": at, "updated_at": at},
		{"id": "00000000-0000-0000-0000-0000000000bd", "space_id": space, "title": "Their notes", "sort_key": "W", "created_at": at, "updated_at": at},
	} {
		if err := a.st.PutRemotePage(p); err != nil {
			t.Fatal(err)
		}
		id := p["id"].(string)
		if err := a.st.SetMembers(id, []store.Member{{PageID: id, UserID: a.session.UserID, Role: "editor"}}); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) { a.moveDialog(c) }, 600, 700)
	offers := func(id string) []string {
		t.Helper()
		a.dialogs.openMove(id)
		tt.Frame()
		var out []string
		for _, s := range []string{locales.DialogMoveTopLevel(), "Work", "Mine", "Other", "Theirs", "Their notes"} {
			if _, ok := tt.Find(s); ok && !strings.Contains(locales.DialogMoveTitle(a.pageTitleOf(id)), s) {
				out = append(out, s)
			}
		}
		return out
	}
	if got := fmt.Sprint(offers(mine.ID)); got != "[Top level Work Other]" {
		t.Errorf("Mine can go to %s", got)
	}
	if got := fmt.Sprint(offers(theirs)); got != "[Their notes]" {
		t.Errorf("Theirs can go to %s", got)
	}
}

func TestBreadcrumbs(t *testing.T) {
	a := templatesApp(t)
	f, _ := a.st.CreateFolder("Product")
	prd, _ := a.st.CreatePage(store.NewPageOptions{Title: "PRD", FolderID: f.ID})
	faq, _ := a.st.CreatePage(store.NewPageOptions{Title: "FAQ", ParentID: prd.ID})
	a.reloadAll()
	a.openPage(faq.ID)
	tt := ui.NewTester(func(c *ui.Context) {
		if p := a.pageByID(a.page.id); p != nil {
			a.breadcrumbs(c, *p)
		}
	}, 600, 60)
	tt.Frame()
	for _, s := range []string{"Product", "PRD", "FAQ"} {
		if !tt.HasText(s) {
			t.Errorf("no %q in %v", s, tt.Texts())
		}
	}
	if err := tt.Click("PRD"); err != nil {
		t.Fatal(err)
	}
	if a.route.ID != prd.ID {
		t.Errorf("route %+v", a.route)
	}
}

func TestSettingsSections(t *testing.T) {
	a := templatesApp(t)
	a.settingsSection = "account"
	tt := ui.NewTester(func(c *ui.Context) { a.settingsNav(c).FillHeight() }, 300, 400)
	tt.Frame()
	if err := tt.Click("Sync and storage"); err != nil {
		t.Fatal(err)
	}
	if a.settingsSection != "offline" {
		t.Errorf("section %q", a.settingsSection)
	}
}

func TestSettingsModal(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen, a.sidebarW = true, 240
	a.route = Route{Kind: "signin"}
	a.router = ui.NewRouter("/signin")
	a.routerAt = a.router.Path()
	tt := ui.NewTester(func(c *ui.Context) { a.view(c) }, 1200, 800)
	tt.Frame()
	if err := tt.Click("Settings (⌘,)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.settingsOpen || a.settingsSection != "account" {
		t.Errorf("the footer's button opened %v at %q", a.settingsOpen, a.settingsSection)
	}
	a.openSettings("providers")
	tt.Frame()
	if !tt.HasText("Agent providers") {
		t.Error("Settings did not open at Agent providers")
	}
	if a.route != (Route{Kind: "signin"}) || a.router.Path() != "/signin" || a.router.CanGoBack() {
		t.Errorf("route %+v, router at %q", a.route, a.router.Path())
	}
	if err := tt.Click("Account"); err != nil {
		t.Fatal(err)
	}
	a.account.deleteOpen = true
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.account.deleteOpen || !a.settingsOpen {
		t.Fatalf("Escape left delete %v, settings %v", a.account.deleteOpen, a.settingsOpen)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.settingsOpen {
		t.Error("Escape did not close Settings")
	}
	if a.route != (Route{Kind: "signin"}) {
		t.Errorf("closing Settings went to %+v", a.route)
	}
}

func TestRouterHistory(t *testing.T) {
	a := templatesApp(t)
	one, _ := a.st.CreatePage(store.NewPageOptions{Title: "One"})
	two, _ := a.st.CreatePage(store.NewPageOptions{Title: "Two"})
	a.reloadAll()
	a.route = Route{Kind: "home"}
	a.router = ui.NewRouter("/")
	a.routerAt = a.router.Path()
	a.navigate(Route{Kind: "page", ID: one.ID})
	a.navigate(Route{Kind: "page", ID: two.ID})
	if a.router.Path() != "/page/"+two.ID || !a.router.CanGoBack() {
		t.Fatalf("router at %q", a.router.Path())
	}
	a.back()
	if a.route.ID != one.ID || a.page == nil || a.page.id != one.ID {
		t.Fatalf("Back went to %+v", a.route)
	}
	a.forward()
	if a.route.ID != two.ID {
		t.Fatalf("Forward went to %+v", a.route)
	}
	for _, r := range []Route{{Kind: "home"}, {Kind: "page", ID: "x"}, {Kind: "signin"}} {
		if got := routeAt(r.path()); got != r {
			t.Errorf("%+v went to %q and back to %+v", r, r.path(), got)
		}
	}
}

func TestSidebarTabs(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	plan, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan", Tags: []string{"work"}})
	_, _ = a.st.CreatePage(store.NewPageOptions{Title: "Standup", Tags: []string{"Work", "daily"}})
	_, _ = a.st.CreatePage(store.NewPageOptions{Title: "Untagged"})
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	if err := tt.Click("New folder"); err != nil {
		t.Fatal(err)
	}
	if !a.dialogs.folderOpen {
		t.Error("New folder opened no dialog")
	}
	a.dialogs.folderOpen = false
	if err := tt.Click("Tags"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settings.SidebarTab != tabTags {
		t.Fatalf("tab %d", a.settings.SidebarTab)
	}
	if !tt.HasText("work") || !tt.HasText("daily") || tt.HasText("Untagged") || tt.HasText("Plan") {
		t.Fatalf("Tags tab shows %v", tt.Texts())
	}
	rightClickOn(t, tt, "daily")
	tt.Frame()
	if !tt.HasText("Rename…") || !tt.HasText("Delete tag") {
		t.Fatalf("a tag's menu shows %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if err := tt.Click("work"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Plan") || !tt.HasText("Standup") {
		t.Fatalf("an open tag shows %v", tt.Texts())
	}
	if err := tt.Click("Plan"); err != nil {
		t.Fatal(err)
	}
	if a.route.Kind != "page" || a.route.ID != plan.ID {
		t.Errorf("route %+v", a.route)
	}
	if err := tt.Click("Pages"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settings.SidebarTab != tabPages || !tt.HasText("Untagged") {
		t.Errorf("Pages tab %d shows %v", a.settings.SidebarTab, tt.Texts())
	}
}

func TestRenameAndDeleteTag(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan", Tags: []string{"work", "q4"}})
	tpl, _ := a.st.CreatePage(store.NewPageOptions{Title: "Review", Template: true, Tags: []string{"q4", "keep"}})
	a.reloadAll()
	a.dialogs.openRenameTag("work")
	tt := ui.NewTester(func(c *ui.Context) { a.tagDialog(c) }, 600, 400)
	tt.Frame()
	tt.Frame()
	tt.Type("job")
	tt.Key(0, ui.KeyEnter)
	if got, _ := a.st.Page(p.ID); !slices.Equal(got.Tags, []string{"job", "q4"}) || a.dialogs.tagOpen {
		t.Fatalf("renamed to %q, dialog open %v", got.Tags, a.dialogs.tagOpen)
	}
	a.reloadAll()
	a.deleteTag("q4")
	if got, _ := a.st.Page(p.ID); !slices.Equal(got.Tags, []string{"job"}) {
		t.Fatalf("after delete %q", got.Tags)
	}
	if got, _ := a.st.Page(tpl.ID); !slices.Equal(got.Tags, []string{"keep"}) {
		t.Fatalf("the template after delete %q", got.Tags)
	}
	a.toasts[len(a.toasts)-1].fn()
	if got, _ := a.st.Page(p.ID); !slices.Equal(got.Tags, []string{"job", "q4"}) {
		t.Errorf("after undo %q", got.Tags)
	}
	if got, _ := a.st.Page(tpl.ID); !slices.Equal(got.Tags, []string{"q4", "keep"}) {
		t.Errorf("the template after undo %q", got.Tags)
	}
}

func TestDeleteFileAsksFirst(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	f, err := a.st.CreateFolder("Docs")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "budget.txt")
	if err := os.WriteFile(src, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.AddFile(src, f.ID, "", "text/plain"); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.treeOpen("folder", f.ID)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 900, 600)
	tt.Frame()
	ask := func() {
		t.Helper()
		rightClickOn(t, tt, "budget.txt")
		tt.Frame()
		if err := tt.Click(locales.SidebarDeleteFile()); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !tt.HasText(locales.SidebarDeleteFileTitle("budget.txt")) {
			t.Fatalf("no alert in %v", tt.Texts())
		}
	}
	ask()
	if err := tt.Click(locales.CommonCancel()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if files, _ := a.st.Files(); len(files) != 1 || a.fileToDelete != nil {
		t.Fatalf("after Cancel, %d files, asking %v", len(files), a.fileToDelete)
	}
	ask()
	if err := tt.Click(locales.CommonDelete()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if files, _ := a.st.Files(); len(files) != 0 {
		t.Errorf("after Delete, %d files", len(files))
	}
}

func TestFolderNotMadeSaysSo(t *testing.T) {
	a := templatesApp(t)
	if err := a.st.Close(); err != nil {
		t.Fatal(err)
	}
	a.dialogs.openRenameFolder(nil)
	a.dialogs.folderName = "Product"
	tt := ui.NewTester(a.folderDialog, 600, 400)
	tt.Frame()
	if err := tt.Click(locales.DialogFolderCreate()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.toasts) != 1 || !strings.HasPrefix(a.toasts[0].text, strings.TrimSuffix(locales.DialogFolderCreateFailed(""), " ")) {
		t.Errorf("toasts %+v", a.toasts)
	}
}

func TestOpenFileNotHereDownloadsIt(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	f, err := a.st.CreateFolder("Docs")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.st.PutFile(store.File{ID: "00000000-0000-0000-0000-0000000000ef", FolderID: f.ID, Name: "budget.txt",
		StoragePath: "u1/budget.txt", MimeType: "text/plain", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.treeOpen("folder", f.ID)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 900, 600)
	tt.Frame()
	rightClickOn(t, tt, "budget.txt")
	tt.Frame()
	if err := tt.Click(locales.CommonOpen()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.toasts) != 1 || a.toasts[0].text != locales.HostDownloading("budget.txt") {
		t.Errorf("toasts %+v", a.toasts)
	}
}

func TestSidebarFooter(t *testing.T) {
	a := templatesApp(t)
	a.backend = remote.SignedInForTest("http://127.0.0.1:1", a.st, "u1")
	a.settings.SidebarOpen, a.updatable = true, true
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	if _, ok := tt.Find(locales.CommonSignIn()); ok {
		t.Errorf("a Sign in button beside the account row: %v", tt.Texts())
	}
	if _, ok := tt.Find(locales.SidebarCheckForUpdates()); !ok {
		t.Error("no icon to check for updates")
	}
	a.updateBusy = locales.SidebarCheckingForUpdates()
	tt.Frame()
	if _, ok := tt.Find(locales.SidebarCheckingForUpdates()); !ok {
		t.Error("nothing shows the check under way")
	}
	if _, ok := tt.Find(locales.SidebarRestartToUpdate("2.0.0")); ok {
		t.Error("an update shows before it is installed")
	}
	if err := tt.Click(locales.SidebarSignInToSync()); err != nil {
		t.Fatal(err)
	}
	if a.route.Kind != "signin" {
		t.Errorf("the account row went to %+v", a.route)
	}
	a.updateBusy, a.updateReady = "", "2.0.0"
	tt.Frame()
	if _, ok := tt.Find(locales.SidebarRestartToUpdate("2.0.0")); !ok {
		t.Error("no icon for the update installed")
	}
	a.session.SignedIn = true
	tt.Frame()
	if _, ok := tt.Find(locales.SidebarSignInToSync()); ok {
		t.Error("signed in, the account row still signs in")
	}
}

func sharedTree(t *testing.T, a *App) (theirs, kid string) {
	t.Helper()
	const space, at = "00000000-0000-0000-0000-0000000000cc", "2026-01-01T00:00:00Z"
	theirs, kid = "00000000-0000-0000-0000-0000000000bb", "00000000-0000-0000-0000-0000000000be"
	for _, p := range []map[string]any{
		{"id": theirs, "space_id": space, "title": "Theirs", "sort_key": "V", "created_at": at, "updated_at": at},
		{"id": kid, "space_id": space, "parent_id": theirs, "title": "Kid", "sort_key": "V", "created_at": at, "updated_at": at},
	} {
		if err := a.st.PutRemotePage(p); err != nil {
			t.Fatal(err)
		}
		id := p["id"].(string)
		if err := a.st.SetMembers(id, []store.Member{{PageID: id, UserID: a.session.UserID, Role: "editor"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "Mine"}); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	return theirs, kid
}

func TestSharedPageListedOnce(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	theirs, _ := sharedTree(t, a)
	for _, p := range a.library().Top {
		if p.ID == theirs {
			t.Errorf("a shared page is among the user's own")
		}
	}
	tt := ui.NewTester(func(c *ui.Context) { a.tree(c) }, 300, 600)
	tt.Frame()
	n := 0
	for i := 0; i < a.treeState.Rows(); i++ {
		if a.treeState.Item(i) == "page:"+theirs {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the shared page has %d rows", n)
	}
}

func TestSharedSubpageStaysInside(t *testing.T) {
	a := templatesApp(t)
	theirs, kid := sharedTree(t, a)
	a.treeState.Indent = 16
	if _, _, _, ok := a.dropSpot(nil, pageDrag{kid}, "page:"+theirs, 10, 1, 26); ok {
		t.Error("the subpage goes before the shared page")
	}
	if _, _, _, ok := a.dropSpot(nil, pageDrag{kid}, "page:"+theirs, 10, 25, 26); ok {
		t.Error("the subpage goes after the shared page")
	}
	if _, _, _, ok := a.dropSpot(nil, pageDrag{kid}, "page:"+kid, -20, 25, 26); ok {
		t.Error("the subpage goes a level out, after the shared page")
	}
	a.treeOpen("page", theirs)
	if at, target, _, ok := a.dropSpot(nil, pageDrag{kid}, "page:"+theirs, 10, 25, 26); !ok || at != dropAfter || target != "page:"+theirs {
		t.Errorf("after the open shared page: %d %s %v", at, target, ok)
	}
}

func TestPagesOfAGoneFolder(t *testing.T) {
	a := templatesApp(t)
	f, err := a.st.CreateFolder("Gone")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Left", FolderID: f.ID})
	if _, err := a.st.DB().Exec(`UPDATE folders SET deleted_at = 1 WHERE id = ?`, f.ID); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	if top := a.library().Top; len(top) == 0 || !slices.ContainsFunc(top, func(x store.Page) bool { return x.ID == p.ID }) {
		t.Fatalf("top level %+v", top)
	}
	if !a.lastSibling(a.pageByID(p.ID), "") {
		t.Error("the page is not last among the top level's")
	}
}

func TestPageRowMenuLeavesOutAddSubpage(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	a.reloadAll()
	for _, header := range []bool{false, true} {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Button(c.Key("menu"), "Menu").Menu(func(m *ui.Menu) { a.pageMenu(m, *a.pageByID(p.ID), header) })
		}, 300, 200)
		tt.Frame()
		if err := tt.Click("Menu"); err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(tt.Menu(), locales.SidebarAddSubpage()); got != header {
			t.Errorf("header %v: menu %v", header, tt.Menu())
		}
	}
}

func TestDeletedSubpageTakesItsLink(t *testing.T) {
	a := templatesApp(t)
	parent, _ := a.st.CreatePage(store.NewPageOptions{Title: "Trip"})
	first, _ := a.st.CreatePage(store.NewPageOptions{Title: "Packing", ParentID: parent.ID})
	second, _ := a.st.CreatePage(store.NewPageOptions{Title: "Tickets", ParentID: parent.ID})
	a.reloadAll()
	a.linkSubpage(parent.ID, first.ID, "")
	a.linkSubpage(parent.ID, second.ID, "")

	a.deletePage(first.ID)
	if a.hasSubpageLink(parent.ID, first.ID) {
		t.Fatal("Trip still links to Packing, deleted")
	}
	a.toasts[len(a.toasts)-1].fn()
	c := a.st.Content(parent.ID)
	if c == nil || len(c.Children) < 2 || c.Children[len(c.Children)-2].Str("page_id") != first.ID {
		t.Fatalf("Packing's link is not back before Tickets': %v", c)
	}
}

func TestDeletedOnlySubpageLeavesABlock(t *testing.T) {
	a := templatesApp(t)
	parent, _ := a.st.CreatePage(store.NewPageOptions{Title: "Trip"})
	child, _ := a.st.CreatePage(store.NewPageOptions{Title: "Packing", ParentID: parent.ID})
	if _, _, err := a.st.SaveContent(parent.ID, doc.FromBlocks([]*doc.Node{doc.SubpageNode(child.ID)})); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(parent.ID)
	if a.page == nil {
		t.Fatal("Trip did not open")
	}
	ed := a.page.editor
	ed.Edit(func(*doc.Content) {})

	a.deletePage(child.ID)
	if len(ed.Content.Children) == 0 {
		t.Fatal("the open page has no block")
	}
	if c := a.st.Content(parent.ID); c == nil || len(c.Children) == 0 {
		t.Fatalf("stored with no block: %v", c)
	}
	a.caretAtEnd()
	ed.Undo()
	if a.hasSubpageLink(parent.ID, child.ID) {
		t.Error("undoing in the page brought back the deleted page's link")
	}
	a.toasts[len(a.toasts)-1].fn()
	if !a.hasSubpageLink(parent.ID, child.ID) {
		t.Error("the toast's Undo did not bring back the link")
	}
}

func TestCaretAtEndOfAPageWithNoBlocks(t *testing.T) {
	a := templatesApp(t)
	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	a.reloadAll()
	a.openPage(p.ID)
	if a.page == nil {
		t.Fatal("the page did not open")
	}
	a.page.editor.Content.Children = nil
	a.caretAtEnd()
	if n := len(a.page.editor.Content.Children); n != 1 {
		t.Errorf("%d blocks", n)
	}
}

func TestTreeArrowsGoByFolders(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen = true
	f, err := a.st.CreateFolder("Product")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: "PRD", FolderID: f.ID}); err != nil {
		t.Fatal(err)
	}
	notes, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	a.reloadAll()
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(260).FillHeight().Children(func() { a.sidebar(c) })
	}, 260, 600)
	tt.Frame()
	clickOn(t, tt, "Notes")
	tt.Frame()
	if a.route.ID != notes.ID {
		t.Fatalf("route %+v", a.route)
	}
	folder := "folder:" + f.ID
	tt.Key(0, ui.KeyUp)
	for range 3 {
		tt.Frame()
	}
	if a.treeState.Open.Has(folder) || a.treeRow < 0 || a.treeState.Item(a.treeRow) != folder {
		t.Fatalf("after Up: open %v, row %d", a.treeState.Open.Has(folder), a.treeRow)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !a.treeState.Open.Has(folder) || !tt.HasText("PRD") {
		t.Errorf("Enter left the folder closed: %v", tt.Texts())
	}
}

func TestSubpageLinkIsNoUndoStep(t *testing.T) {
	a := templatesApp(t)
	trip, _ := a.st.CreatePage(store.NewPageOptions{Title: "Trip", Content: doc.FromBlocks([]*doc.Node{textNode("Plans")})})
	packing, _ := a.st.CreatePage(store.NewPageOptions{Title: "Packing"})
	a.reloadAll()
	a.openPage(trip.ID)
	if a.page == nil {
		t.Fatal("Trip did not open")
	}
	ed := a.page.editor
	ed.Edit(func(c *doc.Content) { c.Children[0].SetDelta(doc.FromMarkdown("Plans for May")) })
	if err := a.pageCore().MovePage(packing.ID, trip.ID, func() error { return a.st.MovePage(packing.ID, trip.ID, "", "") }); err != nil {
		t.Fatal(err)
	}
	if !a.hasSubpageLink(trip.ID, packing.ID) {
		t.Fatal("Trip doesn't link to Packing, moved into it")
	}
	ed.Undo()
	if !a.hasSubpageLink(trip.ID, packing.ID) {
		t.Error("undoing in the page took out the link to Packing, still inside it")
	}
	if got := ed.Content.Children[0].Delta().Plain(); got != "Plans" {
		t.Errorf("undo left %q", got)
	}
	a.savePage()
	if !slices.ContainsFunc(a.st.Content(trip.ID).Children, func(n *doc.Node) bool { return n.Str("page_id") == packing.ID }) {
		t.Error("the link isn't stored")
	}
}

func TestCanMoveInside(t *testing.T) {
	a := templatesApp(t)
	f, _ := a.st.CreateFolder("Work")
	trip, _ := a.st.CreatePage(store.NewPageOptions{Title: "Trip"})
	packing, _ := a.st.CreatePage(store.NewPageOptions{Title: "Packing", ParentID: trip.ID})
	notes, _ := a.st.CreatePage(store.NewPageOptions{Title: "Notes"})
	const theirs, view, space = "00000000-0000-0000-0000-0000000000bb", "00000000-0000-0000-0000-0000000000bd", "00000000-0000-0000-0000-0000000000cc"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	for id, role := range map[string]string{theirs: "editor", view: "viewer"} {
		if err := a.st.PutRemotePage(map[string]any{"id": id, "space_id": space, "title": role, "sort_key": "V", "created_at": at, "updated_at": at}); err != nil {
			t.Fatal(err)
		}
		if err := a.st.SetMembers(id, []store.Member{{PageID: id, UserID: a.session.UserID, Role: role, Accepted: true}}); err != nil {
			t.Fatal(err)
		}
	}
	a.reloadAll()
	for _, tc := range []struct {
		id, key string
		ok      bool
	}{
		{trip.ID, "", true},
		{trip.ID, "folder:" + f.ID, true},
		{notes.ID, "page:" + trip.ID, true},
		{trip.ID, "page:" + trip.ID, false},
		{trip.ID, "page:" + packing.ID, false},
		{notes.ID, "page:" + theirs, false},
		{theirs, "", false},
		{theirs, "folder:" + f.ID, false},
		{theirs, "page:" + notes.ID, false},
		{theirs, "page:" + view, false},
		{"gone", "", false},
	} {
		if got := a.canMoveInside(tc.id, tc.key); got != tc.ok {
			t.Errorf("%s inside %q: %v, want %v", a.pageTitleOf(tc.id), tc.key, got, tc.ok)
		}
	}
}

func TestFolderShowsInTheSidebar(t *testing.T) {
	a := templatesApp(t)
	a.settings.SidebarOpen, a.settings.SidebarTab = false, tabTags
	a.dialogs.openRenameFolder(nil)
	a.dialogs.folderName = "Work"
	tt := ui.NewTester(a.folderDialog, 600, 400)
	tt.Frame()
	if err := tt.Click(locales.DialogFolderCreate()); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	if len(a.folders) != 1 {
		t.Fatalf("folders %+v", a.folders)
	}
	work := a.folders[0]
	if !a.settings.SidebarOpen || a.settings.SidebarTab != tabPages || !a.treeState.Open.Has("folder:"+work.ID) {
		t.Errorf("sidebar open %v at tab %d, folder open %v", a.settings.SidebarOpen, a.settings.SidebarTab, a.treeState.Open.Has("folder:"+work.ID))
	}

	p, _ := a.st.CreatePage(store.NewPageOptions{Title: "Plan", FolderID: work.ID})
	a.reloadAll()
	a.openPage(p.ID)
	a.treeState.Open.Remove("folder:" + work.ID)
	a.settings.SidebarOpen, a.settings.SidebarTab = false, tabTodos
	crumbs := ui.NewTester(func(c *ui.Context) { a.breadcrumbs(c, *a.pageByID(p.ID)) }, 600, 60)
	crumbs.Frame()
	if err := crumbs.Click("Work"); err != nil {
		t.Fatal(err)
	}
	if !a.settings.SidebarOpen || a.settings.SidebarTab != tabPages || !a.treeState.Open.Has("folder:"+work.ID) {
		t.Errorf("after the path's folder: sidebar open %v at tab %d, folder open %v", a.settings.SidebarOpen, a.settings.SidebarTab, a.treeState.Open.Has("folder:"+work.ID))
	}
}

func TestSettingsInTheAppMenu(t *testing.T) {
	a := templatesApp(t)
	items := a.menu(&mygo.MenuItem{Label: "Check for Updates…"})
	var settings *mygo.MenuItem
	for _, it := range items[0].Submenu {
		if it.Accelerator == "CmdOrCtrl+," {
			settings = it
		}
	}
	if settings == nil || settings.Label != locales.AppMenuSettings() {
		t.Fatalf("the app menu has %+v at ⌘,", settings)
	}
	settings.Click(settings, nil)
	if !a.settingsOpen || a.settingsSection != "account" {
		t.Errorf("Settings… opened %v at %q", a.settingsOpen, a.settingsSection)
	}
	settings.Click(settings, nil)
	if a.settingsOpen {
		t.Error("Settings… again left Settings open")
	}
	seen := map[string]bool{}
	var walk func([]*mygo.MenuItem)
	walk = func(items []*mygo.MenuItem) {
		for _, it := range items {
			if k := strings.ToLower(it.Accelerator); k != "" {
				if seen[k] {
					t.Errorf("two items at %s", it.Accelerator)
				}
				seen[k] = true
			}
			walk(it.Submenu)
		}
	}
	walk(items)
}

func clickOn(t *testing.T, tt *ui.Tester, s string) {
	t.Helper()
	x, y := middleOf(t, tt, s)
	tt.ClickAt(x, y)
}

func rightClickOn(t *testing.T, tt *ui.Tester, s string) {
	t.Helper()
	x, y := middleOf(t, tt, s)
	tt.RightClickAt(x, y)
}

func middleOf(t *testing.T, tt *ui.Tester, s string) (x, y float32) {
	t.Helper()
	r, ok := tt.Find(s)
	if !ok {
		t.Fatalf("no %q in %v", s, tt.Texts())
	}
	return r.X + r.W/2, r.Y + r.H/2
}

func TestListsStayWhenAReadFails(t *testing.T) {
	a := templatesApp(t)
	p, err := a.st.CreatePage(store.NewPageOptions{Title: "Notes", Content: doc.FromBlocks([]*doc.Node{
		{ID: "t1", Type: "todo_list", Data: map[string]any{"checked": false}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreateFolder("Work"); err != nil {
		t.Fatal(err)
	}
	a.reloadAll()
	a.openPage(p.ID)
	todos := len(a.todoList())
	for _, table := range []string{"pages", "folders", "files"} {
		if _, err := a.st.DB().Exec(`ALTER TABLE ` + table + ` RENAME TO ` + table + `_gone`); err != nil {
			t.Fatal(err)
		}
	}
	a.reload(store.Change{Table: "*"})
	a.todosStale = true
	if len(a.pages) != 1 || len(a.folders) != 1 || a.pageByID(p.ID) == nil || a.page == nil {
		t.Errorf("pages %d, folders %d, open %v", len(a.pages), len(a.folders), a.page != nil)
	}
	if got := len(a.todoList()); got != todos {
		t.Errorf("%d to-dos, were %d", got, todos)
	}
	reread(&a.pages, a.st.Pages)
	if len(a.pages) != 1 {
		t.Errorf("pages %d", len(a.pages))
	}
}
