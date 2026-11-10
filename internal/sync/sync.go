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

type Service struct {
	st *store.Store
	be *remote.Client

	OnState       func(running bool, err error)
	OnPageContent func(pageID string)
	Offline       func() bool
	KeepAudio     func() string
	ICloud        icloud.Folder
	cloudMu       gosync.Mutex
	Online        func() bool
	DeviceName    func() string
	Updating      func() bool
	Watched       func(pageID string) bool

	cloudOn                     atomic.Bool
	kick                        chan struct{}
	mu                          gosync.Mutex
	running                     gosync.Mutex
	open                        map[string]bool
	loop                        context.Context
	stop                        context.CancelFunc
	cut                         context.CancelFunc
	uploading, lastUpload       *upload
	contentsAt                  int
	contentsAsked, contentsSlow bool
	ownLogChecked               bool
	membersAt                   map[string]time.Time
	retryAt                     map[outboxKey]time.Time
	busyUntil                   time.Time
	watchAfter                  time.Time
	live                        map[string]*remote.Realtime
	heard                       map[string]bool
	hear                        chan struct{}
	wantPull                    atomic.Bool
	beat                        every
	waitingAfter                string
	pageFiles                   map[string]mergedFile
}

type mergedFile struct {
	stamp   icloud.Stamp
	version int64
}

func New(st *store.Store, be *remote.Client) *Service {
	return &Service{st: st, be: be, kick: make(chan struct{}, 1), open: map[string]bool{},
		membersAt: map[string]time.Time{}, retryAt: map[outboxKey]time.Time{}, pageFiles: map[string]mergedFile{},
		live: map[string]*remote.Realtime{}, heard: map[string]bool{}, hear: make(chan struct{}, 1)}
}

func (s *Service) folder() icloud.Folder {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	return s.ICloud
}

func (s *Service) ICloudFolder() icloud.Folder { return s.folder() }

func (s *Service) SetICloudRoot(root string) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()
	s.ICloud.Root = root
}

type target interface {
	ready() bool
	needsNetwork() bool
	pass(ctx context.Context, pull bool) error
	refresh(ctx context.Context, pageID string) (*doc.Content, error)
}

func (s *Service) target() target {
	if s.cloudOn.Load() {
		return cloudTarget{s}
	}
	return supabaseTarget{s}
}

func (s *Service) SetICloud(on bool) {
	s.cloudOn.Store(on)
	if on {
		s.Kick()
	}
}

func (s *Service) ICloudOn() bool { return s.cloudOn.Load() }

func (s *Service) Uploads() bool { return s.target().ready() }

func (s *Service) CanFetch() bool {
	return s.target().ready() || s.joinedICloud()
}

func (s *Service) Fetch(ctx context.Context, bucket, path string) ([]byte, error) {
	r, err := s.FetchReader(ctx, bucket, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

func (s *Service) FetchReader(ctx context.Context, bucket, path string) (io.ReadCloser, error) {
	return s.fetch(ctx, bucket, path, true)
}

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

func (s *Service) deviceName() string {
	if s.DeviceName != nil {
		return s.DeviceName()
	}
	return DeviceName()
}

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
