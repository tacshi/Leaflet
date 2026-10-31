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
	"io/fs"
	"log"
	"net/http"
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
	// OnInvites runs when the pages shared with the user that wait for
	// them to accept (Invites) have changed.
	OnInvites func()

	cloudOn atomic.Bool
	kick    chan struct{}
	mu      gosync.Mutex
	running gosync.Mutex
	open    map[string]bool
	started bool
	// ownLogChecked is set once a pass has read this device's last batch
	// in iCloud Drive (checkOwnLog).
	ownLogChecked bool
	// invites are the account's invites (invitesOf) as the last pass
	// read them.
	invites   []Invite
	invitesOf string
	// membersAt is when the members of each page shared with the user
	// were last loaded, for pullMembers to load them again.
	membersAt map[string]time.Time
}

// New returns the sync service.
func New(st *store.Store, be *remote.Client) *Service {
	return &Service{st: st, be: be, kick: make(chan struct{}, 1), open: map[string]bool{}, membersAt: map[string]time.Time{}}
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
	if err := s.retryMoves(ctx); err != nil {
		log.Printf("sync: %v", err)
	}
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
	err := s.pull(ctx)
	s.readInvites(ctx)
	return err
}

// Invite is a page someone shared with the user, which reaches them once
// they accept it (docs/protocol.md §6).
type Invite struct {
	PageID string
	Title  string
	// Role is viewer or editor.
	Role string
	// Inviter is the name of who shared it, or "".
	Inviter string
	At      time.Time
}

// Invites returns the signed-in account's invites, newest first, as the
// last pass read them: none once signed out.
func (s *Service) Invites() []Invite {
	uid := s.be.UserID()
	s.mu.Lock()
	defer s.mu.Unlock()
	if uid == "" || s.invitesOf != uid {
		return nil
	}
	return slices.Clone(s.invites)
}

// readInvites reads the account's invites. One that cannot be read now
// leaves the list as it was.
func (s *Service) readInvites(ctx context.Context) {
	uid := s.be.UserID()
	res, err := s.be.RPC(ctx, "page_invites", map[string]any{})
	if err != nil {
		if !remote.IsNetwork(err) && !errors.Is(err, remote.ErrSignedOut) {
			log.Printf("sync: invites: %v", err)
		}
		return
	}
	var list []Invite
	rows, _ := res.([]any)
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if store.S(m, "page_id") == "" {
			continue
		}
		list = append(list, Invite{PageID: store.S(m, "page_id"), Title: store.S(m, "title"), Role: store.S(m, "role"),
			Inviter: store.S(m, "inviter_name"), At: store.TimeOrZero(m["created_at"]).UTC()})
	}
	s.setInvites(uid, list)
}

func (s *Service) setInvites(uid string, list []Invite) {
	s.mu.Lock()
	same := s.invitesOf == uid && slices.Equal(s.invites, list)
	s.invites, s.invitesOf = list, uid
	s.mu.Unlock()
	if !same && s.OnInvites != nil {
		s.OnInvites()
	}
}

// ClearInvites forgets the invites, as the account signs out.
func (s *Service) ClearInvites() { s.setInvites("", nil) }

// AcceptInvite accepts an invite: the page comes with the next pass,
// which it asks for.
func (s *Service) AcceptInvite(ctx context.Context, pageID string) error {
	if _, err := s.be.RPC(ctx, "accept_page_invite", map[string]any{"p_page_id": pageID}); err != nil {
		return err
	}
	s.dropInvite(pageID)
	s.Kick()
	return nil
}

// DeclineInvite declines an invite; the page never comes.
func (s *Service) DeclineInvite(ctx context.Context, pageID string) error {
	if _, err := s.be.RPC(ctx, "decline_page_invite", map[string]any{"p_page_id": pageID}); err != nil {
		return err
	}
	s.dropInvite(pageID)
	return nil
}

// dropInvite takes an invite answered off the list.
func (s *Service) dropInvite(pageID string) {
	s.mu.Lock()
	list := slices.DeleteFunc(slices.Clone(s.invites), func(i Invite) bool { return i.PageID == pageID })
	uid := s.invitesOf
	s.mu.Unlock()
	s.setInvites(uid, list)
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
	if err := s.st.SetValue("account", uid); err != nil {
		return err
	}
	// What is made from here on goes into the account's space: the
	// device's own is gone.
	sess := s.st.Session()
	sess.SpaceID, sess.UserID, sess.SignedIn = remoteID, uid, true
	s.st.SetSession(sess)
	// Everything made before sign-in goes up now. The space counts as
	// adopted only once it is all queued: a device that stops before then,
	// or could not queue it all, adopts it again on the next pass, so
	// nothing is left out of the queue that signing out checks.
	failed := s.requeueAll(remoteID)
	if errors.Is(failed, errNotRequeued) {
		return failed
	}
	return errors.Join(failed, s.st.SetValue("space."+uid, remoteID))
}

// requeueAll queues everything on this device to go up, now in the space
// spaceID: the pages and their content, folders, comments, files and
// recordings with their audio. A file or audio this device has only in
// iCloud Drive, as after syncing through it, comes here first, to go up
// from here; one that does not come yet waits, and retryMoves tries it
// again before each pass.
//
// What it could not read or queue it does all again before the next pass
// (requeueKey), so nothing is left behind for good.
func (s *Service) requeueAll(spaceID string) error {
	if err := s.st.SetValue(requeueKey, spaceID); err != nil {
		return fmt.Errorf("%w: %w", errNotRequeued, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), moveWait)
	defer cancel()
	// Each row goes as stored when it is queued, not as read here: a
	// change made meanwhile, as a deletion, is not undone.
	var queued error
	pages, err := s.st.AllPages()
	queued = errors.Join(queued, err)
	for _, p := range pages {
		queued = errors.Join(queued, s.st.Requeue("pages", p.ID))
	}
	folders, err := s.st.Folders()
	queued = errors.Join(queued, err)
	for _, f := range folders {
		queued = errors.Join(queued, s.st.Requeue("folders", f.ID))
	}
	comments, err := s.st.AllComments()
	queued = errors.Join(queued, err)
	for _, c := range comments {
		queued = errors.Join(queued, s.st.Requeue("comments", c.ID))
	}
	files, err := s.st.AllFiles()
	queued = errors.Join(queued, err)
	recordings, err := s.st.Recordings()
	queued = errors.Join(queued, err)
	var failed error
	moves := map[string]bool{}
	// Deleted files go too, as deleted pages do, so a deletion sent in a
	// batch that was lost goes again: its stored copy may be gone.
	for _, f := range files {
		if err := s.requeueFile(ctx, f, spaceID); err != nil {
			moves["files:"+f.ID] = true
			failed = errors.Join(failed, err)
		}
	}
	for _, r := range recordings {
		if err := s.requeueRecording(ctx, r); err != nil {
			moves["recordings:"+r.ID] = true
			failed = errors.Join(failed, err)
		}
	}
	queued = errors.Join(queued, s.setIDs(movesKey, moves))
	if queued != nil {
		return errors.Join(failed, fmt.Errorf("%w: %w", errNotRequeued, queued))
	}
	return errors.Join(failed, s.st.SetValue(requeueKey, ""))
}

// requeueKey keeps the space requeueAll is queuing everything in, until it
// has: one it could not finish runs again before the next pass.
const requeueKey = "sync.requeue_waiting"

// errNotRequeued is a requeueAll that did not queue everything, and runs
// again before the next pass.
var errNotRequeued = errors.New("not everything is queued to go up")

// moveWait is how long requeueAll waits for iCloud Drive to bring what
// this device has only there; a retry waits a quarter of it. Tests
// shorten it.
var moveWait = 2 * time.Minute

// movesKey keeps the files and recordings requeueAll could not bring
// from iCloud Drive yet, as "files:<id>" and "recordings:<id>".
const movesKey = "sync.moves_waiting"

// retryMoves queues the files and recordings requeueAll could not bring
// from iCloud Drive, once they come, and runs again a requeueAll that did
// not finish.
func (s *Service) retryMoves(ctx context.Context) error {
	if space, _ := s.st.Value(requeueKey); space != "" {
		return s.requeueAll(space)
	}
	moves := s.ids(movesKey)
	if len(moves) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, moveWait/4)
	defer cancel()
	var failed error
	left := map[string]bool{}
	files, _ := s.st.Files()
	for _, f := range files {
		if key := "files:" + f.ID; moves[key] {
			if err := s.requeueFile(ctx, f, s.st.Session().SpaceID); err != nil {
				left[key] = true
				failed = errors.Join(failed, err)
			}
		}
	}
	recordings, _ := s.st.Recordings()
	for _, r := range recordings {
		if key := "recordings:" + r.ID; moves[key] {
			if err := s.requeueRecording(ctx, r); err != nil {
				left[key] = true
				failed = errors.Join(failed, err)
			}
		}
	}
	return errors.Join(failed, s.setIDs(movesKey, left))
}

// requeueFile queues a file to go up in space spaceID, with its contents
// when this device has them (a deleted file's row goes alone). It changes
// nothing while the file is in iCloud Drive but does not come here. What
// it queues is the file as stored once it has come, as a change made
// while it waited for it is not undone.
func (s *Service) requeueFile(ctx context.Context, f store.File, spaceID string) error {
	// One iCloud Drive does not list yet, as while the device that has it
	// still copies it there, waits too: queued now, it would go up with a
	// path of its own and nothing stored there.
	if f.DeletedAt.IsZero() && !s.cloudOn.Load() && !onDevice(f.LocalPath) && f.StoragePath != "" &&
		s.joinedICloud() && !s.keptInICloud("files", f.StoragePath) {
		return fmt.Errorf("file %s: %w", f.Name, errNotInICloud)
	}
	if f.DeletedAt.IsZero() && s.onlyInICloud("files", f.StoragePath, f.LocalPath) {
		data, err := s.cloudFetch(ctx, "files", f.StoragePath)
		if err != nil {
			return fmt.Errorf("file %s from iCloud Drive: %w", f.Name, err)
		}
		if _, err := s.st.DownloadedFile(f.ID, data); err != nil && !store.IsNotFound(err) {
			return err
		}
	}
	return s.st.RequeueFile(f.ID, spaceID)
}

// requeueRecording queues a finished recording to go up with its audio,
// which keeps the path it has until the upload gives it another. It
// changes nothing while the audio is in iCloud Drive but does not come
// here. What it queues is the recording as stored once it has come, as
// requeueFile does a file.
func (s *Service) requeueRecording(ctx context.Context, r store.Recording) error {
	if r.Status != "ready" && r.Status != "failed" {
		return nil
	}
	if s.onlyInICloud("recordings", r.AudioPath, r.LocalPath) {
		data, err := s.cloudFetch(ctx, "recordings", r.AudioPath)
		if err != nil {
			return fmt.Errorf("recording %s from iCloud Drive: %w", r.ID, err)
		}
		local := filepath.Join(s.st.Dir, "recordings", r.ID+".wav")
		if err := os.MkdirAll(filepath.Dir(local), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(local, data, 0o600); err != nil {
			return err
		}
		if _, err := s.st.UpdateRecording(r.ID, func(x *store.Recording) { x.LocalPath = local }); err != nil && !store.IsNotFound(err) {
			return err
		}
	}
	return s.st.RequeueRecording(r.ID)
}

// onlyInICloud reports whether a file or audio is in iCloud Drive and not
// on this device, so has to come here before it can go up elsewhere.
// While sync goes through iCloud Drive, as when a device sends everything
// again there, it stays where the other devices read it: waiting for it
// would hold up the pass for nothing.
func (s *Service) onlyInICloud(bucket, storagePath, local string) bool {
	return !s.cloudOn.Load() && !onDevice(local) && storagePath != "" && s.keptInICloud(bucket, storagePath)
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
	case "storage_delete":
		return s.removeStored(ctx, payload)
	case "pages":
		if payload["created_by"] == nil {
			payload["created_by"] = uid
		}
		return s.pushRow(ctx, e, payload)
	case "folders", "files":
		return s.pushRow(ctx, e, payload)
	case "recordings":
		return s.pushRecording(ctx, e, payload)
	case "comments":
		return s.pushComment(ctx, uid, e, payload)
	case "page_history":
		payload["created_by"] = uid
	case "ai_jobs":
		payload["user_id"] = uid
	}
	_, err := s.be.Upsert(ctx, e.Entity, payload, "")
	return err == nil, err
}

// pushChanged sends a row changed here: only the columns it changed when
// the server has the row, so a change another device made meanwhile to
// another column stays, and the whole row when the server has none.
func (s *Service) pushChanged(ctx context.Context, table string, payload map[string]any) (bool, error) {
	cols, partial := payload[store.ChangedKey].([]any)
	delete(payload, store.ChangedKey)
	if partial {
		changes := make(map[string]any, len(cols))
		for _, c := range cols {
			if k, ok := c.(string); ok {
				changes[k] = payload[k]
			}
		}
		if len(changes) == 0 {
			return true, nil
		}
		rows, err := s.be.Update(ctx, table, []string{remote.Eq("id", store.S(payload, "id"))}, changes)
		if err != nil || len(rows) > 0 {
			return err == nil, err
		}
	}
	_, err := s.be.Upsert(ctx, table, payload, "")
	return err == nil, err
}

// pushRow is pushChanged for a row the server may refuse to change: one
// the user can see there but no longer change, as a page shared with them
// to view since they changed it. Its change would never go, and the pulls
// pass over the row while it waits, so every change made to it elsewhere
// would be missed: the change gives way to the server's row instead. A
// row the server does not show stays queued, as one under a page not up
// yet.
func (s *Service) pushRow(ctx context.Context, e store.OutboxEntry, payload map[string]any) (bool, error) {
	done, err := s.pushChanged(ctx, e.Entity, payload)
	if !remote.IsForbidden(err) {
		return done, err
	}
	return s.giveWay(ctx, e, err)
}

// giveWay has a change the server refused give way to the server's row,
// when the server shows it (pushRow), and returns refused otherwise.
func (s *Service) giveWay(ctx context.Context, e store.OutboxEntry, refused error) (bool, error) {
	row, err := s.be.SelectOne(ctx, e.Entity, remote.Query{Select: "*", Filters: []string{remote.Eq("id", e.EntityID)}})
	if err != nil || row == nil {
		return false, refused
	}
	log.Printf("outbox %s/%s: %v; taking the server's row", e.Entity, e.EntityID, refused)
	return s.takeServer(ctx, e, row)
}

// takeServer stores a row from the server over this device's change to
// it, as the pulls do, and reports the change done.
func (s *Service) takeServer(ctx context.Context, e store.OutboxEntry, row map[string]any) (bool, error) {
	// Off the queue first, so the row goes in; a change made here since
	// keeps it queued, and the row stays out, as the pulls leave it.
	if err := s.st.Dequeue(e); err != nil {
		return false, err
	}
	var err error
	switch e.Entity {
	case "pages":
		err = s.st.PutRemotePage(row)
	case "folders":
		err = s.st.PutRemoteFolder(row)
	case "files":
		err = s.st.PutRemoteFile(row)
	case "recordings":
		err = s.st.PutRemoteRecording(row)
	case "comments":
		author := store.S(row, "author_id")
		err = s.st.PutRemoteComment(row, s.authorNames(ctx, []map[string]any{row})[author])
	}
	if err != nil {
		return false, err
	}
	s.st.Notify(e.Entity, e.PageID)
	return true, nil
}

// pushRecording sends a recording as pushRow does. One settled after a
// quit stopped its transcription (store.IfStatusKey) changes the server's
// only while that is still at the status it was here: the server, or the
// device that started the transcription, may have finished it meanwhile,
// and the recording here takes theirs then. One the server does not have
// goes up whole.
func (s *Service) pushRecording(ctx context.Context, e store.OutboxEntry, payload map[string]any) (bool, error) {
	status, _ := payload[store.IfStatusKey].(string)
	delete(payload, store.IfStatusKey)
	if status == "" {
		return s.pushRow(ctx, e, payload)
	}
	changes := map[string]any{}
	for _, k := range []string{"status", "error", "transcript", "duration_ms"} {
		changes[k] = payload[k]
	}
	rows, err := s.be.Update(ctx, "recordings", []string{remote.Eq("id", e.EntityID), remote.Eq("status", status)}, changes)
	if err != nil && !remote.IsForbidden(err) || len(rows) > 0 {
		return err == nil, err
	}
	row, rerr := s.be.SelectOne(ctx, "recordings", remote.Query{Select: "*", Filters: []string{remote.Eq("id", e.EntityID)}})
	switch {
	case rerr != nil:
		return false, rerr
	case row == nil && err != nil:
		return false, err
	case row == nil:
		return s.pushRow(ctx, e, payload)
	}
	return s.takeServer(ctx, e, row)
}

// pushComment updates a comment the server has with what may change, and
// inserts one it has not. An upsert would not do: its insert stamps the
// caller as the author, which the server then refuses as a change to the
// comment, so resolving someone else's comment never went up.
//
// A change the server refuses, as resolving someone else's comment on a
// page the user may only view, gives way to the server's comment, as
// pushRow's does to its row: the pulls pass over a comment while it
// waits, so its author's edits and deletion would never come.
func (s *Service) pushComment(ctx context.Context, uid string, e store.OutboxEntry, payload map[string]any) (bool, error) {
	cols, partial := payload[store.ChangedKey].([]any)
	delete(payload, store.ChangedKey)
	changes := map[string]any{"resolved": payload["resolved"]}
	if author, _ := payload["author_id"].(string); author == "" || author == uid {
		for _, k := range []string{"body", "quote", "agent_name", "deleted_at"} {
			changes[k] = payload[k]
		}
	} else if payload["deleted_at"] != nil {
		changes["deleted_at"] = payload["deleted_at"]
	}
	// Of a comment changed here, only what changed: a resolve made offline
	// does not undo a delete made elsewhere.
	if partial {
		for k := range changes {
			if !slices.Contains(cols, any(k)) {
				delete(changes, k)
			}
		}
	}
	id, _ := payload["id"].(string)
	if partial && len(changes) == 0 {
		return true, nil
	}
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
		if remote.IsConflict(err) || remote.IsForbidden(err) {
			return s.giveWay(ctx, e, errors.New("the comment is someone else's, and you cannot change it"))
		}
		return false, err
	}
	return true, nil
}

// errStale means someone saved the page's content between reading it and
// saving over it.
var errStale = errors.New("the page changed on the server while saving")

// errUnreadable is a page whose content here cannot be read. It is neither
// sent nor merged, as an empty page would take its place: the server's
// copy stays as it is, and the change stays queued.
var errUnreadable = errors.New("the page's content on this device could not be read, so it was not synced")

// errServerUnreadable is a page whose content on the server cannot be
// read here, as one a newer version of Leaflet wrote may not be. It is
// neither merged nor saved over, as an empty page would take its place:
// the server's copy stays, this device keeps its own, and its change
// stays queued.
var errServerUnreadable = errors.New("the page's content on the server could not be read, so it was not synced")

func (s *Service) pushContent(ctx context.Context, pageID string) (bool, error) {
	// A page that is gone has nothing to send; one that cannot be read now
	// stays queued.
	if _, err := s.st.Page(pageID); store.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
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
	if store.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	local, err := doc.Decode(row.Content)
	if err != nil {
		return false, errUnreadable
	}
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: "content, revision, updated_at", Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return false, err
	}
	merged := local
	var conflicts []doc.Conflict
	if remoteRow != nil && behind(row, remoteRow) {
		theirs, err := contentOf(remoteRow["content"])
		if err != nil {
			return false, errServerUnreadable
		}
		merged, conflicts = doc.Merge(baseOf(row), local, theirs)
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
	// What that merge's conflicts lose is kept, as the first merge's is.
	var now store.ContentRow
	next := merged
	var meanwhile []doc.Conflict
	for tries := 1; ; tries++ {
		if now, err = s.st.ContentRow(pageID); err != nil {
			break
		}
		next, meanwhile = merged, nil
		if now.Content != row.Content {
			typed, err := doc.Decode(now.Content)
			if err != nil {
				return false, errUnreadable
			}
			next, meanwhile = doc.Merge(local, typed, merged)
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
	for _, c := range append(conflicts, meanwhile...) {
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

// contentOf reads a page's content as the server sent it.
func contentOf(v any) (*doc.Content, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return doc.Decode(string(raw))
}

func (s *Service) pushFile(ctx context.Context, uid string, payload map[string]any) (bool, error) {
	local, _ := payload["local_path"].(string)
	if !onDevice(local) {
		var err error
		if local, err = s.fetchCopied(ctx, payload); err != nil || local == "" {
			return err == nil, err // gone: nothing to upload
		}
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
		// The upload waits until the recording has its path: else nothing
		// would send it again, and the other devices could not play it.
		rec, err := s.st.UpdateRecording(recID, func(r *store.Recording) {
			r.AudioPath, r.UploadedAt = path, time.Now()
		})
		if store.IsNotFound(err) {
			return true, nil // the recording went meanwhile
		}
		if err != nil {
			return false, err
		}
		_, _ = s.be.Upsert(ctx, "recordings", store.RecordingToRemote(rec), "")
		if s.KeepAudio != nil && s.KeepAudio() == "until_uploaded" {
			// The page plays it from Storage now.
			_ = f.Close()
			_ = os.Remove(local)
			_ = s.st.ClearLocalPath(recID)
		}
	}
	return true, nil
}

// fetchCopied brings to this device the file or audio a copy was made
// from, which goes up again as the copy's own: only those who can view
// the page of the row a stored copy belongs to can read it. It returns
// where the copy is kept here now, or "" when there is nothing to copy:
// the copy had what it needs, or what it copies or the copy is gone.
func (s *Service) fetchCopied(ctx context.Context, payload map[string]any) (string, error) {
	from := store.S(payload, "from_path")
	if from == "" {
		return "", nil
	}
	data, err := s.Fetch(ctx, store.S(payload, "bucket"), from)
	if remote.IsNotFound(err) || errors.Is(err, fs.ErrNotExist) {
		// iCloud Drive may not list it yet, as while the device that has
		// it still uploads it: it is waited for while the original lives.
		if s.cloudOn.Load() && s.originalLives(payload) {
			return "", fmt.Errorf("%w: what this copies is not in iCloud Drive yet", icloud.ErrNotReady)
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var local string
	if recID := store.S(payload, "recording_id"); recID != "" {
		local = filepath.Join(s.st.Dir, "recordings", recID+".wav")
		if err = os.MkdirAll(filepath.Dir(local), 0o750); err == nil {
			err = os.WriteFile(local, data, 0o600)
		}
		if err == nil {
			_, err = s.st.UpdateRecording(recID, func(r *store.Recording) { r.LocalPath = local })
		}
	} else {
		local, err = s.st.DownloadedFile(store.S(payload, "file_id"), data)
	}
	if store.IsNotFound(err) {
		return "", nil
	}
	return local, err
}

// removeStored removes a deleted file's object from Storage, once the
// server has the row that says it is deleted: until then the entry waits,
// as the file is still there for the other devices, and while a copy of it
// waits to go up from it (copyWaits). An object the server does not have
// is gone already. One of a file that lives here, as when its deletion
// gave way to the server's row, stays.
func (s *Service) removeStored(ctx context.Context, payload map[string]any) (bool, error) {
	id := store.S(payload, "file_id")
	if s.st.HasPending("files", id) || s.copyWaits(store.S(payload, "bucket"), store.S(payload, "path")) {
		return false, nil
	}
	if f, err := s.st.File(id); err == nil && f.DeletedAt.IsZero() {
		return true, nil
	}
	err := s.be.Remove(ctx, store.S(payload, "bucket"), store.S(payload, "path"))
	if remote.IsNotFound(err) {
		return true, nil
	}
	return err == nil, err
}

// copyWaits reports whether a copy's upload still waits to read a deleted
// file's stored contents (fetchCopied): until it has, they stay, as the
// copy would go up with nothing in it. One it cannot tell of waits too.
func (s *Service) copyWaits(bucket, path string) bool {
	entries, err := s.st.Pending()
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.Entity != "storage" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(e.Payload), &payload) == nil &&
			store.S(payload, "bucket") == bucket && store.S(payload, "from_path") == path {
			return true
		}
	}
	return false
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

// overlap is how far behind its cursor a pull looks again. The server
// stamps updated_at as a row changes, before the change commits, so a
// change that commits late comes after rows stamped later, which the
// cursor may have passed. Rows seen again are passed over where this
// device has them as they are, and stored again otherwise, which stores
// the row as the server has it, as the first time did.
var overlap = 10 * time.Second

// passed reports whether the cursor is at or past a row, which a pull
// then reads again only for the overlap. A row whose time does not parse
// is not passed.
func (c cursor) passed(r map[string]any, key string) bool {
	at, err := time.Parse(time.RFC3339Nano, store.S(r, "updated_at"))
	if err != nil {
		return false
	}
	cur, err := time.Parse(time.RFC3339Nano, c.at)
	if err != nil {
		return false
	}
	return at.Before(cur) || at.Equal(cur) && store.S(r, key) <= c.key
}

// changed reads up to limit rows changed after the cursor, oldest first,
// after the rows of the overlap behind it. Rows of the same time come in
// key order, so a pull that stops among them resumes at the next one.
func (s *Service) changed(ctx context.Context, table, key, columns string, cur cursor, limit int, filters ...string) ([]map[string]any, error) {
	from := cur
	if at, err := time.Parse(time.RFC3339Nano, cur.at); err == nil && at.Unix() > 0 {
		from = cursor{at: at.Add(-overlap).UTC().Format(time.RFC3339Nano)}
	}
	var out []map[string]any
	for {
		after := remote.Gt("updated_at", from.at)
		if from.key != "" {
			after = "or=(updated_at.gt." + quote(from.at) + ",and(updated_at.eq." + quote(from.at) + "," + key + ".gt." + quote(from.key) + "))"
		}
		rows, err := s.be.Select(ctx, table, remote.Query{Select: columns, Filters: append([]string{after}, filters...), Order: "updated_at," + key, Limit: limit})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		// An overlap fuller than a page reads on, so it does not hold the
		// pull there.
		if len(rows) < limit || !cur.passed(rows[len(rows)-1], key) {
			return out, nil
		}
		last := rows[len(rows)-1]
		from = cursor{store.S(last, "updated_at"), store.S(last, key)}
	}
}

// quote makes a value safe inside a PostgREST or=(…) filter.
func quote(v string) string { return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"` }

// store applies pulled rows in order and moves the cursor past each one
// stored. It stops at the first that cannot be stored, so the next pull
// starts with it again. A row with a change waiting here is passed over:
// the change wins and goes up on the next push. A row the cursor has
// passed, of the overlap, is stored again only when this device does not
// have it as it is (have), as when its change committed late.
func (s *Service) store(table, key string, rows []map[string]any, have func(map[string]any) bool, apply func(map[string]any) error) {
	cur := s.cursor(table)
	start := cur
	stored := false
	for _, r := range rows {
		again := start.passed(r, key)
		if again && have(r) {
			continue
		}
		if !s.st.HasPending(table, store.S(r, key)) {
			if err := apply(r); err != nil {
				log.Printf("pull %s %s: %v", table, store.S(r, key), err)
				break
			}
			stored = true
		}
		if !again {
			cur = cursor{store.S(r, "updated_at"), store.S(r, key)}
		}
	}
	if cur != start {
		s.setCursor(table, cur)
	}
	if stored || cur != start {
		s.st.Notify(table, "")
	}
}

func (s *Service) pullTable(ctx context.Context, table, columns string, apply func(map[string]any) error) error {
	rows, err := s.changed(ctx, table, "id", columns, s.cursor(table), 500)
	if err != nil {
		return err
	}
	have := func(r map[string]any) bool {
		// A local model's connection stays on its device, never stored.
		local, _ := r["is_local"].(bool)
		return local || s.st.HasRowAt(table, store.S(r, "id"), store.Time(r["updated_at"]))
	}
	s.store(table, "id", rows, have, apply)
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
	s.pullMembers(ctx)
	if err := s.pullComments(ctx); err != nil {
		return err
	}
	return s.pullContents(ctx)
}

// pullShared brings the pages newly shared with the user, once they have
// accepted them, with the pages under them and what they hold. Sharing
// changes no page, so the pulls that go by time pass over a page that last
// changed before them. Accepting moves the share's updated_at on, so a
// share accepted after the cursor passed it comes then.
func (s *Service) pullShared(ctx context.Context) error {
	rows, err := s.changed(ctx, "page_permissions", "page_id", "page_id, updated_at", s.cursor("page_permissions"), 100,
		remote.Eq("user_id", s.be.UserID()), remote.NotNull("accepted_at"))
	if err != nil {
		return err
	}
	var failed error
	// A share seen again is of a page this device has, with what is under
	// it, unless its pull failed.
	have := func(r map[string]any) bool {
		_, err := s.st.Page(store.S(r, "page_id"))
		return err == nil
	}
	s.store("page_permissions", "page_id", rows, have, func(r map[string]any) error {
		id := store.S(r, "page_id")
		if failed = s.pullTree(ctx, id); failed == nil {
			failed = s.LoadMembers(ctx, id)
		}
		return failed
	})
	return failed
}

// LoadMembers stores who a page is shared with, as the server has them,
// each with their role on it, which a page above it may give. A page shared
// with the user that they can no longer see, its share withdrawn, goes
// from this device with the pages under it (store.RemoveSharedPages).
func (s *Service) LoadMembers(ctx context.Context, pageID string) error {
	res, err := s.be.RPC(ctx, "page_members", map[string]any{"p_page_id": pageID})
	re := (*remote.Error)(nil)
	forbidden := errors.As(err, &re) && re.Status == http.StatusForbidden
	if err == nil || forbidden {
		s.mu.Lock()
		s.membersAt[pageID] = time.Now()
		s.mu.Unlock()
	}
	if forbidden {
		if gone := s.withdrawn(ctx, pageID); gone != nil {
			return gone
		}
	}
	if err != nil {
		return err
	}
	var members []store.Member
	list, _ := res.([]any)
	for _, r := range list {
		m, _ := r.(map[string]any)
		// A server from before invites says nothing: every share was one.
		accepted, ok := m["accepted"].(bool)
		members = append(members, store.Member{PageID: pageID, UserID: store.S(m, "user_id"), Role: store.S(m, "role"),
			DisplayName: store.S(m, "display_name"), Email: store.S(m, "email"), Accepted: accepted || !ok})
	}
	return s.st.SetMembers(pageID, members)
}

// withdrawn handles members refused for a page: one the server no longer
// shows the user at all goes from this device; any other keeps no roles,
// nor do the pages under it, as those kept would still let them be
// edited here. It returns why it could not.
func (s *Service) withdrawn(ctx context.Context, pageID string) error {
	rows, err := s.be.Select(ctx, "pages", remote.Query{Select: "id", Filters: []string{remote.Eq("id", pageID)}})
	if err == nil && len(rows) == 0 {
		removed, err := s.st.RemoveSharedPages(pageID)
		if err == nil && len(removed) > 0 {
			log.Printf("share of %s withdrawn: removed %d pages", pageID, len(removed))
		}
		return err
	}
	return s.forgetMembers(pageID)
}

// forgetMembers drops the members kept of a page and of the pages under it.
func (s *Service) forgetMembers(pageID string) error {
	pages, err := s.st.AllPages()
	if err != nil {
		return err
	}
	for _, p := range pages {
		if s.st.MembersOrNil(p.ID) != nil && s.st.IsDescendant(p.ID, pageID) {
			if err := s.st.SetMembers(p.ID, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// membersEvery is how often pullMembers loads members it has again, so a
// share withdrawn or changed to view only stops letting the user edit.
const membersEvery = 10 * time.Minute

// pullMembers loads the members of each tree of pages shared with the user
// that has none here, as for a share accepted before sync loaded them, and
// again every membersEvery: an agent outside the app (leaflet mcp) edits
// only what they allow. A tree's top page here has them for the pages
// inside it.
func (s *Service) pullMembers(ctx context.Context) {
	pages, err := s.st.AllPages()
	if err != nil {
		return
	}
	own := s.st.Session().SpaceID
	here := map[string]bool{}
	for _, p := range pages {
		here[p.ID] = p.DeletedAt.IsZero()
	}
	for _, p := range pages {
		if p.SpaceID == own || !p.DeletedAt.IsZero() || here[p.ParentID] {
			continue
		}
		s.mu.Lock()
		at, loaded := s.membersAt[p.ID]
		s.mu.Unlock()
		if loaded && time.Since(at) < membersEvery {
			continue
		}
		if err := s.LoadMembers(ctx, p.ID); err != nil {
			log.Printf("members of %s: %v", p.ID, err)
			continue
		}
		s.reloadMembersUnder(ctx, p.ID, pages)
	}
}

// reloadMembersUnder loads again the members kept of the pages inside a
// tree's top page, as the app keeps them for a page opened: the nearest
// page with the user among them says what they may do, so a share
// changed to view only must not stay editable below the top. Members that
// do not load are dropped, leaving it to the pages above.
func (s *Service) reloadMembersUnder(ctx context.Context, topID string, pages []store.Page) {
	for _, p := range pages {
		if p.ID == topID || s.st.MembersOrNil(p.ID) == nil || !s.st.IsDescendant(p.ID, topID) {
			continue
		}
		if err := s.LoadMembers(ctx, p.ID); err != nil {
			log.Printf("members of %s: %v", p.ID, err)
			if err := s.st.SetMembers(p.ID, nil); err != nil {
				log.Printf("members of %s: %v", p.ID, err)
			}
		}
	}
}

// pullTree stores a page and the pages under it, their comments, files
// and recordings, and has their content fetched. A row with a change
// waiting here keeps it, as in the other pulls.
func (s *Service) pullTree(ctx context.Context, rootID string) error {
	var ids []string
	rows, err := s.be.Select(ctx, "pages", remote.Query{Select: "*", Filters: []string{remote.Eq("id", rootID)}})
	for depth := 0; depth < 64; depth++ {
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		var level []string
		for _, r := range rows {
			id := store.S(r, "id")
			if !s.st.HasPending("pages", id) {
				if err := s.st.PutRemotePage(r); err != nil {
					return err
				}
			}
			level = append(level, id)
		}
		ids = append(ids, level...)
		rows, err = s.selectIn(ctx, "pages", "parent_id", level)
	}
	if len(ids) == 0 {
		return nil
	}
	s.st.Notify("pages", "")
	comments, err := s.selectIn(ctx, "comments", "page_id", ids)
	if err != nil {
		return err
	}
	names := s.authorNames(ctx, comments)
	for _, r := range comments {
		if s.st.HasPending("comments", store.S(r, "id")) {
			continue
		}
		if err := s.st.PutRemoteComment(r, names[store.S(r, "author_id")]); err != nil {
			return err
		}
	}
	for _, step := range []struct {
		table string
		apply func(map[string]any) error
	}{{"files", s.st.PutRemoteFile}, {"recordings", s.st.PutRemoteRecording}} {
		rows, err := s.selectIn(ctx, step.table, "page_id", ids)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if s.st.HasPending(step.table, store.S(r, "id")) {
				continue
			}
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
	return s.setWaiting(waiting)
}

// treeIDs is how many pages pullTree names in one request, and treeRows
// how many rows it reads at a time: the server sends at most 1,000, and
// leaves out the rest without saying so. Tests make them smaller.
var treeIDs, treeRows = 100, 500

// selectIn reads every row of a table whose col is one of ids: a few ids
// at a time, and their rows a page at a time, in the order of the rows'
// IDs.
func (s *Service) selectIn(ctx context.Context, table, col string, ids []string) ([]map[string]any, error) {
	var out []map[string]any
	for chunk := range slices.Chunk(ids, treeIDs) {
		after := ""
		for {
			filters := []string{remote.In(col, chunk)}
			if after != "" {
				filters = append(filters, remote.Gt("id", after))
			}
			rows, err := s.be.Select(ctx, table, remote.Query{Select: "*", Filters: filters, Order: "id", Limit: treeRows})
			if err != nil {
				return nil, err
			}
			out = append(out, rows...)
			if len(rows) < treeRows {
				break
			}
			after = store.S(rows[len(rows)-1], "id")
		}
	}
	return out, nil
}

func (s *Service) pullComments(ctx context.Context) error {
	rows, err := s.changed(ctx, "comments", "id", "*", s.cursor("comments"), 500)
	if err != nil || len(rows) == 0 {
		return err
	}
	names := s.authorNames(ctx, rows)
	have := func(r map[string]any) bool {
		return s.st.HasRowAt("comments", store.S(r, "id"), store.Time(r["updated_at"]))
	}
	s.store("comments", "id", rows, have, func(r map[string]any) error {
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
			_, err := s.refreshRemote(ctx, id)
			if errors.Is(err, errServerUnreadable) || errors.Is(err, errUnreadable) {
				// The page keeps its content here, and waits until both
				// copies can be read; the other pages' content still comes.
				log.Printf("pull %s: %v", id, err)
				waiting[id] = true
				return nil
			}
			if err != nil {
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
		// Content seen again, of the overlap, is fetched only when this
		// device has not merged that version or a later one.
		again := start.passed(r, "page_id")
		if again && s.hasContentAt(id, store.Time(r["updated_at"])) {
			continue
		}
		if failed = refresh(id); failed != nil {
			break
		}
		if !again {
			cur = cursor{store.S(r, "updated_at"), id}
		}
	}
	// The pages that wait are kept before the cursor passes them: the
	// other way round, a crash between would lose them.
	if err := s.setWaiting(waiting); err != nil {
		return errors.Join(failed, err)
	}
	if cur != start {
		s.setCursor("page_content", cur)
	}
	return failed
}

// hasContentAt reports whether this device has merged the server's
// content of a page as of a time, or later.
func (s *Service) hasContentAt(pageID string, at time.Time) bool {
	row, err := s.st.ContentRow(pageID)
	return err == nil && !row.RemoteUpdatedAt.IsZero() && row.RemoteUpdatedAt.UnixMilli() >= at.UnixMilli()
}

// waiting are the pages whose content changed before their own row came,
// so their content is fetched once it has.
func (s *Service) waiting() map[string]bool { return s.ids("sync.content_waiting") }

func (s *Service) setWaiting(ids map[string]bool) error { return s.setIDs("sync.content_waiting", ids) }

// ids reads a set of IDs kept under key.
func (s *Service) ids(key string) map[string]bool {
	out := map[string]bool{}
	if v, ok := s.st.Value(key); ok && v != "" {
		for _, id := range strings.Split(v, ",") {
			out[id] = true
		}
	}
	return out
}

func (s *Service) setIDs(key string, ids map[string]bool) error {
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	slices.Sort(list)
	return s.st.SetValue(key, strings.Join(list, ","))
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
	remoteContent, err := contentOf(remoteRow["content"])
	if err != nil {
		return nil, errServerUnreadable
	}
	// The user or an agent may save while this merges: store the merge
	// only over the content it merged, and merge again otherwise.
	for range 3 {
		local, localErr := s.st.ContentRow(pageID)
		if localErr == nil && !behind(local, remoteRow) {
			c, err := doc.Decode(local.Content)
			if err != nil {
				return nil, errUnreadable
			}
			return c, nil
		}
		next := remoteContent
		var conflicts []doc.Conflict
		// Content here that cannot be read, with no change waiting, is
		// the server's now.
		if localErr == nil && s.st.HasPending("page_content", pageID) {
			mine, err := doc.Decode(local.Content)
			if err != nil {
				return nil, errUnreadable
			}
			next, conflicts = doc.Merge(baseOf(local), mine, remoteContent)
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

// baseOf is the content a page had when it last synced, which merges
// start from: nil before the first sync, and when it cannot be read.
func baseOf(row store.ContentRow) *doc.Content {
	if row.BaseContent == "" {
		return nil
	}
	base, _ := doc.Decode(row.BaseContent)
	return base
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
