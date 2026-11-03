package agents

import (
	"leaflet/internal/doc"
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
// writing, though stored as the user's.
func (h *Hub) crossPage(p store.Page, settings store.Settings) bool {
	return settings.AgentsReadOtherPages && p.ID != "" && !sharedWithOthers(h.st, p) &&
		!h.st.HasGuestComments(p.ID) && (h.PublicLink == nil || !h.PublicLink(p.ID))
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
