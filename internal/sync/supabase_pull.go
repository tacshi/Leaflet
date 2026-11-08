package sync

import (
	"context"
	"log"
	"slices"
	"strings"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// cursor is where a table's pull resumes: the last row stored here, by
// its time and key.
type cursor struct{ at, key string }

// cursorKey keeps a table's cursor, as its time, "|", and its key.
func cursorKey(table string) string { return "sync.cursor." + table }

// keptCursor is a table's cursor as kept, if there is one.
func keptCursor(st *store.Store, table string) (cursor, bool) {
	v, ok := st.Value(cursorKey(table))
	at, key, _ := strings.Cut(v, "|")
	return cursor{at, key}, ok
}

func (s *Service) cursor(table string) cursor {
	if c, ok := keptCursor(s.st, table); ok {
		return c
	}
	return cursor{at: "1970-01-01T00:00:00Z"}
}

func (s *Service) setCursor(table string, c cursor) {
	_ = s.st.SetValue(cursorKey(table), c.at+"|"+c.key)
}

// time is the time of the row the cursor is at.
func (c cursor) time() (time.Time, error) { return time.Parse(time.RFC3339Nano, c.at) }

// PulledAt reports the time of the newest row of table that sync has
// pulled: its cursor's. ok is false when there is no cursor yet, or
// one that cannot be read.
func PulledAt(st *store.Store, table string) (at time.Time, ok bool) {
	c, ok := keptCursor(st, table)
	if !ok {
		return time.Time{}, false
	}
	at, err := c.time()
	return at, err == nil
}

// overlap is how far behind its cursor a pull looks again. The server
// stamps updated_at as a row changes, before the change commits, so a
// change that commits late comes after rows stamped later, which the
// cursor may have passed. Rows seen again are passed over where this
// device has them as they are, and stored again otherwise, which stores
// the row as the server has it, as the first time did.
var overlap = 10 * time.Second

// passed reports whether the cursor is at or past a row, which a pull
// then reads again only for the overlap. A row whose time does not parse
// is not passed.
func (c cursor) passed(r map[string]any, key string) bool {
	at, err := time.Parse(time.RFC3339Nano, store.S(r, "updated_at"))
	if err != nil {
		return false
	}
	cur, err := c.time()
	if err != nil {
		return false
	}
	return at.Before(cur) || at.Equal(cur) && store.S(r, key) <= c.key
}

// changed reads up to limit rows changed after the cursor, oldest first,
// after the rows of the overlap behind it. Rows of the same time come in
// key order, so a pull that stops among them resumes at the next one.
// full reports whether the last page read was full: more may follow.
func (s *Service) changed(ctx context.Context, table, key, columns string, cur cursor, limit int, filters ...string) (rows []map[string]any, full bool, err error) {
	from := cur
	if at, err := time.Parse(time.RFC3339Nano, cur.at); err == nil && at.Unix() > 0 {
		from = cursor{at: at.Add(-overlap).UTC().Format(time.RFC3339Nano)}
	}
	var out []map[string]any
	for {
		after := remote.Gt("updated_at", from.at)
		if from.key != "" {
			after = "or=(updated_at.gt." + quote(from.at) + ",and(updated_at.eq." + quote(from.at) + "," + key + ".gt." + quote(from.key) + "))"
		}
		page, err := s.be.Select(ctx, table, remote.Query{Select: columns, Filters: append([]string{after}, filters...), Order: "updated_at," + key, Limit: limit})
		if err != nil {
			return nil, false, err
		}
		out = append(out, page...)
		// An overlap fuller than a page reads on, so it does not hold the
		// pull there.
		if len(page) < limit || !cur.passed(page[len(page)-1], key) {
			return out, len(page) == limit, nil
		}
		last := page[len(page)-1]
		from = cursor{store.S(last, "updated_at"), store.S(last, key)}
	}
}

// quote makes a value safe inside a PostgREST or=(…) filter.
func quote(v string) string { return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"` }

// store applies pulled rows in order and moves the cursor past each one
// stored. It stops at the first that cannot be stored, so the next pull
// starts with it again. A row with a change waiting here is passed over:
// the change wins and goes up on the next push. A row the cursor has
// passed, of the overlap, is stored again only when this device does not
// have it as it is (have), as when its change committed late.
func (s *Service) store(table, key string, rows []map[string]any, have func(map[string]any) bool, apply func(map[string]any) error) {
	cur := s.cursor(table)
	start := cur
	stored := false
	for _, r := range rows {
		again := start.passed(r, key)
		if again && have(r) {
			continue
		}
		if !s.st.HasPending(table, store.S(r, key)) {
			if err := apply(r); err != nil {
				log.Printf("pull %s %s: %v", table, store.S(r, key), err)
				break
			}
			stored = true
		}
		if !again {
			cur = cursor{store.S(r, "updated_at"), store.S(r, key)}
		}
	}
	if cur != start {
		s.setCursor(table, cur)
	}
	if stored || cur != start {
		s.st.Notify(table, "")
	}
}

// pullPages is how many pages of rows a pull reads of a table in one
// pass, and pullRows how many rows a page holds: a first sync of many
// pages reads on at once, rather than a page each pass. Tests make them
// smaller.
var pullPages, pullRows = 20, 500

// paged runs a pull of a table's rows a page at a time, while pages come
// back full and the cursor moves on, up to pullPages. With more still to
// come then, the next pass runs soon.
func (s *Service) paged(table string, page func(first bool) (full bool, err error)) error {
	for i := range pullPages {
		start := s.cursor(table)
		full, err := page(i == 0)
		if err != nil || !full || s.cursor(table) == start {
			return err
		}
	}
	s.Kick()
	return nil
}

func (s *Service) pull(ctx context.Context) error {
	steps := []struct {
		table, cols string
		apply       func(map[string]any) error
	}{
		{"pages", "*", s.putServerPage},
		{"folders", "*", s.st.PutRemoteFolder},
		{"recordings", "*", s.st.PutRemoteRecording},
		{"files", "*", s.st.PutRemoteFile},
		{"model_providers", "id,preset,name,base_url,auth_kind,has_secret,default_model,is_local,supports_tools,models,expires_at,created_at,updated_at",
			func(r map[string]any) error {
				if b, _ := r["is_local"].(bool); b {
					return nil
				}
				return s.st.PutRemoteProvider(r)
			}},
	}
	for _, step := range steps {
		if err := s.pullTable(ctx, step.table, step.cols, step.apply); err != nil {
			return err
		}
	}
	if err := s.pullProviderRemovals(ctx); err != nil {
		return err
	}
	if err := s.pullShared(ctx); err != nil {
		return err
	}
	// Before the comments and content that others write come.
	if err := s.pullOwnMembers(ctx); err != nil {
		return err
	}
	s.pullMembers(ctx)
	if err := s.pullComments(ctx); err != nil {
		return err
	}
	return s.pullContents(ctx)
}

func (s *Service) pullTable(ctx context.Context, table, columns string, apply func(map[string]any) error) error {
	have := func(r map[string]any) bool {
		// A local model's connection stays on its device, never stored.
		local, _ := r["is_local"].(bool)
		return local || s.st.HasRowAt(table, store.S(r, "id"), store.Time(r["updated_at"]))
	}
	return s.paged(table, func(bool) (bool, error) {
		rows, full, err := s.changed(ctx, table, "id", columns, s.cursor(table), pullRows)
		if err != nil {
			return false, err
		}
		s.store(table, "id", rows, have, apply)
		return full, nil
	})
}

// putServerPage stores a page from the server. A move of this device's
// still waiting to go up can make it close a loop of parents, as when
// another device moved a page under one this device moved under it: the
// server turns the second move away, so it has no loop, but this device,
// which stores the first while its own waits, would leave both pages out
// of the tree. The page of the loop whose move waits goes to the top level
// here meanwhile, unqueued; its move gives way to the server's row once
// refused (pushRow).
func (s *Service) putServerPage(r map[string]any) error { return s.settleLoop(r, s.st.PutRemotePage) }

// settleLoop stores a page from the server with put, as putServerPage
// does, settling the loop of parents it makes.
func (s *Service) settleLoop(r map[string]any, put func(map[string]any) error) error {
	loop := s.st.ParentLoop(store.S(r, "id"), store.S(r, "parent_id"))
	if err := put(r); err != nil || len(loop) == 0 {
		return err
	}
	for _, id := range loop {
		if s.st.HasPending("pages", id) {
			return s.st.DetachPage(id)
		}
	}
	return s.st.DetachPage(slices.Min(loop))
}

// providerRemovalsEvery is how often pullProviderRemovals asks the server
// for every connection it has.
const providerRemovalsEvery = 10 * time.Minute

// pullProviderRemovals removes the cloud model connections the server no
// longer has, every providerRemovalsEvery: one removed on another device
// is deleted there, and leaves no row for the pull by time to bring. Only
// those stored here before the server listed its own go, so one added
// meanwhile stays.
func (s *Service) pullProviderRemovals(ctx context.Context) error {
	uid := s.be.UserID()
	s.mu.Lock()
	due := s.providersSwept.due(uid, providerRemovalsEvery)
	s.mu.Unlock()
	if !due {
		return nil
	}
	here, err := s.st.Providers()
	if err != nil {
		return err
	}
	rows, err := s.be.Select(ctx, "model_providers", remote.Query{Select: "id"})
	if err != nil {
		return err
	}
	there := map[string]bool{}
	for _, r := range rows {
		there[store.S(r, "id")] = true
	}
	for _, p := range here {
		if !p.IsLocal && !there[p.ID] {
			if err := s.st.DeleteProvider(p.ID); err != nil {
				return err
			}
		}
	}
	s.mu.Lock()
	s.providersSwept = every{time.Now(), uid}
	s.mu.Unlock()
	return nil
}

// pullShared brings the pages newly shared with the user, once they have
// accepted them, with the pages under them and what they hold. Sharing
// changes no page, so the pulls that go by time pass over a page that last
// changed before them. Accepting moves the share's updated_at on, so a
// share accepted after the cursor passed it comes then.
func (s *Service) pullShared(ctx context.Context) error {
	return s.paged("page_permissions", func(bool) (bool, error) { return s.pullSharedPage(ctx) })
}

// pullSharedPage is a page of pullShared.
func (s *Service) pullSharedPage(ctx context.Context) (bool, error) {
	rows, full, err := s.changed(ctx, "page_permissions", "page_id", "page_id, updated_at", s.cursor("page_permissions"), 100,
		remote.Eq("user_id", s.be.UserID()), remote.NotNull("accepted_at"))
	if err != nil {
		return false, err
	}
	var failed error
	// A share seen again is of a page this device has, with what is under
	// it, unless its pull failed.
	have := func(r map[string]any) bool {
		_, err := s.st.Page(store.S(r, "page_id"))
		return err == nil
	}
	s.store("page_permissions", "page_id", rows, have, func(r map[string]any) error {
		id := store.S(r, "page_id")
		if failed = s.pullTree(ctx, id); failed == nil {
			failed = s.LoadMembers(ctx, id)
		}
		return failed
	})
	return full, failed
}

// pullTree stores a page and the pages under it, however deep they go,
// their comments, files and recordings, and has their content fetched. A
// row with a change waiting here keeps it, as in the other pulls. A loop
// of parents ends with the pages seen.
func (s *Service) pullTree(ctx context.Context, rootID string) error {
	var ids []string
	seen := map[string]bool{}
	rows, err := s.be.Select(ctx, "pages", remote.Query{Select: "*", Filters: []string{remote.Eq("id", rootID)}})
	for {
		if err != nil {
			return err
		}
		var level []string
		for _, r := range rows {
			id := store.S(r, "id")
			if seen[id] {
				continue
			}
			seen[id] = true
			if !s.st.HasPending("pages", id) {
				if err := s.putServerPage(r); err != nil {
					return err
				}
			}
			level = append(level, id)
		}
		if len(level) == 0 {
			break
		}
		ids = append(ids, level...)
		rows, err = s.selectIn(ctx, "pages", "parent_id", level)
	}
	if len(ids) == 0 {
		return nil
	}
	s.st.Notify("pages", "")
	comments, err := s.selectIn(ctx, "comments", "page_id", ids)
	if err != nil {
		return err
	}
	names, err := s.authorNames(ctx, comments)
	if err != nil {
		return err
	}
	for _, r := range comments {
		if s.st.HasPending("comments", store.S(r, "id")) {
			continue
		}
		if err := s.st.PutRemoteComment(r, names[store.S(r, "author_id")]); err != nil {
			return err
		}
	}
	for _, step := range []struct {
		table string
		apply func(map[string]any) error
	}{{"files", s.st.PutRemoteFile}, {"recordings", s.st.PutRemoteRecording}} {
		rows, err := s.selectIn(ctx, step.table, "page_id", ids)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if s.st.HasPending(step.table, store.S(r, "id")) {
				continue
			}
			if err := step.apply(r); err != nil {
				return err
			}
		}
		s.st.Notify(step.table, "")
	}
	s.st.Notify("comments", "")
	waiting, err := s.waiting()
	if err != nil {
		return err
	}
	for _, id := range ids {
		waiting[id] = true
	}
	return s.setWaiting(waiting)
}

// treeIDs is how many pages pullTree names in one request, and treeRows
// how many rows it reads at a time: the server sends at most 1,000, and
// leaves out the rest without saying so. Tests make them smaller.
var treeIDs, treeRows = 100, 500

// selectIn reads every row of a table whose col is one of ids: a few ids
// at a time, and their rows a page at a time, in the order of the rows'
// IDs.
func (s *Service) selectIn(ctx context.Context, table, col string, ids []string) ([]map[string]any, error) {
	var out []map[string]any
	for chunk := range slices.Chunk(ids, treeIDs) {
		after := ""
		for {
			filters := []string{remote.In(col, chunk)}
			if after != "" {
				filters = append(filters, remote.Gt("id", after))
			}
			rows, err := s.be.Select(ctx, table, remote.Query{Select: "*", Filters: filters, Order: "id", Limit: treeRows})
			if err != nil {
				return nil, err
			}
			out = append(out, rows...)
			if len(rows) < treeRows {
				break
			}
			after = store.S(rows[len(rows)-1], "id")
		}
	}
	return out, nil
}

func (s *Service) pullComments(ctx context.Context) error {
	have := func(r map[string]any) bool {
		return s.st.HasRowAt("comments", store.S(r, "id"), store.Time(r["updated_at"]))
	}
	return s.paged("comments", func(bool) (bool, error) {
		rows, full, err := s.changed(ctx, "comments", "id", "*", s.cursor("comments"), pullRows)
		if err != nil || len(rows) == 0 {
			return false, err
		}
		names, err := s.authorNames(ctx, rows)
		if err != nil {
			return false, err
		}
		s.store("comments", "id", rows, have, func(r map[string]any) error {
			return s.st.PutRemoteComment(r, names[store.S(r, "author_id")])
		})
		return full, nil
	})
}

// authorNames are the names of comments' authors, by their IDs, with
// "Someone" for one without a name. Names that could not be read are an
// error, as a comment stored without its author's would keep that: it
// comes again only as it changes.
func (s *Service) authorNames(ctx context.Context, comments []map[string]any) (map[string]string, error) {
	ids := map[string]bool{}
	for _, r := range comments {
		ids[store.S(r, "author_id")] = true
	}
	names := map[string]string{}
	var list []string
	for id := range ids {
		names[id] = "Someone"
		if id != "" {
			list = append(list, id)
		}
	}
	if len(list) > 0 {
		profiles, err := s.be.Select(ctx, "profiles", remote.Query{Select: "id, display_name", Filters: []string{remote.In("id", list)}})
		if err != nil {
			return nil, err
		}
		for _, p := range profiles {
			if n := store.S(p, "display_name"); n != "" {
				names[store.S(p, "id")] = n
			}
		}
	}
	return names, nil
}
