// Package sync moves changes between this device and the others: through
// Supabase, or through the Leaflet folder in iCloud Drive (docs/protocol.md
// §6, §8). It drains the outbox, pulls rows changed since the last pull,
// and merges page content per block (§1).
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"leaflet/internal/doc"
	"leaflet/internal/icloud"
	"leaflet/internal/remote"
	"leaflet/internal/store"
)

// Service is the sync loop.
type Service struct {
	st *store.Store
	be *remote.Client

	// OnState runs as a pass starts and ends, with its error.
	OnState func(running bool, err error)
	// OnPageContent runs after a page's stored content changed from the
	// server.
	OnPageContent func(pageID string)
	// Offline reports whether the user chose to work offline.
	Offline func() bool
	// KeepAudio is the setting for audio after upload.
	KeepAudio func() string
	// ICloud is the Leaflet folder in iCloud Drive, which sync goes
	// through instead of Supabase while SetICloud has turned it on.
	ICloud icloud.Folder

	cloudOn atomic.Bool
	kick    chan struct{}
	mu      gosync.Mutex
	running gosync.Mutex
	open    map[string]bool
	started bool
}

// New returns the sync service.
func New(st *store.Store, be *remote.Client) *Service {
	return &Service{st: st, be: be, kick: make(chan struct{}, 1), open: map[string]bool{}}
}

// Start runs the loop: soon after changes, when asked, and every 30 seconds.
func (s *Service) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		s.Kick()
		for {
			select {
			case <-s.kick:
				time.Sleep(1200 * time.Millisecond)
			case <-tick.C:
			}
			_ = s.SyncNow()
		}
	}()
}

// Kick asks for a pass soon.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// target is where a pass syncs: the Supabase server, or the Leaflet folder
// in iCloud Drive.
type target interface {
	// ready reports whether there is somewhere to sync: signed in, or
	// iCloud sync on.
	ready() bool
	needsNetwork() bool
	pass(ctx context.Context) error
	refresh(ctx context.Context, pageID string) (*doc.Content, error)
	flush(ctx context.Context, pageID string) error
	// fetch reads a file or a recording's audio another device uploaded.
	fetch(ctx context.Context, bucket, path string) ([]byte, error)
}

func (s *Service) target() target {
	if s.cloudOn.Load() {
		return cloudTarget{s}
	}
	return supabaseTarget{s}
}

type supabaseTarget struct{ s *Service }

func (t supabaseTarget) ready() bool                    { return t.s.be.SignedIn() }
func (t supabaseTarget) needsNetwork() bool             { return true }
func (t supabaseTarget) pass(ctx context.Context) error { return t.s.pass(ctx) }
func (t supabaseTarget) refresh(ctx context.Context, pageID string) (*doc.Content, error) {
	return t.s.refreshRemote(ctx, pageID)
}
func (t supabaseTarget) flush(ctx context.Context, pageID string) error {
	return t.s.flushRemote(ctx, pageID)
}
func (t supabaseTarget) fetch(ctx context.Context, bucket, path string) ([]byte, error) {
	return t.s.be.Download(ctx, bucket, path)
}

// SetICloud turns iCloud sync on or off; a pass running goes on as it
// started. Turned off, the changes made meanwhile wait in the outbox, and
// go once it is on again.
func (s *Service) SetICloud(on bool) {
	s.cloudOn.Store(on)
	if on {
		s.Kick()
	}
}

// ICloudOn reports whether iCloud sync is on.
func (s *Service) ICloudOn() bool { return s.cloudOn.Load() }

// Uploads reports whether files and audio go up to another place for the
// other devices: signed in, or iCloud sync on.
func (s *Service) Uploads() bool { return s.target().ready() }

// CanFetch reports whether Fetch has somewhere to get what another device
// uploaded from: signed in, iCloud sync on, or the Leaflet folder of
// iCloud Drive this device synced through before.
func (s *Service) CanFetch() bool {
	if s.target().ready() {
		return true
	}
	joined, _ := s.st.Value("icloud.space")
	return joined != "" && s.ICloud.Root != ""
}

// Fetch reads a file (bucket "files") or a recording's audio
// ("recordings") that another device uploaded, by its storage path. What
// went up to iCloud Drive stays there after switching to the server,
// until it has gone up again.
func (s *Service) Fetch(ctx context.Context, bucket, path string) ([]byte, error) {
	t := s.target()
	if _, cloud := t.(cloudTarget); cloud {
		return t.fetch(ctx, bucket, path)
	}
	if t.ready() {
		data, err := t.fetch(ctx, bucket, path)
		if err == nil || !s.keptInICloud(bucket, path) {
			return data, err
		}
	}
	if s.keptInICloud(bucket, path) {
		return s.cloudFetch(ctx, bucket, path)
	}
	return nil, errors.New("sync: not signed in, and not in iCloud Drive")
}

// SetOpen marks a page as open, so its content is always pulled, and
// brings an opened page up to date in the background.
func (s *Service) SetOpen(pageID string, open bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !open {
		delete(s.open, pageID)
		return
	}
	s.open[pageID] = true
	go func() {
		if !s.CanSync() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s.running.Lock()
		defer s.running.Unlock()
		if _, err := s.RefreshContent(ctx, pageID); err != nil {
			log.Printf("open page %s: %v", pageID, err)
		}
	}()
}

// CanSync reports whether a pass can run: signed in and online, or with
// iCloud sync on, and not working offline. iCloud Drive keeps what Leaflet
// writes to it until it is online itself.
func (s *Service) CanSync() bool {
	t := s.target()
	if !t.ready() {
		return false
	}
	if s.Offline != nil && s.Offline() {
		return false
	}
	return !t.needsNetwork() || online()
}

// online reports whether the device has a connection; tests stand in
// for the system's answer.
var online = func() bool { return mygo.Network.Status().Online }

// Hold runs fn between passes, so no pass sees its changes half made.
func (s *Service) Hold(fn func()) {
	s.running.Lock()
	defer s.running.Unlock()
	fn()
}

// SyncNow runs one pass, after the one in progress.
func (s *Service) SyncNow() error {
	if !s.CanSync() {
		return nil
	}
	s.running.Lock()
	defer s.running.Unlock()
	if s.OnState != nil {
		s.OnState(true, nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t := s.target()
	err := t.pass(ctx)
	if t.needsNetwork() && remote.IsNetwork(err) {
		err = nil // offline: try again later
	}
	if err != nil && !errors.Is(err, remote.ErrSignedOut) {
		log.Printf("sync: %v", err)
	}
	if s.OnState != nil {
		s.OnState(false, err)
	}
	return err
}

func (s *Service) pass(ctx context.Context) error {
	if err := s.resolveSpace(ctx); err != nil {
		return err
	}
	if err := s.heartbeat(ctx); err != nil {
		return err
	}
	if err := s.push(ctx); err != nil {
		return err
	}
	return s.pull(ctx)
}

// resolveSpace, after sign-in, moves the pages made on this device into
// the account's space and queues them for upload. Pages are only ever
// adopted from the device's own space: another account's are forgotten
// before signing in (store.LoadSession).
func (s *Service) resolveSpace(ctx context.Context) error {
	uid := s.be.UserID()
	if acct := s.st.CachedAccount(); acct != "" && acct != uid {
		return errors.New("this device still keeps another account's pages")
	}
	if _, ok := s.st.Value("space." + uid); ok {
		return nil
	}
	space, err := s.be.SelectOne(ctx, "spaces", remote.Query{Filters: []string{remote.Eq("owner_id", uid)}})
	if err != nil {
		return err
	}
	if space == nil {
		rows, err := s.be.Insert(ctx, "spaces", map[string]any{"name": "My space", "owner_id": uid})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.New("the server made no space")
		}
		space = rows[0]
	}
	remoteID := store.S(space, "id")
	name := store.S(space, "name")
	if name == "" {
		name = "My space"
	}
	if err := s.st.PutSpace(remoteID, name, uid); err != nil {
		return err
	}
	if local, ok := s.st.Value("space.local"); ok && local != remoteID {
		if err := s.st.MoveSpace(local, remoteID); err != nil {
			return err
		}
	}
	if err := s.st.SetValue("space.local", remoteID); err != nil {
		return err
	}
	if err := s.st.SetValue("space."+uid, remoteID); err != nil {
		return err
	}
	if err := s.st.SetValue("account", uid); err != nil {
		return err
	}
	// Everything made before sign-in goes up now.
	failed := s.requeueAll(remoteID)
	sess := s.st.Session()
	sess.SpaceID, sess.UserID, sess.SignedIn = remoteID, uid, true
	s.st.SetSession(sess)
	return failed
}

// requeueAll queues everything on this device to go up, now in the space
// spaceID: the pages and their content, folders, comments, files and
// recordings with their audio. A file or audio this device has only in
// iCloud Drive, as after syncing through it, comes here first, to go up
// from here.
func (s *Service) requeueAll(spaceID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var failed error
	pages, _ := s.st.AllPages()
	for _, p := range pages {
		failed = errors.Join(failed, s.st.Queue("pages", p.ID, store.PageToRemote(p), "", p.ID))
		failed = errors.Join(failed, s.st.Queue("page_content", p.ID, map[string]any{"page_id": p.ID}, "page from this device", p.ID))
	}
	folders, _ := s.st.Folders()
	for _, f := range folders {
		failed = errors.Join(failed, s.st.Queue("folders", f.ID, store.FolderToRemote(f), "", ""))
	}
	comments, _ := s.st.AllComments()
	for _, c := range comments {
		failed = errors.Join(failed, s.st.Queue("comments", c.ID, store.CommentToRemote(c), "", c.PageID))
	}
	files, _ := s.st.Files()
	for _, f := range files {
		if !onDevice(f.LocalPath) && f.StoragePath != "" && s.keptInICloud("files", f.StoragePath) {
			if data, err := s.cloudFetch(ctx, "files", f.StoragePath); err == nil {
				if local, err := s.st.DownloadedFile(f.ID, data); err == nil {
					f.LocalPath = local
				}
			}
		}
		f.SpaceID = spaceID
		f.StoragePath = spaceID + "/" + f.ID + "/" + f.Name
		failed = errors.Join(failed, s.st.PutFile(f))
		failed = errors.Join(failed, s.st.Queue("files", f.ID, store.FileToRemote(f), "file", ""))
		if onDevice(f.LocalPath) {
			ct := f.MimeType
			if ct == "" {
				ct = "application/octet-stream"
			}
			failed = errors.Join(failed, s.st.Queue("storage", "files/"+f.ID, map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": f.LocalPath, "content_type": ct}, "file waiting to upload", ""))
		}
	}
	// A finished recording goes up with its audio, which keeps the path
	// it has until the upload gives it another.
	recordings, _ := s.st.Recordings()
	for _, r := range recordings {
		if r.Status != "ready" && r.Status != "failed" {
			continue
		}
		if !onDevice(r.LocalPath) && r.AudioPath != "" && s.keptInICloud("recordings", r.AudioPath) {
			if data, err := s.cloudFetch(ctx, "recordings", r.AudioPath); err == nil {
				local := filepath.Join(s.st.Dir, "recordings", r.ID+".wav")
				if err := os.MkdirAll(filepath.Dir(local), 0o750); err == nil && os.WriteFile(local, data, 0o600) == nil {
					if rec, err := s.st.UpdateRecording(r.ID, func(x *store.Recording) { x.LocalPath = local }); err == nil {
						r = rec
					}
				}
			}
		}
		failed = errors.Join(failed, s.st.Queue("recordings", r.ID, store.RecordingToRemote(r), "", r.PageID))
		if onDevice(r.LocalPath) {
			failed = errors.Join(failed, s.st.QueueUpload(r))
		}
	}
	return failed
}

// onDevice reports whether a file is on this device.
func onDevice(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// DeviceName names this device in Account › Devices.
func DeviceName() string {
	host, _ := os.Hostname()
	switch runtime.GOOS {
	case "darwin":
		return "Mac · Desktop app" + suffix(host)
	case "windows":
		return "Windows PC · Desktop app" + suffix(host)
	case "linux":
		return "Linux · Desktop app" + suffix(host)
	}
	return "Device"
}

func suffix(host string) string {
	if host == "" {
		return ""
	}
	return " · " + host
}

func (s *Service) heartbeat(ctx context.Context) error {
	id := s.st.DeviceID()
	row, err := s.be.SelectOne(ctx, "devices", remote.Query{Select: "revoked_at", Filters: []string{remote.Eq("id", id)}})
	if err != nil {
		return err
	}
	if row != nil && row["revoked_at"] != nil {
		_ = s.st.SetValue("device.revoked", "true")
		s.be.SignOut()
		return remote.ErrSignedOut
	}
	_, err = s.be.Upsert(ctx, "devices", map[string]any{
		"id": id, "user_id": s.be.UserID(), "name": DeviceName(), "platform": runtime.GOOS,
		"last_active_at": time.Now().UTC().Format(time.RFC3339Nano),
	}, "")
	return err
}

func (s *Service) push(ctx context.Context) error {
	entries, err := s.st.Pending()
	if err != nil {
		return err
	}
	uid := s.be.UserID()
	for _, e := range entries {
		if !s.CanSync() {
			return nil
		}
		done, err := s.pushOne(ctx, uid, e)
		if err != nil {
			if remote.IsNetwork(err) || errors.Is(err, remote.ErrSignedOut) {
				return err
			}
			log.Printf("outbox %s/%s: %v", e.Entity, e.EntityID, err)
			_ = s.st.MarkFailed(e, err.Error())
			continue
		}
		if done {
			_ = s.st.Dequeue(e)
		}
	}
	return nil
}

// pushOne sends one entry and reports whether it can leave the queue.
func (s *Service) pushOne(ctx context.Context, uid string, e store.OutboxEntry) (bool, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		return false, err
	}
	switch e.Entity {
	case "page_content":
		return s.pushContent(ctx, e.EntityID)
	case "storage":
		return s.pushFile(ctx, uid, payload)
	case "pages":
		if payload["created_by"] == nil {
			payload["created_by"] = uid
		}
	case "comments":
		return s.pushComment(ctx, uid, payload)
	case "page_history":
		payload["created_by"] = uid
	case "ai_jobs":
		payload["user_id"] = uid
	}
	_, err := s.be.Upsert(ctx, e.Entity, payload, "")
	return err == nil, err
}

// pushComment updates a comment the server has with what may change, and
// inserts one it has not. An upsert would not do: its insert stamps the
// caller as the author, which the server then refuses as a change to the
// comment, so resolving someone else's comment never went up.
func (s *Service) pushComment(ctx context.Context, uid string, payload map[string]any) (bool, error) {
	changes := map[string]any{"resolved": payload["resolved"]}
	if author, _ := payload["author_id"].(string); author == "" || author == uid {
		for _, k := range []string{"body", "quote", "agent_name", "deleted_at"} {
			changes[k] = payload[k]
		}
	} else if payload["deleted_at"] != nil {
		changes["deleted_at"] = payload["deleted_at"]
	}
	id, _ := payload["id"].(string)
	rows, err := s.be.Update(ctx, "comments", []string{remote.Eq("id", id)}, changes)
	if err != nil || len(rows) > 0 {
		return err == nil, err
	}
	// The server stamps the author on insert (for agent replies, the user
	// who asked).
	if payload["author_id"] == nil {
		payload["author_id"] = uid
	}
	if _, err := s.be.Insert(ctx, "comments", payload); err != nil {
		if remote.IsConflict(err) {
			return false, errors.New("the comment is someone else's, and you cannot change it")
		}
		return false, err
	}
	return true, nil
}

// errStale means someone saved the page's content between reading it and
// saving over it.
var errStale = errors.New("the page changed on the server while saving")

func (s *Service) pushContent(ctx context.Context, pageID string) (bool, error) {
	if _, err := s.st.Page(pageID); err != nil {
		return true, nil
	}
	// Another device or an agent may save in between: merge with what it
	// saved and try again.
	for range 3 {
		done, err := s.saveContent(ctx, pageID)
		if !errors.Is(err, errStale) {
			return done, err
		}
	}
	return false, errors.New("the page kept changing on the server, or you cannot edit it")
}

// saveContent merges this device's content with the server's and saves the
// result, if the server still has the revision it merged with.
func (s *Service) saveContent(ctx context.Context, pageID string) (bool, error) {
	row, err := s.st.ContentRow(pageID)
	if err != nil {
		return true, nil
	}
	local := doc.MustDecode(row.Content)
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: "content, revision, updated_at", Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return false, err
	}
	merged := local
	var conflicts []doc.Conflict
	if remoteRow != nil && behind(row, remoteRow) {
		var base *doc.Content
		if row.BaseContent != "" {
			base = doc.MustDecode(row.BaseContent)
		}
		merged, conflicts = doc.Merge(base, local, contentOf(remoteRow["content"]))
	}
	values := map[string]any{"content": json.RawMessage(merged.Encode()), "plain_text": merged.PlainText()}
	var saved []map[string]any
	if remoteRow == nil {
		values["page_id"] = pageID
		saved, err = s.be.Insert(ctx, "page_content", values)
		if remote.IsConflict(err) {
			return false, errStale
		}
	} else {
		saved, err = s.be.Update(ctx, "page_content", []string{
			remote.Eq("page_id", pageID), remote.Eq("revision", fmt.Sprint(store.Int(remoteRow, "revision"))),
		}, values)
		if err == nil && len(saved) == 0 {
			return false, errStale
		}
	}
	if err != nil {
		return false, err
	}
	savedAt, savedRev := time.Now(), int64(0)
	if len(saved) > 0 {
		savedAt, savedRev = store.Time(saved[0]["updated_at"]), store.Int(saved[0], "revision")
	}
	// The user may have kept typing while this was in flight, or type while
	// this merges: store the merge only over the content it merged.
	var now store.ContentRow
	next := merged
	for tries := 1; ; tries++ {
		if now, err = s.st.ContentRow(pageID); err != nil {
			break
		}
		next = merged
		if now.Content != row.Content {
			next, _ = doc.Merge(local, doc.MustDecode(now.Content), merged)
		}
		beforeStore(pageID)
		err = s.st.PushedContent(pageID, now.Content, next.Encode(), next.PlainText(), merged.Encode(), savedAt, savedRev)
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrContentChanged) {
			return false, err
		}
		if tries == 3 {
			return false, errors.New("the page kept changing here while it synced")
		}
	}
	typedMeanwhile := now.Content != "" && now.Content != row.Content
	current := now.Content
	if current == "" {
		current = row.Content
	}
	if next.Encode() != current {
		s.st.Notify("page_contents", pageID)
		if s.OnPageContent != nil {
			s.OnPageContent(pageID)
		}
	}
	for _, c := range conflicts {
		_ = s.st.AddConflict(pageID, c)
	}
	return !typedMeanwhile, nil
}

// behind reports whether the server has content this device has not
// merged: it never synced the page, or the server has a revision it has
// not seen.
func behind(row store.ContentRow, remoteRow map[string]any) bool {
	return row.RemoteUpdatedAt.IsZero() || store.Int(remoteRow, "revision") != row.RemoteRevision
}

func contentOf(v any) *doc.Content {
	raw, _ := json.Marshal(v)
	c, err := doc.Decode(string(raw))
	if err != nil {
		return doc.Empty()
	}
	return c
}

func (s *Service) pushFile(ctx context.Context, uid string, payload map[string]any) (bool, error) {
	local, _ := payload["local_path"].(string)
	if local == "" {
		return true, nil
	}
	f, err := os.Open(local) // #nosec G304 -- a file in the app's own data folder.
	if err != nil {
		return true, nil // gone: nothing to upload
	}
	defer func() { _ = f.Close() }()
	bucket, _ := payload["bucket"].(string)
	recID, _ := payload["recording_id"].(string)
	path, _ := payload["path"].(string)
	if path == "" {
		path = uid + "/" + recID + ".wav"
	}
	ct, _ := payload["content_type"].(string)
	if err := s.be.Upload(ctx, bucket, path, f, ct); err != nil {
		return false, err
	}
	if recID != "" {
		rec, err := s.st.UpdateRecording(recID, func(r *store.Recording) {
			r.AudioPath, r.UploadedAt = path, time.Now()
		})
		if err == nil {
			_, _ = s.be.Upsert(ctx, "recordings", store.RecordingToRemote(rec), "")
			if s.KeepAudio != nil && s.KeepAudio() == "until_uploaded" {
				// The page plays it from Storage now.
				_ = f.Close()
				_ = os.Remove(local)
				_ = s.st.ClearLocalPath(recID)
			}
		}
	}
	return true, nil
}

// cursor is where a table's pull resumes: the last row stored here, by
// its time and key.
type cursor struct{ at, key string }

func (s *Service) cursor(table string) cursor {
	v, ok := s.st.Value("sync.cursor." + table)
	if !ok {
		return cursor{at: "1970-01-01T00:00:00Z"}
	}
	at, key, _ := strings.Cut(v, "|")
	return cursor{at, key}
}

func (s *Service) setCursor(table string, c cursor) {
	_ = s.st.SetValue("sync.cursor."+table, c.at+"|"+c.key)
}

// changed reads up to limit rows changed after the cursor, oldest first.
// Rows of the same time come in key order, so a pull that stops among
// them resumes at the next one.
func (s *Service) changed(ctx context.Context, table, key, columns string, cur cursor, limit int, filters ...string) ([]map[string]any, error) {
	after := remote.Gt("updated_at", cur.at)
	if cur.key != "" {
		after = "or=(updated_at.gt." + quote(cur.at) + ",and(updated_at.eq." + quote(cur.at) + "," + key + ".gt." + quote(cur.key) + "))"
	}
	return s.be.Select(ctx, table, remote.Query{Select: columns, Filters: append([]string{after}, filters...), Order: "updated_at," + key, Limit: limit})
}

// quote makes a value safe inside a PostgREST or=(…) filter.
func quote(v string) string { return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"` }

// store applies pulled rows in order and moves the cursor past each one
// stored. It stops at the first that cannot be stored, so the next pull
// starts with it again. A row with a change waiting here is passed over:
// the change wins and goes up on the next push.
func (s *Service) store(table, key string, rows []map[string]any, apply func(map[string]any) error) {
	cur := s.cursor(table)
	start := cur
	for _, r := range rows {
		if !s.st.HasPending(table, store.S(r, key)) {
			if err := apply(r); err != nil {
				log.Printf("pull %s %s: %v", table, store.S(r, key), err)
				break
			}
		}
		cur = cursor{store.S(r, "updated_at"), store.S(r, key)}
	}
	if cur != start {
		s.setCursor(table, cur)
		s.st.Notify(table, "")
	}
}

func (s *Service) pullTable(ctx context.Context, table, columns string, apply func(map[string]any) error) error {
	rows, err := s.changed(ctx, table, "id", columns, s.cursor(table), 500)
	if err != nil {
		return err
	}
	s.store(table, "id", rows, apply)
	return nil
}

func (s *Service) pull(ctx context.Context) error {
	steps := []struct {
		table, cols string
		apply       func(map[string]any) error
	}{
		{"pages", "*", s.st.PutRemotePage},
		{"folders", "*", s.st.PutRemoteFolder},
		{"recordings", "*", s.st.PutRemoteRecording},
		{"files", "*", s.st.PutRemoteFile},
		{"model_providers", "id,preset,name,base_url,auth_kind,has_secret,default_model,is_local,supports_tools,models,expires_at,created_at,updated_at",
			func(r map[string]any) error {
				if b, _ := r["is_local"].(bool); b {
					return nil
				}
				return s.st.PutRemoteProvider(r)
			}},
	}
	for _, step := range steps {
		if err := s.pullTable(ctx, step.table, step.cols, step.apply); err != nil {
			return err
		}
	}
	if err := s.pullShared(ctx); err != nil {
		return err
	}
	if err := s.pullComments(ctx); err != nil {
		return err
	}
	return s.pullContents(ctx)
}

// pullShared brings the pages newly shared with the user, with the pages
// under them and what they hold. Sharing changes no page, so the pulls
// that go by time pass over a page that last changed before them.
func (s *Service) pullShared(ctx context.Context) error {
	rows, err := s.changed(ctx, "page_permissions", "page_id", "page_id, updated_at", s.cursor("page_permissions"), 100,
		remote.Eq("user_id", s.be.UserID()))
	if err != nil {
		return err
	}
	var failed error
	s.store("page_permissions", "page_id", rows, func(r map[string]any) error {
		failed = s.pullTree(ctx, store.S(r, "page_id"))
		return failed
	})
	return failed
}

// pullTree stores a page and the pages under it, their comments, files
// and recordings, and has their content fetched.
func (s *Service) pullTree(ctx context.Context, rootID string) error {
	var ids []string
	filter := remote.Eq("id", rootID)
	for depth := 0; depth < 64; depth++ {
		rows, err := s.be.Select(ctx, "pages", remote.Query{Select: "*", Filters: []string{filter}})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		var level []string
		for _, r := range rows {
			if err := s.st.PutRemotePage(r); err != nil {
				return err
			}
			level = append(level, store.S(r, "id"))
		}
		ids = append(ids, level...)
		filter = remote.In("parent_id", level)
	}
	if len(ids) == 0 {
		return nil
	}
	s.st.Notify("pages", "")
	inPages := []string{remote.In("page_id", ids)}
	comments, err := s.be.Select(ctx, "comments", remote.Query{Select: "*", Filters: inPages})
	if err != nil {
		return err
	}
	names := s.authorNames(ctx, comments)
	for _, r := range comments {
		if err := s.st.PutRemoteComment(r, names[store.S(r, "author_id")]); err != nil {
			return err
		}
	}
	for _, step := range []struct {
		table string
		apply func(map[string]any) error
	}{{"files", s.st.PutRemoteFile}, {"recordings", s.st.PutRemoteRecording}} {
		rows, err := s.be.Select(ctx, step.table, remote.Query{Select: "*", Filters: inPages})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := step.apply(r); err != nil {
				return err
			}
		}
		s.st.Notify(step.table, "")
	}
	s.st.Notify("comments", "")
	waiting := s.waiting()
	for _, id := range ids {
		waiting[id] = true
	}
	s.setWaiting(waiting)
	return nil
}

func (s *Service) pullComments(ctx context.Context) error {
	rows, err := s.changed(ctx, "comments", "id", "*", s.cursor("comments"), 500)
	if err != nil || len(rows) == 0 {
		return err
	}
	names := s.authorNames(ctx, rows)
	s.store("comments", "id", rows, func(r map[string]any) error {
		return s.st.PutRemoteComment(r, names[store.S(r, "author_id")])
	})
	return nil
}

// authorNames are the names of comments' authors, by their IDs, with
// "Someone" for one without a name.
func (s *Service) authorNames(ctx context.Context, comments []map[string]any) map[string]string {
	ids := map[string]bool{}
	for _, r := range comments {
		ids[store.S(r, "author_id")] = true
	}
	names := map[string]string{}
	var list []string
	for id := range ids {
		names[id] = "Someone"
		if id != "" {
			list = append(list, id)
		}
	}
	if len(list) > 0 {
		profiles, err := s.be.Select(ctx, "profiles", remote.Query{Select: "id, display_name", Filters: []string{remote.In("id", list)}})
		if err == nil {
			for _, p := range profiles {
				if n := store.S(p, "display_name"); n != "" {
					names[store.S(p, "id")] = n
				}
			}
		}
	}
	return names
}

func (s *Service) pullContents(ctx context.Context) error {
	rows, err := s.changed(ctx, "page_content", "page_id", "page_id, updated_at", s.cursor("page_content"), 200)
	if err != nil {
		return err
	}
	allOffline := s.st.Settings().AllPagesOffline
	// refresh fetches a page's content if this device keeps it. A page
	// not here yet waits for its row, which a later pull brings.
	waiting := s.waiting()
	refresh := func(id string) error {
		p, err := s.st.Page(id)
		if err != nil {
			waiting[id] = true
			return nil
		}
		delete(waiting, id)
		s.mu.Lock()
		open := s.open[id]
		s.mu.Unlock()
		if open || allOffline || p.KeepOffline {
			if _, err := s.refreshRemote(ctx, id); err != nil {
				waiting[id] = true
				return err
			}
		}
		return nil
	}
	var failed error
	for id := range waiting {
		if failed = refresh(id); failed != nil {
			break
		}
	}
	cur := s.cursor("page_content")
	start := cur
	for _, r := range rows {
		if failed != nil {
			break
		}
		id := store.S(r, "page_id")
		if failed = refresh(id); failed != nil {
			break
		}
		cur = cursor{store.S(r, "updated_at"), id}
	}
	if cur != start {
		s.setCursor("page_content", cur)
	}
	s.setWaiting(waiting)
	return failed
}

// waiting are the pages whose content changed before their own row came,
// so their content is fetched once it has.
func (s *Service) waiting() map[string]bool {
	out := map[string]bool{}
	if v, ok := s.st.Value("sync.content_waiting"); ok && v != "" {
		for _, id := range strings.Split(v, ",") {
			out[id] = true
		}
	}
	return out
}

func (s *Service) setWaiting(ids map[string]bool) {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	slices.Sort(list)
	_ = s.st.SetValue("sync.content_waiting", strings.Join(list, ","))
}

// RefreshContent brings a page's content up to date with the other
// devices', merging unsynced edits made here.
func (s *Service) RefreshContent(ctx context.Context, pageID string) (*doc.Content, error) {
	if !s.CanSync() {
		return s.st.Content(pageID), nil
	}
	return s.target().refresh(ctx, pageID)
}

// refreshRemote brings a page's content up to date with the server's.
func (s *Service) refreshRemote(ctx context.Context, pageID string) (*doc.Content, error) {
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: "content, revision, updated_at", Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return nil, err
	}
	if remoteRow == nil {
		return s.st.Content(pageID), nil
	}
	remoteAt := store.Time(remoteRow["updated_at"])
	remoteContent := contentOf(remoteRow["content"])
	// The user or an agent may save while this merges: store the merge
	// only over the content it merged, and merge again otherwise.
	for range 3 {
		local, localErr := s.st.ContentRow(pageID)
		if localErr == nil && !behind(local, remoteRow) {
			return doc.MustDecode(local.Content), nil
		}
		next := remoteContent
		var conflicts []doc.Conflict
		if localErr == nil && s.st.HasPending("page_content", pageID) {
			var base *doc.Content
			if local.BaseContent != "" {
				base = doc.MustDecode(local.BaseContent)
			}
			next, conflicts = doc.Merge(base, doc.MustDecode(local.Content), remoteContent)
		}
		beforeStore(pageID)
		err := s.st.PutSyncedContentOver(pageID, local.Content, next, remoteContent.Encode(), remoteAt, store.Int(remoteRow, "revision"))
		if errors.Is(err, store.ErrContentChanged) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, c := range conflicts {
			if err := s.st.AddConflict(pageID, c); err != nil {
				return nil, err
			}
		}
		if localErr != nil || local.Content != next.Encode() {
			if s.OnPageContent != nil {
				s.OnPageContent(pageID)
			}
		}
		return next, nil
	}
	return nil, errors.New("the page kept changing here while it synced")
}

// beforeStore runs between merging a page's content and storing the merge;
// tests save there, standing in for the user typing.
var beforeStore = func(pageID string) {}

// FlushPage pushes a page's pending changes right away, so a cloud agent
// starts from what the user sees.
func (s *Service) FlushPage(ctx context.Context, pageID string) error {
	if !s.CanSync() {
		return nil
	}
	s.running.Lock()
	defer s.running.Unlock()
	return s.target().flush(ctx, pageID)
}

func (s *Service) flushRemote(ctx context.Context, pageID string) error {
	if err := s.resolveSpace(ctx); err != nil {
		return err
	}
	entries, err := s.st.Pending()
	if err != nil {
		return err
	}
	uid := s.be.UserID()
	for _, e := range entries {
		if (e.Entity == "pages" || e.Entity == "page_content") && e.EntityID == pageID {
			done, err := s.pushOne(ctx, uid, e)
			if err != nil {
				return err
			}
			if done {
				_ = s.st.Dequeue(e)
			}
		}
	}
	return nil
}

// Push sends one queued row right away, as a job must exist before the
// agent function runs it.
func (s *Service) Push(ctx context.Context, entity, id string) error {
	entries, err := s.st.Pending()
	if err != nil {
		return err
	}
	uid := s.be.UserID()
	for _, e := range entries {
		if e.Entity == entity && e.EntityID == id {
			done, err := s.pushOne(ctx, uid, e)
			if err != nil {
				return err
			}
			if done {
				_ = s.st.Dequeue(e)
			}
		}
	}
	return nil
}
