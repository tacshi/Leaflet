package icloud

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const (
	dev  = "0b1c2d3e-0000-4000-8000-000000000001"
	page = "0b1c2d3e-0000-4000-8000-0000000000aa"
)

func folder(t *testing.T) Folder {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Leaflet")
	f := Folder{Root: root, Temp: filepath.Join(t.TempDir(), "tmp")}
	if err := f.Check(true); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestWriteAndRead(t *testing.T) {
	f := folder(t)
	path, err := PagePath(dev, page)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.WriteJSON(path, PageFile{Format: Format, PageID: page, Version: 3}); err != nil {
		t.Fatal(err)
	}
	var p PageFile
	if err := f.ReadJSON(path, &p); err != nil || p.Version != 3 {
		t.Fatalf("read %+v, %v", p, err)
	}
	// Written whole: nothing is left beside it, nor in Temp.
	entries, _ := os.ReadDir(filepath.Dir(f.abs(path)))
	if len(entries) != 1 {
		t.Errorf("left %d files", len(entries))
	}
	if left, _ := os.ReadDir(f.Temp); len(left) != 0 {
		t.Errorf("left %d files in Temp", len(left))
	}
}

func TestReadRefusesNewerAndHalfWrittenFiles(t *testing.T) {
	f := folder(t)
	if err := os.WriteFile(f.abs("a.json"), []byte(`{"format":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ReadJSON("a.json", &Device{}); !errors.Is(err, ErrNewerFormat) {
		t.Errorf("newer: %v", err)
	}
	if err := os.WriteFile(f.abs("b.json"), []byte(`{"format":1,"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ReadJSON("b.json", &Device{}); !errors.Is(err, ErrNotReady) || !errors.Is(err, ErrDamaged) {
		t.Errorf("half written: %v", err)
	}
	if err := f.ReadJSON("c.json", &Device{}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

func TestBatchesListsPlaceholdersAndSpotsCopies(t *testing.T) {
	f := folder(t)
	dir := filepath.Join(f.Root, "devices", dev, "log")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"0000000000000002.json", ".0000000000000001.json.icloud", ".0000000000000003.json.x.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seqs, copies, err := f.Batches(dev)
	if err != nil || !reflect.DeepEqual(seqs, []int64{1, 2}) || copies {
		t.Fatalf("seqs %v copies %v err %v", seqs, copies, err)
	}
	// A batch iCloud has not downloaded is not ready, and is asked for.
	asked := ""
	was := StartDownload
	StartDownload = func(p string) error { asked = p; return nil }
	t.Cleanup(func() { StartDownload = was })
	rel, _ := BatchPath(dev, 1)
	if err := f.ReadJSON(rel, &Batch{}); !errors.Is(err, ErrNotReady) || asked != f.abs(rel) {
		t.Errorf("placeholder: %v, asked for %q", err, asked)
	}
	if !f.Exists(rel) {
		t.Error("a placeholder's file does not exist")
	}
	// Download asks for it too, and not for a file downloaded or missing.
	asked = ""
	f.Download(rel)
	if asked != f.abs(rel) {
		t.Errorf("Download asked for %q", asked)
	}
	asked = ""
	for _, seq := range []int64{2, 9} {
		other, _ := BatchPath(dev, seq)
		f.Download(other)
	}
	if asked != "" {
		t.Errorf("Download asked for %q, downloaded or missing", asked)
	}
	if err := os.WriteFile(filepath.Join(dir, "0000000000000002 2.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, copies, _ := f.Batches(dev); !copies {
		t.Error("a copy iCloud made went unseen")
	}
}

func TestReadSpacePicksTheLowest(t *testing.T) {
	f := folder(t)
	if _, _, err := f.ReadSpace(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no space.json: %v", err)
	}
	low, high := "00000000-0000-4000-8000-000000000001", "ffffffff-0000-4000-8000-000000000001"
	if err := f.WriteJSON("space.json", Space{Format: Format, SpaceID: high}); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteJSON("space 2.json", Space{Format: Format, SpaceID: low}); err != nil {
		t.Fatal(err)
	}
	s, losers, err := f.ReadSpace()
	if err != nil || s.SpaceID != low || !reflect.DeepEqual(losers, []string{"space.json"}) {
		t.Errorf("space %+v losers %v err %v", s, losers, err)
	}
}

func TestPathsRefuseWhatIsNoID(t *testing.T) {
	if _, err := PagePath(dev, "../x"); err == nil {
		t.Error("a page ID with a path in it")
	}
	if _, err := FileBlobPath(page, "../secret"); err == nil {
		t.Error("a file name with a path in it")
	}
	if _, err := FileBlobPath(page, "notes.pdf"); err != nil {
		t.Error(err)
	}
}

func TestCheck(t *testing.T) {
	f := Folder{Root: filepath.Join(t.TempDir(), "none", "Leaflet")}
	if err := f.Check(true); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no iCloud Drive: %v", err)
	}
	f = Folder{Root: filepath.Join(t.TempDir(), "Leaflet")}
	if err := f.Check(false); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no Leaflet folder: %v", err)
	}
	if err := f.Check(true); err != nil {
		t.Errorf("made: %v", err)
	}
}

// A file whose name is as long as the disk takes goes in, though the
// name it is written under first is longer.
func TestCopyInTakesALongName(t *testing.T) {
	f := folder(t)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := FileBlobPath(page, strings.Repeat("n", 240)+".pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.CopyIn(rel, src); err != nil {
		t.Fatal(err)
	}
	if data, err := f.Read(rel); err != nil || string(data) != "hello" {
		t.Errorf("copied %q, %v", data, err)
	}
}

// A copy whose first try fails, as where Temp is on another disk, is
// written whole by the second; one cut short before is written again.
func TestCopyInTriesAgainWithTheWholeFile(t *testing.T) {
	f := folder(t)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := rename
	rename = func(from, to string) error {
		if filepath.Dir(from) == f.Temp {
			return errors.New("cross-device link")
		}
		return old(from, to)
	}
	t.Cleanup(func() { rename = old })
	rel, _ := FileBlobPath(page, "notes.txt")
	if err := f.CopyIn(rel, src); err != nil {
		t.Fatal(err)
	}
	if data, err := f.Read(rel); err != nil || string(data) != "hello" {
		t.Fatalf("copied %q, %v", data, err)
	}
	if err := os.WriteFile(f.abs(rel), []byte("he"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.CopyIn(rel, src); err != nil {
		t.Fatal(err)
	}
	if data, err := f.Read(rel); err != nil || string(data) != "hello" {
		t.Errorf("a copy cut short stayed: %q, %v", data, err)
	}
}

// A placeholder of a file that starts with a dot is left out as the file
// is, and a file listed both as itself and as its placeholder, as while
// iCloud brings it, is listed once: neither reads as a copy iCloud made.
func TestBatchesPassOverDotPlaceholders(t *testing.T) {
	f := folder(t)
	dir := filepath.Join(f.Root, "devices", dev, "log")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"0000000000000001.json", "0000000000000003.json", ".0000000000000003.json.icloud",
		"..0000000000000004.json.x.tmp.icloud", ".icloud", "..icloud"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seqs, copies, err := f.Batches(dev)
	if err != nil || !reflect.DeepEqual(seqs, []int64{1, 3}) || copies {
		t.Errorf("seqs %v copies %v err %v", seqs, copies, err)
	}
}

// A file made only where none is fails, leaving the one there, downloaded
// or not.
func TestCreateJSONLeavesAFileThere(t *testing.T) {
	f := folder(t)
	if err := f.CreateJSON(SpacePath, Space{Format: Format, SpaceID: page}); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateJSON(SpacePath, Space{Format: Format, SpaceID: dev}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("made over one there: %v", err)
	}
	if s, err := f.SpaceOf(SpacePath); err != nil || s.SpaceID != page {
		t.Errorf("space %+v, %v", s, err)
	}
	if err := os.WriteFile(f.abs(placeholder("other.json")), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateJSON("other.json", Space{Format: Format}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("made over one not downloaded: %v", err)
	}
	if left, _ := os.ReadDir(f.Temp); len(left) != 0 {
		t.Errorf("left %d files in Temp", len(left))
	}
}

// Every system with iCloud Drive can hold a file's name in the folder; a
// copy an earlier Leaflet kept by the name it had is still found.
func TestFileBlobsHaveNamesWindowsHolds(t *testing.T) {
	for in, want := range map[string]string{"Q3: report.pdf": "Q3_ report.pdf", "aux.txt": "_aux.txt", "LPT¹": "_LPT¹",
		"notes.pdf ": "notes.pdf", `a\b`: "a_b", "fine.pdf": "fine.pdf", "COM10": "COM10"} {
		if got := PortableName(in); got != want || PortableName(got) != got {
			t.Errorf("PortableName(%q) = %q, want %q", in, got, want)
		}
	}
	f := folder(t)
	rel, err := FileBlobPath(page, "Q3: report.pdf")
	if err != nil || rel != "blobs/files/"+page+"/Q3_ report.pdf" {
		t.Fatalf("path %q, %v", rel, err)
	}
	if runtime.GOOS == "windows" {
		return // no copy has that name there: a ":" names a part of a file
	}
	old := f.abs("blobs/files/" + page + "/Q3: report.pdf")
	if err := os.MkdirAll(filepath.Dir(old), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("q3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := f.Read(rel); err != nil || string(data) != "q3" || !f.Exists(rel) {
		t.Errorf("read %q, %v", data, err)
	}
}

// A file's copy goes with the copies iCloud made of it beside it.
func TestRemoveBlobTakesItsCopies(t *testing.T) {
	f := folder(t)
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, _ := FileBlobPath(page, "notes.txt")
	if err := f.CopyIn(rel, src); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(f.abs(rel)), "notes 2.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveBlob(rel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(f.abs(rel))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("its folder stayed: %v", err)
	}
	if err := f.RemoveBlob(rel); err != nil {
		t.Errorf("removed again: %v", err)
	}
}
