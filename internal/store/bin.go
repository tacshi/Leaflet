package store

import (
	"database/sql"
	"errors"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"
)

// BinDays is how long the bin keeps a deleted page or file before it goes for good.
const BinDays = 30

// BinKept is how long the bin keeps what is in it.
const BinKept = BinDays * 24 * time.Hour

var ErrPurged = errors.New("deleted for good")

// BinKind is what an item of the bin is: a page or a file.
type BinKind string

const (
	BinPage BinKind = "page"
	BinFile BinKind = "file"
)

// BinRef is an item of the bin, by what it is and its id.
type BinRef struct {
	Kind BinKind
	ID   string
}

// BinItem is a page or a file in the bin. A page comes with the pages under it that were deleted with it, which
// Pages counts with it.
type BinItem struct {
	BinRef
	Title     string
	Icon      string
	DeletedAt time.Time
	// GoesAt is when the bin deletes it for good.
	GoesAt   time.Time
	Template bool
	Pages    int
	File     File
}

// binRootWhere is a deleted page of the space that was not deleted with the page above it: one deleted with its
// parent was deleted no earlier than it, and goes in and out of the bin with it.
const binRootWhere = `deleted_at IS NOT NULL AND purged_at IS NULL AND space_id = ? AND NOT EXISTS (SELECT 1 FROM pages up
	WHERE up.id = pages.parent_id AND up.deleted_at IS NOT NULL AND up.purged_at IS NULL AND up.deleted_at <= pages.deleted_at)`

// Bin is what the space's owner deleted and can still restore, the latest deletion first. Pages deleted from other
// spaces are not in it: they are their owner's to restore.
func (s *Store) Bin() ([]BinItem, error) {
	space := s.Session().SpaceID
	// A page deleted before this device had the bin, which kept it whole, has its days in the bin from then.
	since := s.binSince()
	pageGoes := func(deleted time.Time) time.Time {
		if deleted.Before(since) {
			deleted = since
		}
		return deleted.Add(BinKept)
	}
	roots, err := s.queryPages(binRootWhere+` ORDER BY deleted_at DESC, id`, space)
	if err != nil {
		return nil, err
	}
	var items []BinItem
	for _, p := range roots {
		ids, err := binTree(s.db, p)
		if err != nil {
			return nil, err
		}
		items = append(items, BinItem{BinRef: BinRef{Kind: BinPage, ID: p.ID}, Title: p.DisplayTitle(), Icon: p.Icon,
			DeletedAt: p.DeletedAt, GoesAt: pageGoes(p.DeletedAt), Template: p.Template, Pages: len(ids)})
	}
	// A file deleted without the bin keeping it, before the bin or by a Leaflet from before it, went with its contents:
	// there is nothing of it to restore.
	files, err := s.queryFiles(`deleted_at IS NOT NULL AND kept AND purged_at IS NULL AND page_id IS NULL AND space_id = ?`, space)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		items = append(items, BinItem{BinRef: BinRef{Kind: BinFile, ID: f.ID}, Title: f.Name, DeletedAt: f.DeletedAt,
			GoesAt: f.DeletedAt.Add(BinKept), File: f})
	}
	slices.SortStableFunc(items, func(a, b BinItem) int { return b.DeletedAt.Compare(a.DeletedAt) })
	return items, nil
}

// binTree is the page and those under it deleted with it, the page first.
func binTree(q querier, root Page) ([]string, error) {
	ids := []string{root.ID}
	at := map[string]time.Time{root.ID: root.DeletedAt}
	for i := 0; i < len(ids); i++ {
		kids, err := pagesIn(q, `parent_id = ? AND deleted_at IS NOT NULL AND purged_at IS NULL`, ids[i])
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			if _, seen := at[k.ID]; !seen && !k.DeletedAt.Before(at[ids[i]]) {
				at[k.ID] = k.DeletedAt
				ids = append(ids, k.ID)
			}
		}
	}
	return ids, nil
}

// InBin is the pages that go in and out of the bin with the page: it, the pages under it deleted with it, and, when
// it was deleted with the page above it, that page's. It is empty for a page that is not in the bin.
func (s *Store) InBin(id string) ([]string, error) {
	p, err := s.Page(id)
	if err != nil {
		return nil, err
	}
	if p.DeletedAt.IsZero() || !p.PurgedAt.IsZero() {
		return nil, nil
	}
	for seen := map[string]bool{p.ID: true}; p.ParentID != ""; {
		up, err := s.Page(p.ParentID)
		if err != nil || seen[up.ID] || up.DeletedAt.IsZero() || !up.PurgedAt.IsZero() || up.DeletedAt.After(p.DeletedAt) {
			break
		}
		seen[up.ID] = true
		p = up
	}
	return binTree(s.db, p)
}

// RestoreFile takes a file out of the bin, to the folder it was in, or the top level once that folder is gone.
func (s *Store) RestoreFile(id string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		files, err := filesIn(tx, s.Dir, `id = ?`, id)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return errNotFound
		}
		f := files[0]
		// A file deleted without the bin keeping its contents went for good with them.
		if f.Gone() {
			return ErrPurged
		}
		if f.DeletedAt.IsZero() {
			return nil
		}
		old := FileToRemote(f)
		if f.FolderID != "" {
			if folder, err := folderIn(tx, f.FolderID); err == nil && !folder.DeletedAt.IsZero() {
				f.FolderID = ""
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		f.DeletedAt, f.Kept, f.UpdatedAt = time.Time{}, false, time.Now()
		if err := putFile(tx, s.Dir, f); err != nil {
			return err
		}
		return queueChange(tx, "files", f.ID, old, FileToRemote(f), "restored file", f.PageID)
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("files", "")
	return nil
}

// TrashFile puts a file in the bin: it keeps its contents, here and where it is stored, until it goes for good.
func (s *Store) TrashFile(id string) error {
	err := s.inTx(func(tx *sql.Tx) error {
		files, err := filesIn(tx, s.Dir, `id = ?`, id)
		if err != nil || len(files) == 0 || !files[0].DeletedAt.IsZero() {
			return err
		}
		f := files[0]
		old := FileToRemote(f)
		now := time.Now()
		f.DeletedAt, f.Kept, f.UpdatedAt = now, true, now
		if err := putFile(tx, s.Dir, f); err != nil {
			return err
		}
		return queueChange(tx, "files", f.ID, old, FileToRemote(f), "deleted file", f.PageID)
	})
	if err != nil {
		return err
	}
	s.Notify("outbox", "")
	s.Notify("files", "")
	return nil
}

// Purge deletes pages and files of the bin for good: a page with those under it deleted with it.
func (s *Store) Purge(refs ...BinRef) error {
	if len(refs) == 0 {
		return nil
	}
	var copies []string
	err := s.inTx(func(tx *sql.Tx) error {
		now := time.Now()
		for _, ref := range refs {
			var gone []string
			var err error
			switch ref.Kind {
			case BinPage:
				gone, err = s.purgePage(tx, ref.ID, now)
			case BinFile:
				gone, err = purgeFile(tx, s.Dir, ref.ID, now)
			}
			if err != nil {
				return err
			}
			copies = append(copies, gone...)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range copies {
		s.removeLocalCopy(path)
	}
	s.Notify("*", "")
	return nil
}

// EmptyBin deletes everything in the bin for good.
func (s *Store) EmptyBin() error {
	items, err := s.Bin()
	if err != nil {
		return err
	}
	return s.Purge(refs(items)...)
}

// PurgeOld deletes for good what has been in the bin for BinDays by the time, and reports whether there was any.
func (s *Store) PurgeOld(now time.Time) (bool, error) {
	items, err := s.Bin()
	if err != nil {
		return false, err
	}
	old := slices.DeleteFunc(items, func(it BinItem) bool { return it.GoesAt.After(now) })
	return len(old) > 0, s.Purge(refs(old)...)
}

func refs(items []BinItem) []BinRef {
	out := make([]BinRef, len(items))
	for i, it := range items {
		out[i] = it.BinRef
	}
	return out
}

const binSinceKey = "bin.since"

// startBin notes, once, when this device first had the bin.
func (s *Store) startBin() {
	if _, ok := s.Value(binSinceKey); ok {
		return
	}
	if err := s.SetValue(binSinceKey, strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
		log.Printf("bin: %v", err)
	}
}

// binSince is when this device first had the bin. A page deleted before it, which no bin showed, has its days in the
// bin from then.
func (s *Store) binSince() time.Time {
	if v, ok := s.Value(binSinceKey); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.UnixMilli(n)
		}
	}
	return time.Now()
}

// purgePage deletes for good a page of the space in the bin, with those under it deleted with it. A page of another
// space is its owner's to delete for good, and is left as it is.
func (s *Store) purgePage(tx *sql.Tx, id string, now time.Time) ([]string, error) {
	root, err := pageIn(tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if root.DeletedAt.IsZero() || !root.PurgedAt.IsZero() || root.SpaceID != s.Session().SpaceID {
		return nil, nil
	}
	ids, err := binTree(tx, root)
	if err != nil {
		return nil, err
	}
	removals, err := s.storedOn(tx, ids, false)
	if err != nil {
		return nil, err
	}
	copies, err := s.wipePages(tx, ids, true)
	if err != nil {
		return nil, err
	}
	for _, pid := range ids {
		p, err := pageIn(tx, pid)
		if err != nil {
			return nil, err
		}
		p.PurgedAt, p.UpdatedAt, p.ChangedAt = now, now, now
		if err := queuePurged(tx, tombstone(p)); err != nil {
			return nil, err
		}
	}
	return copies, queueRemovals(tx, removals)
}

// storedOn is what is stored of the pages' files and recordings, to remove where it is stored: all of it, or, mine,
// only what this account put there, which no one else may remove.
func (s *Store) storedOn(tx *sql.Tx, ids []string, mine bool) ([]map[string]any, error) {
	in, args := marks(len(ids)), anys(ids)
	sess := s.Session()
	files, err := filesIn(tx, s.Dir, `page_id IN (`+in+`) AND purged_at IS NULL AND storage_path IS NOT NULL`, args...)
	if err != nil {
		return nil, err
	}
	var removals []map[string]any
	for _, f := range files {
		if !mine || f.SpaceID == sess.SpaceID {
			removals = append(removals, fileRemoval(f))
		}
	}
	recs, err := recordingsIn(tx, s.Dir, `page_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if !mine || sess.UserID != "" && strings.HasPrefix(r.AudioPath, sess.UserID+"/") {
			removals = append(removals, map[string]any{"bucket": "recordings", "path": r.AudioPath, "recording_id": r.ID, "page_id": r.PageID})
		}
	}
	return removals, nil
}

func queueRemovals(tx *sql.Tx, removals []map[string]any) error {
	for _, r := range removals {
		key := "files/" + S(r, "file_id")
		if S(r, "bucket") == "recordings" {
			key = "recordings/" + S(r, "recording_id")
		}
		if err := queue(tx, "storage_delete", key, r, "deleted for good", ""); err != nil {
			return err
		}
	}
	return nil
}

// purgedCols is what a page deleted for good changes of its row: its deletion goes with it, as what was queued of the
// page, its deletion too, went with the rest of it.
var purgedCols = []string{"title", "icon", "tags", "default_template_id", "deleted_at", "purged_at"}

func queuePurged(tx *sql.Tx, p Page) error {
	if err := writePage(tx, p); err != nil {
		return err
	}
	if err := unpulled(tx, "pages", p.ID); err != nil {
		return err
	}
	row := PageToRemote(p)
	row[ChangedKey] = purgedCols
	return queue(tx, "pages", p.ID, row, "deleted for good", p.ID)
}

// tombstone is a page deleted for good: its row keeps nothing of what it was.
func tombstone(p Page) Page {
	if p.DeletedAt.IsZero() {
		p.DeletedAt = p.PurgedAt
	}
	p.Title, p.Icon, p.Tags, p.DefaultTemplateID = "", "", nil, ""
	return p
}

func fileRemoval(f File) map[string]any {
	r := map[string]any{"bucket": "files", "path": f.StoragePath, "file_id": f.ID}
	if f.PageID != "" {
		r["page_id"] = f.PageID
	}
	return r
}

func purgeFile(tx *sql.Tx, dir, id string, now time.Time) ([]string, error) {
	files, err := filesIn(tx, dir, `id = ?`, id)
	if err != nil || len(files) == 0 || !files[0].PurgedAt.IsZero() {
		return nil, err
	}
	f := files[0]
	old := FileToRemote(f)
	if f.DeletedAt.IsZero() {
		f.DeletedAt = now
	}
	f.PurgedAt, f.UpdatedAt = now, now
	var copies []string
	if f.LocalPath != "" {
		copies = append(copies, f.LocalPath)
	}
	f.LocalPath = ""
	if _, err := tx.Exec(`UPDATE files SET local_path = NULL WHERE id = ?`, f.ID); err != nil {
		return nil, err
	}
	if err := putFile(tx, dir, f); err != nil {
		return nil, err
	}
	if err := queueChange(tx, "files", f.ID, old, FileToRemote(f), "deleted file for good", f.PageID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'storage' AND entity_id = ?`, "files/"+f.ID); err != nil {
		return nil, err
	}
	if f.StoragePath == "" {
		return copies, nil
	}
	return copies, queue(tx, "storage_delete", "files/"+f.ID, fileRemoval(f), "deleted file for good", f.PageID)
}

// putPurgedPage stores a page another device deleted for good, and takes everything on it off this device. On a page
// of another space, shared with this account, what it put there is its own to remove where it is stored: the page's
// owner may not.
func (s *Store) putPurgedPage(tx *sql.Tx, p Page) ([]string, error) {
	var removals []map[string]any
	if p.SpaceID != s.Session().SpaceID {
		var err error
		if removals, err = s.storedOn(tx, []string{p.ID}, true); err != nil {
			return nil, err
		}
	}
	copies, err := s.wipePages(tx, []string{p.ID}, true)
	if err != nil {
		return nil, err
	}
	if err := writePage(tx, tombstone(p)); err != nil {
		return nil, err
	}
	return copies, queueRemovals(tx, removals)
}

// isPurged reports whether the page was deleted for good here: what comes for it is not kept.
func isPurged(q querier, pageID string) (bool, error) {
	if pageID == "" {
		return false, nil
	}
	var n int
	err := q.QueryRow(`SELECT COUNT(*) FROM pages WHERE id = ? AND purged_at IS NOT NULL`, pageID).Scan(&n)
	return n > 0, err
}

// IsPurged reports whether the page was deleted for good.
func (s *Store) IsPurged(pageID string) bool {
	purged, err := isPurged(s.db, pageID)
	return err == nil && purged
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func anys(ids []string) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}
