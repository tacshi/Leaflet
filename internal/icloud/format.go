package icloud

import (
	"encoding/json"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"time"
)

type Space struct {
	Format          int       `json:"format"`
	SpaceID         string    `json:"space_id"`
	CreatedAt       time.Time `json:"created_at"`
	CreatedByDevice string    `json:"created_by_device"`
}

type Device struct {
	Format       int       `json:"format"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Platform     string    `json:"platform"`
	LastActiveAt time.Time `json:"last_active_at"`
	LogFrom      int64     `json:"log_from"`
}

type Batch struct {
	Format    int       `json:"format"`
	Device    string    `json:"device"`
	Seq       int64     `json:"seq"`
	WrittenAt time.Time `json:"written_at"`
	Snapshot  bool      `json:"snapshot,omitempty"`
	Entries   []Entry   `json:"entries"`
}

type Entry struct {
	Kind    string         `json:"kind"`
	Table   string         `json:"table,omitempty"`
	ID      string         `json:"id,omitempty"`
	At      int64          `json:"at,omitempty"`
	By      string         `json:"by,omitempty"`
	Row     map[string]any `json:"row,omitempty"`
	Changed []string       `json:"changed,omitempty"`
	PageID  string         `json:"page_id,omitempty"`
	Version int64          `json:"version,omitempty"`
}

type PageFile struct {
	Format  int              `json:"format"`
	PageID  string           `json:"page_id"`
	Version int64            `json:"version"`
	Merged  map[string]int64 `json:"merged"`
	Content json.RawMessage  `json:"content"`
}

const SpacePath = "space.json"

func (f Folder) ReadSpace() (Space, []string, error) {
	names, err := f.names(".")
	if err != nil {
		return Space{}, nil, err
	}
	var found []Space
	var paths []string
	var notReady error
	for _, n := range names {
		if !strings.HasPrefix(n, "space") || !strings.HasSuffix(n, ".json") {
			continue
		}
		var s Space
		if err := f.ReadJSON(n, &s); err != nil {
			if errors.Is(err, ErrNewerFormat) {
				return Space{}, nil, err
			}
			notReady = err
			continue
		}
		if !ValidID(s.SpaceID) {
			continue
		}
		found = append(found, s)
		paths = append(paths, n)
	}
	if len(found) == 0 {
		if notReady != nil {
			return Space{}, nil, notReady
		}
		return Space{}, nil, fs.ErrNotExist
	}
	best := 0
	for i, s := range found {
		if s.SpaceID < found[best].SpaceID {
			best = i
		}
	}
	var losers []string
	for i, p := range paths {
		if found[i].SpaceID != found[best].SpaceID {
			losers = append(losers, p)
		}
	}
	slices.Sort(losers)
	return found[best], losers, nil
}

func (f Folder) SpaceOf(path string) (Space, error) {
	var s Space
	err := f.ReadJSON(path, &s)
	return s, err
}
