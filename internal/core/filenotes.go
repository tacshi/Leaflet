package core

import (
	"errors"
	"strings"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

func (p Pages) TranscribeFile(pageID, blockID string, stt store.SttSettings, online bool) error {
	c := p.content(pageID)
	var n *doc.Node
	if c != nil {
		n = c.Find(blockID)
	}
	if n == nil || n.Type != "file" {
		return nil
	}
	f, err := p.Store.File(n.Str("file_id"))
	if err != nil {
		return err
	}
	if f.PageID != pageID {
		return errors.New(locales.RecErrFileElsewhere())
	}
	was := n.Str("notes")
	return p.Rec.TranscribeFile(f, blockID, stt, online, func(segs []store.Segment, err error) {
		if err != nil {
			p.notify(err.Error())
			return
		}
		notes, cut := recording.TranscriptNotes(segs)
		if notes == "" {
			p.notify(locales.RecErrNoWords())
			return
		}
		written, less, err := p.SetFileNotes(pageID, blockID, f.ID, notes, was, stt.Provider)
		switch written {
		case NotesGone:
			return
		case NotesFailed:
			p.notify(err.Error())
			return
		}
		if cut || less {
			p.notify(locales.RecFileNotesCut())
		} else if p.Notify != nil {
			p.Notify(locales.RecFileTranscribed(f.Name))
		}
	})
}

type NotesWritten int

const (
	NotesSaved NotesWritten = iota
	NotesGone
	NotesFailed
)

func (p Pages) SetFileNotes(pageID, blockID, fileID, notes, was, by string) (written NotesWritten, cut bool, err error) {
	merged := func(n *doc.Node) (string, bool, error) {
		now := n.Str("notes")
		if now == was || strings.TrimSpace(now) == "" {
			return notes, false, nil
		}
		now = doc.NormalizeNotes(now)
		fit, less := recording.FitNotes(notes, doc.MaxNotes-utf8.RuneCountInString(now)-len("\n\n"))
		if fit == "" {
			return "", false, errors.New(locales.RecFileNotesFull())
		}
		return now + "\n\n" + fit, less, nil
	}
	ours := func(n *doc.Node) bool {
		return n != nil && n.Type == "file" && n.Str("file_id") == fileID
	}
	gone := func() (NotesWritten, bool, error) {
		p.notify(locales.RecFileBlockGone())
		return NotesGone, false, nil
	}
	if !p.canEdit(pageID) {
		return gone()
	}
	if lp := p.open(pageID); lp != nil {
		n := lp.Doc().Find(blockID)
		if !ours(n) {
			return gone()
		}
		text, less, err := merged(n)
		if err != nil {
			return NotesFailed, false, err
		}
		next := n.Clone()
		next.Set("notes", text)
		next.Set(doc.TranscribedBy, by)
		if err := lp.ApplyOps([]doc.Op{{Op: "replace", ID: blockID, Node: next, PrevNode: n.Clone()}}); err != nil {
			return NotesFailed, false, err
		}
		if p.Step != nil {
			p.Step(pageID, blockID)
		}
		lp.Changed()
		if !p.save(pageID) || !lp.Stored() {
			return NotesFailed, false, errNotSaved()
		}
		return NotesSaved, less, nil
	}
	found := false
	var full error
	err = p.EditPage(pageID, func(c *doc.Content) bool {
		n := c.Find(blockID)
		if found = ours(n); !found {
			return false
		}
		var text string
		if text, cut, full = merged(n); full != nil {
			return false
		}
		n.Set("notes", text)
		n.Set(doc.TranscribedBy, by)
		return true
	})
	switch {
	case err != nil:
		return NotesFailed, false, err
	case !found:
		return gone()
	case full != nil:
		return NotesFailed, false, full
	}
	return NotesSaved, cut, nil
}
