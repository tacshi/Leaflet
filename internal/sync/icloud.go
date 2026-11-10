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

const pageVersionKey = "icloud.page_version"

type cloudTarget struct{ s *Service }

func (t cloudTarget) ready() bool        { return t.s.folder().Root != "" }
func (t cloudTarget) needsNetwork() bool { return false }

func (t cloudTarget) pass(ctx context.Context, _ bool) error { return t.s.cloudPass(ctx) }
func (t cloudTarget) refresh(ctx context.Context, pageID string) (*doc.Content, error) {
	return t.s.cloudRefresh(ctx, pageID)
}

var cloudNow = time.Now

var compactAfter = 500

func (s *Service) intValue(key string) int64 {
	v, _ := s.st.Value(key)
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s *Service) setInt(key string, n int64) error {
	return s.st.SetValue(key, strconv.FormatInt(n, 10))
}

func (s *Service) EnableICloud() error {
	if err := s.folder().Check(true); err != nil {
		return err
	}
	if joined, ok := s.st.Value("icloud.space"); ok && joined != "" {
		space, losers, err := s.folder().ReadSpace()
		replaced := errors.Is(err, fs.ErrNotExist) || err == nil && space.SpaceID != joined
		for _, l := range losers {
			if c, err := s.folder().SpaceOf(l); err == nil && c.SpaceID == joined {
				replaced = false
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
	if err := s.folder().Check(joined == ""); err != nil {
		return err
	}
	if err := s.joinSpace(ctx, joined); err != nil {
		if errors.Is(err, icloud.ErrNotReady) {
			return nil
		}
		return err
	}
	if err := s.leaveServerSpace(); err != nil {
		return err
	}
	me, err := s.checkDeviceID(ctx)
	if errors.Is(err, icloud.ErrNotReady) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(s.cloudSteps(ctx, me), s.writeDevice(me, false))
}

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
			return fmt.Errorf("%w: %w", icloud.ErrNotReady, err)
		} else if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
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
	seqs, _, _ := s.folder().Batches(me)
	if len(seqs) > 0 {
		if err := s.setInt("icloud.seq", seqs[len(seqs)-1]); err != nil {
			return err
		}
	}
	return s.adopted(ctx, space.SpaceID, "icloud.space")
}

const loserKept = 30 * 24 * time.Hour

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

func (s *Service) checkDeviceID(ctx context.Context) (string, error) {
	me := s.st.DeviceID()
	seqs, copies, err := s.folder().Batches(me)
	if err != nil {
		return "", err
	}
	seq, _, err := s.st.Lookup("icloud.seq")
	if err != nil {
		return "", err
	}
	last, _ := strconv.ParseInt(seq, 10, 64)
	if !copies && len(seqs) > 0 && seqs[len(seqs)-1] == last+1 {
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

func (s *Service) checkOwnLog(ctx context.Context, me string) (bool, error) {
	seqs, _, err := s.folder().Batches(me)
	if err != nil || len(seqs) == 0 {
		return err == nil, err
	}
	seq := seqs[len(seqs)-1]
	if _, err := s.readBatch(me, seq); !errors.Is(err, icloud.ErrDamaged) {
		return err == nil || errors.Is(err, icloud.ErrNewerFormat), nil
	}
	log.Printf("icloud: batch %d of this device does not parse; writing it again and sending everything", seq)
	if err := s.writeBatch(me, icloud.Batch{Format: icloud.Format, Device: me, Seq: seq, WrittenAt: cloudNow().UTC()}); err != nil {
		return false, err
	}
	space, _ := s.st.Value("icloud.space")
	return true, s.requeueAll(ctx, space)
}

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

func (s *Service) cloudPush(ctx context.Context, me string) error {
	entries, err := s.st.Queued()
	if err != nil || len(entries) == 0 {
		return err
	}
	s.forgetRetries(entries)
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
		cols    []string
		content *store.PeerContent
	}
	var done []sent
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch {
		case e.Entity == "storage":
			continue
		case e.Entity == "storage_delete":
			continue
		case s.contentsWait(e):
			continue
		case !s.due(e):
			continue
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

func (s *Service) cloudRemove(entry *store.OutboxEntry) {
	payload, err := s.payload(entry)
	e := *entry
	if err != nil {
		s.failed(e, err)
		return
	}
	if payload == nil {
		return
	}
	if waits, stays := s.removal(payload); waits {
		return
	} else if stays {
		_ = s.st.Dequeue(e)
		return
	}
	rel, err := blobPath(store.S(payload, "bucket"), store.S(payload, "path"))
	if err != nil {
		_ = s.st.Dequeue(e)
		return
	}
	if err := s.folder().RemoveBlob(rel); err != nil {
		log.Printf("icloud: remove %s: %v", e.EntityID, err)
		s.failed(e, err)
		return
	}
	_ = s.st.Dequeue(e)
}

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
	_, settled := row[store.IfStatusKey]
	delete(row, store.IfStatusKey)
	var cols []string
	if list, ok := row[store.ChangedKey].([]any); ok {
		for _, c := range list {
			if k, ok := c.(string); ok {
				cols = append(cols, k)
			}
		}
	}
	delete(row, store.ChangedKey)
	delete(row, store.AdoptKey)
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

func (s *Service) publishContent(me, pageID string) (icloud.Entry, *store.PeerContent, error) {
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
		return icloud.Entry{}, nil, nil
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

func (s *Service) cloudUpload(ctx context.Context, entry *store.OutboxEntry) (bool, error) {
	payload, err := s.payload(entry)
	if err != nil || payload == nil {
		return false, err
	}
	e := *entry
	if other, err := s.uploadOfOtherSpace(e); err != nil || other {
		return err == nil, err
	}
	local, waits, err := s.uploadFrom(ctx, payload)
	if err != nil || waits || local == "" {
		return err == nil && !waits, err
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

func (s *Service) uploadOfOtherSpace(entry store.OutboxEntry) (bool, error) {
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

var errOtherSpace = errors.New("of another space")

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

var errNotInICloud = errors.New("not in iCloud Drive yet")

func (s *Service) joinedICloud() bool {
	joined, _ := s.st.Value("icloud.space")
	if joined == "" {
		joined, _ = s.st.Value(fetchSpaceKey)
	}
	return joined != "" && s.folder().Root != ""
}

const fetchSpaceKey = "icloud.fetch_space"

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

func (s *Service) keptInICloud(bucket, storagePath string) bool {
	if !s.joinedICloud() {
		return false
	}
	rel, err := blobPath(bucket, storagePath)
	return err == nil && s.folder().Exists(rel)
}

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

func (s *Service) cloudRead(_ context.Context, bucket, storagePath string) (io.ReadCloser, error) {
	rel, err := blobPath(bucket, storagePath)
	if err != nil {
		return nil, err
	}
	return s.folder().Open(rel)
}

func (s *Service) devices(me string) ([]string, error) {
	all, err := s.folder().Devices()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(d string) bool { return d == me }), nil
}
