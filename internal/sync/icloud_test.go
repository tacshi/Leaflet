package sync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

func nextStamp() {
	for now := time.Now().UnixMilli(); time.Now().UnixMilli() == now; {
		time.Sleep(time.Millisecond)
	}
}

type cloudDevice struct {
	t   *testing.T
	st  *store.Store
	svc *Service
}

func newCloudDevice(t *testing.T, root string) *cloudDevice {
	t.Helper()
	st := openStore(t)
	svc := New(st, remote.New(remote.Settings{}, st))
	svc.ICloud = icloud.Folder{Root: root, Temp: filepath.Join(t.TempDir(), "tmp")}
	svc.cloudOn.Store(true)
	return &cloudDevice{t: t, st: st, svc: svc}
}

func cloudRoot(t *testing.T) string { return filepath.Join(t.TempDir(), "Leaflet") }

func (d *cloudDevice) sync() {
	d.t.Helper()
	if err := d.svc.SyncNow(); err != nil {
		d.t.Fatal(err)
	}
}

func syncAll(devices ...*cloudDevice) {
	for range 2 {
		for _, d := range devices {
			d.sync()
		}
	}
}

func (d *cloudDevice) page(title string, blocks ...*doc.Node) store.Page {
	d.t.Helper()
	p, err := d.st.CreatePage(store.NewPageOptions{Title: title, Content: &doc.Content{Children: blocks}})
	if err != nil {
		d.t.Fatal(err)
	}
	return p
}

func (d *cloudDevice) edit(pageID, blockID, text string) {
	d.t.Helper()
	c := d.st.Content(pageID)
	c.Find(blockID).SetDelta(doc.Delta{{Text: text}})
	if _, _, err := d.st.SaveContent(pageID, c); err != nil {
		d.t.Fatal(err)
	}
}

func (d *cloudDevice) text(pageID, blockID string) string {
	d.t.Helper()
	c := d.st.Content(pageID)
	if c == nil || c.Find(blockID) == nil {
		return ""
	}
	return textOf(c, blockID)
}

func (d *cloudDevice) title(pageID string) string {
	p, err := d.st.Page(pageID)
	if err != nil {
		return ""
	}
	return p.Title
}

func (d *cloudDevice) conflicts() int {
	cs, err := d.st.Conflicts()
	if err != nil {
		d.t.Fatal(err)
	}
	return len(cs)
}

func TestICloudJoinsOneSpace(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	pa := a.page("From A", para("a1", "one"))
	a.sync()
	pb := b.page("From B", para("b1", "two"))
	syncAll(a, b)
	if a.st.Session().SpaceID != b.st.Session().SpaceID {
		t.Fatalf("spaces %s and %s", a.st.Session().SpaceID, b.st.Session().SpaceID)
	}
	for _, d := range []*cloudDevice{a, b} {
		if d.title(pa.ID) != "From A" || d.title(pb.ID) != "From B" {
			t.Errorf("titles %q %q", d.title(pa.ID), d.title(pb.ID))
		}
		if d.text(pa.ID, "a1") != "one" || d.text(pb.ID, "b1") != "two" {
			t.Errorf("content %q %q", d.text(pa.ID, "a1"), d.text(pb.ID, "b1"))
		}
		for _, id := range []string{pa.ID, pb.ID} {
			p, _ := d.st.Page(id)
			if p.SpaceID != d.st.Session().SpaceID {
				t.Errorf("page %s is in space %s, not the device's", id, p.SpaceID)
			}
		}
	}
	if n := a.st.PendingCount() + b.st.PendingCount(); n != 0 {
		t.Errorf("%d changes still waiting", n)
	}
}

func TestICloudJoiningLeavesStarterPagesOut(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	starter := func(d *cloudDevice) store.Page {
		p := d.page("Welcome")
		if err := d.st.MarkStarterPages([]string{p.ID}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	wa, wb := starter(a), starter(b)
	pb := b.page("From B")
	a.sync()
	syncAll(b, a)
	for _, d := range []*cloudDevice{a, b} {
		if d.title(wa.ID) != "Welcome" || d.title(pb.ID) != "From B" {
			t.Errorf("titles %q %q", d.title(wa.ID), d.title(pb.ID))
		}
		if _, err := d.st.Page(wb.ID); !store.IsNotFound(err) {
			t.Errorf("the joining device's starter page is here: %v", err)
		}
	}
}

func TestICloudCarriesTags(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan")
	if err := a.st.SetPageTags(p.ID, []string{"work", "Q4"}); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if got, err := b.st.Page(p.ID); err != nil || !slices.Equal(got.Tags, []string{"work", "Q4"}) {
		t.Errorf("the other device has %q, %v", got.Tags, err)
	}
}

func TestICloudRowsAgree(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan")
	syncAll(a, b)
	if err := a.st.RenamePage(p.ID, "Plan A"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	nextStamp()
	if err := b.st.RenamePage(p.ID, "Plan B"); err != nil {
		t.Fatal(err)
	}
	syncAll(b, a)
	if a.title(p.ID) != "Plan B" || b.title(p.ID) != "Plan B" {
		t.Errorf("titles %q / %q", a.title(p.ID), b.title(p.ID))
	}
	if err := a.st.RenamePage(p.ID, "Plan A again"); err != nil {
		t.Fatal(err)
	}
	if err := b.st.RenamePage(p.ID, "Plan B again"); err != nil {
		t.Fatal(err)
	}
	b.sync()
	a.sync()
	b.sync()
	if a.title(p.ID) != b.title(p.ID) {
		t.Errorf("titles differ: %q / %q", a.title(p.ID), b.title(p.ID))
	}
}

func TestICloudChangesGoColumnByColumn(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p, q, r := a.page("Plan"), a.page("Done"), a.page("Old")
	syncAll(a, b)
	check := func(title, parent string) {
		t.Helper()
		for _, d := range []*cloudDevice{a, b} {
			pp, _ := d.st.Page(p.ID)
			if pp.Title != title || pp.ParentID != parent {
				t.Errorf("page is %q under %q, want %q under %q", pp.Title, pp.ParentID, title, parent)
			}
		}
	}
	if err := b.st.RenamePage(p.ID, "Plan B"); err != nil {
		t.Fatal(err)
	}
	if err := b.st.RenamePage(r.ID, "Old B"); err != nil {
		t.Fatal(err)
	}
	nextStamp()
	if err := a.st.MovePage(p.ID, q.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.DeletePage(r.ID); err != nil {
		t.Fatal(err)
	}
	a.sync()
	syncAll(b, a)
	check("Plan B", q.ID)
	for _, d := range []*cloudDevice{a, b} {
		if rr, _ := d.st.Page(r.ID); rr.Title != "Old B" || rr.DeletedAt.IsZero() {
			t.Errorf("deleted page is %q, deleted %v", rr.Title, rr.DeletedAt)
		}
	}
	if err := b.st.RenamePage(p.ID, "Plan B2"); err != nil {
		t.Fatal(err)
	}
	if err := a.st.MovePage(p.ID, "", "", ""); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if err := b.svc.cloudPull(context.Background(), b.st.DeviceID()); err != nil {
		t.Fatal(err)
	}
	if b.title(p.ID) != "Plan B2" {
		t.Errorf("B's rename waiting was undone: %q", b.title(p.ID))
	}
	syncAll(b, a)
	check("Plan B2", "")
}

func TestICloudKeptPagesBringWhatChangedMeanwhile(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	settings := a.st.Settings()
	settings.AllPagesOffline = false
	if err := a.st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 3 {
		p := a.page("Notes", para("x", "x0"))
		if err := a.st.SetKeepOffline(p.ID, false); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	syncAll(a, b)
	for _, id := range ids {
		b.edit(id, "x", "from B")
	}
	syncAll(a, b)
	texts := func() []string {
		var out []string
		for _, id := range ids {
			out = append(out, a.text(id, "x"))
		}
		return out
	}
	if got := texts(); !slices.Equal(got, []string{"x0", "x0", "x0"}) {
		t.Fatalf("pages not kept have %v", got)
	}

	if err := a.st.SetKeepOffline(ids[0], true); err != nil {
		t.Fatal(err)
	}
	keepContent(t, a.svc, ids[0])
	a.sync()
	if got := texts(); !slices.Equal(got, []string{"from B", "x0", "x0"}) {
		t.Errorf("with the first kept, %v", got)
	}
	settings.AllPagesOffline = true
	if err := a.st.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	keepContent(t, a.svc)
	a.sync()
	if got := texts(); !slices.Equal(got, []string{"from B", "from B", "from B"}) {
		t.Errorf("with all kept, %v", got)
	}
}

func TestICloudMergesContent(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"), para("y", "y0"))
	syncAll(a, b)
	a.edit(p.ID, "x", "xa")
	b.edit(p.ID, "y", "yb")
	syncAll(a, b)
	for _, d := range []*cloudDevice{a, b} {
		if d.text(p.ID, "x") != "xa" || d.text(p.ID, "y") != "yb" {
			t.Errorf("content %q %q", d.text(p.ID, "x"), d.text(p.ID, "y"))
		}
		if d.conflicts() != 0 {
			t.Errorf("%d conflicts on different blocks", d.conflicts())
		}
	}
	a.edit(p.ID, "x", "mine")
	nextStamp()
	b.edit(p.ID, "x", "theirs")
	syncAll(a, b)
	if a.text(p.ID, "x") != "theirs" || b.text(p.ID, "x") != "theirs" {
		t.Errorf("x is %q / %q", a.text(p.ID, "x"), b.text(p.ID, "x"))
	}
	if a.conflicts() != 1 || b.conflicts() != 0 {
		t.Errorf("conflicts: A %d, B %d", a.conflicts(), b.conflicts())
	}
}

func TestICloudMergeKeepsItsConflictsWithItsVersion(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"))
	syncAll(a, b)
	a.edit(p.ID, "x", "mine")
	nextStamp()
	b.edit(p.ID, "x", "theirs")
	b.sync()
	if _, err := a.st.DB().Exec(`CREATE TRIGGER refuse BEFORE INSERT ON conflicts BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	_ = a.svc.SyncNow()
	if _, err := a.st.DB().Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if a.text(p.ID, "x") != "theirs" || a.conflicts() != 1 {
		t.Errorf("x is %q, with %d conflicts", a.text(p.ID, "x"), a.conflicts())
	}
}

func TestICloudUnreadablePageWaits(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"))
	syncAll(a, b)
	b.edit(p.ID, "x", "xb")
	b.sync()
	rel, err := icloud.PagePath(b.st.DeviceID(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var file icloud.PageFile
	if err := b.svc.ICloud.ReadJSON(rel, &file); err != nil {
		t.Fatal(err)
	}
	good := file.Content
	file.Content = []byte(`{"children": 5}`)
	if err := b.svc.ICloud.WriteJSON(rel, file); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if a.text(p.ID, "x") != "x0" || !waitingOn(t, a.svc)[p.ID] {
		t.Fatalf("A has %q, waiting %v", a.text(p.ID, "x"), waitingOn(t, a.svc)[p.ID])
	}
	file.Content = good
	if err := b.svc.ICloud.WriteJSON(rel, file); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if a.text(p.ID, "x") != "xb" {
		t.Errorf("A has %q once it can read B's", a.text(p.ID, "x"))
	}
}

func TestICloudRelayedEditIsNoConflict(t *testing.T) {
	root := cloudRoot(t)
	a, b, c := newCloudDevice(t, root), newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"))
	syncAll(a, b, c)
	a.edit(p.ID, "x", "a")
	a.sync()
	b.sync()
	nextStamp()
	c.edit(p.ID, "x", "c")
	c.sync()
	syncAll(b, a, c)
	for _, d := range []*cloudDevice{a, b, c} {
		if d.text(p.ID, "x") != "c" {
			t.Errorf("x is %q", d.text(p.ID, "x"))
		}
	}
	if a.conflicts() != 1 || b.conflicts() != 0 || c.conflicts() != 0 {
		t.Errorf("conflicts: A %d, B %d, C %d", a.conflicts(), b.conflicts(), c.conflicts())
	}
}

func TestICloudWaitsForFilesNotDownloaded(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Later")
	a.sync()
	me := a.st.DeviceID()
	rel, _ := icloud.BatchPath(me, 1)
	path := filepath.Join(root, filepath.FromSlash(rel))
	hidden := filepath.Join(filepath.Dir(path), ".0000000000000001.json.icloud")
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, hidden); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if b.title(p.ID) != "" {
		t.Fatal("B has a page from a batch it could not read")
	}
	if cur := b.svc.intValue("icloud.cursor." + me); cur != 0 {
		t.Errorf("cursor moved to %d", cur)
	}
	if err := os.Remove(hidden); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if b.title(p.ID) != "Later" {
		t.Errorf("title %q once downloaded", b.title(p.ID))
	}
}

func TestICloudAsksForEveryBatchNotDownloaded(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	var pages []store.Page
	for i := range 5 {
		pages = append(pages, a.page("Page "+strconv.Itoa(i)))
		a.sync()
	}
	me := a.st.DeviceID()
	seqs, _, err := a.svc.ICloud.Batches(me)
	if err != nil || len(seqs) < len(pages) {
		t.Fatalf("A wrote batches %v: %v", seqs, err)
	}
	hidden := func(path string) string {
		return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".icloud")
	}
	for _, seq := range seqs {
		rel, _ := icloud.BatchPath(me, seq)
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.Rename(path, hidden(path)); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	was := icloud.StartDownload
	icloud.StartDownload = func(path string) error { asked = append(asked, path); return nil }
	t.Cleanup(func() { icloud.StartDownload = was })
	b.sync()
	for _, path := range asked {
		_ = os.Rename(hidden(path), path)
	}
	b.sync()
	for _, p := range pages {
		if b.title(p.ID) != p.Title {
			t.Errorf("B has %q of %q after two passes", b.title(p.ID), p.Title)
		}
	}
}

func TestICloudCompacts(t *testing.T) {
	old := compactAfter
	compactAfter = 3
	t.Cleanup(func() { compactAfter = old })
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	p := a.page("Draft", para("x", "x0"))
	for i := range 6 {
		a.sync()
		if err := a.st.RenamePage(p.ID, "Draft "+string(rune('1'+i))); err != nil {
			t.Fatal(err)
		}
		a.edit(p.ID, "x", "x"+string(rune('1'+i)))
	}
	a.sync()
	seqs, _, err := a.svc.ICloud.Batches(a.st.DeviceID())
	if err != nil || len(seqs) > compactAfter+1 {
		t.Fatalf("log holds %v, %v", seqs, err)
	}
	c := newCloudDevice(t, root)
	syncAll(c)
	if c.title(p.ID) != a.title(p.ID) || c.text(p.ID, "x") != a.text(p.ID, "x") {
		t.Errorf("late device has %q %q, A %q %q", c.title(p.ID), c.text(p.ID, "x"), a.title(p.ID), a.text(p.ID, "x"))
	}
}

func TestICloudCompactionKeepsEachColumnsChange(t *testing.T) {
	old := compactAfter
	compactAfter = 3
	t.Cleanup(func() { compactAfter = old })
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Draft")
	syncAll(a, b)
	if err := a.st.SetPageTags(p.ID, []string{"work"}); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		a.sync()
		if err := a.st.RenamePage(p.ID, "Draft "+string(rune('1'+i))); err != nil {
			t.Fatal(err)
		}
	}
	a.sync()
	if seqs, _, _ := a.svc.ICloud.Batches(a.st.DeviceID()); slices.Contains(seqs, 2) {
		t.Fatalf("not compacted: %v", seqs)
	}
	b.sync()
	if got, _ := b.st.Page(p.ID); got.Title != "Draft 6" || !slices.Equal(got.Tags, []string{"work"}) {
		t.Errorf("B has %q tagged %q", got.Title, got.Tags)
	}
}

func TestICloudCompactsPastABatchCutShort(t *testing.T) {
	old, tries, wait := compactAfter, damagedTries, damagedWait
	compactAfter, damagedTries, damagedWait = 3, 2, 0
	t.Cleanup(func() { compactAfter, damagedTries, damagedWait = old, tries, wait })
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	p := a.page("Draft")
	a.sync()
	if err := a.st.RenamePage(p.ID, "Draft 0"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	for _, seq := range []int64{1, 2} {
		rel, _ := icloud.BatchPath(a.st.DeviceID(), seq)
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(`{"format":1,"entr`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 6 {
		if err := a.st.RenamePage(p.ID, "Draft "+string(rune('1'+i))); err != nil {
			t.Fatal(err)
		}
		a.sync()
	}
	seqs, _, _ := a.svc.ICloud.Batches(a.st.DeviceID())
	if slices.Contains(seqs, 1) || slices.Contains(seqs, 2) || len(seqs) > compactAfter+1 {
		t.Errorf("not compacted: %v", seqs)
	}
	if kept := a.st.ValuesWithPrefix("icloud.damaged."); len(kept) > 0 {
		t.Errorf("kept %v of batches gone", kept)
	}
}

func TestHoldingAnEntryAgainHoldsItOnce(t *testing.T) {
	e := icloud.Entry{Kind: "row", Table: "pages", ID: "p", At: 5, By: "b", Changed: []string{"title"}}
	held := foldEntry(nil, e)
	held = foldEntry(held, e)
	if len(held) != 1 {
		t.Errorf("held %d entries", len(held))
	}
	other := icloud.Entry{Kind: "row", Table: "pages", ID: "p", At: 5, By: "b", Changed: []string{"parent_id"}}
	if held = foldEntry(held, other); len(held) != 2 {
		t.Errorf("held %d entries", len(held))
	}
}

func TestHeldEntriesUnreadAreKept(t *testing.T) {
	d := newCloudDevice(t, cloudRoot(t))
	key := heldKey("pages", "p")
	if err := d.st.SetValue(key, `[{"kind": "row",`); err != nil {
		t.Fatal(err)
	}
	e := icloud.Entry{Kind: "row", Table: "pages", ID: "p", At: 5, By: "b", Row: map[string]any{"title": "x"}}
	if err := d.svc.hold(e); err == nil {
		t.Error("held over entries not read")
	}
	if err := d.svc.applyHeld(map[string]bool{}); err == nil {
		t.Error("applied entries not read")
	}
	if v, _ := d.st.Value(key); v != `[{"kind": "row",` {
		t.Errorf("held %q", v)
	}
	unreadableValue(t, d.st, key)
	if err := d.svc.hold(e); err == nil {
		t.Error("held over entries that could not be read")
	}
	if _, _, err := d.st.Lookup(key); err == nil {
		t.Error("the entries were written over")
	}
}

func TestICloudFolderGoneOrReplaced(t *testing.T) {
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	a.page("Kept")
	a.sync()
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(root, other); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.SyncNow(); !errors.Is(err, icloud.ErrUnavailable) {
		t.Errorf("gone: %v", err)
	}
	if _, err := os.Stat(root); err == nil {
		t.Error("the folder was made again")
	}
	if err := os.Rename(other, root); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if err := a.svc.ICloud.WriteJSON(icloud.SpacePath, icloud.Space{Format: icloud.Format, SpaceID: doc.NewRowID()}); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.SyncNow(); !errors.Is(err, icloud.ErrOtherLibrary) {
		t.Errorf("replaced: %v", err)
	}
	a.svc.SetICloud(false)
	if err := a.svc.EnableICloud(); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.SyncNow(); err != nil {
		t.Errorf("joined again: %v", err)
	}
}

func TestICloudTwoDevicesMadeTheSpace(t *testing.T) {
	root, rootB := cloudRoot(t), cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, rootB)
	pa := a.page("A's")
	pb := b.page("B's")
	a.sync()
	b.sync()
	if err := os.CopyFS(filepath.Join(root, "devices"), os.DirFS(filepath.Join(rootB, "devices"))); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rootB, "space.json")) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "space 2.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b.svc.SetICloudRoot(root)
	syncAll(a, b, a)
	if a.st.Session().SpaceID != b.st.Session().SpaceID {
		t.Fatalf("spaces %s and %s", a.st.Session().SpaceID, b.st.Session().SpaceID)
	}
	for _, d := range []*cloudDevice{a, b} {
		if d.title(pa.ID) != "A's" || d.title(pb.ID) != "B's" {
			t.Errorf("titles %q %q", d.title(pa.ID), d.title(pb.ID))
		}
	}
	if _, losers, _ := a.svc.ICloud.ReadSpace(); len(losers) != 1 {
		t.Errorf("copies left: %v", losers)
	}
	was := cloudNow
	cloudNow = func() time.Time { return time.Now().Add(loserKept + time.Hour) }
	t.Cleanup(func() { cloudNow = was })
	syncAll(a, b)
	if _, losers, _ := a.svc.ICloud.ReadSpace(); len(losers) != 0 {
		t.Errorf("copies left: %v", losers)
	}
}

func TestICloudDeviceWithAnotherDevicesID(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan")
	a.sync()
	writing, _ := a.st.Value("icloud.writing")
	if err := a.st.RenamePage(p.ID, "Plan 2"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	old := a.st.DeviceID()
	for k, v := range map[string]string{"icloud.seq": "1", "icloud.writing": writing, "icloud.log_from": "2"} {
		if err := a.st.SetValue(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.st.RenamePage(p.ID, "Plan 3"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if a.st.DeviceID() == old {
		t.Fatal("kept the ID another device writes under")
	}
	if from := a.svc.intValue("icloud.log_from"); from != 0 {
		t.Errorf("the new log starts at %d", from)
	}
	syncAll(b, a, b)
	if b.title(p.ID) != "Plan 3" || a.title(p.ID) != "Plan 3" {
		t.Errorf("titles %q / %q", a.title(p.ID), b.title(p.ID))
	}
}

func TestICloudFilesAndAudio(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	a.svc.KeepAudio = func() string { return "until_uploaded" }
	p := a.page("Attachments")
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	wav := filepath.Join(t.TempDir(), "rec.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := a.st.CreateRecording(p.ID, "blk", "", "", wav, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = a.st.UpdateRecording(rec.ID, func(r *store.Recording) { r.Status = "ready" }); err != nil {
		t.Fatal(err)
	}
	if err := a.st.QueueUpload(rec); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if _, err := os.Stat(wav); err == nil {
		t.Error("the audio stayed on A after it went up")
	}
	ctx := context.Background()
	var bf store.File
	files, _ := b.st.Files()
	for _, x := range files {
		if x.ID == f.ID {
			bf = x
		}
	}
	if bf.ID == "" {
		t.Fatal("B has no file")
	}
	data, err := b.svc.Fetch(ctx, "files", bf.StoragePath)
	if err != nil || string(data) != "hello" {
		t.Errorf("file %q, %v", data, err)
	}
	br, err := b.st.Recording(rec.ID)
	if err != nil || br.AudioPath == "" {
		t.Fatalf("recording %+v, %v", br, err)
	}
	data, err = b.svc.Fetch(ctx, "recordings", br.AudioPath)
	if err != nil || string(data) != "RIFF" {
		t.Errorf("audio %q, %v", data, err)
	}
}

func TestICloudOffAndOnSendsWhatWaited(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan", para("x", "x0"))
	syncAll(a, b)
	a.svc.SetICloud(false)
	a.edit(p.ID, "x", "offline edit")
	if err := a.svc.SyncNow(); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if b.text(p.ID, "x") != "x0" {
		t.Fatal("sync went on while off")
	}
	a.svc.SetICloud(true)
	waiting := a.st.PendingCount()
	syncAll(a, b)
	if b.text(p.ID, "x") != "offline edit" {
		t.Errorf("B has %q", b.text(p.ID, "x"))
	}
	if waiting > 2 {
		t.Errorf("%d changes queued: turning sync on again sent everything", waiting)
	}
}

func TestICloudWaitsForThePageFileABatchNames(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"))
	syncAll(a, b)
	rel, _ := icloud.PagePath(a.st.DeviceID(), p.ID)
	path := filepath.Join(root, filepath.FromSlash(rel))
	old, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	a.edit(p.ID, "x", "xa")
	a.sync()
	fresh, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if err := os.WriteFile(path, fresh, 0o600); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if b.text(p.ID, "x") != "xa" {
		t.Errorf("older file first: B has %q", b.text(p.ID, "x"))
	}
	q := a.page("Later", para("y", "y0"))
	a.sync()
	rel, _ = icloud.PagePath(a.st.DeviceID(), q.ID)
	path = filepath.Join(root, filepath.FromSlash(rel))
	held := path + ".held"
	if err := os.Rename(path, held); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if err := os.Rename(held, path); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if b.text(q.ID, "y") != "y0" {
		t.Errorf("missing file first: B has %q", b.text(q.ID, "y"))
	}
}

func TestICloudSendsAgainAfterABatchFailed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a folder made read-only stops no one running as root")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no mode that keeps files out of a folder")
	}
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"))
	syncAll(a, b)
	logDir := filepath.Join(root, "devices", a.st.DeviceID(), "log")
	if err := os.Chmod(logDir, 0o500); err != nil { // #nosec G302
		t.Fatal(err)
	}
	a.edit(p.ID, "x", "xa")
	if err := a.svc.SyncNow(); err == nil {
		t.Fatal("wrote a batch to a folder it may not write to")
	}
	if err := os.Chmod(logDir, 0o700); err != nil { // #nosec G302
		t.Fatal(err)
	}
	syncAll(a, b)
	if b.text(p.ID, "x") != "xa" {
		t.Errorf("B has %q", b.text(p.ID, "x"))
	}
}

func TestICloudReadsPastACompactionCutShort(t *testing.T) {
	old := compactAfter
	compactAfter = 1000
	t.Cleanup(func() { compactAfter = old })
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	p := a.page("Draft")
	a.sync()
	for i := range 3 {
		if err := a.st.RenamePage(p.ID, "Draft "+string(rune('1'+i))); err != nil {
			t.Fatal(err)
		}
		a.sync()
	}
	me := a.st.DeviceID()
	saved := map[int64][]byte{}
	for _, seq := range []int64{3, 4} {
		rel, _ := icloud.BatchPath(me, seq)
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) // #nosec G304
		if err != nil {
			t.Fatal(err)
		}
		saved[seq] = data
	}
	compactAfter = 3
	if err := a.st.RenamePage(p.ID, "Final"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	for seq, data := range saved {
		rel, _ := icloud.BatchPath(me, seq)
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newCloudDevice(t, root)
	syncAll(c)
	if c.title(p.ID) != "Final" {
		t.Errorf("late device has %q", c.title(p.ID))
	}
}

func TestICloudJoiningAfreshSendsEverything(t *testing.T) {
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	a.svc.KeepAudio = func() string { return "until_uploaded" }
	p := a.page("Meeting")
	if _, err := a.st.AddComment(p.ID, "blk", "Looks good", "", "", "user", "You"); err != nil {
		t.Fatal(err)
	}
	wav := filepath.Join(t.TempDir(), "rec.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := a.st.CreateRecording(p.ID, "blk", "", "", wav, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = a.st.UpdateRecording(rec.ID, func(r *store.Recording) { r.Status = "ready" }); err != nil {
		t.Fatal(err)
	}
	if err := a.st.QueueUpload(rec); err != nil {
		t.Fatal(err)
	}
	syncAll(a)
	moved := filepath.Join(t.TempDir(), "old")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o750); err != nil {
		t.Fatal(err)
	}
	a.svc.SetICloudRoot(moved)
	a.svc.SetICloud(false)
	if !a.svc.CanFetch() {
		t.Fatal("audio in the folder left is out of reach")
	}
	if data, err := a.svc.Fetch(context.Background(), "recordings", rec.ID+".wav"); err != nil || string(data) != "RIFF" {
		t.Fatalf("audio %q, %v", data, err)
	}
	space, _ := a.st.Value("icloud.space")
	if err := a.svc.requeueAll(context.Background(), space); err != nil {
		t.Fatal(err)
	}
	a.svc.SetICloudRoot(root)
	if err := a.svc.EnableICloud(); err != nil {
		t.Fatal(err)
	}
	syncAll(a)
	c := newCloudDevice(t, root)
	syncAll(c)
	threads, _ := c.st.Threads(p.ID)
	if len(threads) != 1 || threads[0].Root.Body != "Looks good" {
		t.Errorf("threads %+v", threads)
	}
	cr, err := c.st.Recording(rec.ID)
	if err != nil {
		t.Fatalf("recording: %v", err)
	}
	if data, err := c.svc.Fetch(context.Background(), "recordings", cr.AudioPath); err != nil || string(data) != "RIFF" {
		t.Errorf("audio %q, %v", data, err)
	}
}

func TestICloudJoinsOnceAllIsQueued(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	a.sync()
	f, err := b.st.CreateFolder("Work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.st.DB().Exec(`CREATE TRIGGER refuse BEFORE INSERT ON outbox WHEN NEW.entity = 'folders'
		BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	entries, _ := b.st.Pending()
	for _, e := range entries {
		if err := b.st.Dequeue(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.svc.joinSpace(context.Background(), ""); !errors.Is(err, errNotRequeued) {
		t.Fatalf("joinSpace: %v", err)
	}
	if joined, _ := b.st.Value("icloud.space"); joined != "" {
		t.Error("joined with the folder not queued")
	}
	if _, err := b.st.DB().Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	syncAll(b, a)
	if joined, _ := b.st.Value("icloud.space"); joined != a.st.Session().SpaceID {
		t.Errorf("B joined %q, A's space is %q", joined, a.st.Session().SpaceID)
	}
	folders, _ := a.st.Folders()
	if len(folders) != 1 || folders[0].ID != f.ID {
		t.Errorf("A has folders %+v", folders)
	}
}

func TestRequeueWaitsForAFileNotDownloaded(t *testing.T) {
	old := moveWait
	moveWait = 200 * time.Millisecond
	t.Cleanup(func() { moveWait = old })
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	p := a.page("Attachments")
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	a.sync()
	file := func() store.File {
		files, _ := a.st.Files()
		for _, x := range files {
			if x.ID == f.ID {
				return x
			}
		}
		t.Fatal("no file")
		return store.File{}
	}
	f = file()
	if err := os.Remove(f.LocalPath); err != nil {
		t.Fatal(err)
	}
	rel, err := blobPath("files", f.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	hidden := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".icloud")
	if err := os.Rename(path, hidden); err != nil {
		t.Fatal(err)
	}

	a.svc.SetICloud(false)
	const space = "00000000-0000-0000-0000-0000000000a2"
	if err := a.svc.moveInto(space, "My space", "00000000-0000-0000-0000-0000000000b1", false); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.requeueAll(context.Background(), space); err == nil {
		t.Error("requeueAll said all went")
	}
	if got := file(); got.StoragePath != f.StoragePath || a.st.HasPending("files", f.ID) {
		t.Errorf("file moved before it came: %+v", got)
	}
	moveWait = time.Minute
	stopped, stop := context.WithCancel(context.Background())
	stop()
	start := time.Now()
	if err := a.svc.requeueAll(stopped, space); err == nil {
		t.Error("requeueAll said all went")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("waited %v after the pass stopped", d)
	}
	moveWait = 200 * time.Millisecond

	if err := os.Rename(hidden, path); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.retryMoves(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := file()
	if got.StoragePath != space+"/"+f.ID+"/"+f.Name || !store.OnDevice(got.LocalPath) {
		t.Errorf("file %+v", got)
	}
	if !a.st.HasPending("files", f.ID) || !a.st.HasPending("storage", "files/"+f.ID) {
		t.Error("file not queued once it came")
	}
	if len(idsOf(t, a.svc, movesKey)) != 0 {
		t.Errorf("still waiting for %v", idsOf(t, a.svc, movesKey))
	}
}

func TestICloudSendingAgainLeavesFilesInTheFolder(t *testing.T) {
	old := moveWait
	moveWait = 200 * time.Millisecond
	t.Cleanup(func() { moveWait = old })
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Attachments")
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if f, err = a.st.File(f.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.LocalPath); err != nil {
		t.Fatal(err)
	}
	rel, _ := blobPath("files", f.StoragePath)
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Rename(path, filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".icloud")); err != nil {
		t.Fatal(err)
	}
	batch, _ := icloud.BatchPath(a.st.DeviceID(), a.svc.intValue("icloud.seq"))
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(batch)), []byte(`{"format":1,"entr`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.svc.ownLogChecked = false
	if err := a.st.RenamePage(p.ID, "Attachments 2"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if len(idsOf(t, a.svc, movesKey)) != 0 {
		t.Errorf("waits for %v", idsOf(t, a.svc, movesKey))
	}
	if got, _ := a.st.File(f.ID); store.OnDevice(got.LocalPath) {
		t.Error("the file came from iCloud Drive to go back there")
	}
	b.sync()
	if b.title(p.ID) != "Attachments 2" {
		t.Errorf("B has %q", b.title(p.ID))
	}
}

func TestICloudStoppedAfterWritingABatch(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan")
	syncAll(a, b)
	if err := a.st.RenamePage(p.ID, "Plan 2"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	me := a.st.DeviceID()
	seq := a.svc.intValue("icloud.seq")
	if err := a.st.SetValue("icloud.seq", strconv.FormatInt(seq-1, 10)); err != nil {
		t.Fatal(err)
	}
	if err := a.st.RenamePage(p.ID, "Plan 3"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if a.st.DeviceID() != me {
		t.Fatal("took a new ID for a batch of its own")
	}
	if got := a.svc.intValue("icloud.seq"); got != seq+1 {
		t.Errorf("seq %d, want %d", got, seq+1)
	}
	syncAll(b)
	if b.title(p.ID) != "Plan 3" {
		t.Errorf("B has %q", b.title(p.ID))
	}
}

func TestICloudRewritesItsOwnBatchCutShort(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan")
	syncAll(a, b)
	if err := a.st.RenamePage(p.ID, "Plan 2"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	me := a.st.DeviceID()
	rel, _ := icloud.BatchPath(me, a.svc.intValue("icloud.seq"))
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.WriteFile(path, []byte(`{"format":1,"entr`), 0o600); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if b.title(p.ID) != "Plan" {
		t.Fatalf("B read a batch cut short: %q", b.title(p.ID))
	}
	a.svc = New(a.st, remote.New(remote.Settings{}, a.st))
	a.svc.ICloud = b.svc.ICloud
	a.svc.ICloud.Temp = filepath.Join(t.TempDir(), "tmp")
	a.svc.cloudOn.Store(true)
	hidden := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".icloud")
	if err := os.Rename(path, hidden); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if err := os.Rename(hidden, path); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if a.st.DeviceID() != me {
		t.Error("took a new ID")
	}
	if b.title(p.ID) != "Plan 2" {
		t.Errorf("B has %q", b.title(p.ID))
	}
}

func TestICloudPassesOverABatchThatStaysCutShort(t *testing.T) {
	tries, wait := damagedTries, damagedWait
	damagedTries, damagedWait = 3, 0
	t.Cleanup(func() { damagedTries, damagedWait = tries, wait })
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan")
	syncAll(a, b)
	if err := a.st.RenamePage(p.ID, "Plan 2"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	rel, _ := icloud.BatchPath(a.st.DeviceID(), a.svc.intValue("icloud.seq"))
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(`{"format":1,"entr`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.st.RenamePage(p.ID, "Plan 3"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	for range damagedTries - 1 {
		b.sync()
		if b.title(p.ID) != "Plan" {
			t.Fatalf("B passed over the batch at once: %q", b.title(p.ID))
		}
	}
	b.sync()
	if b.title(p.ID) != "Plan 3" {
		t.Errorf("B has %q", b.title(p.ID))
	}
}

func TestICloudDeletedFileLeavesTheFolder(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	rel, err := icloud.FileBlobPath(f.ID, f.Name)
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("no copy in the folder: %v", err)
	}
	copyOnB, err := b.st.DownloadedFile(f.ID, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	if err := a.st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if _, err := os.Stat(filepath.Dir(blob)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy stayed in the folder: %v", err)
	}
	if n := a.st.PendingCount(); n != 0 {
		t.Errorf("%d waiting on A", n)
	}
	if files, _ := b.st.Files(); len(files) != 0 {
		t.Errorf("B still has %d files", len(files))
	}
	if _, err := os.Stat(copyOnB); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("B kept its copy: %v", err)
	}
}

func TestICloudFileThatLivesKeepsItsCopy(t *testing.T) {
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, "", "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	a.sync()
	rel, err := icloud.FileBlobPath(f.ID, f.Name)
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("no copy in the folder: %v", err)
	}
	if err := a.st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	lives, err := a.st.File(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	lives.DeletedAt = time.Time{}
	if err := a.st.PutFile(lives); err != nil {
		t.Fatal(err)
	}

	a.sync()
	if _, err := os.Stat(blob); err != nil {
		t.Errorf("the copy left the folder: %v", err)
	}
	if n := a.st.PendingCount(); n != 0 {
		t.Errorf("%d still waiting", n)
	}
}

func TestICloudDeletedFolderKeepsAFileDeletedElsewhere(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	folder, err := a.st.CreateFolder("Folder")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, folder.ID, "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if err := b.st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	b.sync()
	nextStamp()
	if err := a.st.DeleteFolder(folder); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	for i, d := range []*cloudDevice{a, b} {
		if got, _ := d.st.File(f.ID); got.DeletedAt.IsZero() || got.FolderID != "" {
			t.Errorf("device %d: file in %q, deleted %v", i, got.FolderID, got.DeletedAt)
		}
	}
}

func TestRequeueKeepsAFileDeletedWhileItWaits(t *testing.T) {
	old := moveWait
	moveWait = 400 * time.Millisecond
	t.Cleanup(func() { moveWait = old })
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	p := a.page("Attachments")
	add := func(name string) store.File {
		t.Helper()
		src := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := a.st.AddFile(src, "", p.ID, "text/plain")
		if err != nil {
			t.Fatal(err)
		}
		nextStamp()
		return f
	}
	kept, waits := add("kept.txt"), add("waits.txt")
	a.sync()
	w, _ := a.st.File(waits.ID)
	if err := os.Remove(w.LocalPath); err != nil {
		t.Fatal(err)
	}
	rel, err := blobPath("files", w.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Rename(path, filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".icloud")); err != nil {
		t.Fatal(err)
	}
	a.svc.SetICloud(false)
	deleted := make(chan error)
	go func() {
		time.Sleep(moveWait / 4)
		deleted <- a.st.DeleteFile(kept)
	}()
	const space = "00000000-0000-0000-0000-0000000000a2"
	if err := a.svc.moveInto(space, "My space", "00000000-0000-0000-0000-0000000000b1", false); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.requeueAll(context.Background(), space); err == nil {
		t.Error("requeueAll said all went")
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if got, _ := a.st.File(kept.ID); got.DeletedAt.IsZero() {
		t.Error("the file deleted meanwhile came back")
	}
}

func TestICloudHeldChangeThatFailsLetsPullsGoOn(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	syncAll(a, b)
	id := doc.NewRowID()
	if _, err := b.st.DB().Exec(`INSERT INTO comments (id, page_id, block_id, body, created_at, updated_at)
		VALUES (?, 'p', 'b', 'x', 'bad', 'bad')`, id); err != nil {
		t.Fatal(err)
	}
	held := icloud.Entry{Kind: "row", Table: "comments", ID: id, At: 1, By: a.st.DeviceID(), Row: map[string]any{"id": id, "body": "y"}}
	if err := b.svc.hold(held); err != nil {
		t.Fatal(err)
	}
	p := a.page("After")
	a.sync()
	b.sync()
	if got := b.title(p.ID); got != "After" {
		t.Errorf("B has %q", got)
	}
	if len(heldOf(t, b.svc, "comments", id)) != 1 {
		t.Error("the change that did not apply is not held again")
	}
}

func TestICloudBreaksALoopOfMoves(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p, q := a.page("P"), a.page("Q")
	syncAll(a, b)
	if err := a.st.MovePage(p.ID, q.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.st.MovePage(q.ID, p.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	top, under := min(p.ID, q.ID), max(p.ID, q.ID)
	for _, d := range []*cloudDevice{a, b} {
		t1, _ := d.st.Page(top)
		t2, _ := d.st.Page(under)
		if t1.ParentID != "" || t2.ParentID != top {
			t.Errorf("parents %q / %q, want top level / %s", t1.ParentID, t2.ParentID, top)
		}
	}
}

func TestICloudCopiesGoUpAsTheirOwn(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	a.svc.KeepAudio = func() string { return "until_uploaded" }
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	wav := filepath.Join(t.TempDir(), "rec.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := a.page("Attachments")
	f, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := a.st.CreateRecording(p.ID, "blk", "", "", wav, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = a.st.UpdateRecording(rec.ID, func(r *store.Recording) { r.Status = "ready" }); err != nil {
		t.Fatal(err)
	}
	if err := a.st.QueueUpload(rec); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "blk", Type: "recording", Data: map[string]any{"recording_id": rec.ID}},
		{ID: "f", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)

	ctx := context.Background()
	needs, err := b.st.CopyNeeds(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range needs {
		data, err := b.svc.Fetch(ctx, n.Bucket, n.Path)
		if err != nil {
			t.Fatal(err)
		}
		if n.FileID != "" {
			_, err = b.st.DownloadedFile(n.FileID, data)
		} else {
			_, err = b.st.DownloadedAudio(n.RecordingID, data)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	cp, err := b.st.CopyPage(p.ID, store.CopyOptions{Title: "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	blocks := b.st.Content(cp.ID).Children
	recID, fileID := blocks[0].Str("recording_id"), blocks[1].Str("file_id")
	syncAll(b, a)
	r, err := a.st.Recording(recID)
	if err != nil || r.AudioPath != recID+".wav" {
		t.Fatalf("A's copy of the recording %+v, %v", r, err)
	}
	if data, err := a.svc.Fetch(ctx, "recordings", r.AudioPath); err != nil || string(data) != "RIFF" {
		t.Errorf("audio %q, %v", data, err)
	}
	cf, err := a.st.File(fileID)
	if err != nil || cf.PageID != cp.ID {
		t.Fatalf("A's copy of the file %+v, %v", cf, err)
	}
	if data, err := a.svc.Fetch(ctx, "files", cf.StoragePath); err != nil || string(data) != "hello" {
		t.Errorf("file %q, %v", data, err)
	}
}

func TestICloudCopyOfAFileNotDownloadedWaits(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := a.page("Attachments")
	f, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "f", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if f, err = a.st.File(f.ID); err != nil {
		t.Fatal(err)
	}
	rel, _ := blobPath("files", f.StoragePath)
	path := filepath.Join(root, filepath.FromSlash(rel))
	hidden := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".icloud")
	if err := os.Rename(path, hidden); err != nil {
		t.Fatal(err)
	}
	cp, err := b.st.CreatePage(store.NewPageOptions{Title: "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := oldFileCopy(t, b.st, cp.ID, f).ID
	syncAll(b, a)
	if a.title(cp.ID) != "Copy" {
		t.Errorf("A has the copy as %q", a.title(cp.ID))
	}
	if !b.st.HasPending("storage", "files/"+fileID) {
		t.Fatal("the copy's file went up before it came")
	}
	if err := os.Rename(hidden, path); err != nil {
		t.Fatal(err)
	}
	syncAll(b, a)
	cf, err := a.st.File(fileID)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := a.svc.Fetch(context.Background(), "files", cf.StoragePath); err != nil || string(data) != "hello" {
		t.Errorf("file %q, %v", data, err)
	}
}

func TestICloudDeletedFileStaysForACopyThatWaits(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := a.page("Attachments")
	f, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{
		{ID: "f", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}},
	})); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if f, err = b.st.File(f.ID); err != nil {
		t.Fatal(err)
	}
	rel, _ := blobPath("files", f.StoragePath)
	path := filepath.Join(root, filepath.FromSlash(rel))
	hidden := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".icloud")
	if err := os.Rename(path, hidden); err != nil {
		t.Fatal(err)
	}
	cp, err := b.st.CreatePage(store.NewPageOptions{Title: "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := oldFileCopy(t, b.st, cp.ID, f).ID
	b.sync()
	if err := b.st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if !b.st.HasPending("storage", "files/"+fileID) || !b.svc.ICloud.Exists(rel) {
		t.Fatal("the file went before its copy went up")
	}
	if err := os.Rename(hidden, path); err != nil {
		t.Fatal(err)
	}
	b.sync()
	cf, err := b.st.File(fileID)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := b.svc.Fetch(context.Background(), "files", cf.StoragePath); err != nil || string(data) != "hello" {
		t.Errorf("the copy has %q, %v", data, err)
	}
	if b.svc.ICloud.Exists(rel) || b.st.PendingCount() != 0 {
		t.Errorf("the deleted file stayed; %d waiting", b.st.PendingCount())
	}
}

func TestICloudCopyWaitsForAnOriginalNotListedYet(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := a.page("Attachments")
	f, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks([]*doc.Node{{ID: "f", Type: "file", Data: map[string]any{"name": f.Name, "file_id": f.ID}}})); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	f, _ = a.st.File(f.ID)
	rel, _ := blobPath("files", f.StoragePath)
	path := filepath.Join(root, filepath.FromSlash(rel))
	away := filepath.Join(t.TempDir(), "away")
	if err := os.Rename(path, away); err != nil {
		t.Fatal(err)
	}
	cp, err := b.st.CreatePage(store.NewPageOptions{Title: "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := oldFileCopy(t, b.st, cp.ID, f).ID
	syncAll(b, a)
	if !b.st.HasPending("storage", "files/"+fileID) {
		t.Fatal("the copy went without its file")
	}
	if _, err := a.st.File(fileID); err == nil {
		t.Error("the copy's file reached A before its contents")
	}
	if err := os.Rename(away, path); err != nil {
		t.Fatal(err)
	}
	syncAll(b, a)
	cf, err := a.st.File(fileID)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := a.svc.Fetch(context.Background(), "files", cf.StoragePath); err != nil || string(data) != "hello" {
		t.Errorf("the copy's file %q, %v", data, err)
	}
}

func TestICloudSendingAgainKeepsAHeldChange(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p, q := a.page("Plan"), a.page("Done")
	syncAll(a, b)
	if err := b.st.MovePage(p.ID, q.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if err := a.st.RenamePage(p.ID, "Plan A"); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.cloudPull(context.Background(), a.st.DeviceID()); err != nil {
		t.Fatal(err)
	}
	if heldOf(t, a.svc, "pages", p.ID) == nil {
		t.Fatal("B's move is not held")
	}
	space, _ := a.st.Value("icloud.space")
	if err := a.svc.requeueAll(context.Background(), space); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	for _, d := range []*cloudDevice{a, b} {
		got, _ := d.st.Page(p.ID)
		if got.Title != "Plan A" || got.ParentID != q.ID {
			t.Errorf("page is %q under %q, want Plan A under %q", got.Title, got.ParentID, q.ID)
		}
	}
}

func TestICloudSendingAgainKeepsAHeldFileChange(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	folder, err := a.st.CreateFolder("Folder")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := a.st.AddFile(src, folder.ID, "", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if err := b.st.DeleteFile(f); err != nil {
		t.Fatal(err)
	}
	b.sync()
	nextStamp()
	if err := a.st.DeleteFolder(folder); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.cloudPull(context.Background(), a.st.DeviceID()); err != nil {
		t.Fatal(err)
	}
	if heldOf(t, a.svc, "files", f.ID) == nil {
		t.Fatal("B's deletion is not held")
	}
	space, _ := a.st.Value("icloud.space")
	if err := a.svc.requeueAll(context.Background(), space); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	for i, d := range []*cloudDevice{a, b} {
		if got, _ := d.st.File(f.ID); got.DeletedAt.IsZero() || got.FolderID != "" {
			t.Errorf("device %d: file in %q, deleted %v", i, got.FolderID, got.DeletedAt)
		}
	}
}

func TestICloudHeldEntriesOutlastAStop(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p, q := a.page("Plan"), a.page("Done")
	syncAll(a, b)
	if err := b.st.MovePage(p.ID, q.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if err := a.st.RenamePage(p.ID, "Plan A"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.svc.cloudPull(ctx, a.st.DeviceID()); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.cloudPush(ctx, a.st.DeviceID()); err != nil {
		t.Fatal(err)
	}
	const stopped = "stopped"
	applyingHeld = func(icloud.Entry) { panic(stopped) }
	func() {
		defer func() {
			if r := recover(); r != nil && r != stopped {
				panic(r)
			}
		}()
		_ = a.svc.applyHeld(map[string]bool{})
	}()
	applyingHeld = func(icloud.Entry) {}
	if heldOf(t, a.svc, "pages", p.ID) == nil {
		t.Fatal("the held move is gone")
	}
	a.sync()
	if got, _ := a.st.Page(p.ID); got.Title != "Plan A" || got.ParentID != q.ID {
		t.Errorf("page is %q under %q, want Plan A under %q", got.Title, got.ParentID, q.ID)
	}
	if heldOf(t, a.svc, "pages", p.ID) != nil {
		t.Error("still held once applied")
	}
}

func TestICloudAudioNotListedYetWaitsToGoUp(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Meeting")
	wav := filepath.Join(t.TempDir(), "rec.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := a.st.CreateRecording(p.ID, "blk", "", "", wav, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = a.st.UpdateRecording(rec.ID, func(r *store.Recording) { r.Status = "ready" }); err != nil {
		t.Fatal(err)
	}
	if err := a.st.QueueUpload(rec); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	br, err := b.st.Recording(rec.ID)
	if err != nil || br.AudioPath == "" || store.OnDevice(br.LocalPath) {
		t.Fatalf("recording on B %+v, %v", br, err)
	}
	rel, _ := blobPath("recordings", br.AudioPath)
	path := filepath.Join(root, filepath.FromSlash(rel))
	away := filepath.Join(t.TempDir(), "away")
	if err := os.Rename(path, away); err != nil {
		t.Fatal(err)
	}
	b.svc.SetICloud(false)
	ctx := context.Background()
	if err := b.svc.requeueRecording(ctx, br); !errors.Is(err, errNotInICloud) {
		t.Errorf("queued audio it does not have: %v", err)
	}
	if b.st.HasPending("recordings", rec.ID) {
		t.Error("the recording is queued without its audio")
	}
	if err := os.Rename(away, path); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.requeueRecording(ctx, br); err != nil {
		t.Fatal(err)
	}
	if !b.st.HasPending("recordings", rec.ID) || !b.st.HasPending("storage", "recordings/"+rec.ID) {
		t.Error("the recording is not queued with its audio")
	}
}

func TestICloudFileNotListedYetWaitsToGoUp(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := a.page("Attachments")
	f, err := a.st.AddFile(src, "", p.ID, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	f, _ = b.st.File(f.ID)
	rel, _ := blobPath("files", f.StoragePath)
	path := filepath.Join(root, filepath.FromSlash(rel))
	away := filepath.Join(t.TempDir(), "away")
	if err := os.Rename(path, away); err != nil {
		t.Fatal(err)
	}
	b.svc.SetICloud(false)
	ctx := context.Background()
	if err := b.svc.requeueFile(ctx, f, "space"); err == nil {
		t.Error("queued a file it does not have")
	}
	if b.st.HasPending("files", f.ID) {
		t.Error("the file's row is queued without its contents")
	}
	if err := os.Rename(away, path); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.requeueFile(ctx, f, "space"); err != nil {
		t.Fatal(err)
	}
	if !b.st.HasPending("files", f.ID) || !b.st.HasPending("storage", "files/"+f.ID) {
		t.Error("the file is not queued with its contents")
	}
}

func TestICloudSettledRecordingGivesWayToAFinishedOne(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Meeting")
	rec, err := a.st.CreateRecording(p.ID, "b1", "openai-compatible", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.UpdateRecording(rec.ID, func(r *store.Recording) { r.Status = "transcribing" }); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	nextStamp()
	if _, err := a.st.UpdateRecording(rec.ID, func(r *store.Recording) {
		r.Status, r.Transcript = "ready", []store.Segment{{Text: "Hello"}}
	}); err != nil {
		t.Fatal(err)
	}
	a.sync()
	nextStamp()
	if _, err := b.st.SettleRecording(rec.ID, "transcribing", false, func(r *store.Recording) { r.Status = "failed" }); err != nil {
		t.Fatal(err)
	}
	syncAll(b, a)
	for _, d := range []*cloudDevice{a, b} {
		if r, _ := d.st.Recording(rec.ID); r.Status != "ready" || len(r.Transcript) != 1 {
			t.Errorf("recording %s %v", r.Status, r.Transcript)
		}
	}
}

func TestICloudJoinedTheLosingSpace(t *testing.T) {
	rootA, rootB := cloudRoot(t), cloudRoot(t)
	a, b := newCloudDevice(t, rootA), newCloudDevice(t, rootB)
	a.page("A's")
	b.page("B's")
	a.sync()
	b.sync()
	won, lost, wonRoot, lostRoot := a, b, rootA, rootB
	if b.st.Session().SpaceID < a.st.Session().SpaceID {
		won, lost, wonRoot, lostRoot = b, a, rootB, rootA
	}
	z := newCloudDevice(t, lostRoot)
	z.sync()
	if z.st.Session().SpaceID != lost.st.Session().SpaceID {
		t.Fatal("Z did not join the space that loses")
	}
	if err := os.CopyFS(filepath.Join(wonRoot, "devices"), os.DirFS(filepath.Join(lostRoot, "devices"))); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(lostRoot, "space.json")) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wonRoot, "space 2.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	lost.svc.SetICloudRoot(wonRoot)
	z.svc.SetICloudRoot(wonRoot)
	syncAll(lost, won)
	if err := z.svc.SyncNow(); err != nil {
		t.Fatalf("Z: %v", err)
	}
	if z.st.Session().SpaceID != won.st.Session().SpaceID {
		t.Errorf("Z is in %s, not %s", z.st.Session().SpaceID, won.st.Session().SpaceID)
	}
}

func TestICloudPageFilesMergedAreNotReadAgain(t *testing.T) {
	root := cloudRoot(t)
	a, b, c := newCloudDevice(t, root), newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan", para("a1", "one"), para("b1", "two"))
	syncAll(a, b, c)
	b.edit(p.ID, "b1", "two, from B")
	syncAll(a, b, c)
	if c.text(p.ID, "b1") != "two, from B" {
		t.Fatalf("C has %q", c.text(p.ID, "b1"))
	}
	rel, err := icloud.PagePath(b.st.DeviceID(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, info.Size()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	a.edit(p.ID, "a1", "one, from A")
	a.sync()
	c.sync()
	if c.text(p.ID, "a1") != "one, from A" || c.text(p.ID, "b1") != "two, from B" {
		t.Errorf("C has %q, %q", c.text(p.ID, "a1"), c.text(p.ID, "b1"))
	}
	if w := waitingOn(t, c.svc); w[p.ID] {
		t.Errorf("the page waits on B's file: %v", w)
	}
}

func TestICloudPublishGoesOnFromTheVersionKept(t *testing.T) {
	root := cloudRoot(t)
	a := newCloudDevice(t, root)
	p := a.page("Plan", para("a1", "one"))
	a.sync()
	ahead := time.Now().Add(time.Hour).UnixMilli()
	if err := a.svc.setInt(pageVersionKey, ahead); err != nil {
		t.Fatal(err)
	}
	a.edit(p.ID, "a1", "one, edited")
	a.sync()
	rel, _ := icloud.PagePath(a.st.DeviceID(), p.ID)
	var file icloud.PageFile
	if err := a.svc.folder().ReadJSON(rel, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != ahead+1 || a.svc.intValue(pageVersionKey) != ahead+1 {
		t.Errorf("version %d, kept %d, want %d", file.Version, a.svc.intValue(pageVersionKey), ahead+1)
	}
}

func TestICloudPageFilesAreReadAgainOnceTheirBasesGo(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan", para("a1", "one"))
	syncAll(a, b)
	if b.text(p.ID, "a1") != "one" {
		t.Fatalf("B has %q", b.text(p.ID, "a1"))
	}
	b.svc.SetICloud(false)
	if err := b.st.Wipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.st.LoadSession(nil); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.EnableICloud(); err != nil {
		t.Fatal(err)
	}
	syncAll(b)
	if got := b.text(p.ID, "a1"); got != "one" {
		t.Fatalf("after joining again B has %q, waiting for %v", got, waitingOn(t, b.svc))
	}
	c := b.st.Content(p.ID)
	c.Children = append(c.Children, para("b1", "from B"))
	if _, _, err := b.st.SaveContent(p.ID, c); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if a.text(p.ID, "a1") != "one" || a.text(p.ID, "b1") != "from B" {
		t.Errorf("A has %q, %q", a.text(p.ID, "a1"), a.text(p.ID, "b1"))
	}

	if err := b.st.ForgetICloud(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.cloudRefresh(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := b.st.PeerVersion(a.st.DeviceID(), p.ID); err != nil || !ok || v == 0 {
		t.Errorf("A's version kept again: %d, %v, %v", v, ok, err)
	}
}

func TestICloudPageFileBehindItsBatchIsReadAgain(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Plan", para("a1", "one"))
	syncAll(a, b)
	rel, _ := icloud.PagePath(a.st.DeviceID(), p.ID)
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var file icloud.PageFile
	if err := a.svc.folder().ReadJSON(rel, &file); err != nil {
		t.Fatal(err)
	}
	theirs := doc.MustDecode(string(file.Content))
	theirs.Find("a1").SetDelta(doc.Delta{{Text: "two"}})
	theirs.Blocks["a1"]++
	file.Version++
	file.Content = []byte(theirs.Encode())
	if err := a.svc.folder().WriteJSON(rel, file); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.Stat(path); now.Size() != info.Size() {
		t.Fatalf("size %d, was %d", now.Size(), info.Size())
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := b.svc.setInt(wantKey(a.st.DeviceID(), p.ID), file.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.cloudRefresh(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if got := b.text(p.ID, "a1"); got != "two" {
		t.Errorf("B has %q", got)
	}
}

func TestICloudPublicLinkLeases(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Shared", para("a1", "one"))
	syncAll(a, b)
	leases := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); len(a.st.ValuesWithPrefix(linkPrefix)) != n; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("leases %v, want %d", a.st.ValuesWithPrefix(linkPrefix), n)
			}
		}
	}
	stop := a.svc.ICloudLease(p.ID)
	leases(1)
	a.sync()
	links := linksElsewhere(t, b)
	if until := links[p.ID]; len(links) != 1 || until.Before(time.Now().Add(iCloudLinkLease-time.Minute)) {
		t.Errorf("the other device reads %v", links)
	}
	if own := linksElsewhere(t, a); len(own) != 0 {
		t.Errorf("the device serving the link reads its own: %v", own)
	}
	rel, err := icloud.DevicePath(a.st.DeviceID())
	if err != nil {
		t.Fatal(err)
	}
	var before icloud.Device
	if err := a.svc.folder().ReadJSON(rel, &before); err != nil || before.ID != a.st.DeviceID() || before.Format != icloud.Format {
		t.Errorf("device.json as read before: %+v, %v", before, err)
	}

	again := a.svc.ICloudLease(p.ID)
	leases(2)
	stop()
	stop()
	leases(1)
	a.sync()
	if links := linksElsewhere(t, b); len(links) != 1 {
		t.Errorf("with the link started again, the other device reads %v", links)
	}
	again()
	leases(0)
	a.sync()
	if links := linksElsewhere(t, b); len(links) != 0 {
		t.Errorf("once stopped, the other device reads %v", links)
	}
	b.svc.cloudOn.Store(false)
	if links := linksElsewhere(t, b); links != nil {
		t.Errorf("with iCloud sync off: %v", links)
	}
}

func TestICloudPublicLinkEndsWithICloudOff(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Shared", para("a1", "one"))
	syncAll(a, b)
	stop := a.svc.ICloudLease(p.ID)
	for deadline := time.Now().Add(5 * time.Second); len(a.st.ValuesWithPrefix(linkPrefix)) != 1; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no lease")
		}
	}
	a.sync()
	if links := linksElsewhere(t, b); len(links) != 1 {
		t.Fatalf("the other device reads %v", links)
	}
	a.svc.SetICloud(false)
	stop()
	for deadline := time.Now().Add(5 * time.Second); len(linksElsewhere(t, b)) != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("with iCloud sync off, the other device still reads %v", linksElsewhere(t, b))
		}
	}
}

func TestICloudPublicLinkLeaseRunsOut(t *testing.T) {
	d := newCloudDevice(t, cloudRoot(t))
	now := time.Now()
	for key, v := range map[string]string{
		linkPrefix + "old":  "p1|" + strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10),
		linkPrefix + "live": "p2|" + strconv.FormatInt(now.Add(time.Hour).UnixMilli(), 10),
		linkPrefix + "bad":  "p3",
	} {
		if err := d.st.SetValue(key, v); err != nil {
			t.Fatal(err)
		}
	}
	links := d.svc.ownLinks(now)
	if len(links) != 1 || links["p2"].IsZero() {
		t.Errorf("links %v", links)
	}
	if left := d.st.ValuesWithPrefix(linkPrefix); len(left) != 1 || left["live"] == "" {
		t.Errorf("leases kept %v", left)
	}
}

func otherSpacePage(t *testing.T, d *cloudDevice, title string) store.Page {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := doc.NewRowID()
	if err := d.st.PutRemotePage(map[string]any{"id": id, "space_id": doc.NewRowID(), "title": title, "created_at": now, "updated_at": now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.st.SaveContent(id, &doc.Content{Children: []*doc.Node{para("s1", "not yours")}}); err != nil {
		t.Fatal(err)
	}
	p, err := d.st.Page(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestICloudLeavesOtherSpacesOut(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	a.svc.KeepAudio = func() string { return "until_uploaded" }
	shared := otherSpacePage(t, a, "Someone else's")
	own := a.page("Mine")
	comment, err := a.st.AddComment(shared.ID, "s1", "Noted", "", "", "user", "You")
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "plan.pdf")
	if err := os.WriteFile(src, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := a.st.AddFile(src, "", shared.ID, "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	wav := filepath.Join(t.TempDir(), "rec.wav")
	if err := os.WriteFile(wav, []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := a.st.CreateRecording(shared.ID, "s1", "", "", wav, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.UpdateRecording(rec.ID, func(r *store.Recording) { r.Status = "ready" }); err != nil {
		t.Fatal(err)
	}
	syncAll(a, b)
	if b.title(own.ID) != "Mine" {
		t.Fatalf("B has %q of A's own page", b.title(own.ID))
	}
	if _, err := b.st.Page(shared.ID); !store.IsNotFound(err) {
		t.Errorf("B has the other account's page: %v", err)
	}
	if _, err := b.st.Comment(comment.ID); !store.IsNotFound(err) {
		t.Errorf("B has its comment: %v", err)
	}
	if _, err := b.st.File(file.ID); !store.IsNotFound(err) {
		t.Errorf("B has its file: %v", err)
	}
	if _, err := b.st.Recording(rec.ID); !store.IsNotFound(err) {
		t.Errorf("B has its recording: %v", err)
	}
	rel, _ := icloud.PagePath(a.st.DeviceID(), shared.ID)
	if a.svc.ICloud.Exists(rel) {
		t.Error("its content is in iCloud Drive")
	}
	if blob, _ := icloud.FileBlobPath(file.ID, file.Name); a.svc.ICloud.Exists(blob) {
		t.Error("its file is in iCloud Drive")
	}
	if blob, _ := icloud.RecordingBlobPath(rec.ID); a.svc.ICloud.Exists(blob) {
		t.Error("its audio is in iCloud Drive")
	}
	if p, err := a.st.Page(shared.ID); err != nil || p.SpaceID != shared.SpaceID {
		t.Errorf("A's copy: %+v, %v", p, err)
	}
	if f, err := a.st.File(file.ID); err != nil || f.SpaceID != file.SpaceID {
		t.Errorf("A's file: %+v, %v", f, err)
	}
}

func TestICloudRefusesRowsOfOtherSpaces(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	syncAll(a, b)
	theirs := otherSpacePage(t, b, "Shared with B")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stray := doc.NewRowID()
	comment := doc.NewRowID()
	at := time.Now().UnixMilli()
	row := func(table, id string, r map[string]any) icloud.Entry {
		r["id"], r["created_at"], r["updated_at"] = id, now, now
		return icloud.Entry{Kind: "row", Table: table, ID: id, At: at, By: a.st.DeviceID(), Row: r}
	}
	batch := icloud.Batch{Format: icloud.Format, Device: a.st.DeviceID(), Seq: a.svc.intValue("icloud.seq") + 1, WrittenAt: time.Now().UTC(), Entries: []icloud.Entry{
		row("pages", stray, map[string]any{"space_id": doc.NewRowID(), "title": "Not yours"}),
		row("pages", theirs.ID, map[string]any{"space_id": a.st.Session().SpaceID, "title": "Renamed"}),
		row("comments", comment, map[string]any{"page_id": theirs.ID, "block_id": "s1", "body": "Hi", "author_kind": "user"}),
		{Kind: "content", PageID: theirs.ID, Version: at},
	}}
	raw := (&doc.Content{Children: []*doc.Node{para("s1", "not yours"), para("s2", "added")}}).Encode()
	rel, _ := icloud.PagePath(a.st.DeviceID(), theirs.ID)
	if err := a.svc.ICloud.WriteJSON(rel, icloud.PageFile{Format: icloud.Format, PageID: theirs.ID, Version: at, Content: json.RawMessage(raw)}); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.writeBatch(a.st.DeviceID(), batch); err != nil {
		t.Fatal(err)
	}
	b.sync()
	if text := b.text(theirs.ID, "s2"); text != "" {
		t.Errorf("B's page of another space has %q from A", text)
	}
	if _, err := b.st.Page(stray); !store.IsNotFound(err) {
		t.Errorf("B stored another space's page: %v", err)
	}
	if p, err := b.st.Page(theirs.ID); err != nil || p.Title != "Shared with B" || p.SpaceID != theirs.SpaceID {
		t.Errorf("B's page of another space: %+v, %v", p, err)
	}
	if _, err := b.st.Comment(comment); !store.IsNotFound(err) {
		t.Errorf("B stored a comment on another space's page: %v", err)
	}
	if cur := b.svc.intValue("icloud.cursor." + a.st.DeviceID()); cur != batch.Seq {
		t.Errorf("B is at batch %d of A's, not past %d", cur, batch.Seq)
	}
}

func linksElsewhere(t *testing.T, d *cloudDevice) map[string]time.Time {
	t.Helper()
	links, err := d.svc.ICloudLinksElsewhere()
	if err != nil {
		t.Fatalf("links unknown: %v", err)
	}
	return links
}

func TestICloudLinksOfADeviceNotReadAreUnknown(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	syncAll(a, b)
	rel, err := icloud.DevicePath(a.st.DeviceID())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	good, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.ICloudLinksElsewhere(); err != nil {
		t.Fatalf("with device.json read: %v", err)
	}
	unknown := func(how string) {
		t.Helper()
		if links, err := b.svc.ICloudLinksElsewhere(); err == nil {
			t.Errorf("%s: links %v, known", how, links)
		}
	}
	for how, data := range map[string]string{"cut short": `{"format": 1, "id": "`, "newer": `{"format": 99}`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		unknown(how)
		old := time.Now().Add(-iCloudLinkLease - 2*iCloudLinkRenew - time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		if links, err := b.svc.ICloudLinksElsewhere(); err != nil || len(links) != 0 {
			t.Errorf("%s, written long ago: %v, %v", how, links, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".device.json.icloud"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unknown("not downloaded")
	if err := os.Remove(filepath.Join(filepath.Dir(path), ".device.json.icloud")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	devices := filepath.Join(root, "devices")
	if err := os.Chmod(devices, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(devices, 0o750) }) // #nosec G302
	if runtime.GOOS != "windows" {
		unknown("devices not listed")
	}
}

func TestICloudLeaseIsWrittenThoughThePassFails(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Shared", para("a1", "one"))
	syncAll(a, b)
	until := time.Now().Add(iCloudLinkLease)
	if err := a.st.SetValue(linkPrefix+"lease", p.ID+"|"+strconv.FormatInt(until.UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.setInt("icloud.device_at", 0); err != nil {
		t.Fatal(err)
	}
	a.edit(p.ID, "a1", "two")
	stopped, stop := context.WithCancel(context.Background())
	stop()
	if err := a.svc.cloudPass(stopped); err == nil {
		t.Fatal("the pass went on, stopped")
	}
	if links := linksElsewhere(t, b); links[p.ID].IsZero() {
		t.Errorf("the other device reads %v", links)
	}
}

func idsOf(t *testing.T, svc *Service, key string) map[string]bool {
	t.Helper()
	ids, err := svc.ids(key)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func waitingOn(t *testing.T, svc *Service) map[string]bool {
	t.Helper()
	return idsOf(t, svc, "sync.content_waiting")
}

func heldOf(t *testing.T, svc *Service, table, id string) []icloud.Entry {
	t.Helper()
	held, err := svc.held(table, id)
	if err != nil {
		t.Fatal(err)
	}
	return held
}
