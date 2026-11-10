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
	look := func(asked bool, want ...string) {
		t.Helper()
		a.findUpdate(asked)
		for a.updateBusy != "" {
			(<-posted)()
		}
		var got []string
		for _, said := range a.toasts {
			got = append(got, said.text)
		}
		a.toasts = nil
		if len(got) != len(want) || len(want) == 1 && got[0] != want[0] {
			t.Errorf("asked %v: said %q, want %q", asked, got, want)
		}
		if a.updateReady != "" {
			t.Errorf("asked %v: %s waits as if it installed", asked, a.updateReady)
		}
	}
	failed := locales.SidebarInstallUpdateFailed
	look(false, failed("2.0.0"))
	look(false)
	look(true, failed("2.0.0"))
	version = "2.0.1"
	look(false, failed("2.0.1"))
}

func TestCheckThatFailsSaysSoOnlyAsked(t *testing.T) {
	a, posted := standInForUpdates(t, func(context.Context) (*newerVersion, error) {
		return nil, errors.New("offline")
	})
	a.findUpdate(false)
	for a.updateBusy != "" {
		(<-posted)()
	}
	if len(a.toasts) != 0 {
		t.Errorf("unasked, a failed check said %+v", a.toasts)
	}
	a.findUpdate(true)
	for a.updateBusy != "" {
		(<-posted)()
	}
	if len(a.toasts) != 1 || a.toasts[0].text != locales.SidebarCheckForUpdatesFailed() {
		t.Errorf("asked, a failed check said %+v", a.toasts)
	}
}
