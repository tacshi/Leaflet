// Package sync moves changes between this device and Supabase: it drains
// the outbox, pulls rows changed since the last pull, and merges page
// content per block (docs/protocol.md §1).
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"runtime"
	gosync "sync"
	"time"

	"github.com/egoist/mygo"

	"leaflet/internal/doc"
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
			s.SyncNow()
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

// SetOpen marks a page as open, so its content is always pulled.
func (s *Service) SetOpen(pageID string, open bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if open {
		s.open[pageID] = true
	} else {
		delete(s.open, pageID)
	}
}

// CanSync reports whether a pass can run: signed in, online, and not
// working offline.
func (s *Service) CanSync() bool {
	if !s.be.SignedIn() {
		return false
	}
	if s.Offline != nil && s.Offline() {
		return false
	}
	return mygo.Network.Status().Online
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
	err := s.pass(ctx)
	if remote.IsNetwork(err) {
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
// the account's space and queues them for upload.
func (s *Service) resolveSpace(ctx context.Context) error {
	uid := s.be.UserID()
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
	s.st.PutSpace(remoteID, name, uid)
	if local, ok := s.st.Value("space.local"); ok && local != remoteID {
		if err := s.st.MoveSpace(local, remoteID); err != nil {
			return err
		}
	}
	s.st.SetValue("space.local", remoteID)
	s.st.SetValue("space."+uid, remoteID)
	// Everything made before sign-in goes up now.
	pages, _ := s.st.AllPages()
	for _, p := range pages {
		s.st.Queue("pages", p.ID, store.PageToRemote(p), "", p.ID)
		s.st.Queue("page_content", p.ID, map[string]any{"page_id": p.ID}, "page from this device", p.ID)
	}
	folders, _ := s.st.Folders()
	for _, f := range folders {
		s.st.Queue("folders", f.ID, store.FolderToRemote(f), "", "")
	}
	files, _ := s.st.Files()
	for _, f := range files {
		f.SpaceID = remoteID
		f.StoragePath = remoteID + "/" + f.ID + "/" + f.Name
		s.st.PutFile(f)
		s.st.Queue("files", f.ID, store.FileToRemote(f), "file", "")
		if f.LocalPath != "" {
			ct := f.MimeType
			if ct == "" {
				ct = "application/octet-stream"
			}
			s.st.Queue("storage", "files/"+f.ID, map[string]any{"bucket": "files", "path": f.StoragePath, "local_path": f.LocalPath, "content_type": ct}, "file waiting to upload", "")
		}
	}
	sess := s.st.Session()
	sess.SpaceID, sess.UserID, sess.SignedIn = remoteID, uid, true
	s.st.SetSession(sess)
	return nil
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
		s.st.SetValue("device.revoked", "true")
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
			s.st.MarkFailed(e.ID, err.Error())
			continue
		}
		if done {
			s.st.Dequeue(e.ID)
		}
	}
	return nil
}

// pushOne sends one entry and reports whether it can leave the queue.
func (s *Service) pushOne(ctx context.Context, uid string, e store.OutboxEntry) (bool, error) {
	var payload map[string]any
	json.Unmarshal([]byte(e.Payload), &payload)
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
		// The server stamps the author on insert (for agent replies, the
		// user who asked); later upserts must send the same value.
		if payload["author_id"] == nil {
			payload["author_id"] = uid
		}
	case "page_history":
		payload["created_by"] = uid
	case "ai_jobs":
		payload["user_id"] = uid
	}
	_, err := s.be.Upsert(ctx, e.Entity, payload, "")
	return err == nil, err
}

func (s *Service) pushContent(ctx context.Context, pageID string) (bool, error) {
	row, err := s.st.ContentRow(pageID)
	if err != nil {
		return true, nil
	}
	if _, err := s.st.Page(pageID); err != nil {
		return true, nil
	}
	local := doc.MustDecode(row.Content)
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: "content, updated_at", Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return false, err
	}
	merged := local
	var conflicts []doc.Conflict
	if remoteRow != nil {
		remoteAt := store.Time(remoteRow["updated_at"])
		if row.RemoteUpdatedAt.IsZero() || remoteAt.After(row.RemoteUpdatedAt) {
			remoteContent := contentOf(remoteRow["content"])
			var base *doc.Content
			if row.BaseContent != "" {
				base = doc.MustDecode(row.BaseContent)
			}
			merged, conflicts = doc.Merge(base, local, remoteContent)
		}
	}
	saved, err := s.be.Upsert(ctx, "page_content", map[string]any{
		"page_id": pageID, "content": json.RawMessage(merged.Encode()), "plain_text": merged.PlainText(),
	}, "page_id")
	if err != nil {
		return false, err
	}
	savedAt := time.Now()
	if len(saved) > 0 {
		savedAt = store.Time(saved[0]["updated_at"])
	}
	// The user may have kept typing while this was in flight.
	now, _ := s.st.ContentRow(pageID)
	typedMeanwhile := now.Content != "" && now.Content != row.Content
	next := merged
	if typedMeanwhile {
		next, _ = doc.Merge(local, doc.MustDecode(now.Content), merged)
	}
	if err := s.st.PushedContent(pageID, next.Encode(), next.PlainText(), merged.Encode(), savedAt); err != nil {
		return false, err
	}
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
		s.st.AddConflict(pageID, c)
	}
	return !typedMeanwhile, nil
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
	f, err := os.Open(local)
	if err != nil {
		return true, nil // gone: nothing to upload
	}
	defer f.Close()
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
			s.be.Upsert(ctx, "recordings", store.RecordingToRemote(rec), "")
			if s.KeepAudio != nil && s.KeepAudio() == "until_uploaded" {
				// The page plays it from Storage now.
				f.Close()
				os.Remove(local)
				s.st.ClearLocalPath(recID)
			}
		}
	}
	return true, nil
}

func (s *Service) cursor(table string) string {
	if v, ok := s.st.Value("sync.cursor." + table); ok {
		return v
	}
	return "1970-01-01T00:00:00Z"
}

func (s *Service) pullTable(ctx context.Context, table, columns string, apply func(map[string]any) error) error {
	cur := s.cursor(table)
	rows, err := s.be.Select(ctx, table, remote.Query{Select: columns, Filters: []string{remote.Gt("updated_at", cur)}, Order: "updated_at", Limit: 500})
	if err != nil {
		return err
	}
	latest := cur
	for _, r := range rows {
		if !s.st.HasPending(table, store.S(r, "id")) {
			if err := apply(r); err != nil {
				log.Printf("pull %s: %v", table, err)
			}
		}
		latest = store.S(r, "updated_at")
	}
	if latest != cur {
		s.st.SetValue("sync.cursor."+table, latest)
		s.st.Notify(table, "")
	}
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
	if err := s.pullComments(ctx); err != nil {
		return err
	}
	return s.pullContents(ctx)
}

func (s *Service) pullComments(ctx context.Context) error {
	cur := s.cursor("comments")
	rows, err := s.be.Select(ctx, "comments", remote.Query{Filters: []string{remote.Gt("updated_at", cur)}, Order: "updated_at", Limit: 500})
	if err != nil || len(rows) == 0 {
		return err
	}
	ids := map[string]bool{}
	for _, r := range rows {
		if a := store.S(r, "author_id"); a != "" {
			ids[a] = true
		}
	}
	names := map[string]string{}
	if len(ids) > 0 {
		var list []string
		for id := range ids {
			list = append(list, id)
		}
		profiles, err := s.be.Select(ctx, "profiles", remote.Query{Select: "id, display_name", Filters: []string{remote.In("id", list)}})
		if err == nil {
			for _, p := range profiles {
				names[store.S(p, "id")] = store.S(p, "display_name")
			}
		}
	}
	latest := cur
	for _, r := range rows {
		if !s.st.HasPending("comments", store.S(r, "id")) {
			name := names[store.S(r, "author_id")]
			if name == "" {
				name = "Someone"
			}
			s.st.PutRemoteComment(r, name)
		}
		latest = store.S(r, "updated_at")
	}
	s.st.SetValue("sync.cursor.comments", latest)
	s.st.Notify("comments", "")
	return nil
}

func (s *Service) pullContents(ctx context.Context) error {
	cur := s.cursor("page_content")
	rows, err := s.be.Select(ctx, "page_content", remote.Query{Select: "page_id, updated_at", Filters: []string{remote.Gt("updated_at", cur)}, Order: "updated_at", Limit: 200})
	if err != nil {
		return err
	}
	allOffline := s.st.Settings().AllPagesOffline
	latest := cur
	for _, r := range rows {
		id := store.S(r, "page_id")
		p, err := s.st.Page(id)
		s.mu.Lock()
		open := s.open[id]
		s.mu.Unlock()
		if err == nil && (open || allOffline || p.KeepOffline) {
			if _, err := s.RefreshContent(ctx, id); err != nil {
				return err
			}
		}
		latest = store.S(r, "updated_at")
	}
	if latest != cur {
		s.st.SetValue("sync.cursor.page_content", latest)
	}
	return nil
}

// RefreshContent brings a page's content up to date with the server,
// merging unsynced edits made here.
func (s *Service) RefreshContent(ctx context.Context, pageID string) (*doc.Content, error) {
	if !s.CanSync() {
		return s.st.Content(pageID), nil
	}
	remoteRow, err := s.be.SelectOne(ctx, "page_content", remote.Query{Select: "content, updated_at", Filters: []string{remote.Eq("page_id", pageID)}})
	if err != nil {
		return nil, err
	}
	local, localErr := s.st.ContentRow(pageID)
	if remoteRow == nil {
		return s.st.Content(pageID), nil
	}
	remoteAt := store.Time(remoteRow["updated_at"])
	if localErr == nil && !local.RemoteUpdatedAt.IsZero() && !remoteAt.After(local.RemoteUpdatedAt) {
		return doc.MustDecode(local.Content), nil
	}
	remoteContent := contentOf(remoteRow["content"])
	next := remoteContent
	if localErr == nil && s.st.HasPending("page_content", pageID) {
		var base *doc.Content
		if local.BaseContent != "" {
			base = doc.MustDecode(local.BaseContent)
		}
		var conflicts []doc.Conflict
		next, conflicts = doc.Merge(base, doc.MustDecode(local.Content), remoteContent)
		for _, c := range conflicts {
			s.st.AddConflict(pageID, c)
		}
	}
	if err := s.st.PutSyncedContent(pageID, next, remoteContent.Encode(), remoteAt); err != nil {
		return nil, err
	}
	if localErr != nil || local.Content != next.Encode() {
		if s.OnPageContent != nil {
			s.OnPageContent(pageID)
		}
	}
	return next, nil
}

// FlushPage pushes a page's pending changes right away, so a cloud agent
// starts from what the user sees.
func (s *Service) FlushPage(ctx context.Context, pageID string) error {
	if !s.CanSync() {
		return nil
	}
	s.running.Lock()
	defer s.running.Unlock()
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
				s.st.Dequeue(e.ID)
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
				s.st.Dequeue(e.ID)
			}
		}
	}
	return nil
}
