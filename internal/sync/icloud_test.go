package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// cloudDevice is a device that syncs through a folder standing in for
// iCloud Drive's.
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

// syncAll syncs the devices in turn, twice round, as a pass on each
// brings what the others sent.
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

// edit changes a block's text, as typing does.
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

// A page's tags go to the other devices with its row.
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
	time.Sleep(2 * time.Millisecond)
	if err := b.st.RenamePage(p.ID, "Plan B"); err != nil {
		t.Fatal(err)
	}
	// B's rename was waiting when A's came: it still wins, being newer
	// than what B had seen, and the devices agree.
	syncAll(b, a)
	if a.title(p.ID) != "Plan B" || b.title(p.ID) != "Plan B" {
		t.Errorf("titles %q / %q", a.title(p.ID), b.title(p.ID))
	}
	// A change made here while another device's waited wins, whatever
	// the clocks: its version comes after the one it passed over.
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

// A row goes as the columns changed on a device: an offline rename on one
// undoes no move or deletion made on another, whichever came first or
// waited.
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
	time.Sleep(2 * time.Millisecond)
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
	// A rename still waiting here when the move comes keeps its title, and
	// takes the move once it has gone.
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
	// The same block on both: the later edit wins on both, and only the
	// device whose edit lost hears of it.
	a.edit(p.ID, "x", "mine")
	time.Sleep(2 * time.Millisecond)
	b.edit(p.ID, "x", "theirs")
	syncAll(a, b)
	if a.text(p.ID, "x") != "theirs" || b.text(p.ID, "x") != "theirs" {
		t.Errorf("x is %q / %q", a.text(p.ID, "x"), b.text(p.ID, "x"))
	}
	if a.conflicts() != 1 || b.conflicts() != 0 {
		t.Errorf("conflicts: A %d, B %d", a.conflicts(), b.conflicts())
	}
}

func TestICloudRelayedEditIsNoConflict(t *testing.T) {
	root := cloudRoot(t)
	a, b, c := newCloudDevice(t, root), newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"))
	syncAll(a, b, c)
	a.edit(p.ID, "x", "a")
	a.sync()
	b.sync() // B takes A's edit
	time.Sleep(2 * time.Millisecond)
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
	// iCloud has not downloaded A's batch to B's device yet.
	me := a.st.DeviceID()
	rel, _ := icloud.BatchPath(me, 1)
	path := filepath.Join(root, filepath.FromSlash(rel))
	hidden := filepath.Join(filepath.Dir(path), ".0000000000000001.json.icloud")
	data, err := os.ReadFile(path) // #nosec G304 -- a file of the test's own folder.
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
	// A device that comes now reads the snapshot.
	c := newCloudDevice(t, root)
	syncAll(c)
	if c.title(p.ID) != a.title(p.ID) || c.text(p.ID, "x") != a.text(p.ID, "x") {
		t.Errorf("late device has %q %q, A %q %q", c.title(p.ID), c.text(p.ID, "x"), a.title(p.ID), a.text(p.ID, "x"))
	}
}

// A snapshot keeps the change to each column: a device that had the row
// takes from it the tags set before the renames that came after.
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
	// Turned off and on, the device joins the folder as it is now.
	a.svc.SetICloud(false)
	if err := a.svc.EnableICloud(); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.SyncNow(); err != nil {
		t.Errorf("joined again: %v", err)
	}
}

func TestICloudTwoDevicesMadeTheSpace(t *testing.T) {
	// A and B each made the folder at once: iCloud keeps both devices'
	// folders, and B's space.json as a copy beside A's.
	root, rootB := cloudRoot(t), cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, rootB)
	pa := a.page("A's")
	pb := b.page("B's")
	a.sync()
	b.sync()
	if err := os.CopyFS(filepath.Join(root, "devices"), os.DirFS(filepath.Join(rootB, "devices"))); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rootB, "space.json")) // #nosec G304 -- a file of the test's own folder.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "space 2.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b.svc.ICloud.Root = root
	syncAll(a, b, a)
	if a.st.Session().SpaceID != b.st.Session().SpaceID {
		t.Fatalf("spaces %s and %s", a.st.Session().SpaceID, b.st.Session().SpaceID)
	}
	for _, d := range []*cloudDevice{a, b} {
		if d.title(pa.ID) != "A's" || d.title(pb.ID) != "B's" {
			t.Errorf("titles %q %q", d.title(pa.ID), d.title(pb.ID))
		}
	}
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
	// A backup of A from before its last sync, restored: its log goes on
	// past what the device knows.
	for k, v := range map[string]string{"icloud.seq": "1", "icloud.writing": writing} {
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

// iCloud may bring a batch before the page file it names, in either
// order: the page waits until the version the batch named is there.
func TestICloudWaitsForThePageFileABatchNames(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"))
	syncAll(a, b)
	rel, _ := icloud.PagePath(a.st.DeviceID(), p.ID)
	path := filepath.Join(root, filepath.FromSlash(rel))
	old, err := os.ReadFile(path) // #nosec G304 -- a file of the test's own folder.
	if err != nil {
		t.Fatal(err)
	}
	a.edit(p.ID, "x", "xa")
	a.sync()
	fresh, err := os.ReadFile(path) // #nosec G304 -- a file of the test's own folder.
	if err != nil {
		t.Fatal(err)
	}
	// The older page file is still there when the batch comes.
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
	// A new page whose file is not there yet when its batch comes.
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

// A batch that could not be written leaves its page to go again, with
// the notice the other devices need.
func TestICloudSendsAgainAfterABatchFailed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a folder made read-only stops no one running as root")
	}
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	p := a.page("Notes", para("x", "x0"))
	syncAll(a, b)
	logDir := filepath.Join(root, "devices", a.st.DeviceID(), "log")
	if err := os.Chmod(logDir, 0o500); err != nil { // #nosec G302 -- a test's own folder, made read-only.
		t.Fatal(err)
	}
	a.edit(p.ID, "x", "xa")
	if err := a.svc.SyncNow(); err == nil {
		t.Fatal("wrote a batch to a folder it may not write to")
	}
	if err := os.Chmod(logDir, 0o700); err != nil { // #nosec G302 -- a test's own folder.
		t.Fatal(err)
	}
	syncAll(a, b)
	if b.text(p.ID, "x") != "xa" {
		t.Errorf("B has %q", b.text(p.ID, "x"))
	}
}

// A compaction cut short leaves batches before its snapshot: a reader
// goes on from the snapshot.
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
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) // #nosec G304 -- a file of the test's own folder.
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
	// It removed batches 1 and 2 only.
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

// Joining a folder afresh sends everything again: comments and
// recordings too, and files and audio this device has only in the
// folder it leaves.
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
	// The folder goes; turned off and on, A makes a new one.
	moved := filepath.Join(t.TempDir(), "old")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o750); err != nil {
		t.Fatal(err)
	}
	// What only the old folder had stays reachable while A moves it.
	a.svc.ICloud.Root = moved
	a.svc.SetICloud(false)
	if !a.svc.CanFetch() {
		t.Fatal("audio in the folder left is out of reach")
	}
	if data, err := a.svc.Fetch(context.Background(), "recordings", rec.ID+".wav"); err != nil || string(data) != "RIFF" {
		t.Fatalf("audio %q, %v", data, err)
	}
	space, _ := a.st.Value("icloud.space")
	if err := a.svc.requeueAll(space); err != nil {
		t.Fatal(err)
	}
	a.svc.ICloud.Root = root
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

// A device joins the folder only once everything it had is queued for
// it: one that could not queue it all joins again on the next pass, so
// what it had still reaches the other devices.
func TestICloudJoinsOnceAllIsQueued(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	a.sync()
	f, err := b.st.CreateFolder("Work")
	if err != nil {
		t.Fatal(err)
	}
	// The disk refuses the folder's entry, as one that is full would.
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
	if err := b.svc.joinSpace(""); !errors.Is(err, errNotRequeued) {
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

// A file this device has only in iCloud Drive, not downloaded yet when
// everything is sent again, waits with its old place, and goes once it
// comes.
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

	// Signing in turns iCloud sync off, and the file goes up from here.
	a.svc.SetICloud(false)
	const space = "00000000-0000-0000-0000-0000000000a2"
	sess := a.st.Session()
	sess.SpaceID = space
	a.st.SetSession(sess)
	if err := a.svc.requeueAll(space); err == nil {
		t.Error("requeueAll said all went")
	}
	if got := file(); got.StoragePath != f.StoragePath || a.st.HasPending("files", f.ID) {
		t.Errorf("file moved before it came: %+v", got)
	}

	if err := os.Rename(hidden, path); err != nil {
		t.Fatal(err)
	}
	if err := a.svc.retryMoves(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := file()
	if got.StoragePath != space+"/"+f.ID+"/"+f.Name || !onDevice(got.LocalPath) {
		t.Errorf("file %+v", got)
	}
	if !a.st.HasPending("files", f.ID) || !a.st.HasPending("storage", "files/"+f.ID) {
		t.Error("file not queued once it came")
	}
	if len(a.svc.ids(movesKey)) != 0 {
		t.Errorf("still waiting for %v", a.svc.ids(movesKey))
	}
}

// A device that sends everything again to iCloud Drive leaves there what
// it has only there: a file not downloaded does not hold up the pass.
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
	// A's last batch was cut short: it sends everything again.
	batch, _ := icloud.BatchPath(a.st.DeviceID(), a.svc.intValue("icloud.seq"))
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(batch)), []byte(`{"format":1,"entr`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.svc.ownLogChecked = false
	if err := a.st.RenamePage(p.ID, "Attachments 2"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	if len(a.svc.ids(movesKey)) != 0 {
		t.Errorf("waits for %v", a.svc.ids(movesKey))
	}
	if got, _ := a.st.File(f.ID); onDevice(got.LocalPath) {
		t.Error("the file came from iCloud Drive to go back there")
	}
	b.sync()
	if b.title(p.ID) != "Attachments 2" {
		t.Errorf("B has %q", b.title(p.ID))
	}
}

// A device that stopped between writing a batch and keeping its seq finds
// the batch its own, not one another device wrote under its ID.
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

// A batch a crash cut short holds up no device: its writer writes it again
// and sends everything once it runs again.
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
	// A runs again, before iCloud has brought the batch back: it reads it
	// once it comes.
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

// A batch of another device's that stays cut short, with batches after
// it, is passed over in the end, so it does not hold up the rest.
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

// A deleted file's copy leaves the folder once the batch with its
// deletion is written, and leaves each device that reads the batch.
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

// A folder deleted on a device that has not yet heard of a deletion of a
// file in it moves the file to the top level, and the file stays deleted:
// only the move goes.
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
	time.Sleep(3 * time.Millisecond)
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

// Sending everything again queues each file as stored once those before
// it have come from iCloud Drive: a file deleted while it waited for one
// stays deleted.
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
		time.Sleep(3 * time.Millisecond)
		return f
	}
	// Newest first: the file that waits goes before the other.
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
	if err := a.svc.requeueAll("00000000-0000-0000-0000-0000000000a2"); err == nil {
		t.Error("requeueAll said all went")
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if got, _ := a.st.File(kept.ID); got.DeletedAt.IsZero() {
		t.Error("the file deleted meanwhile came back")
	}
}

// A change held for a row that then does not apply is held again, and
// does not hold up the other devices' batches.
func TestICloudHeldChangeThatFailsLetsPullsGoOn(t *testing.T) {
	root := cloudRoot(t)
	a, b := newCloudDevice(t, root), newCloudDevice(t, root)
	syncAll(a, b)
	// A row B cannot read, so the change held for it does not apply.
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
	if len(b.svc.held("comments", id)) != 1 {
		t.Error("the change that did not apply is not held again")
	}
}

// Two devices that move pages inside each other at once would leave them
// in a loop, each under the other: both put the page of it with the least
// ID back at the top level, and keep the other under it.
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

// A page copied on a device that has its file and audio only in iCloud
// Drive gets them as its own there, from the originals'.
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

	cp, err := b.st.CopyPage(p.ID, store.CopyOptions{Title: "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	blocks := b.st.Content(cp.ID).Children
	recID, fileID := blocks[0].Str("recording_id"), blocks[1].Str("file_id")
	syncAll(b, a)
	ctx := context.Background()
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

// A copy whose file iCloud has not downloaded yet goes up on a later pass;
// the rest of the pass goes meanwhile.
func TestICloudCopyOfAFileNotDownloadedWaits(t *testing.T) {
	old := uploadFetchWait
	uploadFetchWait = 50 * time.Millisecond
	t.Cleanup(func() { uploadFetchWait = old })
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
	cp, err := b.st.CopyPage(p.ID, store.CopyOptions{Title: "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := b.st.Content(cp.ID).Children[0].Str("file_id")
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

// A file deleted while a copy of it waits to go up from it stays in the
// folder until the copy has gone up.
func TestICloudDeletedFileStaysForACopyThatWaits(t *testing.T) {
	old := uploadFetchWait
	uploadFetchWait = 50 * time.Millisecond
	t.Cleanup(func() { uploadFetchWait = old })
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
	cp, err := b.st.CopyPage(p.ID, store.CopyOptions{Title: "Copy"})
	if err != nil {
		t.Fatal(err)
	}
	fileID := b.st.Content(cp.ID).Children[0].Str("file_id")
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
