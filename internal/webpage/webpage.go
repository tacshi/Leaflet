package webpage

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/guard"
	"leaflet/internal/recording"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/internal/theme/icons"
	"leaflet/internal/web"
	"leaflet/locales"
)

type Server interface {
	Image(path, typ string) string
	// Picture is the src of the picture at a web link, "" to leave it out.
	Picture(ctx context.Context, link string) string
	File(path, name string) string
	Audio(path string) string
}

type Options struct {
	Store       *store.Store
	Page, Title string
	Titles      map[string]string
	Files       []store.File
	Lang        string
	Head, Tail  string
	Script      func(name string) string
	Embed       func(n *doc.Node) string
	Serve       Server
	Code        func(code, language string) string
	Chart       func(n *doc.Node) string
	Print       bool
	Region      string
	Video       func(v doc.Video) doc.Video
	// Map gives the src of the page a map block shows on its tiles in, in
	// a frame (doc.HTMLOptions.Map); a printed page shows none.
	Map func(n *doc.Node) string
}

// PrintCSP lets a printed page load nothing: its pictures are in it, as its
// diagram script is.
var PrintCSP = rendered.ScriptsCSP(rendered.PrintDiagramJS) + "; img-src data:"

func HTML(c *doc.Content, o Options) string {
	localPath, _ := export.OwnFiles(o.Files)
	own, ownPaths := map[string]bool{}, map[string]bool{}
	for _, f := range o.Files {
		if f.PageID == o.Page {
			own[f.ID] = true
			if lp := localPath(f.ID); lp != "" {
				ownPaths[lp] = true
			}
		}
	}
	opts := doc.HTMLOptions{
		Lang:   o.Lang,
		Head:   o.Head,
		Tail:   o.Tail,
		Script: o.Script,
		Embed:  o.Embed,
		Map:    o.Map,
		Print:  o.Print,
		Region: o.Region,
		Video:  o.Video,
		Image: func(n *doc.Node) string {
			local, ok := strings.CutPrefix(n.Str("url"), "file://")
			if !ok {
				return ""
			}
			if id := n.Str("file_id"); id != "" {
				if !own[id] {
					return ""
				}
				local = localPath(id)
			}
			if !ownPaths[local] {
				return ""
			}
			ext := strings.ToLower(filepath.Ext(local))
			if !doc.ImageExts[ext] {
				return ""
			}
			if info, err := os.Stat(local); err != nil || !info.Mode().IsRegular() {
				return ""
			}
			return o.Serve.Image(local, mime.TypeByExtension(ext))
		},
		Picture:     func(link string) string { return o.Serve.Picture(context.Background(), link) },
		Code:        o.Code,
		CalloutIcon: func(tone string) string { return icons.SVG(doc.CalloutIcon(tone)) },
		Icon:        icons.SVG,
		Chart:       o.Chart,
		Due:         DueText,
		File: func(n *doc.Node) (href, name, detail string) {
			name = n.Str("name")
			for _, f := range o.Files {
				if f.ID != n.Str("file_id") {
					continue
				}
				if !own[f.ID] {
					if name == "" {
						name = locales.CardFile()
					}
					return "", name, ""
				}
				name = f.Name
				path := localPath(f.ID)
				if _, err := os.Stat(path); path == "" || err != nil {
					break
				}
				return o.Serve.File(path, f.Name), name, FileDetail(f.Name, f.SizeBytes)
			}
			if name == "" {
				name = locales.CardFile()
			}
			return "", name, locales.CardNotOnDevice()
		},
		Recording: func(n *doc.Node) doc.RecordingView {
			rec, err := o.Store.Recording(n.Str("recording_id"))
			if err == nil && rec.PageID != o.Page {
				return doc.RecordingView{Title: recording.BlockTitle(n.Str("title"), time.Time{}, false, o.Title)}
			}
			v := doc.RecordingView{Title: recording.BlockTitle(n.Str("title"), rec.CreatedAt, err == nil, o.Title)}
			if err != nil {
				return v
			}
			v.Length = recording.FormatDuration(time.Duration(rec.DurationMs) * time.Millisecond)
			if path := rec.LocalPath; rec.Status != "recording" && export.InFolder(o.Store.Dir, path) {
				if _, err := os.Stat(path); err == nil {
					v.Audio = o.Serve.Audio(path)
				}
			}
			for _, s := range recording.Tidy(rec.Transcript) {
				l := doc.TranscriptLine{Start: float64(s.StartMs) / 1000, Time: recording.FormatDuration(time.Duration(s.StartMs) * time.Millisecond), Text: s.Text}
				if s.Speaker != "" {
					l.Speaker = locales.RecSpeaker(s.Speaker)
				}
				v.Lines = append(v.Lines, l)
			}
			return v
		},
		PageTitle: func(id string) string {
			if t, ok := o.Titles[id]; ok {
				return t
			}
			return locales.CommonUntitled()
		},
	}
	if o.Print {
		opts.Script = nil
		maps := drawsMaps(c)
		opts.ScriptSource = func(name string) string {
			switch {
			case name == "diagram":
				return rendered.PrintDiagramJS
			case name == "map" && maps:
				return rendered.PrintMapJS()
			}
			return ""
		}
		csp := PrintCSP
		if maps {
			// Its maps are drawn on their tiles, which load from the tile
			// server alone, by a script that waits for them; a map not
			// drawn in time shows its plain drawing.
			csp = rendered.PrintMapCSP()
			msgs := locales.In(o.Lang)
			opts.MapData = func(n *doc.Node) string { return rendered.PrintMapData(msgs, doc.MapOf(n)) }
			opts.Tail += rendered.PrintMapWorker()
		}
		opts.Head = `<meta http-equiv="Content-Security-Policy" content="` + csp + `">` + o.Head
		pics := pictures(o.Serve, doc.WebPictures(c))
		opts.Picture = func(link string) string { return pics[link] }
	}
	return doc.PageHTML(o.Title, c, opts)
}

// drawsMaps reports whether a printed page of c draws maps on their tiles:
// whether it has a map with anything on it.
func drawsMaps(c *doc.Content) bool {
	maps := false
	c.Walk(func(n, _ *doc.Node) { maps = maps || n.Type == "map" && doc.MapOf(n).Shape().OK })
	return maps
}

// PrintOrigin is the address a printed page loads at on the Mac and
// Windows: an origin of its own, from which MapLibre's worker runs, and
// at which Windows serves a page of any length.
const PrintOrigin = "https://print.app.capyvibe.leaflet/"

// MapsDrawn waits for a printed page's maps to be drawn on their tiles
// (rendered.PrintMapJS), as the app evaluates it in the page before it
// prints it.
const MapsDrawn = "await window.leafletMapsDrawn"

const (
	// picturesWait bounds how long a printed page waits for its pictures
	// from the web, all together; fetchesAtOnce how many come at a time.
	picturesWait  = time.Minute
	fetchesAtOnce = 4
)

// pictures asks s for the pictures at links, a few at a time, and returns
// their srcs by link.
func pictures(s Server, links []string) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), picturesWait)
	defer cancel()
	srcs := make([]string, len(links))
	turns := make(chan struct{}, fetchesAtOnce)
	var wg sync.WaitGroup
	for i, link := range links {
		wg.Go(func() {
			defer guard.Recover()
			turns <- struct{}{}
			defer func() { <-turns }()
			srcs[i] = s.Picture(ctx, link)
		})
	}
	wg.Wait()
	out := make(map[string]string, len(links))
	for i, link := range links {
		out[link] = srcs[i]
	}
	return out
}

// Inline serves a printed page's pictures and files inside it, so that it
// loads nothing itself.
var Inline Server = inline{}

type inline struct{}

func (inline) Image(path, typ string) string {
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return ""
	}
	return dataURL(data, typ)
}

// Picture fetches the picture at a web link from public addresses alone, as
// the app shows it, and inlines it. What isn't a picture, or is too large to
// show, is left out.
func (inline) Picture(ctx context.Context, link string) string {
	data, err := fetchPicture(ctx, link)
	typ := pictureType(data)
	if err != nil || len(data) >= maxPictureBytes || typ == "" || tooManyPixels(data) {
		return ""
	}
	return dataURL(data, typ)
}

var fetchPicture = web.FetchContext

// maxPictureBytes is what web.FetchContext reads at most: a picture that
// long was cut off.
const maxPictureBytes = 20 << 20

// dataURL is a picture as a data: URL, shrunk to what paper shows.
func dataURL(data []byte, typ string) string {
	if small, smallTyp, ok := forPrint(data); ok {
		data, typ = small, smallTyp
	}
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// pictureType is the media type of a picture's bytes, "" for what isn't a
// picture.
func pictureType(data []byte) string {
	typ := http.DetectContentType(data)
	switch {
	case strings.HasPrefix(typ, "image/"):
		return typ
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		switch string(data[8:12]) {
		case "avif", "avis":
			return "image/avif"
		case "heic", "heix":
			return "image/heic"
		}
	case strings.HasPrefix(typ, "text/xml") || strings.HasPrefix(typ, "text/plain"):
		if bytes.Contains(data[:min(len(data), 1024)], []byte("<svg")) {
			return "image/svg+xml"
		}
	}
	return ""
}

// tooManyPixels says whether a picture is too large to decode, as the app
// refuses to show it.
func tooManyPixels(data []byte) bool {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	// Divided rather than multiplied, so no claimed size overflows.
	return err == nil && cfg.Width > 0 && cfg.Height > doc.MaxPixels/cfg.Width
}

func (inline) File(string, string) string { return "" }

func (inline) Audio(string) string { return "" }

func DueText(n *doc.Node) string {
	due, timed, ok := n.Due()
	if !ok {
		return ""
	}
	label := locales.EditorDueDate(due)
	if due.Year() == time.Now().Year() {
		label = locales.EditorDueDay(due)
	}
	if timed {
		return locales.EditorDueAt(label, due)
	}
	return label
}

// FileSize is how large a file of n bytes is, as its card says it: 2.4 MB.
func FileSize(n int64) string { return FileSizeIn(locales.In(i18n.Locale()), n) }

// FileSizeIn is FileSize in the language of msgs, as the iOS app, which
// keeps its own, asks for it.
func FileSizeIn(msgs locales.Messages, n int64) string {
	switch {
	case n >= 1<<30:
		return msgs.CardSizeGB(math.Round(float64(n)/(1<<30)*10) / 10)
	case n >= 1<<20:
		return msgs.CardSizeMB(math.Round(float64(n)/(1<<20)*10) / 10)
	case n >= 1<<10:
		return msgs.CardSizeKB(int(math.Round(float64(n) / (1 << 10))))
	}
	return msgs.CardSizeBytes(int(n)) // #nosec G115
}

// FileDetail says what a file named name is and how large, as "PDF · 2.4 MB",
// or its size alone when its name has no extension.
func FileDetail(name string, size int64) string {
	kind := strings.ToUpper(strings.TrimPrefix(filepath.Ext(name), "."))
	if kind == "" {
		return FileSize(size)
	}
	return locales.CardFileDetail(kind, FileSize(size))
}
