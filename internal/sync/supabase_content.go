package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: "content, revision, updated_at", Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return false, err
	}
	merged := local
	var conflicts []doc.Conflict
	if remoteRow != nil && behind(row, remoteRow) {
		theirs, err := contentOf(remoteRow["content"])
		if err != nil {
			return false, errServerUnreadable
		}
		merged, conflicts = doc.Merge(baseOf(row), local, theirs)
	}
	values := map[string]any{"content": json.RawMessage(merged.Encode()), "plain_text": merged.PlainText()}
	var saved []map[string]any
	if remoteRow == nil {
		values["page_id"] = pageID
		saved, err = s.be.Insert(ctx, "page_content", values)
		if remote.IsConflict(err) {
			return false, errStale
		}
	} else {
		saved, err = s.be.Update(ctx, "page_content", []string{
			remote.Eq("page_id", pageID), remote.Eq("revision", fmt.Sprint(store.Int(remoteRow, "revision"))),
		}, values)
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
		return s.st.PushedContent(pageID, over, c.Encode(), c.PlainText(), merged.Encode(), savedAt, savedRev)
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
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: "content, revision, updated_at", Filters: []string{remote.Eq("page_id", pageID)}})
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

// refreshRemote brings a page's content up to date with the server's.
func (s *Service) refreshRemote(ctx context.Context, pageID string) (*doc.Content, error) {
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: "content, revision, updated_at", Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return nil, err
	}
	if remoteRow == nil {
		return s.st.Content(pageID), nil
	}
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
		return here, nil
	}
	for _, c := range conflicts {
		if err := s.st.AddConflict(pageID, c); err != nil {
			return nil, err
		}
	}
	return next, nil
}

func (s *Service) pullContents(ctx context.Context) error {
	return s.paged("page_content", func(first bool) (bool, error) { return s.pullContentsPage(ctx, first) })
}

// pullContentsPage is a page of pullContents; the first of a pass also
// fetches the pages that wait.
func (s *Service) pullContentsPage(ctx context.Context, first bool) (bool, error) {
	rows, full, err := s.changed(ctx, "page_content", "page_id", "page_id, updated_at", s.cursor("page_content"), 200)
	if err != nil {
		return false, err
	}
	allOffline := s.st.Settings().AllPagesOffline
	// refresh fetches a page's content if this device keeps it. A page
	// not here yet waits for its row, which a later pull brings.
	waiting := s.waiting()
	refresh := func(id string) error {
		p, err := s.st.Page(id)
		if err != nil {
			waiting[id] = true
			if store.IsNotFound(err) {
				return nil
			}
			return err
		}
		delete(waiting, id)
		if !s.keepsContent(p, allOffline) {
			return nil
		}
		_, err = s.refreshRemote(ctx, id)
		if unreadable(err) {
			// The page keeps its content here, and waits until both copies
			// can be read; the other pages' content still comes.
			log.Printf("pull %s: %v", id, err)
			waiting[id] = true
			return nil
		}
		if err != nil {
			waiting[id] = true
			return err
		}
		return nil
	}
	var failed error
	for id := range waiting {
		if !first {
			break
		}
		if failed = refresh(id); failed != nil {
			break
		}
	}
	cur := s.cursor("page_content")
	start := cur
	for _, r := range rows {
		if failed != nil {
			break
		}
		id := store.S(r, "page_id")
		// Content seen again, of the overlap, is fetched only when this
		// device has not merged that version or a later one.
		again := start.passed(r, "page_id")
		if again && s.hasContentAt(id, store.Time(r["updated_at"])) {
			continue
		}
		if failed = refresh(id); failed != nil {
			break
		}
		if !again {
			cur = cursor{store.S(r, "updated_at"), id}
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

// hasContentAt reports whether this device has merged the server's
// content of a page as of a time, or later.
func (s *Service) hasContentAt(pageID string, at time.Time) bool {
	row, err := s.st.ContentRow(pageID)
	return err == nil && !row.RemoteUpdatedAt.IsZero() && row.RemoteUpdatedAt.UnixMilli() >= at.UnixMilli()
}
