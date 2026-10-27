package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/store"
)

// cloudPull applies the other devices' batches that came since the last
// pull, in each device's order, then brings up to date the pages whose
// content changed.
func (s *Service) cloudPull(ctx context.Context, me string) error {
	peers, err := s.devices(me)
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
	s.setWaiting(waiting)
	return s.refreshWaiting(ctx)
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
		for _, e := range b.Entries {
			if err := s.applyEntry(peer, e, waiting, changed); err != nil {
				return fmt.Errorf("batch %d: %w", seq, err)
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

// applyEntry applies one change of a device's. A row applies when its
// version is newer than this device's; when the row has a change waiting
// here, that change wins, and its version will be newer than the one seen
// now. A page's content waits to be brought up to date, to at least the
// version the batch names.
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
	v := store.RowVersion{At: e.At, Device: e.By}
	if !v.After(s.st.RowVersion(e.Table, e.ID)) {
		return nil
	}
	if s.st.HasPending(e.Table, e.ID) {
		return s.st.SetRowVersion(e.Table, e.ID, v)
	}
	r := e.Row
	r["id"] = e.ID
	// The devices sync one space: a row from before a device joined it
	// has the device's own.
	if _, ok := r["space_id"]; ok {
		r["space_id"] = s.st.Session().SpaceID
	}
	var err error
	switch e.Table {
	case "pages":
		err = s.st.PutRemotePage(r)
	case "folders":
		err = s.st.PutRemoteFolder(r)
	case "files":
		err = s.st.PutRemoteFile(r)
	case "comments":
		err = s.st.PutRemoteComment(r, store.S(r, "author_name"))
	case "recordings":
		err = s.st.PutRemoteRecording(r)
	}
	if err != nil {
		return err
	}
	changed[e.Table] = true
	return s.st.SetRowVersion(e.Table, e.ID, v)
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
	s.setWaiting(waiting)
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
		base = doc.MustDecode(known.Content)
	}
	// The user or an agent may save while this merges: store the merge
	// only over the content it merged, and merge again otherwise.
	var next *doc.Content
	var conflicts []doc.Conflict
	var before string
	stored := false
	for range 3 {
		local, localErr := s.st.ContentRow(pageID)
		next, conflicts, before = theirs, nil, ""
		if localErr == nil {
			before = local.Content
			next, conflicts = doc.Merge(base, doc.MustDecode(local.Content), theirs)
			if next.Encode() == local.Content {
				stored = true
				break
			}
		}
		beforeStore(pageID)
		err := s.st.PutSyncedContentOver(pageID, before, next, "", time.Time{}, 0)
		if errors.Is(err, store.ErrContentChanged) {
			continue
		}
		if err != nil {
			return err
		}
		stored = true
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
	own, _ := s.st.PeerContent(me, pageID)
	if merged != string(file.Content) && merged != own.Content && !s.st.HasPending("page_content", pageID) {
		if err := s.st.Queue("page_content", pageID, map[string]any{"page_id": pageID}, "merged from another device", pageID); err != nil {
			return err
		}
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
