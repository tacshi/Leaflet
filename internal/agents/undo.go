package agents

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/remote"
	"leaflet/internal/store"
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
	if err := h.apply(pageID, run, doc.InvertRun(run.Ops), true); err != nil {
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
	if err := h.apply(pageID, run, run.Ops, false); err != nil {
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
// around it. A page or a folder an op changes that this device does not
// have as the run left it, as a cloud run's before it syncs, stops it
// before anything changes: the change would miss it, and the run's would
// come with the sync after. One that is not coming, as sync has pulled
// the pages made since the run began, is gone for good, or the user
// changed it since, and is passed over, their change kept: a page or a
// folder of the run's that the user deleted stays deleted, through Undo
// and Redo (Run.Left). The page's blocks go next, all at once or not at
// all: when they cannot, nothing changed. Only the database failing
// leaves the pages after them as they were.
func (h *Hub) apply(pageID string, run *Run, ops []doc.Op, undo bool) error {
	var blockOps, pageOps []doc.Op
	var left []string
	// passed are the pages made that stay deleted, whose cards go nowhere.
	passed := map[string]bool{}
	pl := newPlan(h.st, run)
	for _, o := range ops {
		switch {
		case !o.IsPageOp():
			blockOps = append(blockOps, o)
		case pl.ready(o):
			pl.made(o)
			pageOps = append(pageOps, o)
		case pl.deleted(o):
			if undo {
				left = append(left, opTarget(o))
			} else if o.Op == "create_page" {
				passed[o.Page.ID] = true
			}
		case h.awaited(run):
			return errors.New(locales.PageRunPageNotHere())
		}
	}
	// A Redo puts back no card of a page it leaves deleted, as the user
	// deleted it, nor a second of one they brought back since.
	if len(passed) > 0 {
		blockOps = withoutCards(blockOps, passed)
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
	if undo {
		run.Left = left
	}
	var failed error
	pages := h.pages()
	for _, o := range pageOps {
		failed = errors.Join(failed, h.applyPage(pages, run, o))
	}
	return failed
}

// plan is the pages and folders a run's page ops change as the ops before
// each, in one Undo or Redo, leave them: an op is ready by what those do,
// not by what was stored before any of them, as for a page made and then
// tagged, or moved twice.
type plan struct {
	st      *store.Store
	run     *Run
	pages   map[string]*store.Page
	folders map[string]*store.Folder
	// all is every page, file and folder stored, read once, for the first
	// folder an Undo would delete (empty).
	all *snapshot
}

// snapshot is a store's pages, files and folders, failed when one of them
// could not be read.
type snapshot struct {
	pages   []store.Page
	files   []store.File
	folders []store.Folder
	failed  bool
}

func newPlan(st *store.Store, run *Run) *plan {
	return &plan{st: st, run: run, pages: map[string]*store.Page{}, folders: map[string]*store.Folder{}}
}

// withoutCards is ops without the cards they put in of the pages of pages.
func withoutCards(ops []doc.Op, pages map[string]bool) []doc.Op {
	var out []doc.Op
	for _, o := range ops {
		if o.Op == "insert" {
			o.Nodes = slices.DeleteFunc(slices.Clone(o.Nodes), func(n *doc.Node) bool {
				return n.Type == "subpage" && pages[n.Str("page_id")]
			})
			if len(o.Nodes) == 0 {
				continue
			}
		}
		out = append(out, o)
	}
	return out
}

// opTarget is the page or the folder page op o changes.
func opTarget(o doc.Op) string {
	switch {
	case o.Page != nil:
		return o.Page.ID
	case o.Folder != nil:
		return o.Folder.ID
	case o.FolderID != "":
		return o.FolderID
	}
	return o.PageID
}

// page is page id as the ops planned so far leave it, nil for one this
// device does not have.
func (pl *plan) page(id string) *store.Page {
	p, ok := pl.pages[id]
	if !ok {
		if got, err := pl.st.Page(id); err == nil {
			p = &got
		}
		pl.pages[id] = p
	}
	return p
}

// folder is folder id as the ops planned so far leave it, nil for one this
// device does not have.
func (pl *plan) folder(id string) *store.Folder {
	f, ok := pl.folders[id]
	if !ok {
		if got, err := pl.st.Folder(id); err == nil {
			f = &got
		}
		pl.folders[id] = f
	}
	return f
}

// ready reports whether page op o finds what it changes as it was before
// it: the page or the folder, in its place, with its title or its tags,
// and one to delete not deleted already. A folder an Undo would delete
// holds nothing more than the run left in it: what the user filed there
// since stays, and the folder with it. What the latest Undo left deleted,
// as the user had, Redo does not bring back.
func (pl *plan) ready(o doc.Op) bool {
	switch o.Op {
	case "create_folder":
		return o.Folder != nil && pl.folder(o.Folder.ID) != nil && !slices.Contains(pl.run.Left, o.Folder.ID)
	case "delete_folder":
		f := pl.folder(o.FolderID)
		return f != nil && f.DeletedAt.IsZero() && pl.empty(o.FolderID)
	}
	id := opTarget(o)
	p := pl.page(id)
	if p == nil {
		return false
	}
	switch o.Op {
	case "create_page":
		return !slices.Contains(pl.run.Left, id)
	case "delete_page":
		return p.DeletedAt.IsZero()
	case "move_page":
		was := doc.PagePlace{ParentID: p.ParentID, FolderID: p.FolderID}
		return o.Place != nil && o.PrevPlace != nil && p.DeletedAt.IsZero() && was.Same(*o.PrevPlace)
	case "rename_page":
		return p.DeletedAt.IsZero() && p.Title == o.PrevTitle
	case "set_page_tags":
		return p.DeletedAt.IsZero() && slices.Equal(p.Tags, o.PrevTags)
	}
	return true
}

// deleted reports whether page op o is passed over as the user deleted
// what it changes: an Undo's delete of a page or a folder of the run's the
// user deleted since, or a Redo's of one the latest Undo left so.
func (pl *plan) deleted(o doc.Op) bool {
	switch o.Op {
	case "delete_page":
		p := pl.page(o.PageID)
		return p != nil && !p.DeletedAt.IsZero()
	case "delete_folder":
		f := pl.folder(o.FolderID)
		return f != nil && !f.DeletedAt.IsZero()
	case "create_page", "create_folder":
		return slices.Contains(pl.run.Left, opTarget(o))
	}
	return false
}

// made has the plan take page op o as made: what it changes is as it
// leaves it, for the ops after it.
func (pl *plan) made(o doc.Op) {
	switch o.Op {
	case "delete_page":
		pl.page(o.PageID).DeletedAt = time.Now()
	case "create_page":
		ids := []string{o.Page.ID}
		if gone, ok := pl.run.Removed[o.Page.ID]; ok {
			ids = gone.IDs
		}
		for _, id := range ids {
			if p := pl.page(id); p != nil {
				p.DeletedAt = time.Time{}
			}
		}
	case "move_page":
		p := pl.page(o.PageID)
		p.ParentID, p.FolderID = o.Place.ParentID, o.Place.FolderID
	case "rename_page":
		pl.page(o.PageID).Title = o.Title
	case "set_page_tags":
		pl.page(o.PageID).Tags = store.NormalizeTags(o.Tags)
	case "delete_folder":
		pl.folder(o.FolderID).DeletedAt = time.Now()
	case "create_folder":
		pl.folder(o.Folder.ID).DeletedAt = time.Time{}
	}
}

// empty reports whether folder id holds no page, file or folder once the
// ops planned so far are made. One that could not be read is taken to
// hold something, and stays. What is stored is read once a plan, the ops
// planned making their changes over it.
func (pl *plan) empty(id string) bool {
	if pl.all == nil {
		all := &snapshot{}
		var err, ferr, gerr error
		all.pages, err = pl.st.AllPages()
		all.files, ferr = pl.st.Files()
		all.folders, gerr = pl.st.Folders()
		all.failed = err != nil || ferr != nil || gerr != nil
		pl.all = all
	}
	if pl.all.failed {
		return false
	}
	for _, p := range pl.all.pages {
		if q := pl.pages[p.ID]; q != nil {
			p = *q
		}
		if p.FolderID == id && p.DeletedAt.IsZero() {
			return false
		}
	}
	if slices.ContainsFunc(pl.all.files, func(f store.File) bool { return f.FolderID == id }) {
		return false
	}
	return !slices.ContainsFunc(pl.all.folders, func(f store.Folder) bool {
		q := pl.folders[f.ID]
		return f.ParentFolderID == id && (q == nil || q.DeletedAt.IsZero())
	})
}

// applyPage makes page op o, through pages, so a page open in the app
// shows the cards of its subpages come and go.
func (h *Hub) applyPage(pages Pages, run *Run, o doc.Op) error {
	switch o.Op {
	case "delete_page":
		ids, card, err := pages.DeletePage(o.PageID)
		if err != nil {
			return err
		}
		if run.Removed == nil {
			run.Removed = map[string]RemovedPages{}
		}
		run.Removed[o.PageID] = RemovedPages{IDs: ids, Card: card}
	case "create_page":
		gone, ok := run.Removed[o.Page.ID]
		if !ok {
			gone = RemovedPages{IDs: []string{o.Page.ID}, Card: doc.Position{At: "end"}}
		}
		if err := pages.RestorePages(gone.IDs, gone.Card); err != nil {
			return err
		}
		// A page made in a folder that Undo deleted after it left the
		// folder then, as a deleted folder's pages do: it goes back in,
		// once the folder is back too.
		folder := o.Page.FolderID
		if f, err := h.st.Folder(folder); folder == "" || err != nil || !f.DeletedAt.IsZero() {
			return nil
		}
		if p, err := h.st.Page(o.Page.ID); err == nil && p.ParentID == "" && p.FolderID == "" {
			return h.st.PlacePage(p.ID, "", folder, p.SortKey)
		}
	case "move_page":
		// Its card goes back where it was, as Undo has it.
		to := *o.Place
		_, err := pages.MovePageAt(o.PageID, to.ParentID, to.Card, func() error {
			return h.st.PlacePage(o.PageID, to.ParentID, to.FolderID, to.SortKey)
		})
		return err
	case "rename_page":
		return h.st.RenamePage(o.PageID, o.Title)
	case "set_page_tags":
		return h.st.SetPageTags(o.PageID, o.Tags)
	case "delete_folder":
		f, err := h.st.Folder(o.FolderID)
		if err != nil {
			return err
		}
		return h.st.DeleteFolder(f)
	case "create_folder":
		return h.st.RestoreFolder(o.Folder.ID)
	}
	return nil
}

// awaited reports whether a page run made, which this device does not
// have, may still come with a sync: signed in, sync has not pulled pages
// made since the run began. A run of this device's made its changes
// here: none of them is to come.
func (h *Hub) awaited(run *Run) bool {
	if run.Local || run.At.IsZero() || !h.st.Session().SignedIn {
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
