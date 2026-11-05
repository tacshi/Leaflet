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

// errStale means someone saved the page's content between reading it and
// saving over it.
var errStale = errors.New("the page changed on the server while saving")

// errServerUnreadable is a page whose content on the server cannot be
// read here, as one a newer version of Leaflet wrote may not be. It is
// neither merged nor saved over, as an empty page would take its place:
// the server's copy stays, this device keeps its own, and its change
// stays queued.
var errServerUnreadable = errors.New("the page's content on the server could not be read, so it was not synced")

func (s *Service) pushContent(ctx context.Context, pageID string) (bool, error) {
	// A page that is gone has nothing to send; one that cannot be read now
	// stays queued.
	if _, err := s.st.Page(pageID); store.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	// Another device or an agent may save in between: merge with what it
	// saved and try again.
	for range 3 {
		done, err := s.saveContent(ctx, pageID)
		if !errors.Is(err, errStale) {
			return done, err
		}
	}
	// The server saves over no row for someone who may no longer edit the
	// page, as for a save that lost the race: its members, as the server
	// has them now, tell which.
	if cannot, err := s.cannotEdit(ctx, pageID); err != nil {
		return false, err
	} else if cannot {
		return false, errCannotEdit
	}
	return false, errors.New("the page kept changing on the server")
}

// saveContent merges this device's content with the server's and saves the
// result, if the server still has the revision it merged with.
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
	// The server's revision tells whether it has content this device has
	// not merged: its content comes only then, as a page may be large.
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: savedCols, Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return false, err
	}
	merged := local
	var conflicts []doc.Conflict
	if remoteRow != nil && behind(row, remoteRow) {
		// What came is the revision the save goes over: one saved since
		// the first read is merged too.
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
	values := map[string]any{"content": json.RawMessage(encoded), "plain_text": merged.PlainText()}
	// The server answers with the revision and time it saved, not the
	// content this device sent it.
	var saved []map[string]any
	if remoteRow == nil {
		values["page_id"] = pageID
		saved, err = s.be.InsertReturning(ctx, "page_content", values, savedCols)
		if remote.IsConflict(err) {
			return false, errStale
		}
	} else {
		saved, err = s.be.UpdateReturning(ctx, "page_content", []string{
			remote.Eq("page_id", pageID), remote.Eq("revision", fmt.Sprint(store.Int(remoteRow, "revision"))),
		}, values, savedCols)
		if err == nil && len(saved) == 0 {
			return false, errStale
		}
	}
	if err != nil {
		return false, err
	}
	savedAt, savedRev := time.Now(), int64(0)
	if len(saved) > 0 {
		savedAt, savedRev = store.Time(saved[0]["updated_at"]), store.Int(saved[0], "revision")
	}
	// The user may have kept typing while this was in flight, or type while
	// this merges: the merge is stored only over the content it merged.
	// What that merge's conflicts lose is kept, as the first merge's is.
	var meanwhile []doc.Conflict
	// The server has the merge now: unless it is stored here, with its
	// revision, the change stays queued, and goes again over it.
	before, next, err := s.storeMerge(pageID, func(now store.ContentRow, kept bool) (*doc.Content, error) {
		meanwhile = nil
		switch {
		case !kept:
			return nil, nil // the page went meanwhile
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
		return s.st.PushedContent(pageID, over, content, plain, encoded, savedAt, savedRev)
	})
	if err != nil || next == nil {
		return err == nil, err
	}
	for _, c := range append(conflicts, meanwhile...) {
		_ = s.st.AddConflict(pageID, c)
	}
	// What was typed meanwhile is not on the server yet.
	return before == row.Content, nil
}

// savedCols are the columns of a page's content on the server that say
// which version it has, and contentCols those with the content too.
const (
	savedCols   = "revision, updated_at"
	contentCols = "content, revision, updated_at"
)

// behind reports whether the server has content this device has not
// merged: it never synced the page, or the server has a revision it has
// not seen.
func behind(row store.ContentRow, remoteRow map[string]any) bool {
	return row.RemoteUpdatedAt.IsZero() || store.Int(remoteRow, "revision") != row.RemoteRevision
}

// baseOf is the content a page had when it last synced, which merges
// start from: nil before the first sync, and when it cannot be read.
func baseOf(row store.ContentRow) *doc.Content {
	if row.BaseContent == "" {
		return nil
	}
	base, _ := doc.Decode(row.BaseContent)
	return base
}

// contentOf reads a page's content as the server sent it.
func contentOf(v any) (*doc.Content, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return doc.Decode(string(raw))
}

// errCannotEdit is page content the server will not save, as the user may
// no longer edit the page: the change gives way to the server's content.
var errCannotEdit = errors.New("you can no longer edit the page")

// cannotEdit reports whether the user may not edit a page shared with
// them, by its members loaded again from the server. Their own pages they
// may always edit.
func (s *Service) cannotEdit(ctx context.Context, pageID string) (bool, error) {
	p, err := s.st.Page(pageID)
	if err != nil || p.SpaceID == s.st.Session().SpaceID {
		return false, err
	}
	if err := s.LoadMembers(ctx, pageID); err != nil {
		return false, err
	}
	// A share withdrawn takes the page from this device.
	if p, err = s.st.Page(pageID); store.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return !s.st.CanEdit(p), nil
}

// takeServerContent has a page's content the server refused give way to
// the server's, as pushRow's change does to its row: the pulls pass over
// a page while its change waits, so the edits of those who may edit it
// would never come. A change made here since stays queued, and merges.
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
	// Off the queue first, so the content goes in, as takeServer has it.
	if err := s.st.Dequeue(e); err != nil {
		return false, err
	}
	log.Printf("outbox %s/%s: %v; taking the server's content", e.Entity, pageID, refused)
	if theirs == nil {
		return true, nil
	}
	_, _, err = s.storeMerge(pageID, func(_ store.ContentRow, kept bool) (*doc.Content, error) {
		if !kept || s.st.HasPending("page_content", pageID) {
			return nil, nil // gone, or changed since: that change merges on the next push
		}
		return theirs, nil
	}, func(over string, c *doc.Content) error {
		return s.st.PutSyncedContentOver(pageID, over, c, theirs.Encode(), store.Time(remoteRow["updated_at"]), store.Int(remoteRow, "revision"))
	})
	return err == nil, err
}

// refreshRemote brings a page's content up to date with the server's. The
// content comes only when the server has a revision this device has not
// merged.
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
	if remoteRow, err = s.be.SelectOne(ctx, "page_content", remote.Query{Select: contentCols, Filters: []string{remote.Eq("page_id", pageID)}}); err != nil {
		return nil, err
	}
	if remoteRow == nil {
		return s.st.Content(pageID), nil
	}
	return s.storeRemote(pageID, remoteRow)
}

// storeRemote merges the server's content of a page, a row of contentCols,
// into this device's, and stores it.
func (s *Service) storeRemote(pageID string, remoteRow map[string]any) (*doc.Content, error) {
	remoteAt := store.Time(remoteRow["updated_at"])
	remoteContent, err := contentOf(remoteRow["content"])
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
		// Content here that cannot be read, with no change waiting, is the
		// server's now.
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
		return s.st.PutSyncedContentOver(pageID, over, c, remoteContent.Encode(), remoteAt, store.Int(remoteRow, "revision"))
	})
	if err != nil {
		return nil, err
	}
	if next == nil {
		// This device has that revision, which the server stamped again
		// without changing it, as a move of the page does: kept as of then,
		// it is passed over by the pulls after, rather than fetched again
		// by each while it is among the rows they read again (overlap).
		if err := s.st.MergedRemoteAt(pageID, store.Int(remoteRow, "revision"), remoteAt); err != nil {
			return nil, err
		}
		return here, nil
	}
	for _, c := range conflicts {
		if err := s.st.AddConflict(pageID, c); err != nil {
			return nil, err
		}
	}
	return next, nil
}

// pullContents brings the content of the pages changed on the server since
// the cursor, then that of the pages that wait (pullWaiting): the pages
// that wait never hold up the ones the cursor passes over.
func (s *Service) pullContents(ctx context.Context) error {
	if err := s.paged("page_content", func(bool) (bool, error) { return s.pullContentsPage(ctx) }); err != nil {
		return err
	}
	return s.pullWaiting(ctx)
}

// contentBatch is how many pages' content one request fetches; tests make
// it smaller.
var contentBatch = 50

// pullContentsPage is a page of pullContents: the content changed after
// the cursor, which moves on past each page handled. A page not here yet
// waits for its row, which a later pull brings; one this device does not
// keep is brought as it is opened.
func (s *Service) pullContentsPage(ctx context.Context) (bool, error) {
	rows, full, err := s.changed(ctx, "page_content", "page_id", "page_id, updated_at", s.cursor("page_content"), 200)
	if err != nil {
		return false, err
	}
	allOffline := s.st.Settings().AllPagesOffline
	waiting := s.waiting()
	start := s.cursor("page_content")
	// handled says, of each row, whether it is done with; fetch are the
	// pages whose content comes, in the rows' order.
	handled := make([]bool, len(rows))
	var fetch []string
	var failed error
	for i, r := range rows {
		id := store.S(r, "page_id")
		// Content this device has merged as of that version or later, as
		// the one it pushed itself, does not come again.
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
	// The cursor moves on past the rows handled, up to the first that is
	// not; rows of the overlap behind it leave it where it is.
	cur := start
	for i, r := range rows {
		if !handled[i] {
			break
		}
		if !start.passed(r, "page_id") {
			cur = cursor{store.S(r, "updated_at"), store.S(r, "page_id")}
		}
	}
	// The pages that wait are kept before the cursor passes them: the
	// other way round, a crash between would lose them.
	if err := s.setWaiting(waiting); err != nil {
		return false, errors.Join(failed, err)
	}
	if cur != start {
		s.setCursor("page_content", cur)
	}
	return full, failed
}

// waitingBatch is how many of the pages that wait one pass brings, the
// open ones first, then the others from after the one the pass before
// brought last: a pass that left more to bring, and brought some, has the
// next run soon. Tests make it smaller.
var waitingBatch = 500

// pullWaiting brings the content of the pages that wait, as their row came
// after their content changed, or as this device keeps them from now on
// (KeepContent), if it keeps them. Pages that stay waiting, as content
// this version cannot read, do not keep the others from coming: each pass
// goes on round them from where the last stopped.
func (s *Service) pullWaiting(ctx context.Context) error {
	waiting := s.waiting()
	if len(waiting) == 0 {
		return nil
	}
	allOffline := s.st.Settings().AllPagesOffline
	var open, rest []string
	for _, id := range slices.Sorted(maps.Keys(waiting)) {
		p, err := s.st.Page(id)
		if err != nil {
			continue // its row comes later
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

// fetchContents fetches the content of pages from the server, contentBatch
// at a time, in order, and stores each merged. It reports the pages done
// with, which leave waiting: those stored, those the server has no content
// of, and those whose content cannot be read, which wait, so the other
// pages' content still comes. It stops at the first that fails, which
// waits too.
func (s *Service) fetchContents(ctx context.Context, ids []string, waiting map[string]bool) (map[string]bool, error) {
	done := map[string]bool{}
	for chunk := range slices.Chunk(ids, contentBatch) {
		rows, err := s.be.Select(ctx, "page_content", remote.Query{Select: "page_id, " + contentCols, Filters: []string{remote.In("page_id", chunk)}})
		if err != nil {
			for _, id := range chunk {
				waiting[id] = true
			}
			return done, err
		}
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
				// The page keeps its content here, and waits until both
				// copies can be read; the other pages' content still comes.
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

// hasContentAt reports whether this device has merged the server's
// content of a page as of a time, or later. The content is not read.
func (s *Service) hasContentAt(pageID string, at time.Time) bool {
	merged, err := s.st.RemoteAt(pageID)
	return err == nil && !merged.IsZero() && merged.UnixMilli() >= at.UnixMilli()
}
