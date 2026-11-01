package mobile

import (
	"encoding/json"
	"errors"

	"leaflet/internal/keychain"
	"leaflet/internal/store"
)

// Platform is what the iOS app gives the core, implemented in Swift. The
// core calls it from its own goroutines, never from the app's main
// thread, and waits for each answer.
type Platform interface {
	// SecretGet returns the secret kept under name in the keychain, or
	// nothing (no bytes, and no error) when there is none: Swift can't
	// return nil from a method that throws.
	SecretGet(name string) ([]byte, error)
	// SecretSet keeps value under name, in place of what it had.
	SecretSet(name string, value []byte) error
	// SecretDelete removes the secret under name, if there is one.
	SecretDelete(name string) error
	// Online reports whether the device has a connection.
	Online() bool
	// DeviceName names this device in Account › Devices, such as
	// "Rocky's iPad".
	DeviceName() string
	// OpenURL opens a web page outside the app.
	OpenURL(url string)
	// WatchICloud starts or stops watching the Leaflet folder in iCloud
	// Drive, for the app to call Kick when another device writes to it.
	WatchICloud(on bool)
	// StartDownload asks iCloud to download the file at path, named as
	// it will be once downloaded.
	StartDownload(path string) error
	// Log takes a line the core logs.
	Log(line string)
}

// Events hears what changes in the core, implemented in Swift. Events
// come in order, one at a time, on a goroutine of their own, so a handler
// may call the core back; the app moves them to its main thread.
type Events interface {
	// Changed says a table changed, and the page concerned, if one is:
	// the library and open views read again what they show of it.
	Changed(table, pageID string)
	// SyncState says a sync pass started or ended, with why it failed,
	// or "".
	SyncState(running bool, failure string)
	// PageUpdated gives an open page's content as it is now, at rev,
	// after a change from outside the app's editor: reason is "external"
	// (sync, a restore, leaflet mcp), "agent" or "merge" (the app's edit
	// merged with one of those). newStep says an agent's change starts
	// an undo step of its own.
	PageUpdated(pageID, content string, rev int64, reason string, newStep bool)
	// PageReadOnly says whether the user may change an open page.
	PageReadOnly(pageID string, readOnly bool)
	// PageSaving says whether an open page's edits wait to be saved, as
	// that changes: its byline says "Saving…" meanwhile.
	PageSaving(pageID string, saving bool)
	// Tint gives the blocks an agent run last changed on a page, as a
	// JSON array of IDs.
	Tint(pageID, ids string)
	// Notice tells the user something, in words for them.
	Notice(message string)
	// AgentsChanged says an agent's state changed, on some page: the app
	// reads the open pages' AgentState and Threads again.
	AgentsChanged()
	// RecordingChanged gives the recording in progress and the
	// transcriptions running, as RecordingState returns them, as they
	// change.
	RecordingChanged(state string)
}

// secrets keeps the core's secrets in the iOS keychain, through the app.
type secrets struct{ p Platform }

func (s secrets) Get(name string) ([]byte, error) {
	v, err := s.p.SecretGet(name)
	if err == nil && len(v) == 0 {
		return nil, keychain.ErrNotFound
	}
	return v, err
}

func (s secrets) Set(name string, value []byte) error { return s.p.SecretSet(name, value) }

func (s secrets) Delete(name string) error { return s.p.SecretDelete(name) }

// events delivers events to the app in order, off the core's goroutine,
// so a handler that calls the core back can't wait on it.
type events struct {
	to Events
	q  *queue
}

func (e *events) changed(c store.Change) {
	e.q.post(func() { e.to.Changed(c.Table, c.PageID) })
}

func (e *events) syncState(running bool, err error) {
	failure := ""
	if err != nil {
		failure = err.Error()
	}
	e.q.post(func() { e.to.SyncState(running, failure) })
}

func (e *events) pageUpdated(pageID, content string, rev int64, reason string, newStep bool) {
	e.q.post(func() { e.to.PageUpdated(pageID, content, rev, reason, newStep) })
}

func (e *events) pageReadOnly(pageID string, readOnly bool) {
	e.q.post(func() { e.to.PageReadOnly(pageID, readOnly) })
}

func (e *events) pageSaving(pageID string, saving bool) {
	e.q.post(func() { e.to.PageSaving(pageID, saving) })
}

func (e *events) tint(pageID string, ids []string) {
	raw, _ := json.Marshal(ids)
	e.q.post(func() { e.to.Tint(pageID, string(raw)) })
}

func (e *events) notice(message string) {
	e.q.post(func() { e.to.Notice(message) })
}

// errNotOpen is the error for a page the app edits without opening it.
var errNotOpen = errors.New("the page isn't open")

func (e *events) agentsChanged() {
	e.q.post(func() { e.to.AgentsChanged() })
}

func (e *events) recordingChanged(state string) {
	e.q.post(func() { e.to.RecordingChanged(state) })
}
