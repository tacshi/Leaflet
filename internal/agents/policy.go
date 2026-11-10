package agents

import (
	"context"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/remote"
	"leaflet/internal/store"
	syncsvc "leaflet/internal/sync"
)

func (h *Hub) toolContext(pageID, title, agent string, content *doc.Content, settings store.Settings) ToolContext {
	p, err := h.st.Page(pageID)
	tc := PageTools(h.st, pageID, title, agent, content, err == nil && h.crossPage(p, settings))
	tc.ReadOnly = err != nil || !h.st.CanEdit(p)
	if err != nil || !h.private(p) {
		tc.CreatePage = nil
	}
	return tc
}

func (h *Hub) crossPage(p store.Page, settings store.Settings) bool {
	return settings.AgentsReadOtherPages && p.ID != "" && h.private(p)
}

func (h *Hub) alone(ctx context.Context, id string, online bool) bool {
	p, err := h.st.Page(id)
	return err == nil && h.private(p) && !h.publicElsewhere(ctx, id, online)
}

func (h *Hub) linkedNow(ctx context.Context, id string, online bool) bool {
	return h.PublicLink != nil && h.PublicLink(id) || h.publicElsewhere(ctx, id, online)
}

func (h *Hub) private(p store.Page) bool {
	return !OthersHave(h.st, p) && (h.PublicLink == nil || !h.PublicLink(p.ID))
}

func OthersHave(st *store.Store, p store.Page) bool {
	if sharedWithOthers(st, p) {
		return true
	}
	guested, err := st.HasGuestComments(p.ID)
	return guested || err != nil
}

const publicLinkLease = 15 * time.Minute

func (h *Hub) ServePublicLink(pageID string) (stop func()) {
	if h.be == nil || !h.be.SignedIn() {
		if h.ICloudLease != nil {
			return h.ICloudLease(pageID)
		}
		return func() {}
	}
	h.linksMu.Lock()
	if h.leases == nil {
		h.leases = map[string]int{}
	}
	h.leases[pageID]++
	gen := h.leases[pageID]
	h.linksMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var held string
	go func() {
		defer guard.Recover()
		defer close(done)
		for {
			if until, err := h.be.RPC(ctx, "lease_public_link", map[string]any{"p_page_id": pageID}); err != nil {
				if ctx.Err() == nil {
					log.Printf("public link: %v", err)
				}
			} else if s, ok := until.(string); ok {
				held = s
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(publicLinkLease):
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			go func() {
				defer guard.Recover()
				<-done
				h.linksMu.Lock()
				newer := h.leases[pageID] != gen
				h.linksMu.Unlock()
				if newer || held == "" {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, err := h.be.RPC(ctx, "lease_public_link", map[string]any{"p_page_id": pageID, "p_ending": held}); err != nil {
					log.Printf("public link: %v", err)
				}
			}()
		})
	}
}

func (h *Hub) publicElsewhere(ctx context.Context, pageID string, online bool) bool {
	if h.be == nil || !h.be.SignedIn() {
		linked, known := h.iCloudLinked()
		return !known || linked[pageID]
	}
	if !online {
		h.linksMu.Lock()
		defer h.linksMu.Unlock()
		return h.lastLease(pageID)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	row, err := h.be.SelectOne(ctx, "pages", remote.Query{Select: "public_link_until",
		Filters: []string{remote.Eq("id", pageID), remote.Gt("public_link_until", "now")}})
	h.linksMu.Lock()
	defer h.linksMu.Unlock()
	if err != nil {
		return h.lastLease(pageID)
	}
	if h.links == nil {
		h.links = map[string]time.Time{}
	}
	s, _ := row["public_link_until"].(string)
	until, _ := time.Parse(time.RFC3339Nano, s)
	h.links[pageID] = until
	return row != nil
}

func (h *Hub) lastLease(pageID string) bool {
	if until, ok := h.links[pageID]; ok {
		return until.After(time.Now())
	}
	p, err := h.st.Page(pageID)
	return err != nil || !h.listed[p.SpaceID]
}

const iCloudLinkDrift = 15 * time.Minute

func (h *Hub) iCloudLinked() (linked map[string]bool, known bool) {
	linked = map[string]bool{}
	if h.ICloudLinks == nil {
		return linked, true
	}
	leases, err := h.ICloudLinks()
	if err != nil {
		log.Printf("public links in iCloud Drive: %v", err)
	}
	now := time.Now()
	for id, until := range leases {
		if until.Add(iCloudLinkDrift).After(now) {
			linked[id] = true
		}
	}
	return linked, err == nil
}

var errOffline = errors.New("offline")

func (h *Hub) publicElsewhereIn(ctx context.Context, space string, online bool) func(pageID string) bool {
	if h.be == nil || !h.be.SignedIn() {
		linked, known := h.iCloudLinked()
		return func(id string) bool { return !known || linked[id] }
	}
	out := map[string]bool{}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rows []map[string]any
	err := errOffline
	if online {
		rows, err = h.be.Select(ctx, "pages", remote.Query{Select: "id,public_link_until",
			Filters: []string{remote.Eq("space_id", space), remote.Gt("public_link_until", "now")}})
	}
	h.linksMu.Lock()
	defer h.linksMu.Unlock()
	if err != nil {
		now, listed := time.Now(), h.listed[space]
		known := make(map[string]bool, len(h.links))
		for id, until := range h.links {
			known[id] = until.After(now)
		}
		return func(id string) bool {
			linked, ok := known[id]
			return linked || !ok && !listed
		}
	}
	if h.links == nil {
		h.links = map[string]time.Time{}
	}
	if h.listed == nil {
		h.listed = map[string]bool{}
	}
	h.listed[space] = true
	for _, r := range rows {
		id, _ := r["id"].(string)
		s, _ := r["public_link_until"].(string)
		until, _ := time.Parse(time.RFC3339Nano, s)
		h.links[id], out[id] = until, true
	}
	for id := range h.links {
		if p, err := h.st.Page(id); !out[id] && err == nil && p.SpaceID == space {
			delete(h.links, id)
		}
	}
	return func(id string) bool { return out[id] }
}

func (h *Hub) privacy(pages []store.Page) (private, people func(store.Page) bool) {
	others, err := h.st.SharedBeyond(h.st.Session().UserID)
	guested, gerr := h.st.GuestCommented()
	if err != nil || gerr != nil {
		return func(store.Page) bool { return false }, func(store.Page) bool { return true }
	}
	byID := make(map[string]store.Page, len(pages))
	for _, p := range pages {
		byID[p.ID] = p
	}
	shared := sharing(h.st, func(id string) bool { return others[id] }, func(id string) (store.Page, error) {
		if p, ok := byID[id]; ok {
			return p, nil
		}
		return h.st.Page(id)
	})
	return func(p store.Page) bool {
		return !shared(p) && !guested[p.ID] && (h.PublicLink == nil || !h.PublicLink(p.ID))
	}, shared
}

func sharedWithOthers(st *store.Store, p store.Page) bool {
	me := st.Session().UserID
	return sharing(st, func(id string) bool {
		members, err := st.Members(id)
		return err != nil || slices.ContainsFunc(members, func(m store.Member) bool { return m.UserID != me })
	}, st.Page)(p)
}

func ShowsToOthers(st *store.Store, p, parent store.Page) bool {
	if !OthersHave(st, parent) {
		return false
	}
	if membersUnread(st) {
		return p.SpaceID == st.Session().SpaceID
	}
	return !othersSurelyHave(st, p)
}

func othersSurelyHave(st *store.Store, p store.Page) bool {
	me := st.Session().UserID
	if sharing(st, func(id string) bool {
		members, err := st.Members(id)
		return err == nil && slices.ContainsFunc(members, func(m store.Member) bool { return m.UserID != me })
	}, st.Page)(p) {
		return true
	}
	guested, err := st.HasGuestComments(p.ID)
	return err == nil && guested
}

func membersUnread(st *store.Store) bool {
	known, _ := st.Value(syncsvc.OwnMembersKey)
	return st.Session().SignedIn && known == ""
}

func sharing(st *store.Store, others func(id string) bool, page func(id string) (store.Page, error)) func(store.Page) bool {
	sess := st.Session()
	if membersUnread(st) {
		return func(store.Page) bool { return true }
	}
	found := map[string]bool{}
	var shared func(p store.Page) bool
	shared = func(p store.Page) bool {
		if p.ID == "" {
			return false
		}
		if v, ok := found[p.ID]; ok {
			return v
		}
		found[p.ID] = false
		v := p.SpaceID != sess.SpaceID || others(p.ID)
		if !v && p.ParentID != "" {
			if parent, err := page(p.ParentID); err == nil {
				v = shared(parent)
			}
		}
		found[p.ID] = v
		return v
	}
	return shared
}

func (h *Hub) web(settings store.Settings) bool {
	return settings.AgentsUseWeb && !settings.WorkOffline && h.host.Online()
}
