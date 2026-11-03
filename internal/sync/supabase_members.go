package sync

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// LoadMembers stores who a page is shared with, as the server has them,
// each with their role on it, which a page above it may give. A page shared
// with the user that they can no longer see, its share withdrawn, goes
// from this device with the pages under it (store.RemoveSharedPages).
func (s *Service) LoadMembers(ctx context.Context, pageID string) error {
	res, err := s.be.RPC(ctx, "page_members", map[string]any{"p_page_id": pageID})
	re := (*remote.Error)(nil)
	forbidden := errors.As(err, &re) && re.Status == http.StatusForbidden
	// They count as loaded (pullMembers) only once they are handled: one
	// that failed is tried again on the next pass.
	loaded := func(err error) error {
		if err == nil {
			s.mu.Lock()
			s.membersAt[pageID] = time.Now()
			s.mu.Unlock()
		}
		return err
	}
	if forbidden {
		// Withdrawn, the page is handled once it is gone or keeps no
		// roles: the pulls go on past it.
		return loaded(s.withdrawn(ctx, pageID))
	}
	if err != nil {
		return err
	}
	var members []store.Member
	list, _ := res.([]any)
	for _, r := range list {
		m, _ := r.(map[string]any)
		// A server from before invites says nothing: every share was one.
		accepted, ok := m["accepted"].(bool)
		members = append(members, store.Member{PageID: pageID, UserID: store.S(m, "user_id"), Role: store.S(m, "role"),
			DisplayName: store.S(m, "display_name"), Email: store.S(m, "email"), Accepted: accepted || !ok})
	}
	return loaded(s.st.SetMembers(pageID, members))
}

// withdrawn handles members refused for a page: one the server no longer
// shows the user at all goes from this device; any other keeps no roles,
// nor do the pages under it, as those kept would still let them be
// edited here. It returns why it could not.
func (s *Service) withdrawn(ctx context.Context, pageID string) error {
	rows, err := s.be.Select(ctx, "pages", remote.Query{Select: "id", Filters: []string{remote.Eq("id", pageID)}})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		removed, err := s.st.RemoveSharedPages(pageID)
		if err == nil && len(removed) > 0 {
			log.Printf("share of %s withdrawn: removed %d pages", pageID, len(removed))
		}
		return err
	}
	return s.forgetMembers(pageID)
}

// forgetMembers drops the members kept of a page and of the pages under it.
func (s *Service) forgetMembers(pageID string) error {
	pages, err := s.st.AllPages()
	if err != nil {
		return err
	}
	for _, p := range pages {
		if s.st.MembersOrNil(p.ID) != nil && s.st.IsDescendant(p.ID, pageID) {
			if err := s.st.SetMembers(p.ID, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// membersEvery is how often pullMembers loads members it has again, so a
// share withdrawn or changed to view only stops letting the user edit.
const membersEvery = 10 * time.Minute

// pullMembers loads the members of each tree of pages shared with the user
// that has none here, as for a share accepted before sync loaded them, and
// again every membersEvery: an agent outside the app (leaflet mcp) edits
// only what they allow. A tree's top page here has them for the pages
// inside it.
func (s *Service) pullMembers(ctx context.Context) {
	pages, err := s.st.AllPages()
	if err != nil {
		return
	}
	own := s.st.Session().SpaceID
	here := map[string]bool{}
	for _, p := range pages {
		here[p.ID] = p.DeletedAt.IsZero()
	}
	for _, p := range pages {
		if p.SpaceID == own || !p.DeletedAt.IsZero() || here[p.ParentID] {
			continue
		}
		s.mu.Lock()
		at, loaded := s.membersAt[p.ID]
		s.mu.Unlock()
		if loaded && time.Since(at) < membersEvery {
			continue
		}
		if err := s.LoadMembers(ctx, p.ID); err != nil {
			log.Printf("members of %s: %v", p.ID, err)
			continue
		}
		s.reloadMembersUnder(ctx, p.ID, pages)
	}
}

// OwnMembersKey keeps when sync last read who the user's own pages are
// shared with (pullOwnMembers). Until it has, as on a device that never
// synced since signing in, an agent takes each of them for shared
// (agents.sharedWithOthers), so it does not read the user's other pages
// on one where what someone else wrote could ask it to copy them there.
const OwnMembersKey = "sync.own_members_at"

// pullOwnMembers keeps who the user's own pages are shared with current:
// the server lists the shares they accepted of the pages the user owns,
// and a page whose members here leave out someone it lists loads them
// again, or takes the share's people as they are when they do not load.
// Every membersEvery, an own page whose members here name others no share
// of it or of a page it is inside does loads them again, so a share
// withdrawn goes.
func (s *Service) pullOwnMembers(ctx context.Context) error {
	uid := s.be.UserID()
	shared := map[string]map[string]string{} // page → user → role
	var from cursor
	for {
		filters := []string{"user_id=neq." + uid, remote.NotNull("accepted_at")}
		if from.at != "" {
			filters = append(filters, "or=(page_id.gt."+quote(from.at)+",and(page_id.eq."+quote(from.at)+",user_id.gt."+quote(from.key)+"))")
		}
		rows, err := s.be.Select(ctx, "page_permissions", remote.Query{Select: "page_id, user_id, role", Filters: filters, Order: "page_id,user_id", Limit: treeRows})
		if err != nil {
			return err
		}
		for _, r := range rows {
			page := store.S(r, "page_id")
			if shared[page] == nil {
				shared[page] = map[string]string{}
			}
			shared[page][store.S(r, "user_id")] = store.S(r, "role")
		}
		if len(rows) < treeRows {
			break
		}
		last := rows[len(rows)-1]
		from = cursor{store.S(last, "page_id"), store.S(last, "user_id")}
	}
	own := s.st.Session().SpaceID
	for id, users := range shared {
		p, err := s.st.Page(id)
		if err != nil || p.SpaceID != own {
			continue // not here yet, or not the user's
		}
		if s.hasMembers(id, users) {
			continue
		}
		if err := s.LoadMembers(ctx, id); err == nil && s.hasMembers(id, users) {
			continue
		} else if err != nil {
			log.Printf("members of %s: %v", id, err)
		}
		members := []store.Member{{PageID: id, UserID: uid, Role: "owner", Accepted: true}}
		for user, role := range users {
			members = append(members, store.Member{PageID: id, UserID: user, Role: role, Accepted: true})
		}
		if err := s.st.SetMembers(id, members); err != nil {
			return err
		}
	}
	if err := s.dropOwnMembers(ctx, shared); err != nil {
		return err
	}
	return s.st.SetValue(OwnMembersKey, time.Now().UTC().Format(time.RFC3339Nano))
}

// hasMembers reports whether the members kept of a page name each of
// users.
func (s *Service) hasMembers(pageID string, users map[string]string) bool {
	kept := map[string]bool{}
	for _, m := range s.st.MembersOrNil(pageID) {
		kept[m.UserID] = true
	}
	for u := range users {
		if !kept[u] {
			return false
		}
	}
	return true
}

// ownSwept is the key of membersAt for the last sweep of dropOwnMembers.
const ownSwept = "own pages"

// dropOwnMembers loads again, every membersEvery, the members of the
// user's own pages that name others while no share the server lists
// (shared) is of the page or a page it is inside. Members that do not
// load stay, and the page counts as shared meanwhile.
func (s *Service) dropOwnMembers(ctx context.Context, shared map[string]map[string]string) error {
	s.mu.Lock()
	at, swept := s.membersAt[ownSwept]
	s.mu.Unlock()
	if swept && time.Since(at) < membersEvery {
		return nil
	}
	pages, err := s.st.AllPages()
	if err != nil {
		return err
	}
	uid, own := s.be.UserID(), s.st.Session().SpaceID
	for _, p := range pages {
		if p.SpaceID != own || !slices.ContainsFunc(s.st.MembersOrNil(p.ID), func(m store.Member) bool { return m.UserID != uid }) {
			continue
		}
		under := false
		for top := range shared {
			if s.st.IsDescendant(p.ID, top) {
				under = true
				break
			}
		}
		if !under {
			if err := s.LoadMembers(ctx, p.ID); err != nil {
				log.Printf("members of %s: %v", p.ID, err)
			}
		}
	}
	s.mu.Lock()
	s.membersAt[ownSwept] = time.Now()
	s.mu.Unlock()
	return nil
}

// reloadMembersUnder loads again the members kept of the pages inside a
// tree's top page, as the app keeps them for a page opened: the nearest
// page with the user among them says what they may do, so a share
// changed to view only must not stay editable below the top. Members that
// do not load are dropped, leaving it to the pages above.
func (s *Service) reloadMembersUnder(ctx context.Context, topID string, pages []store.Page) {
	for _, p := range pages {
		if p.ID == topID || s.st.MembersOrNil(p.ID) == nil || !s.st.IsDescendant(p.ID, topID) {
			continue
		}
		if err := s.LoadMembers(ctx, p.ID); err != nil {
			log.Printf("members of %s: %v", p.ID, err)
			if err := s.st.SetMembers(p.ID, nil); err != nil {
				log.Printf("members of %s: %v", p.ID, err)
			}
		}
	}
}
