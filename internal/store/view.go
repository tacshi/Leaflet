package store

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"time"
)

const openViewKey = "open.view"

const LastPageKey = "last.page"

type OpenView struct {
	PageID    string        `json:"page_id"`
	Selection ViewSelection `json:"selection"`
	At        time.Time     `json:"updated_at"`
	// PID is the process of the app that keeps the view, and Began about when
	// it began, which SetOpenView sets: a view outlives an app that crashed,
	// and its process tells it.
	PID   int       `json:"pid,omitzero"`
	Began time.Time `json:"began,omitzero"`
}

// processBegan is about when this process began: as its first packages start.
var processBegan = time.Now()

type ViewSelection struct {
	StartBlock  string `json:"start_block"`
	StartOffset int    `json:"start_offset"`
	EndBlock    string `json:"end_block"`
	EndOffset   int    `json:"end_offset"`
	Text        string `json:"text"`
	Truncated   bool   `json:"truncated,omitzero"`
	Color       string `json:"color,omitempty"`
	Highlight   string `json:"highlight,omitempty"`
	MixedColors bool   `json:"mixed_colors,omitzero"`
}

func (s *Store) SetOpenView(v *OpenView) error {
	if v == nil {
		_, err := s.db.Exec(`DELETE FROM key_values WHERE key = ?`, openViewKey)
		return err
	}
	kept := *v
	kept.PID, kept.Began = os.Getpid(), processBegan
	raw, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return setValue(s.db, openViewKey, string(raw))
}

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
