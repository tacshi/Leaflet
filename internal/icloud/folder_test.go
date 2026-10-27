package icloud

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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
	if err := f.ReadJSON("b.json", &Device{}); !errors.Is(err, ErrNotReady) {
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
	startDownload = func(p string) error { asked = p; return nil }
	t.Cleanup(func() { startDownload = func(string) error { return nil } })
	rel, _ := BatchPath(dev, 1)
	if err := f.ReadJSON(rel, &Batch{}); !errors.Is(err, ErrNotReady) || asked != f.abs(rel) {
		t.Errorf("placeholder: %v, asked for %q", err, asked)
	}
	if !f.Exists(rel) {
		t.Error("a placeholder's file does not exist")
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
