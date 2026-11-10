package core

import (
	"errors"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

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

func (p Pages) NameVersion(pageID, name string) error {
	name = strings.TrimSpace(name)
	if !p.save(pageID) {
		return errNotSaved()
	}
	c := p.Store.Content(pageID)
	if c == nil || name == "" {
		return nil
	}
	return p.Store.AddHistory(pageID, c, "manual", "", name)
}

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
		if old, err = doc.Decode(h.Content); err != nil {
			return "", "", err
		}
	}
	if old == nil {
		return "", "", errors.New(locales.DialogHistoryVersionGone())
	}
	if !p.save(pageID) {
		return "", "", errNotSaved()
	}
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
