package mobile

import (
	"errors"
	"log"
	"slices"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// A page's subpages show in it as cards, subpage blocks, which the core
// keeps in step as the desktop app does: a subpage made from the library
// gets a card at the end of its parent, and a page moving out of a parent
// leaves it, its card going with it. A page deleted takes its card with
// it, which Undo puts back.

// linkSubpage puts a card for subpage id at the end of page parent,
// unless parent has one. Run on the core's queue.
func (c *Core) linkSubpage(parent, id string) error { return c.linkSubpageAfter(parent, id, "") }

// linkSubpageAfter puts the card after the card for subpage after, as a
// copy's goes after its original's, or at the end without one.
func (c *Core) linkSubpageAfter(parent, id, after string) error {
	return c.linkSubpageAt(parent, id, func(content *doc.Content) doc.Position {
		at := doc.Position{At: "end"}
		content.Walk(func(n, _ *doc.Node) {
			if after != "" && n.Type == "subpage" && n.Str("page_id") == after {
				at = doc.Position{After: n.ID}
			}
		})
		return at
	})
}

// linkSubpageAt puts the card where at says in parent's content, or at
// the end when that place is gone.
func (c *Core) linkSubpageAt(parent, id string, at func(*doc.Content) doc.Position) error {
	return c.editContent(parent, func(content *doc.Content) bool {
		if hasSubpage(content, id) {
			return false
		}
		link := doc.SubpageNode(id)
		if content.Insert(at(content), link) == nil {
			return true
		}
		return content.Insert(doc.Position{At: "end"}, link) == nil
	})
}

// unlinkDeleted takes the card for page id out of its parent as the page
// is deleted, keeping where it was for relinkRestored. Run on the core's
// queue.
func (c *Core) unlinkDeleted(id string) {
	p, err := c.core.Store.Page(id)
	if err != nil || p.ParentID == "" {
		return
	}
	at, ok := c.content(p.ParentID).SubpagePlace(id)
	if !ok {
		at = doc.Position{At: "end"}
	}
	if err := c.unlinkSubpage(p.ParentID, id); err != nil {
		log.Printf("page %s: unlink from %s: %v", id, p.ParentID, err)
		return
	}
	c.cards[id] = at
}

// relinkRestored puts back the cards of pages restored, those of ids
// whose parent wasn't deleted with them, where they were. Run on the
// core's queue.
func (c *Core) relinkRestored(ids []string) {
	for _, id := range ids {
		at, ok := c.cards[id]
		delete(c.cards, id)
		p, err := c.core.Store.Page(id)
		if err != nil || p.ParentID == "" || slices.Contains(ids, p.ParentID) {
			continue
		}
		if !ok {
			at = doc.Position{At: "end"}
		}
		if err := c.linkSubpageAt(p.ParentID, id, func(*doc.Content) doc.Position { return at }); err != nil {
			log.Printf("page %s: link in %s: %v", id, p.ParentID, err)
		}
	}
}

// unlinkSubpage takes the cards for subpage id out of page parent. Run on
// the core's queue.
func (c *Core) unlinkSubpage(parent, id string) error {
	return c.editContent(parent, func(content *doc.Content) bool {
		var links []string
		content.Walk(func(n, _ *doc.Node) {
			if n.Type == "subpage" && n.Str("page_id") == id {
				links = append(links, n.ID)
			}
		})
		for _, l := range links {
			content.Remove(l)
		}
		return len(links) > 0
	})
}

func hasSubpage(content *doc.Content, id string) bool {
	found := false
	content.Walk(func(n, _ *doc.Node) { found = found || n.Type == "subpage" && n.Str("page_id") == id })
	return found
}

// editContent changes page id's content with edit, which reports whether
// it changed it: through the page while the app has it open, so its
// editor shows the change, or over its stored content, again on content
// synced in between. Run on the core's queue.
func (c *Core) editContent(id string, edit func(*doc.Content) bool) error {
	if !c.canEdit(id) {
		return nil
	}
	if p := c.pages[id]; p != nil {
		if p.live.ReadOnly() {
			return nil
		}
		next := p.cur.Copy()
		if !edit(next) {
			return nil
		}
		p.Replace(next)
		p.live.Edited()
		return nil
	}
	st := c.core.Store
	err := store.ErrContentChanged
	for range 5 {
		var over string
		content := doc.Empty()
		row, rowErr := st.ContentRow(id)
		switch {
		case rowErr == nil:
			// Content that can't be read is left alone, not saved over.
			if content, err = doc.Decode(row.Content); err != nil {
				return err
			}
			over = row.Content
		case !store.IsNotFound(rowErr):
			return rowErr
		}
		if !edit(content) {
			return nil
		}
		_, _, err = st.SaveContentOver(id, over, content)
		if !errors.Is(err, store.ErrContentChanged) {
			break
		}
	}
	if err == nil {
		c.core.Sync.Kick()
	}
	return err
}
