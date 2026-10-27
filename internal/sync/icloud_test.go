package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if err := a.st.RenamePage(p.ID, "Plan 2"); err != nil {
		t.Fatal(err)
	}
	a.sync()
	old := a.st.DeviceID()
	// A backup of A from before its last sync, restored: its log goes on
	// past what the device knows.
	if err := a.st.SetValue("icloud.seq", "1"); err != nil {
		t.Fatal(err)
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
