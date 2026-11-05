// Package sync moves changes between this device and the others: through
// Supabase, or through the Leaflet folder in iCloud Drive (docs/protocol.md
// §6, §8). It drains the outbox, pulls rows changed since the last pull,
// and merges page content per block (§1).
//
// The loop (loop.go) runs each pass through a target: the server
// (supabase*.go) or iCloud Drive (icloud*.go). Both keep to the same
// rules for the outbox's waits and retries (outbox.go), for this device's
// pages joining a library (adopt.go), for storing merged page content
// (content.go), and for the rows of the tables that sync (rows.go).
package sync

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	gosync "sync"
	"sync/atomic"
	"time"

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
	// through instead of Supabase while SetICloud has turned it on. It is
	// set before the service runs; SetICloudRoot moves it after, as the
	// passes, the app and uploads read it from goroutines of their own.
	ICloud  icloud.Folder
	cloudMu gosync.Mutex
	// Online reports whether the device has a connection, as the host's
	// system says; nil counts as online.
	Online func() bool
	// DeviceName names this device in Account › Devices and to the other
	// devices in iCloud Drive; nil is the package's DeviceName.
	DeviceName func() string
	// Updating reports whether the Supabase project is being brought up to
	// this version of Leaflet, while which the changes wait to go up: sent
	// before, they may name columns its schema lacks yet. nil is never.
	Updating func() bool
	// Shown reports whether the open pages are on screen, for the loop to
	// check them for what changed on the server (watchOpen): not while the
	// window is hidden or minimized. nil is always, as on iOS, where the
	// loop stops in the background.
	Shown func() bool

	cloudOn atomic.Bool
	kick    chan struct{}
	mu      gosync.Mutex
	running gosync.Mutex
	open    map[string]bool
	// loop is the context of the loop Start started, which Stop cancels,
	// and stop cancels it; both are nil while no loop runs.
	loop context.Context
	stop context.CancelFunc
	// cut cancels the pass running, loop's or SyncNow's, for Stop to cut
	// it short; nil between passes.
	cut context.CancelFunc
	// ownLogChecked is set once a pass has read this device's last batch
	// in iCloud Drive (checkOwnLog).
	ownLogChecked bool
	// membersAt is when the members of each page shared with the user
	// were last loaded, for pullMembers to load them again.
	membersAt map[string]time.Time
	// retryAt is when each outbox entry that failed may go again, by its
	// ID and revision: a replaced entry goes at once. Guarded by mu; it
	// starts empty with each run, which tries every entry once more.
	retryAt map[outboxKey]time.Time
	// busyUntil is when the server asked the loop's passes to wait for,
	// as it answered busy with a Retry-After (backOff); guarded by mu.
	busyUntil time.Time
	// wantPull is set by Kick for the loop's next pass to pull too, which
	// one KickPush asked for alone would not.
	wantPull atomic.Bool
	// beat is when the device last said it is in use (heartbeat), and
	// providersSwept when the cloud model connections removed elsewhere
	// were last looked for (pullProviderRemovals); guarded by mu.
	beat, providersSwept every
	// waitingAfter is the last of the pages that wait, in ID order, that
	// a pass brought, for the next to go on after (pullWaiting); guarded
	// by mu.
	waitingAfter string
	// pageFiles are the other devices' page files in iCloud Drive as this
	// device last merged them, by root and path, so one that has not
	// changed since is not read again (cloudRefresh); guarded by mu. It
	// starts empty with each run.
	pageFiles map[string]mergedFile
}

// mergedFile is a page file's stamp as it was merged, and its version.
type mergedFile struct {
	stamp   icloud.Stamp
	version int64
}

// New returns the sync service.
func New(st *store.Store, be *remote.Client) *Service {
	return &Service{st: st, be: be, kick: make(chan struct{}, 1), open: map[string]bool{},
		membersAt: map[string]time.Time{}, retryAt: map[outboxKey]time.Time{}, pageFiles: map[string]mergedFile{}}
}

// folder is the Leaflet folder in iCloud Drive as it is now.
func (s *Service) folder() icloud.Folder {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	return s.ICloud
}

// ICloudFolder is the Leaflet folder in iCloud Drive sync goes through
// while iCloud sync is on.
func (s *Service) ICloudFolder() icloud.Folder { return s.folder() }

// SetICloudRoot moves the Leaflet folder to root, as when the user picks
// another. Call it under Hold, so no pass reads half of one folder and
// half of the other.
func (s *Service) SetICloudRoot(root string) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	s.ICloud.Root = root
}

// target is where a pass syncs: the Supabase server (supabaseTarget), or
// the Leaflet folder in iCloud Drive (cloudTarget).
type target interface {
	// ready reports whether there is somewhere to sync: signed in, or
	// iCloud sync on.
	ready() bool
	needsNetwork() bool
	// pass runs a pass; one without pull may leave what changed
	// elsewhere for the next, as one that sends a save does.
	pass(ctx context.Context, pull bool) error
	refresh(ctx context.Context, pageID string) (*doc.Content, error)
	flush(ctx context.Context, pageID string) error
}

func (s *Service) target() target {
	if s.cloudOn.Load() {
		return cloudTarget{s}
	}
	return supabaseTarget{s}
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
	return s.target().ready() || s.joinedICloud()
}

// Fetch reads a file (bucket "files") or a recording's audio
// ("recordings") that another device uploaded, by its storage path, whole.
// What went up to iCloud Drive stays there after switching to the server,
// until it has gone up again. FetchReader reads one as it comes.
func (s *Service) Fetch(ctx context.Context, bucket, path string) ([]byte, error) {
	r, err := s.FetchReader(ctx, bucket, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// FetchReader opens what Fetch reads, to read as it comes: a file or a
// recording's audio may be hundreds of megabytes, which are then never
// all in memory, as the store keeps them (store.DownloadedFileFrom). The
// caller closes it.
func (s *Service) FetchReader(ctx context.Context, bucket, path string) (io.ReadCloser, error) {
	return s.fetch(ctx, bucket, path, true)
}

// fetch is FetchReader, which waits while iCloud downloads what is in
// iCloud Drive; with wait false it asks iCloud for it, and is
// icloud.ErrNotReady until it has come.
func (s *Service) fetch(ctx context.Context, bucket, path string, wait bool) (io.ReadCloser, error) {
	fromCloud := s.cloudRead
	if wait {
		fromCloud = s.cloudFetch
	}
	if s.cloudOn.Load() {
		return fromCloud(ctx, bucket, path)
	}
	if s.be.SignedIn() {
		r, err := s.be.Download(ctx, bucket, path)
		if err == nil || !s.keptInICloud(bucket, path) {
			return r, err
		}
	}
	if s.keptInICloud(bucket, path) {
		return fromCloud(ctx, bucket, path)
	}
	return nil, errors.New("sync: not signed in, and not in iCloud Drive")
}

// deviceName names this device in Account › Devices, as the host has it.
func (s *Service) deviceName() string {
	if s.DeviceName != nil {
		return s.DeviceName()
	}
	return DeviceName()
}

// DeviceName names this desktop in Account › Devices.
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
