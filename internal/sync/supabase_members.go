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

func (s *Service) LoadMembers(ctx context.Context, pageID string) error {
	res, err := s.be.RPC(ctx, "page_members", map[string]any{"p_page_id": pageID})
	re := (*remote.Error)(nil)
	forbidden := errors.As(err, &re) && re.Status == http.StatusForbidden
	loaded := func(err error) error {
		if err == nil {
			s.mu.Lock()
			s.membersAt[pageID] = time.Now()
			s.mu.Unlock()
		}
		return err
	}
	if forbidden {
		return loaded(s.withdrawn(ctx, pageID))
	}
	if err != nil {
		return err
	}
	list, ok := res.([]any)
	if !ok {
		return errors.New("the server's list of a page's members could not be read")
	}
	var members []store.Member
	for _, r := range list {
		m, _ := r.(map[string]any)
		accepted, ok := m["accepted"].(bool)
		members = append(members, store.Member{PageID: pageID, UserID: store.S(m, "user_id"), Role: store.S(m, "role"),
			DisplayName: store.S(m, "display_name"), Email: store.S(m, "email"), Accepted: accepted || !ok})
	}
	return loaded(s.st.SetMembers(pageID, members))
}

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

func (s *Service) forgetMembers(pageID string) error {
	pages, err := s.st.AllPages()
	if err != nil {
		return err
	}
	tree, err := s.st.TreeOf(pages)
	if err != nil {
		return err
	}
	for _, p := range pages {
		if tree.Members(p.ID) != nil && tree.IsDescendant(p.ID, pageID) {
			if err := s.st.SetMembers(p.ID, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

const membersEvery = 10 * time.Minute

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

const OwnMembersKey = "sync.own_members_at"

func (s *Service) pullOwnMembers(ctx context.Context) error {
	uid := s.be.UserID()
	shared := map[string]map[string]string{}
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
			continue
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
	if at, ok := s.st.Value(OwnMembersKey); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && time.Since(t) < membersEvery {
			return nil
		}
	}
	return s.st.SetValue(OwnMembersKey, time.Now().UTC().Format(time.RFC3339Nano))
}

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

const ownSwept = "own pages"

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
	tree, err := s.st.TreeOf(pages)
	if err != nil {
		return err
	}
	uid, own := s.be.UserID(), s.st.Session().SpaceID
	for _, p := range pages {
		if p.SpaceID != own || !slices.ContainsFunc(tree.Members(p.ID), func(m store.Member) bool { return m.UserID != uid }) {
			continue
		}
		under, seen := false, map[string]bool{}
		for id := p.ID; id != "" && !seen[id] && !under; {
			seen[id], under = true, shared[id] != nil
			id = tree.Parent(id)
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

func (s *Service) reloadMembersUnder(ctx context.Context, topID string, pages []store.Page) {
	tree, err := s.st.TreeOf(pages)
	if err != nil {
		log.Printf("members under %s: %v", topID, err)
		return
	}
	for _, p := range pages {
		if p.ID == topID || tree.Members(p.ID) == nil || !tree.IsDescendant(p.ID, topID) {
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
