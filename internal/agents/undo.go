package agents

import (
	"errors"
	"log"
	"slices"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
	"leaflet/locales"
)

func (h *Hub) UndoRun(pageID, runID string) error {
	s := h.Session(pageID)
	run := s.Run(runID)
	if run == nil || run.Undone {
		return nil
	}
	if err := h.apply(pageID, run, doc.InvertRun(run.landed()), true); err != nil {
		log.Printf("undo run: %v", err)
		return err
	}
	run.Undone = true
	h.saveRun(s, runID)
	h.host.Tint(pageID, nil)
	h.markJob(runID, "reverted")
	return nil
}

func (h *Hub) RedoRun(pageID, runID string) error {
	s := h.Session(pageID)
	run := s.Run(runID)
	if run == nil || !run.Undone {
		return nil
	}
	if err := h.apply(pageID, run, run.landed(), false); err != nil {
		log.Printf("redo run: %v", err)
		return err
	}
	run.Undone = false
	h.saveRun(s, runID)
	h.host.Tint(pageID, run.BlockIDs())
	h.markJob(runID, "done")
	return nil
}

func (h *Hub) apply(pageID string, run *Run, ops []doc.Op, undo bool) error {
	var blockOps, pageOps []doc.Op
	var left []string
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
	if len(passed) > 0 {
		blockOps = withoutCards(blockOps, passed)
	}
	if len(blockOps) > 0 {
		var err error
		if ed := h.host.Live(pageID); ed != nil {
			if err = applyAll(ed.Peek().Copy(), blockOps); err == nil {
				err = ed.ApplyOps(blockOps)
			}
		} else {
			_, err = storeOps(h.st, pageID, blockOps, false, true, run.ID, "undo run")
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

type plan struct {
	st      *store.Store
	run     *Run
	pages   map[string]*store.Page
	folders map[string]*store.Folder
	all     *snapshot
}

type snapshot struct {
	pages   []store.Page
	files   []store.File
	folders []store.Folder
	failed  bool
}

func newPlan(st *store.Store, run *Run) *plan {
	return &plan{st: st, run: run, pages: map[string]*store.Page{}, folders: map[string]*store.Folder{}}
}

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
		if o.Place != nil && o.Place.ParentID != "" {
			if to := pl.page(o.Place.ParentID); to != nil && !to.DeletedAt.IsZero() {
				return false
			}
		}
		return o.Place != nil && o.PrevPlace != nil && p.DeletedAt.IsZero() && was.Same(*o.PrevPlace)
	case "rename_page":
		return p.DeletedAt.IsZero() && p.Title == o.PrevTitle
	case "set_page_tags":
		return p.DeletedAt.IsZero() && slices.Equal(p.Tags, o.PrevTags)
	}
	return true
}

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
		folder := o.Page.FolderID
		if f, err := h.st.Folder(folder); folder == "" || err != nil || !f.DeletedAt.IsZero() {
			return nil
		}
		if p, err := h.st.Page(o.Page.ID); err == nil && p.ParentID == "" && p.FolderID == "" {
			return h.st.PlacePage(p.ID, "", folder, p.SortKey)
		}
	case "move_page":
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

func (h *Hub) awaited(run *Run) bool {
	if run.Local || run.At.IsZero() || !h.st.Session().SignedIn {
		return false
	}
	pulled, ok := syncsvc.PulledAt(h.st, "pages")
	return !ok || pulled.Before(run.At)
}

func (h *Hub) markJob(id, status string) {
	if _, err := h.st.SetJobStatus(id, status); err != nil {
		log.Printf("job status: %v", err)
	}
}
