package app

import (
	"bytes"
	"errors"
	"image"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/netimage"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/web"
)

// Pictures show through MyGo's netimage, which downloads what is on screen
// first, keeps bitmaps no larger than they show, and keeps web pictures on
// disk as long as their servers' caching headers say, so a page shows them
// at once after a restart or offline. imageCache adds what Leaflet needs on
// top: pictures come only from the library's own files and public addresses,
// one too large to decode is refused, and an animated GIF plays.
type imageCache struct {
	root    string
	mu      sync.Mutex
	gifs    map[string]*gifData
	players map[string]*gifPlayer
	making  map[string]bool
	// cleared counts clears: a player made across one is closed.
	cleared int
}

type gifData struct {
	data []byte
	used time.Time
}

type gifPlayer struct {
	p     *ui.VideoPlayer
	shown time.Time
}

const (
	playerIdle = 30 * time.Second
	// gifIdle is how long an animated GIF's bytes stay once it's not shown.
	gifIdle = 2 * time.Minute
	// maxPictureBytes bounds a picture from the web, as web.Fetch does.
	maxPictureBytes = 20 << 20
	maxPixels       = doc.MaxPixels
	maxGIFPixels    = 200_000_000
	// cardSide bounds a link card's 150×96 picture, at 3 pixels a DIP.
	// factSide bounds a fact card's picture, up to 190×237.5 DIP: cut to
	// fill it, a 16:9 photo's longest side shows up to 422 DIP, about 1270
	// pixels at 3 a DIP. previewSide bounds a file's preview, which fills
	// the window.
	cardSide    = 450
	factSide    = 1280
	previewSide = 4096
)

var (
	errTooLarge   = errors.New("picture too large")
	errNotOwnFile = export.ErrNotOwnFile
	errNotPicture = errors.New("not a picture's link")
)

// newImageCache makes the cache netimage downloads through: there is one
// netimage, so the last cache made is the one in use.
func newImageCache(root string) *imageCache {
	ic := &imageCache{root: root, gifs: map[string]*gifData{}, players: map[string]*gifPlayer{}, making: map[string]bool{}}
	netimage.SetClient(&http.Client{Transport: pictureTransport{ic: ic, web: web.Transport()}})
	return ic
}

// get returns the picture at link, nil while it comes; failed says the last
// try failed, which netimage tries again a while later as views ask. The view
// of c is built again once it comes. side bounds the bitmap's longest side,
// 0 for netimage's default.
func (ic *imageCache) get(c *ui.Context, link string, side int) (pic ui.ImageSource, failed bool) {
	if link == "" {
		return nil, false
	}
	pic, err := netimage.Load(c, link, netimage.Options{MaxSide: side})
	return pic, err != nil
}

// fileURL is the link a picture of the library's is read by.
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/… on Windows
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func filePath(u *url.URL) string {
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p)
}

// gifKey is how a link names its GIF, the same however it is spelled.
func gifKey(link string) string {
	if u, err := url.Parse(link); err == nil {
		return u.String()
	}
	return link
}

func (ic *imageCache) keepGIF(link string, data []byte) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	ic.gifs[gifKey(link)] = &gifData{data: data, used: time.Now()}
}

// player returns a player of the animated GIF at link for the block, nil
// while it's made, or for a picture that doesn't move. done is called once
// it's made.
func (ic *imageCache) player(link, block string, done func()) *ui.VideoPlayer {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	g, ok := ic.gifs[gifKey(link)]
	key := gifKey(link) + "#" + block
	if !ok || ic.making[key] {
		return nil
	}
	g.used = time.Now()
	if gp, ok := ic.players[key]; ok {
		gp.shown = time.Now()
		return gp.p
	}
	ic.making[key] = true
	data, cleared := g.data, ic.cleared
	go func() {
		d, err := ui.NewGIFDecoder(data)
		var p *ui.VideoPlayer
		if err == nil {
			p, err = ui.NewVideoPlayer(d)
		}
		if err != nil {
			log.Printf("gif %s: %v", link, err)
		} else {
			p.SetLoop(true)
		}
		ic.mu.Lock()
		delete(ic.making, key)
		if ic.cleared != cleared {
			if p != nil {
				p.Close()
			}
		} else {
			ic.players[key] = &gifPlayer{p: p, shown: time.Now()}
		}
		ic.mu.Unlock()
		done()
	}()
	return nil
}

// release closes the players of blocks no longer shown, and lets go of the
// GIFs not shown for a while. netimage lets go of its bitmap of such a GIF
// too, so that showing it again reads it again, and it plays.
func (ic *imageCache) release(now time.Time) {
	var gone []string
	ic.mu.Lock()
	for key, gp := range ic.players {
		if now.Sub(gp.shown) < playerIdle {
			continue
		}
		if gp.p != nil {
			gp.p.Close()
		}
		delete(ic.players, key)
	}
	for link, g := range ic.gifs {
		if now.Sub(g.used) >= gifIdle {
			delete(ic.gifs, link)
			gone = append(gone, link)
		}
	}
	ic.mu.Unlock()
	for _, link := range gone {
		netimage.Forget(link)
	}
}

// clear closes the players and empties netimage's memory and disk, as
// Settings' Clear and leaving an account do. netimage is emptied before the
// app made its cache too.
func (ic *imageCache) clear() {
	if ic != nil {
		ic.mu.Lock()
		for key, gp := range ic.players {
			if gp.p != nil {
				gp.p.Close()
			}
			delete(ic.players, key)
		}
		clear(ic.gifs)
		ic.cleared++
		ic.mu.Unlock()
	}
	forgetAllPictures()
}

// forgetAllPictures empties netimage's memory and its folder on disk.
var forgetAllPictures = netimage.Clear

// pictureTransport is how netimage downloads: the library's own files from
// disk, the web only at public addresses. It refuses what is too large, and
// keeps an animated GIF's bytes for its player.
type pictureTransport struct {
	ic  *imageCache
	web http.RoundTripper
}

func (t pictureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var res *http.Response
	var data []byte
	switch r.URL.Scheme {
	case "file":
		// Only as asked for: a page on the web can't redirect to a file.
		if r.Response != nil {
			return nil, errNotOwnFile
		}
		path := filePath(r.URL)
		if !inFolder(t.ic.root, path) {
			return nil, errNotOwnFile
		}
		d, err := os.ReadFile(path) // #nosec G304 -- in the library's folder
		if err != nil {
			return nil, err
		}
		data = d
		// It's on disk already: netimage keeps no copy.
		res = &http.Response{Status: "200 OK", StatusCode: http.StatusOK, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Cache-Control": {"no-store"}}, Request: r}
	case "http", "https":
		got, err := t.web.RoundTrip(r)
		if err != nil || got.StatusCode != http.StatusOK {
			return got, err
		}
		d, err := io.ReadAll(io.LimitReader(got.Body, maxPictureBytes+1))
		_ = got.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(d) > maxPictureBytes {
			return nil, errTooLarge
		}
		res, data = got, d
	default:
		return nil, errNotPicture
	}
	if err := checkPixels(data); err != nil {
		return nil, err
	}
	if animatedGIF(data) {
		t.ic.keepGIF(firstURL(r), data)
		// Kept in memory only, as its player is, so it plays after a restart.
		res.Header.Set("Cache-Control", "no-store")
	}
	res.Body = io.NopCloser(bytes.NewReader(data))
	res.ContentLength = int64(len(data))
	res.Header.Del("Content-Encoding")
	return res, nil
}

// firstURL is the link asked for, before redirects.
func firstURL(r *http.Request) string {
	for r.Response != nil && r.Response.Request != nil {
		r = r.Response.Request
	}
	return r.URL.String()
}

// checkPixels refuses a picture too large to decode. One whose size can't
// be read isn't a bitmap the decoders read, so it can only be drawn as an
// SVG, whose paths take no pixels of its size.
func checkPixels(data []byte) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	// Divided rather than multiplied, so no claimed size overflows.
	if cfg.Width > 0 && cfg.Height > maxPixels/cfg.Width {
		return errTooLarge
	}
	if _, area := gifFrames(data); area > maxGIFPixels {
		return errTooLarge
	}
	return nil
}

func animatedGIF(data []byte) bool {
	n, _ := gifFrames(data)
	return n > 1
}

func gifFrames(data []byte) (frames, area int) {
	if !bytes.HasPrefix(data, []byte("GIF8")) || len(data) < 13 {
		return 0, 0
	}
	table := func(packed byte) int {
		if packed&0x80 == 0 {
			return 0
		}
		return 3 << (packed&7 + 1)
	}
	skip := func(i int) int {
		for i < len(data) && data[i] != 0 {
			i += 1 + int(data[i])
		}
		return i + 1
	}
	i := 13 + table(data[10])
	for i < len(data) {
		switch data[i] {
		case 0x21:
			i = skip(i + 2)
		case 0x2C:
			if i+10 > len(data) {
				return frames, area
			}
			w, h := int(data[i+5])|int(data[i+6])<<8, int(data[i+7])|int(data[i+8])<<8
			frames, area = frames+1, area+w*h
			i = skip(i + 10 + table(data[i+9]) + 1)
		default:
			return frames, area
		}
	}
	return frames, area
}
