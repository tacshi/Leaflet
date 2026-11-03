package core

import (
	"errors"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// A page's history: the versions kept of it, which History lists, a
// version named, and an earlier one put back.

// HistoryLabel says what a version is: its name, or why it was kept.
func HistoryLabel(h store.HistoryEntry) string {
	if h.Label != "" {
		return h.Label
	}
	switch h.Reason {
	case "agent_run":
		return locales.DialogHistoryAgentRun()
	case "autosave":
		return locales.DialogHistoryAutosave()
	case "restore":
		return locales.DialogHistoryBeforeRestore()
	case "conflict":
		return locales.DialogHistoryConflict()
	}
	return ""
}

// NameVersion keeps page pageID as it is now, its edits saved first, as a
// version named name. A page with no content, or a name of spaces, keeps
// none.
func (p Pages) NameVersion(pageID, name string) error {
	name = strings.TrimSpace(name)
	p.save(pageID)
	c := p.Store.Content(pageID)
	if c == nil || name == "" {
		return nil
	}
	return p.Store.AddHistory(pageID, c, "manual", "", name)
}

// RestoreVersion puts version id back as page pageID's content, keeping
// the page as it was as a version first, and returns that version's ID,
// for Undo to restore it, or "" when the page had no content to keep. A
// version saved with no blocks leaves one to type in. run is the page's
// last agent run, "" for none, whose Undo the app no longer offers: its
// edits are gone or back with the version, and undoing it would do
// nothing, or something else.
func (p Pages) RestoreVersion(pageID, id string) (kept, run string, err error) {
	if !p.canEdit(pageID) {
		return "", "", errors.New(locales.AppErrorReadOnly())
	}
	entries, err := p.Store.History(pageID)
	if err != nil {
		return "", "", err
	}
	var old *doc.Content
	for _, h := range entries {
		if h.ID != id {
			continue
		}
		// One that can't be read would empty the page.
		if old, err = doc.Decode(h.Content); err != nil {
			return "", "", err
		}
	}
	if old == nil {
		return "", "", errors.New(locales.DialogHistoryVersionGone())
	}
	p.save(pageID)
	if cur := p.Store.Content(pageID); cur != nil {
		label := locales.DialogHistoryBeforeRestoring(time.Now())
		if err := p.Store.AddHistory(pageID, cur, "restore", "", label); err != nil {
			return "", "", err
		}
		kept = p.newestRestore(pageID, label)
	}
	children := old.Children
	if len(children) == 0 {
		children = doc.Empty().Children
	}
	if err := p.EditPage(pageID, func(c *doc.Content) bool {
		c.Children = children
		return true
	}); err != nil {
		return "", "", err
	}
	if p.Hub != nil {
		if r := p.Hub.Session(pageID).Latest(); r != nil {
			run = r.ID
		}
	}
	return kept, run, nil
}

// newestRestore is the ID of the version RestoreVersion just kept of a
// page, by label: the newest kept so.
func (p Pages) newestRestore(pageID, label string) string {
	entries, err := p.Store.History(pageID)
	if err != nil {
		return ""
	}
	for _, h := range entries {
		if h.Reason == "restore" && h.Label == label {
			return h.ID
		}
	}
	return ""
}
