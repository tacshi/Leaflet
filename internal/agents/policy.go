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

// What a run may read: the page's tools, and the user's other pages only
// where no one else writes or reads.

// toolContext is the tools a run on a page has: the page's, the user's
// other pages where crossPage lets it read them, and none that edit a
// page the user may only view.
func (h *Hub) toolContext(pageID, title, agent string, content *doc.Content, settings store.Settings) ToolContext {
	p, err := h.st.Page(pageID)
	tc := PageTools(h.st, pageID, title, agent, content, err == nil && h.crossPage(p, settings))
	// An agent edits only what the user may.
	tc.ReadOnly = err != nil || !h.st.CanEdit(p)
	// A page of the user's own made from one someone else writes or reads
	// would take what they wrote into the user's pages, where the user's
	// workspace agent reads it as theirs: only from a page of no one else.
	if err != nil || !h.private(p) {
		tc.CreatePage = nil
	}
	return tc
}

// crossPage reports whether an agent on p may read the user's other
// pages. Not on a page other people write in too: what they wrote could
// tell the agent to copy the user's other pages into it. Nor on one with
// a public link, or a guest's comment from one: its visitors read what
// the agent copies into it, and a guest's comment is someone else's
// writing, though stored as the user's. A public link this device serves
// it knows here; one another device serves, the server says, or iCloud
// Drive signed out, which a run on this device asks before it starts
// (publicElsewhere), and the agent function reads itself.
func (h *Hub) crossPage(p store.Page, settings store.Settings) bool {
	return settings.AgentsReadOtherPages && p.ID != "" && h.private(p)
}

// alone reports whether page id is the user's alone, as the page a run
// makes pages of the user's own from must be, and each page it reads
// (ToolContext.Alone): private, and with no public link another device
// serves (publicElsewhere).
func (h *Hub) alone(ctx context.Context, id string, online bool) bool {
	p, err := h.st.Page(id)
	return err == nil && h.private(p) && !h.publicElsewhere(ctx, id, online)
}

// linkedNow reports whether page id has a public link now: one this
// device serves (PublicLink), or one another does, as the server or iCloud
// Drive says (publicElsewhere). A run reads it again as it goes on
// (ToolContext.LinkedNow).
func (h *Hub) linkedNow(ctx context.Context, id string, online bool) bool {
	return h.PublicLink != nil && h.PublicLink(id) || h.publicElsewhere(ctx, id, online)
}

// private reports whether no one but the user reads or writes p: no one
// else has it, no guest commented on it, and it has no public link.
func (h *Hub) private(p store.Page) bool {
	return !sharedWithOthers(h.st, p) && !h.st.HasGuestComments(p.ID) && (h.PublicLink == nil || !h.PublicLink(p.ID))
}

// publicLinkLease is how often the device serving a public link says so
// again (ServePublicLink): each lease the server gives lasts an hour by
// its own clock, so one missed renewal leaves the link counted, and the
// link of a device that quit without saying stops counting by then.
const publicLinkLease = 15 * time.Minute

// ServePublicLink says on the server that this device serves a public
// link of page pageID, and says it again before each lease runs out
// (publicLinkLease), until stop: the agent function's runs, and other
// devices', then read none of the user's other pages into the page, as
// this device's do not (PublicLink). The server sets each lease by its own
// clock, which those runs compare it with. Stopping ends the lease this
// one holds alone: a newer one of the page, as of the link started again
// for other addresses, stays, whichever reaches the server first. Signed
// out, the page goes to other devices through iCloud Drive alone, and it
// says so there (ICloudLease). Any goroutine may call it, and stop.
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
	// held is the lease the server last gave, which the goroutine alone
	// writes, and stop reads once it has ended.
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

// publicElsewhere reports whether page pageID has a public link another
// device serves, as the server says by its own clock (ServePublicLink); a
// run on this device asks it as it starts. Where the server cannot be
// asked, as offline, the lease it last gave stands, by this device's
// clock, none before it gave one: what the run writes reaches the link
// only once this device syncs the page. Signed out, the other devices say
// so in iCloud Drive (iCloudLinked).
func (h *Hub) publicElsewhere(ctx context.Context, pageID string, online bool) bool {
	if h.be == nil || !h.be.SignedIn() {
		return h.iCloudLinked()[pageID]
	}
	// Offline, the server is not asked: the lease it last gave stands.
	if !online {
		h.linksMu.Lock()
		defer h.linksMu.Unlock()
		return h.links[pageID].After(time.Now())
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	row, err := h.be.SelectOne(ctx, "pages", remote.Query{Select: "public_link_until",
		Filters: []string{remote.Eq("id", pageID), remote.Gt("public_link_until", "now")}})
	h.linksMu.Lock()
	defer h.linksMu.Unlock()
	if err != nil {
		return h.links[pageID].After(time.Now())
	}
	if h.links == nil {
		h.links = map[string]time.Time{}
	}
	s, _ := row["public_link_until"].(string)
	until, _ := time.Parse(time.RFC3339Nano, s)
	h.links[pageID] = until
	return row != nil
}

// iCloudLinkDrift is how far another device's clock may be off from this
// one's, as its leases in iCloud Drive are by its own: a lease counts
// until that long after it ends.
const iCloudLinkDrift = 15 * time.Minute

// iCloudLinked are the pages with a public link another device says in
// iCloud Drive that it serves (ICloudLinks), as their leases stand now.
func (h *Hub) iCloudLinked() map[string]bool {
	out := map[string]bool{}
	if h.ICloudLinks == nil {
		return out
	}
	now := time.Now()
	for id, until := range h.ICloudLinks() {
		if until.Add(iCloudLinkDrift).After(now) {
			out[id] = true
		}
	}
	return out
}

// errOffline is the server not asked, this device being offline.
var errOffline = errors.New("offline")

// publicElsewhereIn are the pages of space with a public link another
// device serves, as publicElsewhere has one page's: a workspace run asks
// it as it starts, of all the pages it may change. Where the server cannot
// be asked, the leases it last gave stand.
func (h *Hub) publicElsewhereIn(ctx context.Context, space string, online bool) map[string]bool {
	if h.be == nil || !h.be.SignedIn() {
		return h.iCloudLinked()
	}
	out := map[string]bool{}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rows []map[string]any
	err := errOffline
	// Offline, the server is not asked: the leases it last gave stand.
	if online {
		rows, err = h.be.Select(ctx, "pages", remote.Query{Select: "id,public_link_until",
			Filters: []string{remote.Eq("space_id", space), remote.Gt("public_link_until", "now")}})
	}
	h.linksMu.Lock()
	defer h.linksMu.Unlock()
	if err != nil {
		for id, until := range h.links {
			if until.After(time.Now()) {
				out[id] = true
			}
		}
		return out
	}
	if h.links == nil {
		h.links = map[string]time.Time{}
	}
	for _, r := range rows {
		id, _ := r["id"].(string)
		s, _ := r["public_link_until"].(string)
		until, _ := time.Parse(time.RFC3339Nano, s)
		h.links[id], out[id] = until, true
	}
	// Those of the space it no longer lists have ended.
	for id := range h.links {
		if p, err := h.st.Page(id); !out[id] && err == nil && p.SpaceID == space {
			delete(h.links, id)
		}
	}
	return out
}

// privacy is private for many pages at once, as list_pages asks it of
// all the user's, and people sharedWithOthers: who else has them and
// which have a guest's comment are read once for them all, and pages
// finds those they are inside. Either unread, none is taken for private,
// and each for had by others.
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

// sharedWithOthers reports whether anyone but the user has a page: it is
// in someone else's space, or it or a page it is inside has members other
// than the user. Signed in, the user's own pages count as shared until
// sync has read who they are shared with: members are kept of a page only
// once something loaded them, so none kept says nothing before then.
func sharedWithOthers(st *store.Store, p store.Page) bool {
	me := st.Session().UserID
	return sharing(st, func(id string) bool {
		return slices.ContainsFunc(st.MembersOrNil(id), func(m store.Member) bool { return m.UserID != me })
	}, st.Page)(p)
}

// sharing is sharedWithOthers over others, which reports whether a page
// has members other than the user, and page, which finds the page one is
// inside. What it finds of a page it keeps, for the pages inside it.
func sharing(st *store.Store, others func(id string) bool, page func(id string) (store.Page, error)) func(store.Page) bool {
	sess := st.Session()
	if known, _ := st.Value(syncsvc.OwnMembersKey); sess.SignedIn && known == "" {
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
		// Not shared while it is worked out, so a loop of parents ends.
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

// web reports whether a run may use the web: the user lets agents, and
// the app is online.
func (h *Hub) web(settings store.Settings) bool {
	return settings.AgentsUseWeb && !settings.WorkOffline && h.host.Online()
}
