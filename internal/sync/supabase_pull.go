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
		if len(page) < limit || !cur.passed(page[len(page)-1], key) {
			return out, len(page) == limit, nil
		}
		last := page[len(page)-1]
		from = cursor{store.S(last, "updated_at"), store.S(last, key)}
	}
}

func quote(v string) string { return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"` }

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
		{"pages", "*", s.putServerPage},
		{"folders", "*", s.st.PutRemoteFolder},
		{"recordings", "*", s.st.PutRemoteRecording},
		{"files", "*", s.st.PutRemoteFile},
	}
	for _, step := range steps {
		if err := s.pullTable(ctx, step.table, step.cols, step.apply); err != nil {
			return err
		}
	}
	if err := s.pullShared(ctx); err != nil {
		return err
	}
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
		return s.st.HasRowAt(table, store.S(r, "id"), store.Time(r["updated_at"]))
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

func (s *Service) putServerPage(r map[string]any) error { return s.settleLoop(r, s.st.PutRemotePage) }

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

func (s *Service) pullShared(ctx context.Context) error {
	return s.paged("page_permissions", func(bool) (bool, error) { return s.pullSharedPage(ctx) })
}

func (s *Service) pullSharedPage(ctx context.Context) (bool, error) {
	rows, full, err := s.changed(ctx, "page_permissions", "page_id", "page_id, updated_at", s.cursor("page_permissions"), 100,
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
