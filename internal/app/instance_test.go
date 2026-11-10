package app

import (
	"testing"
)

func standIn(t *testing.T, locked bool) (asked, launched *int) {
	t.Helper()
	lock, run := singleInstance, launch
	t.Cleanup(func() { singleInstance, launch = lock, run })
	asked, launched = new(int), new(int)
	singleInstance = func() bool { *asked++; return locked }
	launch = func(*App) { *launched++ }
	return asked, launched
}

func TestSecondInstanceEnds(t *testing.T) {
	t.Setenv("LEAFLET_DATA_DIR", "")
	asked, launched := standIn(t, false)
	Run()
	if *asked != 1 || *launched != 0 {
		t.Errorf("asked %d, launched %d", *asked, *launched)
	}
}

func TestFirstInstanceRuns(t *testing.T) {
	t.Setenv("LEAFLET_DATA_DIR", "")
	asked, launched := standIn(t, true)
	Run()
	if *asked != 1 || *launched != 1 {
		t.Errorf("asked %d, launched %d", *asked, *launched)
	}
}

func TestOwnDataFolderRunsBeside(t *testing.T) {
	t.Setenv("LEAFLET_DATA_DIR", t.TempDir())
	asked, launched := standIn(t, false)
	Run()
	if *asked != 0 || *launched != 1 {
		t.Errorf("asked %d, launched %d", *asked, *launched)
	}
}
