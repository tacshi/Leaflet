package store

import (
	"encoding/json/v2"
	"fmt"
	"time"
)

// openViewKey keeps what the app's window shows (OpenView).
const openViewKey = "open.view"

// LastPageKey keeps the page the app opened last, which it opens again as
// it starts.
const LastPageKey = "last.page"

// OpenView is what the app's window shows, for those outside it (leaflet
// mcp and the command line), on their own connections: it is here only
// while the app runs with a page open. A crash leaves it, until the app
// starts again.
type OpenView struct {
	PageID    string        `json:"page_id"`
	Selection ViewSelection `json:"selection"`
	At        time.Time     `json:"updated_at"`
}

// ViewSelection is the selection of the open page: blocks by ID, offsets
// in characters, the start before the end. A caret has them the same and
// no text; its colors are those of what is typed next.
type ViewSelection struct {
	StartBlock  string `json:"start_block"`
	StartOffset int    `json:"start_offset"`
	EndBlock    string `json:"end_block"`
	EndOffset   int    `json:"end_offset"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated,omitzero"`
	// Color and Highlight are the first selected character's, as the app
	// keeps them, none for the page's own; MixedColors says that not all
	// of it has them.
	Color       string `json:"color,omitempty"`
	Highlight   string `json:"highlight,omitempty"`
	MixedColors bool   `json:"mixed_colors,omitzero"`
}

// SetOpenView stores what the window shows, nil for no page. Nothing in
// the app reads it, so it wakes nothing, as a setting's change would.
func (s *Store) SetOpenView(v *OpenView) error {
	if v == nil {
		_, err := s.db.Exec(`DELETE FROM key_values WHERE key = ?`, openViewKey)
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return setValue(s.db, openViewKey, string(raw))
}

// OpenView is what the app's window shows, nil when it shows no page or
// the app is closed.
func (s *Store) OpenView() (*OpenView, error) {
	raw, ok, err := s.Lookup(openViewKey)
	if err != nil || !ok {
		return nil, err
	}
	v := &OpenView{}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return nil, fmt.Errorf("open view: %w", err)
	}
	return v, nil
}
