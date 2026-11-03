package agents

import (
	"context"
	"errors"
	"log"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/remote"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

// Undoing and redoing a whole run, and keeping its job's status.

// UndoRun reverts a whole run, even after later edits elsewhere on the
// page. One whose edits could not be reverted stays as it was, and says
// why.
func (h *Hub) UndoRun(pageID, runID string) error {
	s := h.Session(pageID)
	run := s.Run(runID)
	if run == nil || run.Undone {
		return nil
	}
	if err := h.apply(pageID, run, doc.InvertRun(run.Ops)); err != nil {
		log.Printf("undo run: %v", err)
		return err
	}
	run.Undone = true
	h.saveRun(s, runID)
	h.host.Tint(pageID, nil)
	h.markJob(runID, "reverted")
	return nil
}

// RedoRun applies an undone run again. One whose edits could not be
// applied stays undone, and says why.
func (h *Hub) RedoRun(pageID, runID string) error {
	s := h.Session(pageID)
	run := s.Run(runID)
	if run == nil || !run.Undone {
		return nil
	}
	if err := h.apply(pageID, run, run.Ops); err != nil {
		log.Printf("redo run: %v", err)
		return err
	}
	run.Undone = false
	h.saveRun(s, runID)
	h.host.Tint(pageID, run.BlockIDs())
	h.markJob(runID, "done")
	return nil
}

// apply makes a run's ops, or their inverse, on its page and on the pages
// it made or deleted. A page it made that this device does not have yet,
// as a cloud run's before it syncs, stops it before anything changes:
// deleting it would miss it, and it would come back with the sync. One
// that is not coming, as sync has pulled the pages made since the run
// began, is gone for good, and passed over. The page's blocks go next,
// all at once or not at all: when they cannot, nothing changed. Only the
// database failing leaves the pages after them as they were.
func (h *Hub) apply(pageID string, run *Run, ops []doc.Op) error {
	var blockOps, pageOps []doc.Op
	for _, o := range ops {
		id := o.PageID
		if o.Page != nil {
			id = o.Page.ID
		}
		switch _, err := h.st.Page(id); {
		case o.Op != "delete_page" && o.Op != "create_page":
			blockOps = append(blockOps, o)
		case err == nil:
			pageOps = append(pageOps, o)
		case id != "" && h.awaited(run):
			return errors.New(locales.PageRunSubpageNotHere())
		}
	}
	if len(blockOps) > 0 {
		var err error
		if ed := h.host.Live(pageID); ed != nil {
			// Tried on a copy first: the editor applies what it can.
			if err = applyAll(ed.Doc().Copy(), blockOps); err == nil {
				err = ed.ApplyOps(blockOps)
			}
		} else {
			err = storeOps(h.st, pageID, blockOps, false, true, run.ID, "undo run")
		}
		if err != nil {
			return err
		}
	}
	var failed error
	for _, o := range pageOps {
		switch {
		case o.Op == "delete_page":
			if _, err := h.st.DeletePage(o.PageID); err != nil {
				failed = errors.Join(failed, err)
			}
		case o.Page != nil:
			failed = errors.Join(failed, h.st.RestorePages([]string{o.Page.ID}))
		}
	}
	return failed
}

// awaited reports whether a page run made, which this device does not
// have, may still come with a sync: signed in, sync has not pulled pages
// made since the run began.
func (h *Hub) awaited(run *Run) bool {
	if run.At.IsZero() || !h.st.Session().SignedIn {
		return false
	}
	pulled, ok := syncsvc.PulledAt(h.st, "pages")
	return !ok || pulled.Before(run.At)
}

// markJob changes a run's status: in the job this device keeps of a local
// run, or else in the cloud run's row on the server, whose status the
// agent function reads to leave an undone run out of the chat's history.
func (h *Hub) markJob(id, status string) {
	local, err := h.st.SetJobStatus(id, status)
	if err != nil {
		log.Printf("job status: %v", err)
	}
	if !local && h.be.SignedIn() && h.host.Online() {
		guard.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := h.sendJobStatus(ctx, id, status); err != nil {
				log.Printf("job status: %v", err)
			}
		})
	}
}

// sendJobStatus changes a cloud run's status on the server. Not as an
// upsert: the server checks its insert first, and a row of only an ID and
// a status, without its page or surface, is refused before it finds the
// row there.
func (h *Hub) sendJobStatus(ctx context.Context, id, status string) error {
	_, err := h.be.Update(ctx, "ai_jobs", []string{remote.Eq("id", id)}, map[string]any{"status": status})
	return err
}
