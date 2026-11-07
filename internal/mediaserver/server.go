// Package mediaserver serves a device's files to the app's own web views,
// which play them: a web view takes media from an address it can ask for
// parts of, and plays that from a loopback server on every platform. It
// listens on this machine alone, and what it serves goes by a token no
// other program can guess, for as long as the app has it registered.
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

// Server serves registered files. Its zero value is ready: it starts
// listening with the first Register.
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

// Item is what Register serves: a file at Path, of the type Type, which
// comes from the app and never from the file, and, for a player, its Page,
// with the content security policy Policy, and Cover, an image of the type
// CoverType, for its page to show the system what plays.
type Item struct {
	Path, Type string
	Page       string
	Policy     string
	Cover      []byte
	CoverType  string
}

// Register serves it, and returns the address of its page, or of its file
// where it has no page. Revoke ends it.
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

// Revoke stops serving what Register gave address for. What already reads
// it reads on.
func (s *Server) Revoke(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.token(address); ok {
		delete(s.items, t)
	}
}

// token is the token of an address Register gave.
func (s *Server) token(address string) (string, bool) {
	rest, ok := strings.CutPrefix(address, "http://"+s.addr+"/")
	if !ok {
		return "", false
	}
	t, _, _ := strings.Cut(rest, "/")
	return t, true
}

// listen starts the server, once. Call it with mu held.
func (s *Server) listen() error {
	if s.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	// No timeout on writing: a long film is read for as long as it plays.
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

// Close stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		_ = s.srv.Close()
		s.srv = nil
	}
	s.items = nil
}

// ServeHTTP serves a token's page and file, to the address it listens on
// alone: a web page elsewhere can't reach it by a name of its own that
// leads here.
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
		f, err := os.Open(it.path) // #nosec G304 -- a path the app registered.
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
