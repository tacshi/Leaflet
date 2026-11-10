package sync

import (
	"bytes"
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

func (s *Service) cloudPull(ctx context.Context, me string) error {
	peers, err := s.devices(me)
	if err != nil {
		return err
	}
	changed := map[string]bool{}
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

var (
	damagedTries = 20
	damagedWait  = 24 * time.Hour
)

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
	log.Printf("icloud: batch %d of %s still does not parse, and batches came after it; passing over it", seq, peer)
	_ = s.st.SetValue(key, fmt.Sprintf("%d|%d|%d", seq, first, tries))
	return true
}

const damagedPrefix = "icloud.damaged."

func (s *Service) forgetDamaged(peer string, upTo int64) {
	for k := range s.st.ValuesWithPrefix(damagedPrefix + peer) {
		n, err := strconv.ParseInt(strings.TrimPrefix(k, "."), 10, 64)
		if k == "" || strings.HasPrefix(k, ".") && err == nil && n <= upTo {
			_ = s.st.DeleteValue(damagedPrefix + peer + k)
		}
	}
}

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
				return nil
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

func (s *Service) snapshotAfter(peer string, seqs []int64, i int) int {
	for ; i < len(seqs); i++ {
		if b, err := s.readBatch(peer, seqs[i]); err == nil && b.Snapshot {
			return i
		}
	}
	return -1
}

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
		return nil
	}
	if !synced(e.Table) || !icloud.ValidID(e.ID) || e.Row == nil {
		return nil
	}
	return s.applyRow(e, changed)
}

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

func (s *Service) refreshWaiting(ctx context.Context) error {
	waiting, err := s.waiting()
	if err != nil {
		return err
	}
	allOffline := s.st.Settings().AllPagesOffline
	var failed error
	for id := range waiting {
		p, err := s.st.Page(id)
		if err != nil {
			continue
		}
		if !s.keepsContent(p, allOffline) {
			continue
		}
		if _, err := s.cloudRefresh(ctx, id); err != nil {
			switch {
			case unreadable(err):
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

func wantKey(peer, pageID string) string { return "icloud.want." + peer + "." + pageID }

func (s *Service) cloudRefresh(ctx context.Context, pageID string) (*doc.Content, error) {
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

func (s *Service) mergedFile(rel string, stamp icloud.Stamp) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen, ok := s.pageFiles[s.folder().Root+"\x00"+rel]
	if !ok || !seen.stamp.ModTime.Equal(stamp.ModTime) || seen.stamp.Size != stamp.Size {
		return 0, false
	}
	return seen.version, true
}

func (s *Service) keepMerged(rel string, stamp icloud.Stamp, version int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageFiles[s.folder().Root+"\x00"+rel] = mergedFile{stamp, version}
}

var errPeerUnreadable = errors.New("the page's content from another device could not be read, so it was not synced")

func (s *Service) mergePeer(me, peer, pageID string, file icloud.PageFile) error {
	known, hasBase, err := s.st.PeerContent(peer, pageID)
	if err != nil {
		return err
	}
	if hasBase && file.Version <= known.Version {
		return nil
	}
	if len(bytes.TrimSpace(file.Content)) == 0 {
		return fmt.Errorf("%w: no content", errPeerUnreadable)
	}
	theirs, err := doc.Decode(string(file.Content))
	if err != nil {
		return fmt.Errorf("%w: %w", errPeerUnreadable, err)
	}
	var base *doc.Content
	if hasBase {
		base, _ = doc.Decode(known.Content)
	}
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

func sameVersion(c *doc.Content, id string, node *doc.Node) bool {
	n := c.Find(id)
	if node == nil {
		return n == nil
	}
	return doc.SameBlock(n, node)
}
