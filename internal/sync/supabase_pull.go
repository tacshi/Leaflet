package sync

import (
	"context"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"leaflet/internal/remote"
	"leaflet/internal/store"
)

type cursor struct{ at, key string }

func cursorKey(table string) string { return "sync.cursor." + table }

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

func (c cursor) time() (time.Time, error) { return time.Parse(time.RFC3339Nano, c.at) }

func PulledAt(st *store.Store, table string) (at time.Time, ok bool) {
	c, ok := keptCursor(st, table)
	if !ok {
		return time.Time{}, false
	}
	at, err := c.time()
	return at, err == nil
}

var overlap = 10 * time.Second

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

// changed is the table's rows changed after the cursor, from overlap before it when back, as the first page of a pull
// reads them: a write the server took just before the cursor's, which came in after it, is not passed by. A later page
// goes on from where the one before stopped.
func (s *Service) changed(ctx context.Context, table, key, columns string, cur cursor, back bool, limit int, filters ...string) (rows []map[string]any, full bool, err error) {
	from := cur
	if at, err := time.Parse(time.RFC3339Nano, cur.at); back && err == nil && at.Unix() > 0 {
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
		if len(page) < limit || !cur.passed(page[len(page)-1], key) {
			return out, len(page) == limit, nil
		}
		last := page[len(page)-1]
		from = cursor{store.S(last, "updated_at"), store.S(last, key)}
	}
}

func quote(v string) string { return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"` }

// store applies the pulled rows, and reports whether it got through them all.
func (s *Service) store(table, key string, rows []map[string]any, have func(map[string]any) bool, apply func(map[string]any) error) bool {
	cur := s.cursor(table)
	start := cur
	stored, all := false, true
	for _, r := range rows {
		again := start.passed(r, key)
		if again && have(r) {
			continue
		}
		if !s.st.HasPending(table, store.S(r, key)) {
			if err := apply(r); err != nil {
				log.Printf("pull %s %s: %v", table, store.S(r, key), err)
				all = false
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
	return all
}

var pullPages, pullRows = 20, 500

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
		{"pages", "*", s.st.PutRemotePage},
		{"folders", "*", s.st.PutRemoteFolder},
		{"recordings", "*", s.st.PutRemoteRecording},
		{"files", "*", s.st.PutRemoteFile},
	}
	caughtUp := map[string]bool{}
	for _, step := range steps {
		var err error
		if caughtUp[step.table], err = s.pullAll(ctx, step.table, step.cols, step.apply); err != nil {
			return err
		}
	}
	if caughtUp["folders"] {
		if err := s.st.SettleFolderLoops(); err != nil {
			return err
		}
	}
	if err := s.pullTreesWaiting(ctx); err != nil {
		return err
	}
	if err := s.pullShared(ctx); err != nil {
		return err
	}
	if caughtUp["pages"] {
		if err := s.st.SettlePageLoops(); err != nil {
			return err
		}
	}
	if err := s.pullOwnMembers(ctx); err != nil {
		return err
	}
	s.pullMembers(ctx)
	if err := s.pullComments(ctx); err != nil {
		return err
	}
	if err := s.pullContents(ctx); err != nil {
		return err
	}
	if caughtUp["pages"] && caughtUp["files"] {
		s.pulled()
	}
	return nil
}

func (s *Service) pullTable(ctx context.Context, table, columns string, apply func(map[string]any) error) error {
	_, err := s.pullAll(ctx, table, columns, apply)
	return err
}

// pullAll pulls the table's changed rows, and reports whether it has them all now. Only then are the rows a whole:
// a change of several rows comes a row at a time, and loops of parents are looked for once it is all in.
func (s *Service) pullAll(ctx context.Context, table, columns string, apply func(map[string]any) error) (caughtUp bool, err error) {
	have := func(r map[string]any) bool {
		return s.st.HasRowAt(table, store.S(r, "id"), store.Time(r["updated_at"]))
	}
	err = s.paged(table, func(first bool) (bool, error) {
		rows, full, err := s.changed(ctx, table, "id", columns, s.cursor(table), first, pullRows)
		if err != nil {
			return false, err
		}
		all := s.store(table, "id", rows, have, apply)
		caughtUp = all && !full
		return full, nil
	})
	return caughtUp && err == nil, err
}

func (s *Service) pullShared(ctx context.Context) error {
	return s.paged("page_permissions", func(first bool) (bool, error) { return s.pullSharedPage(ctx, first) })
}

func (s *Service) pullSharedPage(ctx context.Context, first bool) (bool, error) {
	rows, full, err := s.changed(ctx, "page_permissions", "page_id", "page_id, updated_at", s.cursor("page_permissions"), first, 100,
		remote.Eq("user_id", s.be.UserID()), remote.NotNull("accepted_at"))
	if err != nil {
		return false, err
	}
	var failed error
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
				if err := s.st.PutRemotePage(r); err != nil {
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

// treesKey keeps the pages whose trees are to be pulled again whole, until they are: those a deletion for good the
// server refused brought back, as what came for them while it waited here was passed by.
const treesKey = "sync.trees_waiting"

// pullTreeAgain pulls the page's tree again whole, now, or with the next pull if it cannot.
func (s *Service) pullTreeAgain(ctx context.Context, pageID string) error {
	trees, err := s.ids(treesKey)
	if err != nil {
		return err
	}
	trees[pageID] = true
	if err := s.setIDs(treesKey, trees); err != nil {
		return err
	}
	return s.pullTreesWaiting(ctx)
}

func (s *Service) pullTreesWaiting(ctx context.Context) error {
	trees, err := s.ids(treesKey)
	if err != nil || len(trees) == 0 {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(trees)) {
		if err := s.pullTree(ctx, id); err != nil {
			return err
		}
		delete(trees, id)
		if err := s.setIDs(treesKey, trees); err != nil {
			return err
		}
	}
	return nil
}

var treeIDs, treeRows = 100, 500

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
	return s.paged("comments", func(first bool) (bool, error) {
		rows, full, err := s.changed(ctx, "comments", "id", "*", s.cursor("comments"), first, pullRows)
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
