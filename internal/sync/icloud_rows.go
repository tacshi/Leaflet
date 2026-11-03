package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"leaflet/internal/icloud"
	"leaflet/internal/store"
)

// Rows go through iCloud Drive column by column (docs/protocol.md §8), as
// they go up to the server: a row's entry names the columns its device
// changed, so an offline rename on one device does not undo a move or a
// deletion made on another. An entry that names none, as in batches from
// before, is a whole row. Each column here has the newest change to it.
// What this device has of a row's versions is in row_versions:
//
//	<table>           the last whole row it took or sent
//	<table>.<column>  the last change to that column alone, when newer
//
// A row with a change waiting here is not stored over: the entries that
// come for it meanwhile wait in key_values, under
// icloud.held.<table>.<id>, and apply once its change has gone.

const heldPrefix = "icloud.held."

// version is a row entry's version.
func version(e icloud.Entry) store.RowVersion { return store.RowVersion{At: e.At, Device: e.By} }

// colVersion is the version of a column of a row here: of the change to
// it alone, or of the whole row, whichever is newer.
func (s *Service) colVersion(table, id, col string, whole store.RowVersion) (store.RowVersion, error) {
	v, err := s.st.RowVersion(table+"."+col, id)
	if err != nil || !v.After(whole) {
		return whole, err
	}
	return v, nil
}

// setVersion records a change this device has of a row: of cols, or of
// the whole row for nil.
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

// newestSeen is the newest version this device has seen of any column of
// a row, with the entries held for it.
func (s *Service) newestSeen(table, id string, row map[string]any) (store.RowVersion, error) {
	newest, err := s.newestHad(table, id, row)
	if err != nil {
		return newest, err
	}
	for _, e := range s.held(table, id) {
		if v := version(e); v.After(newest) {
			newest = v
		}
	}
	return newest, nil
}

// newestHad is the newest version this device has of any column of a row,
// as newestSeen, but without the entries held for it.
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

// applyRow applies a row entry of another device's: each column it
// changed whose change here is older. A row this device has none of
// comes whole; its columns the entry did not change count as older than
// any change, so whichever comes applies.
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
		if _, ok := e.Row[c]; !ok || c == "id" || c == "updated_at" {
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
	// The change waiting here wins on the columns it changed, and goes
	// with a version after this one; the others apply once it has gone.
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
	// The devices sync one space: a row from before a device joined it
	// has the device's own.
	if _, ok := r["space_id"]; ok {
		r["space_id"] = s.st.Session().SpaceID
	}
	if err := s.putRow(e.Table, r, s.putCloudPage); err != nil {
		return err
	}
	// A change made here meanwhile kept the row from being stored.
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

// held are the entries of other devices' waiting for a row's change here
// to go.
func (s *Service) held(table, id string) []icloud.Entry {
	v, ok := s.st.Value(heldKey(table, id))
	if !ok {
		return nil
	}
	var out []icloud.Entry
	_ = json.Unmarshal([]byte(v), &out)
	return out
}

// hold keeps entries of one row until its change here has gone.
func (s *Service) hold(entries ...icloud.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	table, id := entries[0].Table, entries[0].ID
	held := s.held(table, id)
	for _, e := range entries {
		held = foldEntry(held, e)
	}
	raw, err := json.Marshal(held)
	if err != nil {
		return err
	}
	return s.st.SetValue(heldKey(table, id), string(raw))
}

// applyHeld applies the entries held for rows whose change has gone since.
// A row's that do not all apply stay held, and the other rows' still
// apply. They go once they have applied, not before, as a stop in between
// would lose them: one applied again changes nothing.
func (s *Service) applyHeld(changed map[string]bool) error {
	var failed error
	for key, raw := range s.st.ValuesWithPrefix(heldPrefix) {
		table, id, _ := strings.Cut(key, ".")
		if s.st.HasPending(table, id) {
			continue
		}
		var held []icloud.Entry
		_ = json.Unmarshal([]byte(raw), &held)
		var err error
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
			// Changed here meanwhile: applyRow held the rest again.
		default:
			failed = errors.Join(failed, s.st.DeleteValue(heldPrefix+key))
		}
	}
	return failed
}

// applyingHeld runs before applyHeld applies each entry; tests stop there.
var applyingHeld = func(icloud.Entry) {}

// foldEntry adds an entry of a row to others of the same row, leaving out
// any one another of them holds all of.
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

// covers reports whether row entry a holds all of b: it is no older, and
// changed every column b changed. An entry covers itself, so one held
// again is held once.
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
