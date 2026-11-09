package sync

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/store"
)

// iCloud sync (docs/protocol.md §8): each device writes its changes to its
// own folder in the Leaflet folder of iCloud Drive, and reads the other
// devices'. What it keeps of them is in key_values under "icloud.":
//
//	icloud.space           the space of the folder this device joined
//	icloud.fetch_space     the space of one it synced through under an ID
//	                       it gave up, only to fetch from (fetchSpaceKey)
//	icloud.seq             the last batch this device wrote
//	icloud.writing         the batch it writes, "<seq>|<written_at>", kept
//	                       before it writes it
//	icloud.log_from        the first batch of this device's log, its
//	                       snapshot once it has compacted (device.json)
//	icloud.damaged.<device>.<seq> a batch of a device's that does not
//	                       parse, "<seq>|<first seen, ms>|<passes>":
//	                       another's, or this one's own as it compacts
//	icloud.cursor.<device> the last batch of another device applied here
//	icloud.want.<device>.<page> the version of a device's page file a
//	                       batch named, until it has merged here
//	icloud.page_version    the highest version of a page file this device
//	                       wrote, kept before it writes one
//	icloud.device_at       when device.json was last written (ms), 0 for
//	                       the next pass to write it
//	icloud.link.<lease>    a public link this device serves, which its
//	                       device.json says (icloud_links.go)
//	icloud.held.<table>.<id> other devices' entries of a row with a
//	                       change waiting here (icloud_rows.go)

// pageVersionKey keeps the highest version of a page file this device
// wrote (publishContent).
const pageVersionKey = "icloud.page_version"

// cloudTarget syncs through the Leaflet folder in iCloud Drive.
type cloudTarget struct{ s *Service }

func (t cloudTarget) ready() bool        { return t.s.folder().Root != "" }
func (t cloudTarget) needsNetwork() bool { return false }

// pass always reads what the other devices wrote: the folder is on this
// device, and reading it costs no requests.
func (t cloudTarget) pass(ctx context.Context, _ bool) error { return t.s.cloudPass(ctx) }
func (t cloudTarget) refresh(ctx context.Context, pageID string) (*doc.Content, error) {
	return t.s.cloudRefresh(ctx, pageID)
}

// flush has nothing to do: no cloud agent runs on what is in iCloud Drive.
func (t cloudTarget) flush(context.Context, string) error { return nil }

// cloudNow is the time iCloud sync stamps versions with; tests move it.
var cloudNow = time.Now

// compactAfter is how many batches a device's log holds before it folds
// them into one.
var compactAfter = 500

func (s *Service) intValue(key string) int64 {
	v, _ := s.st.Value(key)
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s *Service) setInt(key string, n int64) error {
	return s.st.SetValue(key, strconv.FormatInt(n, 10))
}

// EnableICloud turns iCloud sync on, making the Leaflet folder when
// iCloud Drive has none. A folder removed or replaced since this device
// last synced is joined afresh, as one never synced with.
func (s *Service) EnableICloud() error {
	if err := s.folder().Check(true); err != nil {
		return err
	}
	if joined, ok := s.st.Value("icloud.space"); ok && joined != "" {
		space, losers, err := s.folder().ReadSpace()
		replaced := errors.Is(err, fs.ErrNotExist) || err == nil && space.SpaceID != joined
		for _, l := range losers {
			if c, err := s.folder().SpaceOf(l); err == nil && c.SpaceID == joined {
				replaced = false // two devices made it at once: the pass settles it
			}
		}
		if replaced {
			if err := s.st.ForgetICloud(); err != nil {
				return err
			}
		}
	}
	s.SetICloud(true)
	return nil
}

func (s *Service) cloudPass(ctx context.Context) error {
	joined, _ := s.st.Value("icloud.space")
	// A folder that went after this device joined it stays gone: making it
	// again would start another set of pages beside the one in the
	// devices' folders.
	if err := s.folder().Check(joined == ""); err != nil {
		return err
	}
	if err := s.joinSpace(ctx, joined); err != nil {
		if errors.Is(err, icloud.ErrNotReady) {
			return nil // try again once iCloud has it
		}
		return err
	}
	if err := s.leaveServerSpace(); err != nil {
		return err
	}
	me, err := s.checkDeviceID(ctx)
	if errors.Is(err, icloud.ErrNotReady) {
		return nil // its own last batch, once iCloud has it again
	}
	if err != nil {
		return err
	}
	// device.json is written however the rest goes, so a lease taken or
	// ended since (keepLink) reaches the other devices though a step
	// fails.
	return errors.Join(s.cloudSteps(ctx, me), s.writeDevice(me, false))
}

// cloudSteps is what a pass does once this device knows the ID it writes
// under: it sends what is queued, takes what the other devices sent, and
// compacts its log.
func (s *Service) cloudSteps(ctx context.Context, me string) error {
	if !s.ownLogChecked {
		checked, err := s.checkOwnLog(ctx, me)
		if err != nil {
			return err
		}
		s.ownLogChecked = checked
	}
	if err := s.cloudPush(ctx, me); err != nil {
		return err
	}
	if err := s.cloudPull(ctx, me); err != nil {
		return err
	}
	if err := s.compact(me); err != nil {
		log.Printf("icloud: compact: %v", err)
	}
	return nil
}

// joinSpace has this device sync the pages of the folder's space.json,
// making it from this device's space when there is none. Joining moves
// this device's pages into that space and queues them all, as signing in
// does.
func (s *Service) joinSpace(ctx context.Context, joined string) error {
	me := s.st.DeviceID()
	if !icloud.ValidID(me) {
		return errors.New("icloud: this device's ID could not be read")
	}
	space, losers, err := s.folder().ReadSpace()
	made := false
	if errors.Is(err, fs.ErrNotExist) {
		if joined != "" {
			return icloud.ErrOtherLibrary
		}
		made = true
		space = icloud.Space{Format: icloud.Format, SpaceID: s.st.Session().SpaceID, CreatedAt: cloudNow().UTC(), CreatedByDevice: me}
		if err := s.folder().CreateJSON(icloud.SpacePath, space); errors.Is(err, fs.ErrExist) {
			// Another device made it meanwhile: this one joins that one,
			// on the next pass.
			return fmt.Errorf("%w: %w", icloud.ErrNotReady, err)
		} else if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// Two devices made space.json at once: the copy of the space that
	// lost goes, by the device that made it, once it is old (loserKept).
	lost := false
	for _, l := range losers {
		copyOf, err := s.folder().SpaceOf(l)
		if err != nil {
			continue
		}
		if copyOf.SpaceID == joined {
			lost = true
		}
		if copyOf.CreatedByDevice == me && cloudNow().Sub(copyOf.CreatedAt) > loserKept {
			_ = s.folder().Remove(l)
		}
	}
	// Joined, while this device's pages are still in the folder's space:
	// an account signed in to since moved them into its own, and one whose
	// session ended without its pages going left them there. Their rows
	// would go to the folder as another space's, which no device stores,
	// and the folder's would be passed over here, so they join it again.
	if joined == space.SpaceID && s.st.Session().SpaceID == joined {
		return nil
	}
	if joined != "" && joined != space.SpaceID && !lost {
		return icloud.ErrOtherLibrary
	}
	if err := s.moveInto(space.SpaceID, "My space", store.LocalUserID, !made && space.CreatedByDevice != me); err != nil {
		return err
	}
	if err := s.st.ForgetICloud(); err != nil {
		return err
	}
	// Batches this device wrote to the folder before stay; the next one
	// goes after them.
	seqs, _, _ := s.folder().Batches(me)
	if len(seqs) > 0 {
		if err := s.setInt("icloud.seq", seqs[len(seqs)-1]); err != nil {
			return err
		}
	}
	// The folder counts as joined only once everything is queued for it.
	return s.adopted(ctx, space.SpaceID, "icloud.space")
}

// loserKept is how long a space.json that lost stays in the folder: a
// device that joined its space before it lost learns of it by finding it
// beside the one that won, and would take the folder for another library
// if it had gone, until the user turned iCloud sync off and on.
const loserKept = 30 * 24 * time.Hour

// leaveServerSpace has the account this device signed in to before, whose
// session ended without its pages going, take them up afresh when it signs
// in again (resolveSpace): what changes here or comes from the other
// devices goes through iCloud Drive now, and leaves the queue without
// reaching the server. Kept adopted, the account would send none of it,
// and the server's versions would later be stored over it.
func (s *Service) leaveServerSpace() error {
	acct := s.st.CachedAccount()
	if acct == "" {
		return nil
	}
	if _, ok := s.st.Value("space." + acct); !ok {
		return nil
	}
	return s.st.DeleteValue("space." + acct)
}

// checkDeviceID returns the ID this device writes under. Two devices with
// one ID, as a backup restored to a new Mac has, would write over each
// other's batches: a batch after this device's last, or a copy iCloud
// made of one, says another device writes as this one. This one takes a
// new ID then, reads the old one's folder as another device's, and sends
// everything again.
func (s *Service) checkDeviceID(ctx context.Context) (string, error) {
	me := s.st.DeviceID()
	seqs, copies, err := s.folder().Batches(me)
	if err != nil {
		return "", err
	}
	// A seq that cannot be read is not 0: this device would take a batch
	// of its own for another's.
	seq, _, err := s.st.Lookup("icloud.seq")
	if err != nil {
		return "", err
	}
	last, _ := strconv.ParseInt(seq, 10, 64)
	if !copies && len(seqs) > 0 && seqs[len(seqs)-1] == last+1 {
		// This device may have stopped between writing a batch and
		// keeping its seq.
		own, err := s.wroteBatch(me, last+1)
		if err != nil {
			return "", err
		}
		if own {
			if err := s.setInt("icloud.seq", last+1); err != nil {
				return "", err
			}
			last++
		}
	}
	if !copies && (len(seqs) == 0 || seqs[len(seqs)-1] <= last) {
		return me, nil
	}
	next := doc.NewRowID()
	log.Printf("icloud: another device writes as %s; this one is %s now", me, next)
	// The new ID's log starts at its first batch, with nothing written.
	for _, kv := range [][2]string{{"device.id", next}, {"icloud.seq", "0"}, {"icloud.log_from", "0"}, {"icloud.cursor." + me, "0"}, {"icloud.device_at", "0"}} {
		if err := s.st.SetValue(kv[0], kv[1]); err != nil {
			return "", err
		}
	}
	if err := s.st.DeleteValue("icloud.writing"); err != nil {
		return "", err
	}
	space, _ := s.st.Value("icloud.space")
	return next, s.requeueAll(ctx, space)
}

// wroteBatch reports whether batch seq of this device's log is the one it
// kept, before writing it, that it was writing: the same seq and time.
// One that does not parse is this device's too, cut short by the crash
// that kept its seq from being saved: a batch another device wrote under
// this one's ID would parse. One iCloud has not brought back is
// ErrNotReady.
func (s *Service) wroteBatch(me string, seq int64) (bool, error) {
	v, _ := s.st.Value("icloud.writing")
	n, at, _ := strings.Cut(v, "|")
	if n != strconv.FormatInt(seq, 10) {
		return false, nil
	}
	b, err := s.readBatch(me, seq)
	if errors.Is(err, icloud.ErrDamaged) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return b.Device == me && b.Seq == seq && b.WrittenAt.Format(time.RFC3339Nano) == at, nil
}

// writeBatch writes a batch of this device's log, keeping what it writes
// first (wroteBatch), and then its seq.
func (s *Service) writeBatch(me string, b icloud.Batch) error {
	rel, err := icloud.BatchPath(me, b.Seq)
	if err != nil {
		return err
	}
	if err := s.st.SetValue("icloud.writing", strconv.FormatInt(b.Seq, 10)+"|"+b.WrittenAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := s.folder().WriteJSON(rel, b); err != nil {
		return err
	}
	return s.setInt("icloud.seq", b.Seq)
}

// checkOwnLog reads, once a run, this device's last batch. One a crash cut
// short before it reached the disk would hold up the other devices, which
// read each device's batches in order, for good: it is written again
// with nothing in it, and everything is sent again, as what it held is
// lost. It reports whether it could read the batch: one iCloud has not
// downloaded yet is read on a later pass.
func (s *Service) checkOwnLog(ctx context.Context, me string) (bool, error) {
	seqs, _, err := s.folder().Batches(me)
	if err != nil || len(seqs) == 0 {
		return err == nil, err
	}
	seq := seqs[len(seqs)-1]
	if _, err := s.readBatch(me, seq); !errors.Is(err, icloud.ErrDamaged) {
		// Fine, or not here to tell yet.
		return err == nil || errors.Is(err, icloud.ErrNewerFormat), nil
	}
	log.Printf("icloud: batch %d of this device does not parse; writing it again and sending everything", seq)
	if err := s.writeBatch(me, icloud.Batch{Format: icloud.Format, Device: me, Seq: seq, WrittenAt: cloudNow().UTC()}); err != nil {
		return false, err
	}
	space, _ := s.st.Value("icloud.space")
	return true, s.requeueAll(ctx, space)
}

// writeDevice writes device.json, with the public links this device
// serves (ownLinks), at most once an hour unless now.
func (s *Service) writeDevice(me string, now bool) error {
	at := cloudNow()
	if !now && at.Sub(time.UnixMilli(s.intValue("icloud.device_at"))) < time.Hour {
		return nil
	}
	rel, err := icloud.DevicePath(me)
	if err != nil {
		return err
	}
	d := deviceFile{Device: icloud.Device{Format: icloud.Format, ID: me, Name: s.deviceName(), Platform: runtime.GOOS,
		LastActiveAt: at.UTC(), LogFrom: s.intValue("icloud.log_from")}, PublicLinks: s.ownLinks(at)}
	if err := s.folder().WriteJSON(rel, d); err != nil {
		return err
	}
	return s.setInt("icloud.device_at", at.UnixMilli())
}

// cloudPush writes the outbox to one new batch of this device's log:
// files and audio first, then page content, then the batch that names
// them, so a device that reads the batch finds what it names.
func (s *Service) cloudPush(ctx context.Context, me string) error {
	entries, err := s.st.Queued()
	if err != nil || len(entries) == 0 {
		return err
	}
	s.forgetRetries(entries)
	// Uploads first: a recording's upload changes its row, which then
	// goes in this batch.
	uploaded := false
	for _, e := range entries {
		if e.Entity != "storage" || !s.due(e) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := s.cloudUpload(ctx, &e)
		if err != nil {
			log.Printf("icloud: upload %s: %v", e.EntityID, err)
			s.failed(e, err)
			continue
		}
		if done {
			_ = s.st.Dequeue(e)
			uploaded = true
		}
	}
	if uploaded {
		if entries, err = s.st.Queued(); err != nil {
			return err
		}
	}
	batch := icloud.Batch{Format: icloud.Format, Device: me, Seq: s.intValue("icloud.seq") + 1, WrittenAt: cloudNow().UTC()}
	type sent struct {
		e       store.OutboxEntry
		version store.RowVersion
		row     bool
		// cols are the columns the row's entry changed, nil for all.
		cols []string
		// content is the page's version published, recorded once the
		// batch that tells the other devices is written.
		content *store.PeerContent
	}
	var done []sent
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		case e.Entity == "storage":
			continue // waits or failed above; tried again next pass
		case e.Entity == "storage_delete":
			continue // once the batch with the deletion is written
		case s.contentsWait(e):
			continue // once its contents are in the folder
		case !s.due(e):
			continue // failed lately; it waits a while
		case synced(e.Entity):
			entry, err := s.rowEntry(me, &e)
			if errors.Is(err, errOtherSpace) {
				done = append(done, sent{e: e})
				continue
			}
			if err != nil {
				log.Printf("icloud: %s %s: %v", e.Entity, e.EntityID, err)
				s.failed(e, err)
				continue
			}
			batch.Entries = append(batch.Entries, entry)
			done = append(done, sent{e: e, version: store.RowVersion{At: entry.At, Device: me}, row: true, cols: entry.Changed})
		case e.Entity == "page_content":
			entry, published, err := s.publishContent(me, e.EntityID)
			if err != nil {
				log.Printf("icloud: page %s: %v", e.EntityID, err)
				s.failed(e, err)
				continue
			}
			if published != nil {
				batch.Entries = append(batch.Entries, entry)
			}
			done = append(done, sent{e: e, content: published})
		default:
			// Page history and agent runs stay on each device.
			done = append(done, sent{e: e})
		}
	}
	if len(batch.Entries) > 0 {
		rel, err := icloud.BatchPath(me, batch.Seq)
		if err != nil {
			return err
		}
		if s.folder().Exists(rel) {
			return errors.New("icloud: another device wrote this device's next batch")
		}
		if err := s.writeBatch(me, batch); err != nil {
			return err
		}
	}
	for _, d := range done {
		if d.row {
			if err := s.setVersion(d.e.Entity, d.e.EntityID, d.cols, d.version); err != nil {
				return err
			}
		}
		if c := d.content; c != nil {
			if err := s.st.PutPeerContent(me, d.e.EntityID, c.Version, c.Content); err != nil {
				return err
			}
		}
		_ = s.st.Dequeue(d.e)
	}
	for _, e := range entries {
		if e.Entity == "storage_delete" && s.due(e) {
			s.cloudRemove(&e)
		}
	}
	return nil
}

// cloudRemove removes a deleted file's copy from the folder once it may go
// (removal), as a batch has the file's deletion: a device that reads the
// batch no longer needs it, and one that reads none of it yet finds the
// file gone a little sooner. A device that sends everything again sends
// deleted files too, so a deletion in a batch that was lost still reaches
// the others.
func (s *Service) cloudRemove(entry *store.OutboxEntry) {
	payload, err := s.payload(entry)
	e := *entry
	if err != nil {
		s.failed(e, err)
		return
	}
	if payload == nil {
		return // no longer queued
	}
	if waits, stays := s.removal(payload); waits {
		return // till the deletion is in a batch, and no copy goes up from it
	} else if stays {
		_ = s.st.Dequeue(e)
		return
	}
	rel, err := blobPath(store.S(payload, "bucket"), store.S(payload, "path"))
	if err != nil {
		_ = s.st.Dequeue(e) // no copy was ever kept by such a name
		return
	}
	if err := s.folder().RemoveBlob(rel); err != nil {
		log.Printf("icloud: remove %s: %v", e.EntityID, err)
		s.failed(e, err)
		return
	}
	_ = s.st.Dequeue(e)
}

// rowEntry is a queued row as a batch has it: the row with its time, the
// columns changed here when the queue names them (store.ChangedKey), and
// for a comment its author's name, which no server here looks up. Its
// version is newer than any this device has seen of the row, so a change
// made here while another device's waited still wins over it. A row of
// another space than the one the devices sync, or on a page of one, is
// errOtherSpace: it never goes there.
func (s *Service) rowEntry(me string, entry *store.OutboxEntry) (icloud.Entry, error) {
	row, err := s.payload(entry)
	if err != nil {
		return icloud.Entry{}, err
	}
	if row == nil {
		return icloud.Entry{}, errors.New("no longer queued")
	}
	e := *entry
	if other, err := s.otherSpace(e.Entity, e.EntityID, row, true); err != nil || other {
		return icloud.Entry{}, cmp.Or(err, errOtherSpace)
	}
	// A recording settled after a quit (store.IfStatusKey) goes as just
	// after the version this device had of it, not after every one it has
	// seen: a transcription another device finished since is newer, and
	// keeps its transcript.
	_, settled := row[store.IfStatusKey]
	delete(row, store.IfStatusKey)
	// Only the columns changed here apply where the row is, so a change
	// another device made meanwhile to another column stays.
	var cols []string
	if list, ok := row[store.ChangedKey].([]any); ok {
		for _, c := range list {
			if k, ok := c.(string); ok {
				cols = append(cols, k)
			}
		}
	}
	delete(row, store.ChangedKey)
	changed, err := s.st.RowUpdatedAt(e.Entity, e.EntityID)
	if err != nil {
		return icloud.Entry{}, err
	}
	at := changed.UnixMilli()
	if changed.IsZero() {
		at = cloudNow().UnixMilli()
	}
	had, err := s.newestHad(e.Entity, e.EntityID, row)
	if err != nil {
		return icloud.Entry{}, err
	}
	if settled && had.At > 0 {
		at = had.At + 1
	} else {
		seen, err := s.newestSeen(e.Entity, e.EntityID, row)
		if err != nil {
			return icloud.Entry{}, err
		}
		at = max(at, seen.At+1)
	}
	row["updated_at"] = time.UnixMilli(at).UTC().Format(time.RFC3339Nano)
	if e.Entity == "comments" {
		if c, err := s.st.Comment(e.EntityID); err == nil {
			row["author_name"] = c.AuthorName
		}
	}
	return icloud.Entry{Kind: "row", Table: e.Entity, ID: e.EntityID, At: at, By: me, Row: row, Changed: cols}, nil
}

// publishContent writes this device's version of a page to its page file,
// and returns the batch entry that tells the other devices, with the
// version published, nil when it was already. The version counts as
// published once the batch is written: until then it goes again.
func (s *Service) publishContent(me, pageID string) (icloud.Entry, *store.PeerContent, error) {
	// A page that is gone has nothing to publish, nor one of another space
	// than the devices sync; one that cannot be read now stays queued.
	if p, err := s.st.Page(pageID); store.IsNotFound(err) || err == nil && p.SpaceID != s.st.Session().SpaceID {
		return icloud.Entry{}, nil, nil
	} else if err != nil {
		return icloud.Entry{}, nil, err
	}
	row, err := s.st.ContentRow(pageID)
	if store.IsNotFound(err) {
		return icloud.Entry{}, nil, nil
	} else if err != nil {
		return icloud.Entry{}, nil, err
	}
	own, _, err := s.st.PeerContent(me, pageID)
	if err != nil {
		return icloud.Entry{}, nil, err
	}
	if own.Content == row.Content && own.Version > 0 {
		return icloud.Entry{}, nil, nil // published as it is
	}
	peers, err := s.st.PeerContents(pageID)
	if err != nil {
		return icloud.Entry{}, nil, err
	}
	merged := map[string]int64{}
	for _, p := range peers {
		if p.Peer != me {
			merged[p.Peer] = p.Version
		}
	}
	// After the version last written, whether or not its batch was: the
	// highest this device wrote is kept before each page file is written
	// (pageVersionKey), so the file need not be read for it. A device from
	// before it was kept reads its file once.
	written := own.Version
	rel, err := icloud.PagePath(me, pageID)
	if err != nil {
		return icloud.Entry{}, nil, err
	}
	if v, ok := s.st.Value(pageVersionKey); ok {
		n, _ := strconv.ParseInt(v, 10, 64)
		written = max(written, n)
	} else {
		var last icloud.PageFile
		if s.folder().ReadJSON(rel, &last) == nil {
			written = max(written, last.Version)
		}
	}
	version := max(cloudNow().UnixMilli(), written+1)
	if err := s.setInt(pageVersionKey, version); err != nil {
		return icloud.Entry{}, nil, err
	}
	file := icloud.PageFile{Format: icloud.Format, PageID: pageID, Version: version, Merged: merged, Content: json.RawMessage(row.Content)}
	if err := s.folder().WriteJSON(rel, file); err != nil {
		return icloud.Entry{}, nil, err
	}
	return icloud.Entry{Kind: "content", PageID: pageID, Version: version},
		&store.PeerContent{Peer: me, Version: version, Content: row.Content}, nil
}

// cloudUpload copies a file or a recording's audio into the folder, and
// reports whether its entry can leave the queue: one whose copy waits for
// iCloud to download what it was made from stays (uploadFrom).
func (s *Service) cloudUpload(ctx context.Context, entry *store.OutboxEntry) (bool, error) {
	payload, err := s.payload(entry)
	if err != nil || payload == nil {
		return false, err
	}
	e := *entry
	// A file or audio of another space never goes there: its entry leaves
	// the queue, as its row's does.
	if other, err := s.uploadOfOtherSpace(e); err != nil || other {
		return err == nil, err
	}
	local, waits, err := s.uploadFrom(ctx, payload)
	if err != nil || waits || local == "" {
		return err == nil && !waits, err // gone: nothing to upload
	}
	if recID := store.S(payload, "recording_id"); recID != "" {
		rel, err := icloud.RecordingBlobPath(recID)
		if err != nil {
			return false, err
		}
		if err := s.folder().CopyIn(rel, local); err != nil {
			return false, err
		}
		_, _, err = s.uploadedAudio(recID, recID+".wav", local, cloudNow())
		return err == nil, err
	}
	fileID := strings.TrimPrefix(e.EntityID, "files/")
	rel, err := icloud.FileBlobPath(fileID, path.Base(store.S(payload, "path")))
	if err == nil {
		err = s.folder().CopyIn(rel, local)
	}
	return err == nil, err
}

// uploadOfOtherSpace reports whether an upload is of a file or a
// recording of another space than the one the devices sync, or on a page
// of one (otherSpace). One whose row is gone is of none.
func (s *Service) uploadOfOtherSpace(entry store.OutboxEntry) (bool, error) {
	// The entry is "files/<id>" or "recordings/<id>".
	table, id, _ := strings.Cut(entry.EntityID, "/")
	if table != "files" && table != "recordings" {
		return false, nil
	}
	row, here, err := s.localRow(table, id)
	if err != nil || !here {
		return false, err
	}
	return s.otherSpace(table, id, row, true)
}

// errOtherSpace is a row of another space than the one the devices sync
// through iCloud Drive, which never goes there.
var errOtherSpace = errors.New("of another space")

// blobPath is where the folder keeps a file or a recording's audio, by its
// storage path in either iCloud Drive or Supabase.
func blobPath(bucket, storagePath string) (string, error) {
	switch bucket {
	case "files":
		parts := strings.Split(storagePath, "/")
		if len(parts) < 2 {
			return "", errors.New("icloud: no such file")
		}
		return icloud.FileBlobPath(parts[len(parts)-2], parts[len(parts)-1])
	case "recordings":
		return icloud.RecordingBlobPath(strings.TrimSuffix(path.Base(storagePath), ".wav"))
	}
	return "", errors.New("icloud: nothing kept in " + bucket)
}

// errNotInICloud is a file or audio that went through iCloud Drive, and
// is neither on this device nor listed in the folder yet.
var errNotInICloud = errors.New("not in iCloud Drive yet")

// joinedICloud reports whether this device synced through a Leaflet
// folder of iCloud Drive it can still read.
func (s *Service) joinedICloud() bool {
	joined, _ := s.st.Value("icloud.space")
	if joined == "" {
		joined, _ = s.st.Value(fetchSpaceKey)
	}
	return joined != "" && s.folder().Root != ""
}

// fetchSpaceKey keeps the space of the folder this device synced through
// under an ID it has given up (sync's heartbeat): it joins the folder
// afresh, but what went up there is still fetched from it meanwhile.
const fetchSpaceKey = "icloud.fetch_space"

// originalLives reports whether what a copy's upload comes from (its
// from_path) still lives here: a file not deleted, or a recording. Its
// contents in the folder are only late then, as iCloud Drive lists what
// another device copies there only once it has come.
func (s *Service) originalLives(payload map[string]any) bool {
	from := store.S(payload, "from_path")
	if store.S(payload, "recording_id") != "" {
		_, err := s.st.Recording(strings.TrimSuffix(path.Base(from), ".wav"))
		return err == nil
	}
	parts := strings.Split(from, "/")
	if len(parts) < 2 {
		return false
	}
	f, err := s.st.File(parts[len(parts)-2])
	return err == nil && f.DeletedAt.IsZero()
}

// keptInICloud reports whether the Leaflet folder of iCloud Drive this
// device synced through has a file or audio. It looks only once this
// device has joined one, so it asks the system for no access before.
func (s *Service) keptInICloud(bucket, storagePath string) bool {
	if !s.joinedICloud() {
		return false
	}
	rel, err := blobPath(bucket, storagePath)
	return err == nil && s.folder().Exists(rel)
}

// cloudFetch opens what a device copied into the folder, waiting while
// iCloud downloads it.
func (s *Service) cloudFetch(ctx context.Context, bucket, storagePath string) (io.ReadCloser, error) {
	for {
		r, err := s.cloudRead(ctx, bucket, storagePath)
		if !errors.Is(err, icloud.ErrNotReady) {
			return r, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// cloudRead opens what a device copied into the folder, to read as it
// comes, once iCloud has downloaded it: until then it asks iCloud for it,
// and is icloud.ErrNotReady.
func (s *Service) cloudRead(_ context.Context, bucket, storagePath string) (io.ReadCloser, error) {
	rel, err := blobPath(bucket, storagePath)
	if err != nil {
		return nil, err
	}
	return s.folder().Open(rel)
}

// devices are the other devices that write to the folder.
func (s *Service) devices(me string) ([]string, error) {
	all, err := s.folder().Devices()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(d string) bool { return d == me }), nil
}
