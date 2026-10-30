package icloud

import (
	"encoding/json"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"time"
)

// The files of the folder (docs/protocol.md §8).

// Space is space.json: which pages the folder holds.
type Space struct {
	Format          int       `json:"format"`
	SpaceID         string    `json:"space_id"`
	CreatedAt       time.Time `json:"created_at"`
	CreatedByDevice string    `json:"created_by_device"`
}

// Device is a device's device.json.
type Device struct {
	Format       int       `json:"format"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Platform     string    `json:"platform"`
	LastActiveAt time.Time `json:"last_active_at"`
	// LogFrom is the first batch of the log; those before it were folded
	// into it.
	LogFrom int64 `json:"log_from"`
}

// Batch is one batch of a device's log: the changes one sync sent.
type Batch struct {
	Format    int       `json:"format"`
	Device    string    `json:"device"`
	Seq       int64     `json:"seq"`
	WrittenAt time.Time `json:"written_at"`
	// Snapshot is a batch that holds the batches before it, folded.
	Snapshot bool    `json:"snapshot,omitempty"`
	Entries  []Entry `json:"entries"`
}

// Entry is a change in a batch: a row, or a page's content.
type Entry struct {
	Kind string `json:"kind"` // row | content
	// A row: its table and ID, its version, and the row.
	Table string         `json:"table,omitempty"`
	ID    string         `json:"id,omitempty"`
	At    int64          `json:"at,omitempty"`
	By    string         `json:"by,omitempty"`
	Row   map[string]any `json:"row,omitempty"`
	// Changed is the columns of the row the device changed, which are all
	// that apply where the row is; without it, as in batches from before
	// it, the whole row changed. Row stays whole, for a device that has
	// none of it.
	Changed []string `json:"changed,omitempty"`
	// A page's content: the version of the device's page file.
	PageID  string `json:"page_id,omitempty"`
	Version int64  `json:"version,omitempty"`
}

// PageFile is a device's version of a page's content.
type PageFile struct {
	Format  int    `json:"format"`
	PageID  string `json:"page_id"`
	Version int64  `json:"version"`
	// Merged is which version of each other device's page file this one
	// had merged when it wrote this.
	Merged  map[string]int64 `json:"merged"`
	Content json.RawMessage  `json:"content"`
}

// SpacePath is space.json.
const SpacePath = "space.json"

// ReadSpace reads space.json. Two devices that made it at once leave
// iCloud's copies of it beside it ("space 2.json"): the one of the
// lowest space wins, whichever file has it. It returns fs.ErrNotExist
// when there is none, and the losing copies.
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

// SpaceOf reads one copy of space.json.
func (f Folder) SpaceOf(path string) (Space, error) {
	var s Space
	err := f.ReadJSON(path, &s)
	return s, err
}
