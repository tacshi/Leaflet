package agents

import (
	"leaflet/internal/doc"
	"leaflet/internal/store"
)

type Pages interface {
	LinkSubpage(parent, id, after string) error
	UnlinkSubpage(parent, id string) error
	MovePageAt(id, parent string, at *doc.Position, move func() error) (left *doc.Position, err error)
	DeletePage(id string) (ids []string, card doc.Position, err error)
	RestorePages(ids []string, card doc.Position) error
}

type pagesHost interface{ Pages() Pages }

func (h *Hub) pages() Pages {
	if ph, ok := h.host.(pagesHost); ok {
		if p := ph.Pages(); p != nil {
			return p
		}
	}
	return storePages{h.st}
}

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
