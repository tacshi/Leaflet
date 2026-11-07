package core

import (
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/locales"
)

// TranscribeFile transcribes file block blockID of page pageID with the
// engine of stt, and puts the transcript in the block's notes when it is
// done, on the main thread or the core's queue as ever: the page may be
// closed by then, or the block gone. It says why when the engine can't
// start now.
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
		if err := p.SetFileNotes(pageID, blockID, notes, was); err != nil {
			p.notify(err.Error())
			return
		}
		if cut {
			p.notify(locales.RecFileNotesCut())
		} else if p.Notify != nil {
			p.Notify(locales.RecFileTranscribed(f.Name))
		}
		p.Rec.FileTranscribed(f, stt)
	})
}

// SetFileNotes writes notes, a file's transcript, in file block blockID of
// page pageID, whose notes were was when it was asked for. Notes written
// since stay, the transcript after them: a transcript that took a while
// takes no one's writing. On an open page it is one step to undo. A block
// gone by then, or a page that can no longer be changed, is said so.
func (p Pages) SetFileNotes(pageID, blockID, notes, was string) error {
	merged := func(n *doc.Node) string {
		now := n.Str("notes")
		if now == was || strings.TrimSpace(now) == "" {
			return notes
		}
		return doc.NormalizeNotes(now + "\n\n" + notes)
	}
	gone := func() error {
		p.notify(locales.RecFileBlockGone())
		return nil
	}
	if !p.canEdit(pageID) {
		return gone()
	}
	if lp := p.open(pageID); lp != nil {
		n := lp.Doc().Find(blockID)
		if n == nil || n.Type != "file" {
			return gone()
		}
		next := n.Clone()
		next.Set("notes", merged(n))
		if err := lp.ApplyOps([]doc.Op{{Op: "replace", ID: blockID, Node: next, PrevNode: n.Clone()}}); err != nil {
			return err
		}
		if p.Step != nil {
			p.Step(pageID, blockID)
		}
		p.save(pageID)
		return nil
	}
	found := false
	err := p.EditPage(pageID, func(c *doc.Content) bool {
		n := c.Find(blockID)
		if n == nil || n.Type != "file" {
			return false
		}
		found = true
		n.Set("notes", merged(n))
		return true
	})
	if err == nil && !found {
		return gone()
	}
	return err
}
