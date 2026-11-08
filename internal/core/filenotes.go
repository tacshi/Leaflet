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

// TranscribeFile transcribes file block blockID of page pageID with the
// engine of stt, and puts the transcript in the block's notes when it is
// done, on the main thread or the core's queue as ever: the page may be
// closed by then, or the block gone. It says why when the engine can't
// start now. Only a file of the page itself is transcribed into it: a
// block may name any file, as one written into a shared page can, and
// another page's would be read with this user's access and its words put
// where that page's readers may not be.
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
		written, less, err := p.SetFileNotes(pageID, blockID, f.ID, notes, was)
		switch written {
		case NotesGone:
			// SetFileNotes said so.
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
		// What the server keeps of the transcript goes only once the page
		// has it, stored.
		p.Rec.FileTranscribed(f, stt)
	})
}

// NotesWritten is what came of writing a file's transcript in its block.
type NotesWritten int

const (
	// NotesSaved is a transcript in its block, and stored.
	NotesSaved NotesWritten = iota
	// NotesGone is a transcript with nowhere to go: its block was deleted
	// or given another file, or the page can no longer be changed. The
	// user was told so.
	NotesGone
	// NotesFailed is a transcript that could not be stored, for the reason
	// that comes with it.
	NotesFailed
)

// SetFileNotes writes notes, the transcript of file fileID, in file block
// blockID of page pageID, whose notes were was when it was asked for.
// Notes written since stay, the transcript after them, as much of it as
// the notes have room for: a transcript that took a while takes no one's
// writing. On an open page it is one step to undo. It reports what came of
// it, and cut, whether the transcript was cut to fit after notes written
// since. A block gone by then, or one that holds another file, or a page
// that can no longer be changed, is said so.
func (p Pages) SetFileNotes(pageID, blockID, fileID, notes, was string) (written NotesWritten, cut bool, err error) {
	// merged is n's notes with the transcript, and whether it was cut.
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
		if err := lp.ApplyOps([]doc.Op{{Op: "replace", ID: blockID, Node: next, PrevNode: n.Clone()}}); err != nil {
			return NotesFailed, false, err
		}
		if p.Step != nil {
			p.Step(pageID, blockID)
		}
		lp.Changed()
		// A page whose content here became unreadable, or went, as it
		// saved reports nothing left to save without storing the notes.
		if !p.save(pageID) || !lp.Stored() {
			// The notes stay in the page, to be saved with its next edits.
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
