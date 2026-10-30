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
	err = s.applyHeld(changed)
	for table := range changed {
		s.st.Notify(table, "")
	}
	if err != nil {
		return err
	}
	waiting := s.waiting()
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
// written batches after it.
func (s *Service) passDamaged(peer string, seq int64, later bool, err error) bool {
	key := "icloud.damaged." + peer
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
	log.Printf("icloud: batch %d of %s still does not parse, and batches came after it; passing over it", seq, peer)
	_ = s.st.DeleteValue(key)
	return true
}

// pullDevice applies a device's batches after its cursor. It stops at
// one that is not here yet, or not downloaded, and the next pull starts
// with it. A gap is passed over only to a snapshot after it, which holds
// the batches folded into it: a compaction cut short leaves batches
// before its snapshot.
func (s *Service) pullDevice(peer string, waiting map[string]bool) error {
	seqs, _, err := s.ICloud.Batches(peer)
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
	return b, s.ICloud.ReadJSON(rel, &b)
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
	if !syncedRows[e.Table] || !icloud.ValidID(e.ID) || e.Row == nil {
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
	waiting := s.waiting()
	allOffline := s.st.Settings().AllPagesOffline
	var failed error
	for id := range waiting {
		p, err := s.st.Page(id)
		if err != nil {
			continue // its row comes in a later batch
		}
		s.mu.Lock()
		open := s.open[id]
		s.mu.Unlock()
		if !open && !allOffline && !p.KeepOffline {
			continue
		}
		if _, err := s.cloudRefresh(ctx, id); err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		delete(waiting, id)
	}
	if err := s.setWaiting(waiting); err != nil {
		return errors.Join(failed, err)
	}
	if errors.Is(failed, icloud.ErrNotReady) {
		return nil
	}
	return failed
}

// wantKey keeps the version of a device's page file a batch named, until
// this device has merged it: iCloud may bring the batch first.
func wantKey(peer, pageID string) string { return "icloud.want." + peer + "." + pageID }

// cloudRefresh merges into this device's version of a page every other
// device's version that is newer than the one it last merged, which is
// the base of the merge. A page file older than its batch named, or not
// here yet, is ErrNotReady, and the page waits for it. It then queues its own version when that differs
// from the one merged and from what it published, so the devices end up
// with one version, the same whichever merged first.
func (s *Service) cloudRefresh(ctx context.Context, pageID string) (*doc.Content, error) {
	me := s.st.DeviceID()
	peers, err := s.devices(me)
	if err != nil {
		return nil, err
	}
	var notReady error
	for _, peer := range peers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel, err := icloud.PagePath(peer, pageID)
		if err != nil {
			return nil, err
		}
		want := s.intValue(wantKey(peer, pageID))
		if !s.ICloud.Exists(rel) {
			if want > 0 {
				notReady = icloud.ErrNotReady
			}
			continue
		}
		var file icloud.PageFile
		if err := s.ICloud.ReadJSON(rel, &file); err != nil {
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
			return nil, err
		}
		if want > 0 {
			if err := s.st.DeleteValue(wantKey(peer, pageID)); err != nil {
				return nil, err
			}
		}
	}
	if notReady != nil {
		return s.st.Content(pageID), notReady
	}
	return s.st.Content(pageID), nil
}

// mergePeer merges one device's version of a page into this device's.
func (s *Service) mergePeer(me, peer, pageID string, file icloud.PageFile) error {
	known, hasBase := s.st.PeerContent(peer, pageID)
	if hasBase && file.Version <= known.Version {
		return nil
	}
	theirs, err := doc.Decode(string(file.Content))
	if err != nil {
		return nil // not a page this Leaflet can read
	}
	var base *doc.Content
	if hasBase {
		base, _ = doc.Decode(known.Content)
	}
	// The merge is queued to go to the other devices when it differs
	// from the version merged and from what this device published, unless
	// a change of this device's waits already.
	own, _ := s.st.PeerContent(me, pageID)
	label := func(merged string) string {
		if merged != string(file.Content) && merged != own.Content && !s.st.HasPending("page_content", pageID) {
			return "merged from another device"
		}
		return ""
	}
	// The user or an agent may save while this merges: store the merge
	// only over the content it merged, and merge again otherwise. It is
	// stored and queued together, so it cannot be left here only.
	var next *doc.Content
	var conflicts []doc.Conflict
	var before string
	stored, queued := false, false
	for range 3 {
		local, localErr := s.st.ContentRow(pageID)
		next, conflicts, before = theirs, nil, ""
		if localErr == nil {
			before = local.Content
			mine, err := doc.Decode(local.Content)
			if err != nil {
				// Merging into an empty page would drop this device's
				// version: it is left for when it can be read.
				log.Printf("merge %s from %s: %v", pageID, peer, errUnreadable)
				return nil
			}
			next, conflicts = doc.Merge(base, mine, theirs)
			if next.Encode() == local.Content {
				stored = true
				break
			}
		}
		beforeStore(pageID)
		l := label(next.Encode())
		err := s.st.PutMergedContentOver(pageID, before, next, l)
		if errors.Is(err, store.ErrContentChanged) {
			continue
		}
		if err != nil {
			return err
		}
		stored, queued = true, l != ""
		break
	}
	if !stored {
		return errors.New("the page kept changing here while it synced")
	}
	if err := s.st.PutPeerContent(peer, pageID, file.Version, string(file.Content)); err != nil {
		return err
	}
	for _, c := range s.ownConflicts(me, peer, pageID, file, conflicts) {
		if err := s.st.AddConflict(pageID, c); err != nil {
			return err
		}
	}
	merged := next.Encode()
	if merged != before && s.OnPageContent != nil {
		s.OnPageContent(pageID)
	}
	// Content this device had as merged, so stored nothing, is queued
	// here.
	if l := label(merged); !queued && l != "" {
		if err := s.st.Queue("page_content", pageID, map[string]any{"page_id": pageID}, l, pageID); err != nil {
			return err
		}
		queued = true
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
