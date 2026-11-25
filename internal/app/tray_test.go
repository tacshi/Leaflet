package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"leaflet/locales"
)

func standInForUpdates(t *testing.T, look func(context.Context) (*newerVersion, error)) (a *App, posted chan func()) {
	t.Helper()
	old, every := lookForUpdate, updateEvery
	t.Cleanup(func() { lookForUpdate, updateEvery = old, every })
	lookForUpdate = look
	posted = make(chan func(), 64)
	a = &App{updatable: true}
	a.post = func(fn func()) { posted <- fn }
	return a, posted
}

func TestLooksForUpdatesWhileRunning(t *testing.T) {
	looks := make(chan struct{}, 8)
	a, posted := standInForUpdates(t, func(context.Context) (*newerVersion, error) {
		looks <- struct{}{}
		return nil, nil
	})
	updateEvery = time.Millisecond
	a.keepLookingForUpdates()
	deadline := time.After(5 * time.Second)
	for n := 0; n < 3; {
		select {
		case <-looks:
			n++
		case fn := <-posted:
			fn()
		case <-deadline:
			t.Fatalf("%d looks for a newer version, want 3", n)
		}
	}
	if len(a.toasts) != 0 {
		t.Errorf("looking on its own said %+v", a.toasts)
	}
}

func TestNoUpdatesNoTimer(t *testing.T) {
	(&App{}).keepLookingForUpdates()
}

func TestUpdateThatFailsToInstallSaysSo(t *testing.T) {
	version := "2.0.0"
	a, posted := standInForUpdates(t, func(context.Context) (*newerVersion, error) {
		return &newerVersion{version, func(context.Context) error { return errors.New("bin is in use") }}, nil
	})
	look := func(want ...string) {
		t.Helper()
		a.findUpdate()
		for a.updateLooking {
			(<-posted)()
		}
		var got []string
		for _, said := range a.toasts {
			got = append(got, said.text)
		}
		a.toasts = nil
		if len(got) != len(want) || len(want) == 1 && got[0] != want[0] {
			t.Errorf("said %q, want %q", got, want)
		}
		if a.updateReady != "" || a.updateBusy != "" {
			t.Errorf("%q waits as if it installed, %q still downloads", a.updateReady, a.updateBusy)
		}
	}
	failed := locales.SidebarInstallUpdateFailed
	look(failed("2.0.0"))
	look()
	version = "2.0.1"
	look(failed("2.0.1"))
}

func TestCheckThatFailsSaysNothing(t *testing.T) {
	a, posted := standInForUpdates(t, func(context.Context) (*newerVersion, error) {
		return nil, errors.New("offline")
	})
	a.findUpdate()
	for a.updateLooking {
		(<-posted)()
	}
	if len(a.toasts) != 0 || a.updateBusy != "" || a.updateReady != "" {
		t.Errorf("a failed check said %+v, shows %q, waits with %q", a.toasts, a.updateBusy, a.updateReady)
	}
}

func TestOnlyAnUpdateFoundShows(t *testing.T) {
	found, installing, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a, posted := standInForUpdates(t, func(context.Context) (*newerVersion, error) {
		<-found
		return &newerVersion{"2.0.0", func(context.Context) error {
			close(installing)
			<-finish
			return nil
		}}, nil
	})
	a.findUpdate()
	if !a.updateLooking || a.updateBusy != "" {
		t.Errorf("checking shows %q", a.updateBusy)
	}
	a.findUpdate() // a second look while the first runs does nothing
	close(found)
	(<-posted)()
	<-installing
	if want := locales.SidebarDownloadingUpdate("2.0.0"); a.updateBusy != want {
		t.Errorf("downloading shows %q, want %q", a.updateBusy, want)
	}
	close(finish)
	for a.updateLooking {
		(<-posted)()
	}
	if a.updateBusy != "" || a.updateReady != "2.0.0" || len(a.toasts) != 0 {
		t.Errorf("installed: shows %q, waits with %q, said %+v", a.updateBusy, a.updateReady, a.toasts)
	}
}
