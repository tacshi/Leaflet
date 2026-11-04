package agents

import (
	"context"
	"log"
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
	return tc
}

// crossPage reports whether an agent on p may read the user's other
// pages. Not on a page other people write in too: what they wrote could
// tell the agent to copy the user's other pages into it. Nor on one with
// a public link, or a guest's comment from one: its visitors read what
// the agent copies into it, and a guest's comment is someone else's
// writing, though stored as the user's. A public link this device serves
// it knows here; one another device serves, the server says, which a run
// on this device asks it before it starts (publicElsewhere), and the agent
// function reads itself.
func (h *Hub) crossPage(p store.Page, settings store.Settings) bool {
	return settings.AgentsReadOtherPages && p.ID != "" && !sharedWithOthers(h.st, p) &&
		!h.st.HasGuestComments(p.ID) && (h.PublicLink == nil || !h.PublicLink(p.ID))
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
// out, the page is on no other device to say it to. Any goroutine may call
// it, and stop.
func (h *Hub) ServePublicLink(pageID string) (stop func()) {
	if h.be == nil || !h.be.SignedIn() {
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
// only once this device syncs the page.
func (h *Hub) publicElsewhere(ctx context.Context, pageID string) bool {
	if h.be == nil || !h.be.SignedIn() {
		return false
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

// sharedWithOthers reports whether anyone but the user has a page: it is
// in someone else's space, or it or a page it is inside has members other
// than the user. Signed in, the user's own pages count as shared until
// sync has read who they are shared with: members are kept of a page only
// once something loaded them, so none kept says nothing before then.
func sharedWithOthers(st *store.Store, p store.Page) bool {
	sess := st.Session()
	if known, _ := st.Value(syncsvc.OwnMembersKey); sess.SignedIn && known == "" {
		return true
	}
	seen := map[string]bool{}
	for p.ID != "" && !seen[p.ID] {
		seen[p.ID] = true
		if p.SpaceID != sess.SpaceID {
			return true
		}
		for _, m := range st.MembersOrNil(p.ID) {
			if m.UserID != sess.UserID {
				return true
			}
		}
		if p.ParentID == "" {
			break
		}
		parent, err := st.Page(p.ParentID)
		if err != nil {
			break
		}
		p = parent
	}
	return false
}

// web reports whether a run may use the web: the user lets agents, and
// the app is online.
func (h *Hub) web(settings store.Settings) bool {
	return settings.AgentsUseWeb && !settings.WorkOffline && h.host.Online()
}
