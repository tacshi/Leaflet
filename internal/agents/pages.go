package agents

import (
	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// Pages changes the user's pages as the app has them (core.Pages): a page
// open in the app through its editor, as a change from outside it, the
// cards of its subpages kept in step. Its methods run on the main thread.
type Pages interface {
	LinkSubpage(parent, id, after string) error
	UnlinkSubpage(parent, id string) error
	// MovePageAt moves page id under page parent, or to the top level, by
	// move, its card going in parent at at, or at its end for nil, and
	// returns where its card was in the parent it left, nil for none.
	MovePageAt(id, parent string, at *doc.Position, move func() error) (left *doc.Position, err error)
	DeletePage(id string) (ids []string, card doc.Position, err error)
	RestorePages(ids []string, card doc.Position) error
}

// pagesHost is a host that changes pages as the app has them.
type pagesHost interface{ Pages() Pages }

// pages is how the hub changes pages: as the host has them, or in the
// store alone, without their cards, for a host that has no Pages.
func (h *Hub) pages() Pages {
	if ph, ok := h.host.(pagesHost); ok {
		if p := ph.Pages(); p != nil {
			return p
		}
	}
	return storePages{h.st}
}

// storePages changes pages in the store alone.
type storePages struct{ st *store.Store }

func (storePages) LinkSubpage(string, string, string) error { return nil }

func (storePages) UnlinkSubpage(string, string) error { return nil }

func (storePages) MovePageAt(_, _ string, _ *doc.Position, move func() error) (*doc.Position, error) {
	return nil, move()
}

func (p storePages) DeletePage(id string) ([]string, doc.Position, error) {
	ids, err := p.st.DeletePage(id)
	return ids, doc.Position{At: "end"}, err
}

func (p storePages) RestorePages(ids []string, _ doc.Position) error { return p.st.RestorePages(ids) }
