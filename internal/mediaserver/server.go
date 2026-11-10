package mediaserver

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Server struct {
	mu    sync.Mutex
	addr  string
	srv   *http.Server
	items map[string]item
}

type item struct {
	path, typ, policy string
	page              string
	cover             []byte
	coverType         string
}

type Item struct {
	Path, Type string
	Page       string
	Policy     string
	Cover      []byte
	CoverType  string
}

func (s *Server) Register(it Item) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.listen(); err != nil {
		return "", err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	token := hex.EncodeToString(key)
	if s.items == nil {
		s.items = map[string]item{}
	}
	s.items[token] = item{path: it.Path, typ: it.Type, policy: it.Policy, page: it.Page, cover: it.Cover, coverType: it.CoverType}
	base := "http://" + s.addr + "/" + token + "/"
	if it.Page == "" {
		return base + "media", nil
	}
	return base, nil
}

func (s *Server) Revoke(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.token(address); ok {
		delete(s.items, t)
	}
}

func (s *Server) token(address string) (string, bool) {
	rest, ok := strings.CutPrefix(address, "http://"+s.addr+"/")
	if !ok {
		return "", false
	}
	t, _, _ := strings.Cut(rest, "/")
	return t, true
}

func (s *Server) listen() error {
	if s.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		_ = s.srv.Close()
		s.srv = nil
	}
	s.items = nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	host := s.addr
	token, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	it, ok := s.items[token]
	s.mu.Unlock()
	if !ok || r.Host != host || r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	switch rest {
	case "":
		if it.page == "" {
			http.NotFound(w, r)
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		if it.policy != "" {
			h.Set("Content-Security-Policy", it.policy)
		}
		_, _ = w.Write([]byte(it.page))
	case "media":
		f, err := os.Open(it.path) // #nosec G304
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		h.Set("Content-Type", it.typ)
		http.ServeContent(w, r, "", info.ModTime(), f)
	case "cover":
		if len(it.cover) == 0 {
			http.NotFound(w, r)
			return
		}
		h.Set("Content-Type", it.coverType)
		_, _ = w.Write(it.cover)
	default:
		http.NotFound(w, r)
	}
}
