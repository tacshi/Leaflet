package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"leaflet/internal/icloud"
	"leaflet/internal/store"
)

const heldPrefix = "icloud.held."

func version(e icloud.Entry) store.RowVersion { return store.RowVersion{At: e.At, Device: e.By} }

func (s *Service) colVersion(table, id, col string, whole store.RowVersion) (store.RowVersion, error) {
	v, err := s.st.RowVersion(table+"."+col, id)
	if err != nil || !v.After(whole) {
		return whole, err
	}
	return v, nil
}

func (s *Service) setVersion(table, id string, cols []string, v store.RowVersion) error {
	if cols == nil {
		return s.st.SetRowVersion(table, id, v)
	}
	for _, c := range cols {
		if err := s.st.SetRowVersion(table+"."+c, id, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) newestSeen(table, id string, row map[string]any) (store.RowVersion, error) {
	newest, err := s.newestHad(table, id, row)
	if err != nil {
		return newest, err
	}
	held, err := s.held(table, id)
	if err != nil {
		return newest, err
	}
	for _, e := range held {
		if v := version(e); v.After(newest) {
			newest = v
		}
	}
	return newest, nil
}

func (s *Service) newestHad(table, id string, row map[string]any) (store.RowVersion, error) {
	newest, err := s.st.RowVersion(table, id)
	for c := range row {
		if err != nil {
			break
		}
		newest, err = s.colVersion(table, id, c, newest)
	}
	return newest, err
}

func (s *Service) applyRow(e icloud.Entry, changed map[string]bool) error {
	v := version(e)
	whole, err := s.st.RowVersion(e.Table, e.ID)
	if err != nil {
		return err
	}
	cols := e.Changed
	if cols == nil {
		if !v.After(whole) {
			return nil
		}
		cols = slices.Sorted(maps.Keys(e.Row))
	}
	var newer []string
	for _, c := range cols {
		if _, ok := e.Row[c]; !ok || c == "id" || c == "updated_at" || c == "space_id" {
			continue
		}
		had, err := s.colVersion(e.Table, e.ID, c, whole)
		if err != nil {
			return err
		}
		if v.After(had) {
			newer = append(newer, c)
		}
	}
	if len(newer) == 0 {
		return nil
	}
	if other, err := s.otherSpace(e.Table, e.ID, e.Row, false); err != nil || other {
		if other {
			log.Printf("icloud: %s %s: of another space, not stored", e.Table, e.ID)
		}
		return err
	}
	if s.st.HasPending(e.Table, e.ID) {
		return s.hold(e)
	}
	local, here, err := s.localRow(e.Table, e.ID)
	if err != nil {
		return err
	}
	r := maps.Clone(e.Row)
	if here {
		r = local
		for _, c := range newer {
			r[c] = e.Row[c]
		}
		if store.TimeOrZero(e.Row["updated_at"]).After(store.TimeOrZero(local["updated_at"])) {
			r["updated_at"] = e.Row["updated_at"]
		}
	}
	r["id"] = e.ID
	r[store.PeerKey] = true
	if err := s.putRow(e.Table, r, s.st.PutRemotePage); err != nil {
		return err
	}
	if s.st.HasPending(e.Table, e.ID) {
		return s.hold(e)
	}
	changed[e.Table] = true
	if e.Changed == nil {
		return s.st.SetRowVersion(e.Table, e.ID, v)
	}
	return s.setVersion(e.Table, e.ID, newer, v)
}

func heldKey(table, id string) string { return heldPrefix + table + "." + id }

func (s *Service) held(table, id string) ([]icloud.Entry, error) {
	v, ok, err := s.st.Lookup(heldKey(table, id))
	if err != nil || !ok {
		return nil, err
	}
	return parseHeld(v)
}

func parseHeld(raw string) ([]icloud.Entry, error) {
	var out []icloud.Entry
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("entries held: %w", err)
	}
	return out, nil
}

func (s *Service) hold(entries ...icloud.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	table, id := entries[0].Table, entries[0].ID
	held, err := s.held(table, id)
	if err != nil {
		return err
	}
	for _, e := range entries {
		held = foldEntry(held, e)
	}
	raw, err := json.Marshal(held)
	if err != nil {
		return err
	}
	return s.st.SetValue(heldKey(table, id), string(raw))
}

func (s *Service) applyHeld(changed map[string]bool) error {
	var failed error
	for key, raw := range s.st.ValuesWithPrefix(heldPrefix) {
		table, id, _ := strings.Cut(key, ".")
		if s.st.HasPending(table, id) {
			continue
		}
		held, err := parseHeld(raw)
		for _, e := range held {
			if e.Kind != "row" || e.Table != table || e.ID != id || !synced(table) || e.Row == nil {
				continue
			}
			applyingHeld(e)
			if err = s.applyRow(e, changed); err != nil {
				break
			}
		}
		switch {
		case err != nil:
			failed = errors.Join(failed, fmt.Errorf("%s %s: %w", table, id, err))
		case s.st.HasPending(table, id):
		default:
			failed = errors.Join(failed, s.st.DeleteValue(heldPrefix+key))
		}
	}
	return failed
}

var applyingHeld = func(icloud.Entry) {}

func foldEntry(entries []icloud.Entry, e icloud.Entry) []icloud.Entry {
	var out []icloud.Entry
	for _, o := range entries {
		if covers(o, e) {
			return entries
		}
		if !covers(e, o) {
			out = append(out, o)
		}
	}
	return append(out, e)
}

func covers(a, b icloud.Entry) bool {
	if version(b).After(version(a)) {
		return false
	}
	if a.Changed == nil {
		return true
	}
	if b.Changed == nil {
		return false
	}
	for _, c := range b.Changed {
		if !slices.Contains(a.Changed, c) {
			return false
		}
	}
	return true
}
