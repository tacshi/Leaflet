package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

var errStale = errors.New("the page changed on the server while saving")

func notApplied(err error) bool {
	if errors.Is(err, errStale) {
		return true
	}
	re, ok := errors.AsType[*remote.Error](err)
	return ok && (re.Status >= 400 && re.Status < 500 || re.Code != "")
}

var errServerUnreadable = errors.New("the page's content on the server could not be read, so it was not synced")

func (s *Service) pushContent(ctx context.Context, pageID string) (bool, error) {
	if _, err := s.st.Page(pageID); store.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	for range 3 {
		done, err := s.saveContent(ctx, pageID)
		if !errors.Is(err, errStale) {
			return done, err
		}
	}
	if cannot, err := s.cannotEdit(ctx, pageID); err != nil {
		return false, err
	} else if cannot {
		return false, errCannotEdit
	}
	return false, errors.New("the page kept changing on the server")
}

func (s *Service) saveContent(ctx context.Context, pageID string) (bool, error) {
	row, err := s.st.ContentRow(pageID)
	if store.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	local, err := doc.Decode(row.Content)
	if err != nil {
		return false, errUnreadable
	}
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: savedCols, Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return false, err
	}
	merged := local
	var conflicts []doc.Conflict
	if remoteRow != nil && behind(row, remoteRow) {
		if remoteRow, err = s.be.SelectOne(ctx, "page_content", remote.Query{Select: contentCols, Filters: []string{remote.Eq("page_id", pageID)}}); err != nil {
			return false, err
		}
	}
	if remoteRow != nil && behind(row, remoteRow) {
		theirs, err := contentOf(remoteRow["content"])
		if err != nil {
			return false, errServerUnreadable
		}
		merged, conflicts = doc.Merge(baseOf(row), local, theirs)
	}
	encoded := merged.Encode()
	if clean := store.CleanJSON(encoded); clean != encoded {
		if merged, err = doc.Decode(clean); err != nil {
			return false, errUnreadable
		}
		encoded = clean
	}
	values := map[string]any{"content": json.RawMessage(encoded), "plain_text": store.CleanText(merged.PlainText())}
	held, err := s.st.HoldConflicts(pageID, conflicts)
	if err != nil {
		return false, err
	}
	var saved []map[string]any
	if remoteRow == nil {
		values["page_id"] = pageID
		saved, err = s.be.InsertReturning(ctx, "page_content", values, savedCols)
		if remote.IsConflict(err) {
			err = errStale
		}
	} else {
		saved, err = s.be.UpdateReturning(ctx, "page_content", []string{
			remote.Eq("page_id", pageID), remote.Eq("revision", fmt.Sprint(store.Int(remoteRow, "revision"))),
		}, values, savedCols)
		if err == nil && len(saved) == 0 {
			err = errStale
		}
	}
	if notApplied(err) {
		return false, errors.Join(err, s.st.DropConflicts(held))
	}
	if err != nil {
		return false, err
	}
	savedAt, savedRev := time.Now(), int64(0)
	if len(saved) > 0 {
		savedAt, savedRev = store.Time(saved[0]["updated_at"]), store.Int(saved[0], "revision")
	}
	var meanwhile []doc.Conflict
	before, next, err := s.storeMerge(pageID, func(now store.ContentRow, kept bool) (*doc.Content, error) {
		meanwhile = nil
		switch {
		case !kept:
			return nil, nil
		case now.Content == row.Content:
			return merged, nil
		}
		typed, err := doc.Decode(now.Content)
		if err != nil {
			return nil, errUnreadable
		}
		var next *doc.Content
		next, meanwhile = doc.Merge(local, typed, merged)
		return next, nil
	}, func(over string, c *doc.Content) error {
		content, plain := encoded, values["plain_text"].(string)
		if c != merged {
			content, plain = c.Encode(), c.PlainText()
		}
		return s.st.PushedContent(pageID, over, content, plain, encoded, savedAt, savedRev, meanwhile...)
	})
	if err != nil || next == nil {
		return err == nil, err
	}
	return before == row.Content, nil
}

const (
	savedCols   = "revision, updated_at"
	contentCols = "content, revision, updated_at"
)

func behind(row store.ContentRow, remoteRow map[string]any) bool {
	return row.RemoteUpdatedAt.IsZero() || store.Int(remoteRow, "revision") != row.RemoteRevision
}

func baseOf(row store.ContentRow) *doc.Content {
	if row.BaseContent == "" {
		return nil
	}
	base, _ := doc.Decode(row.BaseContent)
	return base
}

func contentOf(v any) (*doc.Content, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return doc.Decode(string(raw))
}

var errCannotEdit = errors.New("you can no longer edit the page")

func (s *Service) cannotEdit(ctx context.Context, pageID string) (bool, error) {
	p, err := s.st.Page(pageID)
	if err != nil || p.SpaceID == s.st.Session().SpaceID {
		return false, err
	}
	if err := s.LoadMembers(ctx, pageID); err != nil {
		return false, err
	}
	if p, err = s.st.Page(pageID); store.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return !s.st.CanEdit(p), nil
}

func (s *Service) takeServerContent(ctx context.Context, e store.OutboxEntry, refused error) (bool, error) {
	pageID := e.EntityID
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: contentCols, Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return false, err
	}
	var theirs *doc.Content
	if remoteRow != nil {
		if theirs, err = contentOf(remoteRow["content"]); err != nil {
			return false, errServerUnreadable
		}
	}
	log.Printf("outbox %s/%s: %v; taking the server's content", e.Entity, pageID, refused)
	if theirs == nil {
		return true, nil
	}
	_, _, err = s.storeMerge(pageID, func(_ store.ContentRow, kept bool) (*doc.Content, error) {
		if !kept {
			return nil, nil
		}
		return theirs, nil
	}, func(over string, c *doc.Content) error {
		return s.st.TakeSyncedContentOver(e, pageID, over, c, theirs.Encode(), store.Time(remoteRow["updated_at"]), store.Int(remoteRow, "revision"))
	})
	return err == nil, err
}

func (s *Service) refreshRemote(ctx context.Context, pageID string) (*doc.Content, error) {
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: savedCols, Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return nil, err
	}
	if remoteRow == nil {
		return s.st.Content(pageID), nil
	}
	if row, err := s.st.ContentRow(pageID); err == nil && !behind(row, remoteRow) {
		c, err := doc.Decode(row.Content)
		if err != nil {
			return nil, errUnreadable
		}
		return c, nil
	}
	return s.fetchRemote(ctx, pageID)
}

func (s *Service) checkRemote(ctx context.Context, pageID string) error {
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: savedCols, Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil || remoteRow == nil {
		return err
	}
	at, revision, err := s.st.RemoteAt(pageID)
	if err != nil {
		return err
	}
	if !behind(store.ContentRow{RemoteUpdatedAt: at, RemoteRevision: revision}, remoteRow) || s.cannotRead(pageID, remoteRow) {
		return nil
	}
	_, err = s.fetchRemote(ctx, pageID)
	return err
}

// cannotRead reports whether the server's row is at the revision this build already fetched and could not decode,
// which a newer build wrote: fetching it again would bring the same content. It is kept for as long as the app runs,
// so an updated app tries again.
func (s *Service) cannotRead(pageID string, remoteRow map[string]any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision, known := s.unread[pageID]
	return known && revision == store.Int(remoteRow, "revision")
}

func (s *Service) setCannotRead(pageID string, remoteRow map[string]any, cannot bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cannot {
		s.unread[pageID] = store.Int(remoteRow, "revision")
	} else {
		delete(s.unread, pageID)
	}
}

// unreadStill is the pages of ids whose content on the server is still at the revision that could not be decoded.
func (s *Service) unreadStill(ctx context.Context, ids []string) (map[string]bool, error) {
	s.mu.Lock()
	doubted := slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
		_, known := s.unread[id]
		return !known
	})
	s.mu.Unlock()
	same := map[string]bool{}
	for chunk := range slices.Chunk(doubted, contentBatch) {
		rows, err := s.be.Select(ctx, "page_content", remote.Query{Select: "page_id, revision", Filters: []string{remote.In("page_id", chunk)}})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if id := store.S(r, "page_id"); s.cannotRead(id, r) {
				same[id] = true
			}
		}
	}
	return same, nil
}

func (s *Service) fetchRemote(ctx context.Context, pageID string) (*doc.Content, error) {
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: contentCols, Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return nil, err
	}
	if remoteRow == nil {
		return s.st.Content(pageID), nil
	}
	return s.storeRemote(pageID, remoteRow)
}

func (s *Service) storeRemote(pageID string, remoteRow map[string]any) (*doc.Content, error) {
	remoteAt := store.Time(remoteRow["updated_at"])
	remoteContent, err := contentOf(remoteRow["content"])
	s.setCannotRead(pageID, remoteRow, err != nil)
	if err != nil {
		return nil, errServerUnreadable
	}
	var here *doc.Content
	var conflicts []doc.Conflict
	_, next, err := s.storeMerge(pageID, func(local store.ContentRow, kept bool) (*doc.Content, error) {
		here, conflicts = nil, nil
		if kept && !behind(local, remoteRow) {
			c, err := doc.Decode(local.Content)
			if err != nil {
				return nil, errUnreadable
			}
			here = c
			return nil, nil
		}
		if !kept || !s.st.HasPending("page_content", pageID) {
			return remoteContent, nil
		}
		mine, err := doc.Decode(local.Content)
		if err != nil {
			return nil, errUnreadable
		}
		var next *doc.Content
		next, conflicts = doc.Merge(baseOf(local), mine, remoteContent)
		return next, nil
	}, func(over string, c *doc.Content) error {
		return s.st.PutSyncedContentOver(pageID, over, c, remoteContent.Encode(), remoteAt, store.Int(remoteRow, "revision"), conflicts...)
	})
	if err != nil {
		return nil, err
	}
	if next == nil {
		if err := s.st.MergedRemoteAt(pageID, store.Int(remoteRow, "revision"), remoteAt); err != nil {
			return nil, err
		}
		return here, nil
	}
	return next, nil
}

func (s *Service) pullContents(ctx context.Context) error {
	s.contentsAsked, s.contentsSlow = false, false
	err := s.paged("page_content", func(bool) (bool, error) { return s.pullContentsPage(ctx) })
	if err == nil {
		err = s.pullWaiting(ctx)
	}
	if err == nil && s.contentsAsked && !s.contentsSlow {
		s.contentsAt = min(2*s.contentsAt, contentBatch)
	}
	return err
}

var contentBatch = 50

func (s *Service) pullContentsPage(ctx context.Context) (bool, error) {
	rows, full, err := s.changed(ctx, "page_content", "page_id", "page_id, updated_at", s.cursor("page_content"), 200)
	if err != nil {
		return false, err
	}
	allOffline := s.st.Settings().AllPagesOffline
	waiting, err := s.waiting()
	if err != nil {
		return false, err
	}
	start := s.cursor("page_content")
	handled := make([]bool, len(rows))
	var fetch []string
	var failed error
	for i, r := range rows {
		id := store.S(r, "page_id")
		if s.hasContentAt(id, store.Time(r["updated_at"])) {
			handled[i] = true
			continue
		}
		p, err := s.st.Page(id)
		switch {
		case store.IsNotFound(err):
			waiting[id], handled[i] = true, true
			continue
		case err != nil:
			failed = err
		case !s.keepsContent(p, allOffline):
			delete(waiting, id)
			handled[i] = true
			continue
		}
		if failed != nil {
			break
		}
		fetch = append(fetch, id)
	}
	if failed == nil {
		done, err := s.fetchContents(ctx, fetch, waiting)
		failed = err
		for i, r := range rows {
			if done[store.S(r, "page_id")] {
				handled[i] = true
			}
		}
	}
	cur := start
	for i, r := range rows {
		if !handled[i] {
			break
		}
		if !start.passed(r, "page_id") {
			cur = cursor{store.S(r, "updated_at"), store.S(r, "page_id")}
		}
	}
	if err := s.setWaiting(waiting); err != nil {
		return false, errors.Join(failed, err)
	}
	if cur != start {
		s.setCursor("page_content", cur)
	}
	return full, failed
}

var waitingBatch = 500

func (s *Service) pullWaiting(ctx context.Context) error {
	waiting, err := s.waiting()
	if err != nil || len(waiting) == 0 {
		return err
	}
	allOffline := s.st.Settings().AllPagesOffline
	var open, rest []string
	for _, id := range slices.Sorted(maps.Keys(waiting)) {
		p, err := s.st.Page(id)
		if err != nil {
			continue
		}
		if !s.keepsContent(p, allOffline) {
			delete(waiting, id)
			continue
		}
		s.mu.Lock()
		isOpen := s.open[id]
		s.mu.Unlock()
		if isOpen {
			open = append(open, id)
		} else {
			rest = append(rest, id)
		}
	}
	s.mu.Lock()
	after := s.waitingAfter
	s.mu.Unlock()
	if i, found := slices.BinarySearch(rest, after); after != "" {
		if found {
			i++
		}
		rest = slices.Concat(rest[i:], rest[:i])
	}
	ids := slices.Concat(open, rest)
	more := len(ids) > waitingBatch
	after = ""
	if more {
		ids = ids[:waitingBatch]
		if taken := waitingBatch - len(open); taken > 0 {
			after = rest[taken-1]
		}
	}
	s.mu.Lock()
	s.waitingAfter = after
	s.mu.Unlock()
	before := len(waiting)
	_, failed := s.fetchContents(ctx, ids, waiting)
	if err := s.setWaiting(waiting); err != nil {
		return errors.Join(failed, err)
	}
	if more && failed == nil && len(waiting) < before {
		s.Kick()
	}
	return failed
}

func (s *Service) fetchContents(ctx context.Context, ids []string, waiting map[string]bool) (map[string]bool, error) {
	done := map[string]bool{}
	skip, err := s.unreadStill(ctx, ids)
	if err != nil {
		for _, id := range ids {
			waiting[id] = true
		}
		return done, err
	}
	for id := range skip {
		waiting[id], done[id] = true, true
	}
	ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return skip[id] })
	s.contentsAsked = s.contentsAsked || len(ids) > 0
	for len(ids) > 0 {
		size := contentBatch
		if s.contentsAt > 0 {
			size = min(s.contentsAt, size)
		}
		chunk := ids[:min(size, len(ids))]
		rows, err := s.be.Select(ctx, "page_content", remote.Query{Select: "page_id, " + contentCols, Filters: []string{remote.In("page_id", chunk)}})
		if err != nil && remote.IsTooSlow(err) && ctx.Err() == nil {
			s.contentsSlow = true
			if len(chunk) > 1 {
				s.contentsAt = len(chunk) / 2
				continue
			}
			if s.serverAnswers(ctx) {
				log.Printf("pull %s: %v", chunk[0], err)
				waiting[chunk[0]], done[chunk[0]] = true, true
				ids = ids[1:]
				continue
			}
		}
		if err != nil {
			for _, id := range chunk {
				waiting[id] = true
			}
			return done, err
		}
		ids = ids[len(chunk):]
		byID := make(map[string]map[string]any, len(rows))
		for _, r := range rows {
			byID[store.S(r, "page_id")] = r
		}
		for i, id := range chunk {
			r, ok := byID[id]
			if ok {
				_, err = s.storeRemote(id, r)
			}
			if unreadable(err) {
				log.Printf("pull %s: %v", id, err)
				waiting[id], done[id] = true, true
				err = nil
				continue
			}
			if err != nil {
				for _, id := range chunk[i:] {
					waiting[id] = true
				}
				return done, err
			}
			delete(waiting, id)
			done[id] = true
		}
	}
	return done, nil
}

func (s *Service) hasContentAt(pageID string, at time.Time) bool {
	merged, _, err := s.st.RemoteAt(pageID)
	return err == nil && !merged.IsZero() && merged.UnixMilli() >= at.UnixMilli()
}
