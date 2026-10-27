package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
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
//	icloud.seq             the last batch this device wrote
//	icloud.cursor.<device> the last batch of another device applied here
//	icloud.device_at       when device.json was last written (ms)

type cloudTarget struct{ s *Service }

func (t cloudTarget) ready() bool                    { return t.s.ICloud.Root != "" }
func (t cloudTarget) needsNetwork() bool             { return false }
func (t cloudTarget) pass(ctx context.Context) error { return t.s.cloudPass(ctx) }
func (t cloudTarget) refresh(ctx context.Context, pageID string) (*doc.Content, error) {
	return t.s.cloudRefresh(ctx, pageID)
}

// flush has nothing to do: no cloud agent runs on what is in iCloud Drive.
func (t cloudTarget) flush(context.Context, string) error { return nil }
func (t cloudTarget) fetch(ctx context.Context, bucket, path string) ([]byte, error) {
	return t.s.cloudFetch(ctx, bucket, path)
}

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
	if err := s.ICloud.Check(true); err != nil {
		return err
	}
	if joined, ok := s.st.Value("icloud.space"); ok && joined != "" {
		space, losers, err := s.ICloud.ReadSpace()
		replaced := errors.Is(err, fs.ErrNotExist) || err == nil && space.SpaceID != joined
		for _, l := range losers {
			if c, err := s.ICloud.SpaceOf(l); err == nil && c.SpaceID == joined {
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
	if err := s.ICloud.Check(joined == ""); err != nil {
		return err
	}
	if err := s.joinSpace(joined); err != nil {
		if errors.Is(err, icloud.ErrNotReady) {
			return nil // try again once iCloud has it
		}
		return err
	}
	me, err := s.checkDeviceID()
	if err != nil {
		return err
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
	return s.writeDevice(me, false)
}

// joinSpace has this device sync the pages of the folder's space.json,
// making it from this device's space when there is none. Joining moves
// this device's pages into that space and queues them all, as signing in
// does.
func (s *Service) joinSpace(joined string) error {
	me := s.st.DeviceID()
	space, losers, err := s.ICloud.ReadSpace()
	if errors.Is(err, fs.ErrNotExist) {
		if joined != "" {
			return icloud.ErrOtherLibrary
		}
		space = icloud.Space{Format: icloud.Format, SpaceID: s.st.Session().SpaceID, CreatedAt: cloudNow().UTC(), CreatedByDevice: me}
		if err := s.ICloud.WriteJSON(icloud.SpacePath, space); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// Two devices made space.json at once: the copy of the space that
	// lost goes, by the device that made it.
	lost := false
	for _, l := range losers {
		copyOf, err := s.ICloud.SpaceOf(l)
		if err != nil {
			continue
		}
		if copyOf.SpaceID == joined {
			lost = true
		}
		if copyOf.CreatedByDevice == me {
			_ = s.ICloud.Remove(l)
		}
	}
	if joined == space.SpaceID {
		return nil
	}
	if joined != "" && !lost {
		return icloud.ErrOtherLibrary
	}
	local := s.st.Session().SpaceID
	if local != space.SpaceID {
		if err := s.st.PutSpace(space.SpaceID, "My space", store.LocalUserID); err != nil {
			return err
		}
		if err := s.st.MoveSpace(local, space.SpaceID); err != nil {
			return err
		}
		if err := s.st.SetValue("space.local", space.SpaceID); err != nil {
			return err
		}
		sess := s.st.Session()
		sess.SpaceID = space.SpaceID
		s.st.SetSession(sess)
	}
	if err := s.st.ForgetICloud(); err != nil {
		return err
	}
	// Batches this device wrote to the folder before stay; the next one
	// goes after them.
	seqs, _, _ := s.ICloud.Batches(me)
	if len(seqs) > 0 {
		if err := s.setInt("icloud.seq", seqs[len(seqs)-1]); err != nil {
			return err
		}
	}
	if err := s.st.SetValue("icloud.space", space.SpaceID); err != nil {
		return err
	}
	return s.requeueAll(space.SpaceID)
}

// checkDeviceID returns the ID this device writes under. Two devices with
// one ID, as a backup restored to a new Mac has, would write over each
// other's batches: a batch after this device's last, or a copy iCloud
// made of one, says another device writes as this one. This one takes a
// new ID then, reads the old one's folder as another device's, and sends
// everything again.
func (s *Service) checkDeviceID() (string, error) {
	me := s.st.DeviceID()
	seqs, copies, err := s.ICloud.Batches(me)
	if err != nil {
		return "", err
	}
	last := s.intValue("icloud.seq")
	if !copies && (len(seqs) == 0 || seqs[len(seqs)-1] <= last) {
		return me, nil
	}
	next := doc.NewRowID()
	log.Printf("icloud: another device writes as %s; this one is %s now", me, next)
	for _, kv := range [][2]string{{"device.id", next}, {"icloud.seq", "0"}, {"icloud.cursor." + me, "0"}, {"icloud.device_at", "0"}} {
		if err := s.st.SetValue(kv[0], kv[1]); err != nil {
			return "", err
		}
	}
	space, _ := s.st.Value("icloud.space")
	return next, s.requeueAll(space)
}

// writeDevice writes device.json, at most once an hour unless now.
func (s *Service) writeDevice(me string, now bool) error {
	at := cloudNow()
	if !now && at.Sub(time.UnixMilli(s.intValue("icloud.device_at"))) < time.Hour {
		return nil
	}
	rel, err := icloud.DevicePath(me)
	if err != nil {
		return err
	}
	d := icloud.Device{Format: icloud.Format, ID: me, Name: DeviceName(), Platform: runtime.GOOS,
		LastActiveAt: at.UTC(), LogFrom: s.intValue("icloud.log_from")}
	if err := s.ICloud.WriteJSON(rel, d); err != nil {
		return err
	}
	return s.setInt("icloud.device_at", at.UnixMilli())
}

// syncedRows are the tables whose rows go through iCloud Drive; history,
// agent runs and model connections stay on each device.
var syncedRows = map[string]bool{"pages": true, "folders": true, "files": true, "comments": true, "recordings": true}

// cloudPush writes the outbox to one new batch of this device's log:
// files and audio first, then page content, then the batch that names
// them, so a device that reads the batch finds what it names.
func (s *Service) cloudPush(ctx context.Context, me string) error {
	entries, err := s.st.Pending()
	if err != nil || len(entries) == 0 {
		return err
	}
	// Uploads first: a recording's upload changes its row, which then
	// goes in this batch.
	uploaded := false
	for _, e := range entries {
		if e.Entity != "storage" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.cloudUpload(e); err != nil {
			log.Printf("icloud: upload %s: %v", e.EntityID, err)
			_ = s.st.MarkFailed(e, err.Error())
			continue
		}
		_ = s.st.Dequeue(e)
		uploaded = true
	}
	if uploaded {
		if entries, err = s.st.Pending(); err != nil {
			return err
		}
	}
	batch := icloud.Batch{Format: icloud.Format, Device: me, Seq: s.intValue("icloud.seq") + 1, WrittenAt: cloudNow().UTC()}
	type sent struct {
		e       store.OutboxEntry
		version store.RowVersion
		row     bool
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
			continue // failed above; tried again next pass
		case syncedRows[e.Entity]:
			entry, err := s.rowEntry(me, e)
			if err != nil {
				log.Printf("icloud: %s %s: %v", e.Entity, e.EntityID, err)
				_ = s.st.MarkFailed(e, err.Error())
				continue
			}
			batch.Entries = append(batch.Entries, entry)
			done = append(done, sent{e: e, version: store.RowVersion{At: entry.At, Device: me}, row: true})
		case e.Entity == "page_content":
			entry, published, err := s.publishContent(me, e.EntityID)
			if err != nil {
				log.Printf("icloud: page %s: %v", e.EntityID, err)
				_ = s.st.MarkFailed(e, err.Error())
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
		if s.ICloud.Exists(rel) {
			return errors.New("icloud: another device wrote this device's next batch")
		}
		if err := s.ICloud.WriteJSON(rel, batch); err != nil {
			return err
		}
		if err := s.setInt("icloud.seq", batch.Seq); err != nil {
			return err
		}
	}
	for _, d := range done {
		if d.row {
			if err := s.st.SetRowVersion(d.e.Entity, d.e.EntityID, d.version); err != nil {
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
	return nil
}

// rowEntry is a queued row as a batch has it: the row with its time, and
// for a comment its author's name, which no server here looks up. Its
// version is newer than any this device has seen of the row, so a change
// made here while another device's waited still wins over it.
func (s *Service) rowEntry(me string, e store.OutboxEntry) (icloud.Entry, error) {
	var row map[string]any
	if err := json.Unmarshal([]byte(e.Payload), &row); err != nil {
		return icloud.Entry{}, err
	}
	changed, err := s.st.RowUpdatedAt(e.Entity, e.EntityID)
	if err != nil {
		return icloud.Entry{}, err
	}
	at := changed.UnixMilli()
	if changed.IsZero() {
		at = cloudNow().UnixMilli()
	}
	at = max(at, s.st.RowVersion(e.Entity, e.EntityID).At+1)
	row["updated_at"] = time.UnixMilli(at).UTC().Format(time.RFC3339Nano)
	if e.Entity == "comments" {
		if c, err := s.st.Comment(e.EntityID); err == nil {
			row["author_name"] = c.AuthorName
		}
	}
	return icloud.Entry{Kind: "row", Table: e.Entity, ID: e.EntityID, At: at, By: me, Row: row}, nil
}

// publishContent writes this device's version of a page to its page file,
// and returns the batch entry that tells the other devices, with the
// version published, nil when it was already. The version counts as
// published once the batch is written: until then it goes again.
func (s *Service) publishContent(me, pageID string) (icloud.Entry, *store.PeerContent, error) {
	if _, err := s.st.Page(pageID); err != nil {
		return icloud.Entry{}, nil, nil
	}
	row, err := s.st.ContentRow(pageID)
	if err != nil {
		return icloud.Entry{}, nil, nil
	}
	own, _ := s.st.PeerContent(me, pageID)
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
	// After the version last written, whether or not its batch was.
	written := own.Version
	rel, err := icloud.PagePath(me, pageID)
	if err != nil {
		return icloud.Entry{}, nil, err
	}
	var last icloud.PageFile
	if s.ICloud.ReadJSON(rel, &last) == nil {
		written = max(written, last.Version)
	}
	version := max(cloudNow().UnixMilli(), written+1)
	file := icloud.PageFile{Format: icloud.Format, PageID: pageID, Version: version, Merged: merged, Content: json.RawMessage(row.Content)}
	if err := s.ICloud.WriteJSON(rel, file); err != nil {
		return icloud.Entry{}, nil, err
	}
	return icloud.Entry{Kind: "content", PageID: pageID, Version: version},
		&store.PeerContent{Peer: me, Version: version, Content: row.Content}, nil
}

// cloudUpload copies a file or a recording's audio into the folder.
func (s *Service) cloudUpload(e store.OutboxEntry) error {
	var payload map[string]any
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return err
	}
	local := store.S(payload, "local_path")
	if local == "" {
		return nil
	}
	if _, err := os.Stat(local); err != nil {
		return nil // gone: nothing to upload
	}
	if recID := store.S(payload, "recording_id"); recID != "" {
		rel, err := icloud.RecordingBlobPath(recID)
		if err != nil {
			return err
		}
		if err := s.ICloud.CopyIn(rel, local); err != nil {
			return err
		}
		if _, err := s.st.UpdateRecording(recID, func(r *store.Recording) {
			r.AudioPath, r.UploadedAt = recID+".wav", cloudNow()
		}); err != nil {
			return nil // the recording went meanwhile
		}
		if s.KeepAudio != nil && s.KeepAudio() == "until_uploaded" {
			// The page plays it from iCloud Drive now.
			_ = os.Remove(local)
			_ = s.st.ClearLocalPath(recID)
		}
		return nil
	}
	fileID := strings.TrimPrefix(e.EntityID, "files/")
	rel, err := icloud.FileBlobPath(fileID, path.Base(store.S(payload, "path")))
	if err != nil {
		return err
	}
	return s.ICloud.CopyIn(rel, local)
}

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

// keptInICloud reports whether the Leaflet folder of iCloud Drive this
// device synced through has a file or audio. It looks only once this
// device has joined one, so it asks the system for no access before.
func (s *Service) keptInICloud(bucket, storagePath string) bool {
	if joined, _ := s.st.Value("icloud.space"); joined == "" || s.ICloud.Root == "" {
		return false
	}
	rel, err := blobPath(bucket, storagePath)
	return err == nil && s.ICloud.Exists(rel)
}

// cloudFetch reads what a device copied into the folder, waiting while
// iCloud downloads it.
func (s *Service) cloudFetch(ctx context.Context, bucket, storagePath string) ([]byte, error) {
	rel, err := blobPath(bucket, storagePath)
	if err != nil {
		return nil, err
	}
	for {
		data, err := s.ICloud.Read(rel)
		if !errors.Is(err, icloud.ErrNotReady) {
			return data, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// devices are the other devices that write to the folder.
func (s *Service) devices(me string) ([]string, error) {
	all, err := s.ICloud.Devices()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(d string) bool { return d == me }), nil
}
