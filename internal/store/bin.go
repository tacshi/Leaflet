package store

import (
	"database/sql"
	"errors"
	"log"
	"maps"
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

// BinRef is an item of the bin, by what it is, its id and its deletion as the bin listed it. Only that deletion is
// deleted for good: an item restored since, here or on another device whose change came in, or deleted again, which
// gave it its days in the bin anew, is left as it is.
type BinRef struct {
	Kind      BinKind
	ID        string
	DeletedAt time.Time
}

// BinItem is a page or a file in the bin. A page comes with the pages under it that were deleted with it, which
// Pages counts with it.
type BinItem struct {
	BinRef
	Title string
	Icon  string
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
		items = append(items, BinItem{BinRef: BinRef{Kind: BinPage, ID: p.ID, DeletedAt: p.DeletedAt}, Title: p.DisplayTitle(),
			Icon: p.Icon, GoesAt: pageGoes(p.DeletedAt), Template: p.Template, Pages: len(ids)})
	}
	// A file deleted without the bin keeping it, before the bin or by a Leaflet from before it, went with its contents:
	// there is nothing of it to restore.
	files, err := s.queryFiles(`deleted_at IS NOT NULL AND kept AND purged_at IS NULL AND page_id IS NULL AND space_id = ?`, space)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		items = append(items, BinItem{BinRef: BinRef{Kind: BinFile, ID: f.ID, DeletedAt: f.DeletedAt}, Title: f.Name,
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
	return s.updateFile(id, "restored file", func(tx *sql.Tx, f *File) (bool, error) {
		// A file deleted without the bin keeping its contents went for good with them.
		if f.Gone() {
			return false, ErrPurged
		}
		if f.DeletedAt.IsZero() {
			return false, nil
		}
		if f.FolderID != "" {
			if folder, err := folderIn(tx, f.FolderID); err == nil && !folder.DeletedAt.IsZero() {
				f.FolderID = ""
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
		}
		f.DeletedAt, f.Kept = time.Time{}, false
		return true, nil
	})
}

// TrashFile puts a file in the bin: it keeps its contents, here and where it is stored, until it goes for good.
func (s *Store) TrashFile(id string) error {
	err := s.updateFile(id, "deleted file", func(_ *sql.Tx, f *File) (bool, error) {
		if !f.DeletedAt.IsZero() {
			return false, nil
		}
		f.DeletedAt, f.Kept = f.UpdatedAt, true
		return true, nil
	})
	if IsNotFound(err) {
		return nil
	}
	return err
}

// Wipe says when deleting a page for good takes what was on it off this device.
type Wipe int

const (
	// WipeNow takes it off at once: no server orders the deletion with what other devices did since.
	WipeNow Wipe = iota
	// WipeOnceTaken keeps it until the server takes the deletion (WipePurged). The server refuses the deletion of a
	// page another device restored, or deleted again, since the deletion this device saw: the page then comes back
	// whole (putRemotePage).
	WipeOnceTaken
)

// Purge deletes pages and files of the bin for good, each as the bin listed it: a page with those under it deleted
// with it.
func (s *Store) Purge(wipe Wipe, refs ...BinRef) error {
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
				gone, err = s.purgePage(tx, ref, now, wipe)
			case BinFile:
				gone, err = purgeFile(tx, s.Dir, ref.ID, now, wipe, func(f File) bool {
					return f.Kept && !f.DeletedAt.IsZero() && sameDeletion(f.DeletedAt, ref.DeletedAt)
				})
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
func (s *Store) EmptyBin(wipe Wipe) error {
	items, err := s.Bin()
	if err != nil {
		return err
	}
	return s.Purge(wipe, refs(items)...)
}

// PurgeOld deletes for good what has been in the bin for BinDays by the time, and reports whether there was any.
func (s *Store) PurgeOld(now time.Time, wipe Wipe) (bool, error) {
	items, err := s.Bin()
	if err != nil {
		return false, err
	}
	old := slices.DeleteFunc(items, func(it BinItem) bool { return it.GoesAt.After(now) })
	return len(old) > 0, s.Purge(wipe, refs(old)...)
}

// StoredObject is an object where pages' files and recordings are stored, by its bucket and path.
type StoredObject struct{ Bucket, Path string }

// WipePurged takes what was on a page deleted for good off this device once the server has taken its deletion, and
// has it removed where it is stored: what this device knew of, and what the server listed, which another device, or
// someone the page was shared with, put there. A page not deleted for good here is left as it is.
func (s *Store) WipePurged(pageID string, listed []StoredObject) error {
	var copies []string
	err := s.inTx(func(tx *sql.Tx) error {
		if gone, err := isPurged(tx, pageID); err != nil || !gone {
			return err
		}
		removals, err := s.storedOn(tx, []string{pageID}, false)
		if err != nil {
			return err
		}
		if copies, err = s.wipePages(tx, []string{pageID}, true); err != nil {
			return err
		}
		if err := liftOutOf(tx, []string{pageID}); err != nil {
			return err
		}
		known := map[StoredObject]bool{}
		for _, r := range removals {
			known[StoredObject{S(r, "bucket"), S(r, "path")}] = true
		}
		for _, o := range listed {
			if o.Bucket == "" || o.Path == "" || known[o] {
				continue
			}
			if elsewhere, err := storedElsewhere(tx, o); err != nil {
				return err
			} else if elsewhere {
				log.Printf("bin: %s/%s, listed with page %s, is what this device has elsewhere: left as it is", o.Bucket, o.Path, pageID)
				continue
			}
			known[o] = true
			removals = append(removals, map[string]any{"bucket": o.Bucket, "path": o.Path, "page_id": pageID})
		}
		return queueRemovals(tx, removals)
	})
	if err != nil {
		return err
	}
	for _, path := range copies {
		s.removeLocalCopy(path)
	}
	s.Notify("outbox", pageID)
	return nil
}

// DropUnsent removes a page made on this device that has gone nowhere yet, with all that is queued of it, and reports
// whether it did: a page that went up, or came from elsewhere, is left as it is.
func (s *Store) DropUnsent(id string) (bool, error) {
	dropped := false
	var copies []string
	err := s.inTx(func(tx *sql.Tx) error {
		cols, queued, err := queuedCols(tx, "pages", id)
		if err != nil || !queued || cols != nil {
			return err
		}
		var pulled sql.NullInt64
		if err := tx.QueryRow(`SELECT pulled_at FROM pages WHERE id = ?`, id).Scan(&pulled); err != nil || pulled.Valid {
			return err
		}
		var versions int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM row_versions WHERE (tbl = 'pages' OR tbl LIKE 'pages.%') AND id = ?`, id).Scan(&versions); err != nil || versions > 0 {
			return err
		}
		if copies, err = s.wipePages(tx, []string{id}, false); err != nil {
			return err
		}
		dropped = true
		return nil
	})
	if err != nil || !dropped {
		return false, err
	}
	for _, path := range copies {
		s.removeLocalCopy(path)
	}
	s.Notify("*", "")
	return true, nil
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

// purgePage deletes for good a page of the space in the bin, deleted as the bin listed it, with those under it deleted
// with it. A page restored since, or deleted again, is left as it is, and so is a page of another space: it is its
// owner's to delete for good.
func (s *Store) purgePage(tx *sql.Tx, ref BinRef, now time.Time, wipe Wipe) ([]string, error) {
	root, err := pageIn(tx, ref.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if root.DeletedAt.IsZero() || !sameDeletion(root.DeletedAt, ref.DeletedAt) || !root.PurgedAt.IsZero() ||
		root.SpaceID != s.Session().SpaceID {
		return nil, nil
	}
	ids, err := binTree(tx, root)
	if err != nil {
		return nil, err
	}
	var removals []map[string]any
	var copies []string
	if wipe == WipeNow {
		if removals, err = s.storedOn(tx, ids, false); err != nil {
			return nil, err
		}
		if copies, err = s.wipePages(tx, ids, true); err != nil {
			return nil, err
		}
		if err := liftOutOf(tx, ids); err != nil {
			return nil, err
		}
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
		if !ownFilePath(f) {
			log.Printf("bin: file %s names %q, not its own object: left as it is", f.ID, f.StoragePath)
			continue
		}
		if !mine || f.SpaceID == sess.SpaceID {
			// It waits for the page's deletion to go up, as the server may refuse it.
			r := fileRemoval(f.ID, f.StoragePath)
			r["page_id"] = f.PageID
			removals = append(removals, r)
		}
	}
	recs, err := recordingsIn(tx, s.Dir, `page_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if r.AudioPath != "" && !ownAudioPath(r) {
			log.Printf("bin: recording %s names %q, not its own audio: left as it is", r.ID, r.AudioPath)
			continue
		}
		if !mine || sess.UserID != "" && strings.HasPrefix(r.AudioPath, sess.UserID+"/") {
			removals = append(removals, map[string]any{"bucket": "recordings", "path": r.AudioPath, "recording_id": r.ID, "page_id": r.PageID})
		}
	}
	return removals, nil
}

// ownFilePath reports whether the file's storage_path names its own object, {space}/{file}/{name}, as StorageKey
// makes it and the server binds it: a row that names another's object, as a page's writer could have put on it, has
// none of it removed.
func ownFilePath(f File) bool {
	name, ok := strings.CutPrefix(f.StoragePath, f.SpaceID+"/"+f.ID+"/")
	return ok && f.SpaceID != "" && f.ID != "" && name != "" && !strings.Contains(name, "/")
}

// ownAudioPath reports whether the recording's audio_path names its own audio: {recorder}/{recording}.{ext}, as the
// server binds it, or {recording}.{ext} in iCloud Drive.
func ownAudioPath(r Recording) bool {
	name := r.AudioPath
	if user, rest, ok := strings.Cut(name, "/"); ok {
		if user == "" {
			return false
		}
		name = rest
	}
	stem, _, _ := strings.Cut(name, ".")
	return stem == r.ID && !strings.Contains(name, "/")
}

// storedElsewhere reports whether an object the server listed with a page deleted for good is one of a file or a
// recording this device has on another page, which stays: once the page is wiped, any row that names it here is not
// on that page.
func storedElsewhere(tx *sql.Tx, o StoredObject) (bool, error) {
	var n int
	var err error
	switch o.Bucket {
	case "files":
		parts := strings.Split(o.Path, "/")
		if len(parts) != 3 {
			return true, nil
		}
		err = tx.QueryRow(`SELECT COUNT(*) FROM files WHERE (id = ? OR storage_path = ?) AND purged_at IS NULL`, parts[1], o.Path).Scan(&n)
	case "recordings":
		_, name, _ := strings.Cut(o.Path, "/")
		stem, _, _ := strings.Cut(name, ".")
		err = tx.QueryRow(`SELECT COUNT(*) FROM recordings WHERE id = ? OR audio_path = ?`, stem, o.Path).Scan(&n)
	}
	return n > 0, err
}

func queueRemovals(tx *sql.Tx, removals []map[string]any) error {
	for _, r := range removals {
		// One known only by where it is stored, as the server listed it, goes by its path.
		key := S(r, "bucket") + "/" + S(r, "path")
		switch {
		case S(r, "file_id") != "":
			key = "files/" + S(r, "file_id")
		case S(r, "recording_id") != "":
			key = "recordings/" + S(r, "recording_id")
		}
		if err := queue(tx, "storage_delete", key, r, "deleted for good", ""); err != nil {
			return err
		}
	}
	return nil
}

// purgedBlank is what a page deleted for good keeps in the columns that said what it was: nothing.
var purgedBlank = map[string]any{"title": "", "icon": nil, "tags": []any{}, "default_template_id": nil}

// purgedCols is what a page deleted for good changes of its row: its deletion goes with it, as the purge takes the
// place of what was queued of the page, its deletion too.
var purgedCols = append(slices.Sorted(maps.Keys(purgedBlank)), "deleted_at", "purged_at")

// TombstoneRow is a page's row, as from a sync log, as a page deleted for good keeps it: none of what it was.
func TombstoneRow(row map[string]any) map[string]any {
	out := maps.Clone(row)
	for k, v := range purgedBlank {
		if _, ok := out[k]; ok {
			out[k] = v
		}
	}
	return out
}

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

// purgedFileBlank is what a file deleted for good keeps in the columns that said what it was: nothing, as the server
// keeps (scrub_deleted_file), its values as a row read from JSON has them.
var purgedFileBlank = map[string]any{"name": "", "mime_type": "", "size_bytes": float64(0), "storage_path": nil}

// FileTombstoneRow is a file's row, as from a sync log, as a file deleted for good keeps it: none of what it was.
func FileTombstoneRow(row map[string]any) map[string]any {
	out := maps.Clone(row)
	for k, v := range purgedFileBlank {
		if _, ok := out[k]; ok {
			out[k] = v
		}
	}
	return out
}

// fileTombstone is a file deleted for good: its row keeps nothing of what it was. Where it was stored goes only in the
// removal of what is stored there.
func fileTombstone(f File) File {
	if f.DeletedAt.IsZero() {
		f.DeletedAt = f.PurgedAt
	}
	f.Name, f.MimeType, f.SizeBytes, f.StoragePath = "", "", 0, ""
	return f
}

// tombstone is a page deleted for good: its row keeps nothing of what it was.
func tombstone(p Page) Page {
	if p.DeletedAt.IsZero() {
		p.DeletedAt = p.PurgedAt
	}
	p.Title, p.Icon, p.Tags, p.DefaultTemplateID = "", "", nil, ""
	return p
}

// fileRemoval is the removal of where a file is stored. It waits for the file's row to go up, and goes by itself: the
// page the file was on going for good later takes nothing of it.
func fileRemoval(id, path string) map[string]any {
	return map[string]any{"bucket": "files", "path": path, "file_id": id}
}

// sameDeletion reports whether two deletions are one, as a device keeps them, to the millisecond.
func sameDeletion(a, b time.Time) bool { return a.UnixMilli() == b.UnixMilli() }

// purgeFile deletes a file for good, if may says so of it as it is: its row keeps nothing of what it was, an upload
// still waiting goes, and the removal of where it is stored is queued, to run once the deletion has gone up. Its copy
// here goes at once (WipeNow), or once the server takes the deletion (WipePurgedFile): one the server refuses, of a
// file restored or deleted again since, comes back with the file (putRemoteFile).
func purgeFile(tx *sql.Tx, dir, id string, now time.Time, wipe Wipe, may func(File) bool) ([]string, error) {
	var stored string
	var copies []string
	_, changed, err := changeFile(tx, dir, id, "deleted file for good", now, func(tx *sql.Tx, f *File) (bool, error) {
		if !f.PurgedAt.IsZero() || !may(*f) {
			return false, nil
		}
		if ownFilePath(*f) {
			stored = f.StoragePath
		}
		f.PurgedAt = now
		*f = fileTombstone(*f)
		if wipe == WipeNow && f.LocalPath != "" {
			copies, f.LocalPath = append(copies, f.LocalPath), ""
			if _, err := tx.Exec(`UPDATE files SET local_path = NULL WHERE id = ?`, f.ID); err != nil {
				return false, err
			}
		}
		return true, nil
	}, "deleted_at", "purged_at")
	if IsNotFound(err) || err == nil && !changed {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM outbox WHERE entity = 'storage' AND entity_id = ?`, "files/"+id); err != nil {
		return nil, err
	}
	if stored == "" {
		return copies, nil
	}
	return copies, queue(tx, "storage_delete", "files/"+id, fileRemoval(id, stored), "deleted file for good", "")
}

// WipePurgedFile takes the copy of a file deleted for good off this device once the server has taken its deletion. A
// file the server kept came back with its row, and keeps it.
func (s *Store) WipePurgedFile(id string) error {
	if s.removeCopy(id) {
		s.Notify("files", "")
	}
	return nil
}

// putPurgedPage stores a page deleted for good elsewhere, and takes everything on it off this device, and where it is
// stored what this device knew of: the device that deleted it may not have heard of all of it. On a page of another
// space, shared with this account, only what this account put there: the rest is the page's owner's to remove. A page
// of the space left under it goes to the top level, as the server puts it.
func (s *Store) putPurgedPage(tx *sql.Tx, p Page) ([]string, error) {
	own := p.SpaceID == s.Session().SpaceID
	removals, err := s.storedOn(tx, []string{p.ID}, !own)
	if err != nil {
		return nil, err
	}
	copies, err := s.wipePages(tx, []string{p.ID}, true)
	if err != nil {
		return nil, err
	}
	if own {
		if err := liftOutOf(tx, []string{p.ID}); err != nil {
			return nil, err
		}
	}
	if err := writePage(tx, tombstone(p)); err != nil {
		return nil, err
	}
	return copies, queueRemovals(tx, removals)
}

// liftOutOf puts the live pages under pages deleted for good at the top level, as the server does (pages_purged), and
// as the sidebar shows them: nothing of a page deleted for good, nor its shares, reaches a page that was under it. It
// queues nothing: the server, and every other device as it hears of the deletion, does the same.
func liftOutOf(tx *sql.Tx, ids []string) error {
	_, err := tx.Exec(`UPDATE pages SET parent_id = NULL WHERE parent_id IN (`+marks(len(ids))+`)
		AND deleted_at IS NULL AND purged_at IS NULL`, anys(ids)...) // #nosec G202
	return err
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

// notPurged is ErrPurged for a page deleted for good, on which nothing is written again.
func notPurged(q querier, pageID string) error {
	gone, err := isPurged(q, pageID)
	if err == nil && gone {
		return ErrPurged
	}
	return err
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
