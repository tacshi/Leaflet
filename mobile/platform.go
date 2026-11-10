package mobile

import (
	"encoding/json"
	"errors"
	"fmt"

	"leaflet/internal/core"
	"leaflet/internal/icloud"
	"leaflet/internal/keychain"
	"leaflet/internal/store"
	"leaflet/locales"
)

type Platform interface {
	SecretGet(name string) ([]byte, error)
	SecretSet(name string, value []byte) error
	SecretDelete(name string) error
	Online() bool
	DeviceName() string
	OpenURL(url string)
	WatchICloud(on bool)
	StartDownload(path string) error
	Log(line string)
}

type Events interface {
	Changed(table, pageID string)
	SyncState(running bool, failure string)
	PageUpdated(pageID, content string, rev int64, reason string, newStep bool)
	PageReadOnly(pageID string, readOnly bool)
	PageSaving(pageID string, saving bool)
	Tint(pageID, ids string)
	Notice(message string)
	AgentsChanged(pageID string)
	RecordingChanged(state string)
	RecordingLevels(levels string)
}

type secrets struct{ p Platform }

func (s secrets) Get(name string) ([]byte, error) {
	v, err := s.p.SecretGet(name)
	if err == nil && len(v) == 0 {
		return nil, keychain.ErrNotFound
	}
	if err != nil {
		return nil, keychainError(err)
	}
	return v, nil
}

func (s secrets) Set(name string, value []byte) error {
	if err := s.p.SecretSet(name, value); err != nil {
		return keychainError(err)
	}
	return nil
}

func (s secrets) Delete(name string) error {
	if err := s.p.SecretDelete(name); err != nil {
		return keychainError(err)
	}
	return nil
}

const errSecItemNotFound = -25300

func keychainError(err error) error {
	if status, ok := keychain.OSStatus(err); ok && status == errSecItemNotFound {
		return fmt.Errorf("%w: %w", keychain.ErrNotFound, err)
	}
	return keychain.Denied(err)
}

type events struct {
	to Events
	q  *queue
}

func (e *events) changed(c store.Change) {
	e.q.post(func() { e.to.Changed(c.Table, c.PageID) })
}

func (e *events) syncState(running bool, err error) {
	failure := ""
	if msg, ok := core.SyncMessage(err); ok {
		failure = msg
	} else if msg, ok := core.ICloudMessage(err); ok {
		failure = msg
	} else if errors.Is(err, icloud.ErrNoAccess) {
		failure = locales.SettingsICloudFolderLost()
	} else if err != nil {
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

var errNotOpen = errors.New("the page isn't open")

func (e *events) agentsChanged(pageID string) {
	e.q.post(func() { e.to.AgentsChanged(pageID) })
}

func (e *events) recordingChanged(state string) {
	e.q.post(func() { e.to.RecordingChanged(state) })
}

func (e *events) recordingLevels(levels string) {
	e.q.post(func() { e.to.RecordingLevels(levels) })
}
