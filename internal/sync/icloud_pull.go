package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/store"
)

// cloudPull applies the other devices' batches that came since the last
// pull, in each device's order, then brings up to date the pages whose
// content changed. Changes held for a row while one of this device's
// waited apply first, once it has gone.
func (s *Service) cloudPull(ctx context.Context, me string) error {
	peers, err := s.devices(me)
	if err != nil {
		return err
	}
	changed := map[string]bool{}
	// One that does not apply is held again, and does not hold up the
	// other devices' batches.
	if err := s.applyHeld(changed); err != nil {
		log.Printf("icloud: changes held: %v", err)
	}
	for table := range changed {
		s.st.Notify(table, "")
	}
	waiting, err := s.waiting()
	if err != nil {
		return err
	}
	for _, peer := range peers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.pullDevice(peer, waiting); err != nil {
			if errors.Is(err, icloud.ErrNewerFormat) {
				return err
			}
			log.Printf("icloud: pull %s: %v", peer, err)
		}
	}
	if err := s.setWaiting(waiting); err != nil {
		return err
	}
	return s.refreshWaiting(ctx)
}

// A batch of another device's that does not parse, though iCloud has
// downloaded it, holds up the device's log. It is read again each pass,
// as iCloud may still be writing it, and the device that wrote it writes
// it again when it was its last (checkOwnLog). Only once the device has
// written batches after it, so will not, and it has stayed so for
// damagedTries passes over damagedWait, is it passed over: what it held
// is lost here, which is better than every later change.
var (
	damagedTries = 20
	damagedWait  = 24 * time.Hour
)

// passDamaged records that batch seq of a device's does not parse, and
// reports whether to pass over it now. later says whether the device has
// written batches after it. Each batch is counted on its own, as a log
// being compacted may have more than one.
func (s *Service) passDamaged(peer string, seq int64, later bool, err error) bool {
	key := damagedPrefix + peer + "." + strconv.FormatInt(seq, 10)
	v, _ := s.st.Value(key)
	parts := strings.Split(v, "|")
	now := cloudNow().UnixMilli()
	first, tries := now, int64(0)
	if len(parts) == 3 && parts[0] == strconv.FormatInt(seq, 10) {
		first, _ = strconv.ParseInt(parts[1], 10, 64)
		tries, _ = strconv.ParseInt(parts[2], 10, 64)
	} else {
		log.Printf("icloud: batch %d of %s does not parse; waiting for it: %v", seq, peer, err)
	}
	tries++
	if !later || tries < int64(damagedTries) || now-first < damagedWait.Milliseconds() {
		_ = s.st.SetValue(key, fmt.Sprintf("%d|%d|%d", seq, first, tries))
		return false
	}
	// The count stays until the batch is read past or gone (forgetDamaged):
	// a compaction that stops after it passes over it again at once.
	log.Printf("icloud: batch %d of %s still does not parse, and batches came after it; passing over it", seq, peer)
	_ = s.st.SetValue(key, fmt.Sprintf("%d|%d|%d", seq, first, tries))
	return true
}

// damagedPrefix starts the keys passDamaged counts a device's batches
// under, "<device>.<seq>".
const damagedPrefix = "icloud.damaged."

// forgetDamaged drops what passDamaged kept of a device's batches up to
// seq, which are read or gone now, and the one key an earlier version
// kept for the device.
func (s *Service) forgetDamaged(peer string, upTo int64) {
	for k := range s.st.ValuesWithPrefix(damagedPrefix + peer) {
		n, err := strconv.ParseInt(strings.TrimPrefix(k, "."), 10, 64)
		if k == "" || strings.HasPrefix(k, ".") && err == nil && n <= upTo {
			_ = s.st.DeleteValue(damagedPrefix + peer + k)
		}
	}
}

// pullDevice applies a device's batches after its cursor. It stops at
// one that is not here yet, or not downloaded, and the next pull starts
// with it. A gap is passed over only to a snapshot after it, which holds
// the batches folded into it: a compaction cut short leaves batches
// before its snapshot.
func (s *Service) pullDevice(peer string, waiting map[string]bool) error {
	seqs, _, err := s.folder().Batches(peer)
	if err != nil {
		return err
	}
	key := "icloud.cursor." + peer
	cursor := s.intValue(key)
	changed := map[string]bool{}
	defer func() {
		for table := range changed {
			s.st.Notify(table, "")
		}
		s.forgetDamaged(peer, s.intValue(key))
	}()
	for i := 0; i < len(seqs); i++ {
		seq := seqs[i]
		if seq <= cursor {
			continue
		}
		b, err := s.readBatch(peer, seq)
		if errors.Is(err, icloud.ErrDamaged) {
			// A snapshot after it holds it; failing that, the next batch
			// may be passed over in the end. One after a gap waits with
			// the gap.
			if j := s.snapshotAfter(peer, seqs, i+1); j >= 0 {
				i = j - 1
				continue
			}
			if seq != cursor+1 || !s.passDamaged(peer, seq, i+1 < len(seqs), err) {
				return nil
			}
			cursor = seq
			if err := s.setInt(key, cursor); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			if errors.Is(err, icloud.ErrNotReady) {
				// The batches after it are asked for too, so the next pass
				// takes them all: on iOS, iCloud downloads only what it is
				// asked for, and a device catching up on a long log would
				// take a pass for each batch.
				for _, later := range seqs[i+1:] {
					if rel, err := icloud.BatchPath(peer, later); err == nil {
						s.folder().Download(rel)
					}
				}
				return nil
			}
			return err
		}
		if seq != cursor+1 && !b.Snapshot {
			j := s.snapshotAfter(peer, seqs, i+1)
			if j < 0 {
				return nil // the batches between are on their way
			}
			i = j - 1
			continue
		}
		named := false
		for _, e := range b.Entries {
			if err := s.applyEntry(peer, e, waiting, changed); err != nil {
				return fmt.Errorf("batch %d: %w", seq, err)
			}
			named = named || e.Kind == "content"
		}
		// The pages the batch names wait before the cursor passes it: the
		// other way round, a crash between would lose them.
		if named {
			if err := s.setWaiting(waiting); err != nil {
				return err
			}
		}
		cursor = seq
		if err := s.setInt(key, cursor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) readBatch(peer string, seq int64) (icloud.Batch, error) {
	var b icloud.Batch
	rel, err := icloud.BatchPath(peer, seq)
	if err != nil {
		return b, err
	}
	return b, s.folder().ReadJSON(rel, &b)
}

// snapshotAfter is the index of the first snapshot among a device's
// batches from index i, or -1.
func (s *Service) snapshotAfter(peer string, seqs []int64, i int) int {
	for ; i < len(seqs); i++ {
		if b, err := s.readBatch(peer, seqs[i]); err == nil && b.Snapshot {
			return i
		}
	}
	return -1
}

// applyEntry applies one change of a device's. A row's columns apply
// where their version is newer than this device's (applyRow); when the
// row has a change waiting here, that change wins on its columns, and its
// version will be newer than the one seen now. A page's content waits to
// be brought up to date, to at least the version the batch names.
func (s *Service) applyEntry(peer string, e icloud.Entry, waiting, changed map[string]bool) error {
	switch e.Kind {
	case "content":
		if !icloud.ValidID(e.PageID) {
			return nil
		}
		waiting[e.PageID] = true
		key := wantKey(peer, e.PageID)
		if e.Version > s.intValue(key) {
			return s.setInt(key, e.Version)
		}
		return nil
	case "row":
	default:
		return nil // a kind a newer Leaflet writes
	}
	if !synced(e.Table) || !icloud.ValidID(e.ID) || e.Row == nil {
		return nil
	}
	return s.applyRow(e, changed)
}

// putCloudPage stores a page of another device's. Two devices that moved
// pages inside each other at once leave them in a loop, each under the
// other and none under a top-level page; no server turns the second move
// away. The page of the loop with the least ID goes to the top level here,
// which every device chooses alike, whichever of the rows it had first.
func (s *Service) putCloudPage(r map[string]any) error {
	id := store.S(r, "id")
	loop := s.st.ParentLoop(id, store.S(r, "parent_id"))
	if len(loop) == 0 {
		return s.st.PutRemotePage(r)
	}
	least := slices.Min(loop)
	if least == id {
		r["parent_id"] = nil
		return s.st.PutRemotePage(r)
	}
	if err := s.st.PutRemotePage(r); err != nil {
		return err
	}
	return s.st.DetachPage(least)
}

// refreshWaiting brings up to date the pages whose content changed on
// another device, if this device keeps them: open, all pages kept
// offline, or this one kept. Others wait until they are opened.
func (s *Service) refreshWaiting(ctx context.Context) error {
	waiting, err := s.waiting()
	if err != nil {
		return err
	}
	allOffline := s.st.Settings().AllPagesOffline
	// A page whose file iCloud has not brought yet waits for it, and is no
	// failure; another page's error still is.
	var failed error
	for id := range waiting {
		p, err := s.st.Page(id)
		if err != nil {
			continue // its row comes in a later batch
		}
		if !s.keepsContent(p, allOffline) {
			continue
		}
		if _, err := s.cloudRefresh(ctx, id); err != nil {
			switch {
			case unreadable(err):
				// The page keeps its content here, and waits until every
				// version can be read; the other pages still come.
				log.Printf("icloud: merge %s: %v", id, err)
			case !errors.Is(err, icloud.ErrNotReady):
				failed = errors.Join(failed, err)
			}
			continue
		}
		delete(waiting, id)
	}
	if err := s.setWaiting(waiting); err != nil {
		return errors.Join(failed, err)
	}
	return failed
}

// wantKey keeps the version of a device's page file a batch named, until
// this device has merged it: iCloud may bring the batch first.
func wantKey(peer, pageID string) string { return "icloud.want." + peer + "." + pageID }

// cloudRefresh merges into this device's version of a page every other
// device's version that is newer than the one it last merged, which is
// the base of the merge. A page file older than its batch named, or not
// here yet, is ErrNotReady, and the page waits for it. A version that
// cannot be read, its or this device's, is errPeerUnreadable or
// errUnreadable, and the page waits too, while the other devices' still
// merge. It then queues its own version when that differs from the one
// merged and from what it published, so the devices end up with one
// version, the same whichever merged first.
func (s *Service) cloudRefresh(ctx context.Context, pageID string) (*doc.Content, error) {
	// A page of another space than the devices sync, as one shared with
	// the user by another account, takes no other device's version: none
	// of theirs is of it.
	if p, err := s.st.Page(pageID); err == nil && p.SpaceID != s.st.Session().SpaceID {
		return s.st.Content(pageID), nil
	}
	me := s.st.DeviceID()
	peers, err := s.devices(me)
	if err != nil {
		return nil, err
	}
	var notReady, unread error
	for _, peer := range peers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel, err := icloud.PagePath(peer, pageID)
		if err != nil {
			return nil, err
		}
		want := s.intValue(wantKey(peer, pageID))
		if !s.folder().Exists(rel) {
			if want > 0 {
				notReady = icloud.ErrNotReady
			}
			continue
		}
		// A page file as it was when this device last merged it, by its
		// time and size, is not read again while what it merged is still
		// here, as mergePeer would pass over it: its version is known. One
		// whose base went since, as with the device wiped or its iCloud
		// sync forgotten, is read and merged again, as is one older than
		// a batch named, which may have been written again as it was.
		stamp, stamped := s.folder().Stamp(rel)
		version, merged := s.mergedFile(rel, stamp)
		if merged && version >= want {
			known, has, err := s.st.PeerVersion(peer, pageID)
			if err != nil {
				return nil, err
			}
			merged = has && version <= known
		} else {
			merged = false
		}
		if !stamped || !merged {
			var file icloud.PageFile
			if err := s.folder().ReadJSON(rel, &file); err != nil {
				if errors.Is(err, icloud.ErrNotReady) {
					notReady = err
					continue
				}
				return nil, err
			}
			if file.Version < want {
				notReady = icloud.ErrNotReady
				continue
			}
			if err := s.mergePeer(me, peer, pageID, file); err != nil {
				if unreadable(err) {
					unread = err
					continue
				}
				return nil, err
			}
			version = file.Version
			if stamped {
				s.keepMerged(rel, stamp, version)
			}
		}
		if version < want {
			notReady = icloud.ErrNotReady
			continue
		}
		if want > 0 {
			if err := s.st.DeleteValue(wantKey(peer, pageID)); err != nil {
				return nil, err
			}
		}
	}
	if unread != nil {
		return s.st.Content(pageID), unread
	}
	if notReady != nil {
		return s.st.Content(pageID), notReady
	}
	return s.st.Content(pageID), nil
}

// mergedFile reports whether the page file at rel is as it was when this
// device last merged it, by its stamp, and the version it had.
func (s *Service) mergedFile(rel string, stamp icloud.Stamp) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen, ok := s.pageFiles[s.folder().Root+"\x00"+rel]
	if !ok || !seen.stamp.ModTime.Equal(stamp.ModTime) || seen.stamp.Size != stamp.Size {
		return 0, false
	}
	return seen.version, true
}

// keepMerged keeps that the page file at rel, of stamp, was merged at
// version.
func (s *Service) keepMerged(rel string, stamp icloud.Stamp, version int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageFiles[s.folder().Root+"\x00"+rel] = mergedFile{stamp, version}
}

// errPeerUnreadable is a page whose version from another device cannot be
// read here, as one a newer version of Leaflet wrote may not be. It is
// not merged, as an empty page would take this device's place, and waits
// until it can be read, as errServerUnreadable does.
var errPeerUnreadable = errors.New("the page's content from another device could not be read, so it was not synced")

// mergePeer merges one device's version of a page into this device's.
func (s *Service) mergePeer(me, peer, pageID string, file icloud.PageFile) error {
	known, hasBase, err := s.st.PeerContent(peer, pageID)
	if err != nil {
		return err
	}
	if hasBase && file.Version <= known.Version {
		return nil
	}
	theirs, err := doc.Decode(string(file.Content))
	if err != nil {
		return fmt.Errorf("%w: %w", errPeerUnreadable, err)
	}
	var base *doc.Content
	if hasBase {
		base, _ = doc.Decode(known.Content)
	}
	// The merge is queued to go to the other devices when it differs
	// from the version merged and from what this device published, unless
	// a change of this device's waits already.
	own, _, err := s.st.PeerContent(me, pageID)
	if err != nil {
		return err
	}
	label := func(merged string) string {
		if merged != string(file.Content) && merged != own.Content && !s.st.HasPending("page_content", pageID) {
			return "merged from another device"
		}
		return ""
	}
	// The merge is stored and queued together, so it cannot be left here
	// only, and with the version merged and its conflicts: once that
	// version is recorded, it is never merged again, so a conflict not
	// recorded with it would never be met again.
	from := store.PeerContent{Peer: peer, Version: file.Version, Content: string(file.Content)}
	var merged *doc.Content
	var conflicts []doc.Conflict
	queued := false
	_, next, err := s.storeMerge(pageID, func(local store.ContentRow, kept bool) (*doc.Content, error) {
		merged, conflicts, queued = theirs, nil, false
		if !kept {
			return theirs, nil
		}
		mine, err := doc.Decode(local.Content)
		if err != nil {
			// Merging into an empty page would drop this device's
			// version: it is left for when it can be read.
			return nil, errUnreadable
		}
		merged, conflicts = doc.Merge(base, mine, theirs)
		if merged.Encode() == local.Content {
			return nil, nil
		}
		return merged, nil
	}, func(over string, c *doc.Content) error {
		l := label(c.Encode())
		queued = l != ""
		return s.st.PutMergedContentOver(pageID, over, c, l, from, s.ownConflicts(me, peer, pageID, file, conflicts)...)
	})
	if err != nil {
		return err
	}
	// Content this device had as merged, so stored nothing, is queued
	// with the version merged.
	if next == nil {
		l := label(merged.Encode())
		queued = l != ""
		if err := s.st.PeerMerged(pageID, from, l, s.ownConflicts(me, peer, pageID, file, conflicts)...); err != nil {
			return err
		}
	}
	if queued {
		s.Kick()
	}
	return nil
}

// ownConflicts are the conflicts of a merge worth a notice on this
// device: those where its own edit lost to one the other device made
// without having seen it. A version this device only passed on, which
// another device has too, is that device's to hear of; an edit made
// before the version of this device's the other had merged, it saw.
func (s *Service) ownConflicts(me, peer, pageID string, file icloud.PageFile, conflicts []doc.Conflict) []doc.Conflict {
	if len(conflicts) == 0 {
		return nil
	}
	others, _ := s.st.PeerContents(pageID)
	var out []doc.Conflict
	for _, c := range conflicts {
		if c.Kept() != "remote" || c.LocalEditedAt <= file.Merged[me] {
			continue
		}
		relayed := false
		for _, o := range others {
			if o.Peer == me || o.Peer == peer {
				continue
			}
			if sameVersion(doc.MustDecode(o.Content), c.BlockID, c.Local) {
				relayed = true
				break
			}
		}
		if !relayed {
			out = append(out, c)
		}
	}
	return out
}

// sameVersion reports whether a page has a block as node is, or, for nil,
// has it deleted.
func sameVersion(c *doc.Content, id string, node *doc.Node) bool {
	n := c.Find(id)
	if node == nil {
		return n == nil
	}
	return doc.SameBlock(n, node)
}
