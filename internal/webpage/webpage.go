// Package webpage writes a page as a web page that stands alone: as a
// public link shares it, and as Export › PDF prints it, on the desktop and
// on iOS. It holds nothing of MyGo's interface, which the iOS core goes
// without: what the desktop draws with it, code's colors and charts, it
// passes in.
package webpage

import (
	"encoding/base64"
	"math"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/recording"
	"leaflet/internal/rendered"
	"leaflet/internal/store"
	"leaflet/internal/theme/icons"
	"leaflet/locales"
)

// Server is where the web page finds the files of this device it shows:
// those of its page alone, which HTML has checked are.
type Server interface {
	// Image returns where a picture at path, of type typ, is served, or
	// "" to leave it out.
	Image(path, typ string) string
	// File returns where a file at path, named name, is served to
	// download, or "" for nowhere: its card shows it all the same.
	File(path, name string) string
	// Audio returns where a recording's audio at path is served, or ""
	// for nowhere, which leaves out its player.
	Audio(path string) string
}

// Options are what a page needs to be written as a web page.
type Options struct {
	Store *store.Store
	// Page is the page's ID and Title its title.
	Page, Title string
	// Titles are the pages' titles by ID, which name its subpages.
	Titles map[string]string
	// Files are the files the app knows, of every page.
	Files []store.File
	// Lang is the page's language.
	Lang string
	// Head, Tail, Script and Embed are as doc.HTMLOptions has them; a
	// printed page takes no Script.
	Head, Tail string
	Script     func(name string) string
	Embed      func(n *doc.Node) string
	// Serve serves the page's own files.
	Serve Server
	// Code colors code and Chart draws charts, as the desktop does; nil
	// writes code plain and a chart's numbers as a table.
	Code  func(code, language string) string
	Chart func(n *doc.Node) string
	// Print writes the page to be printed, as doc.HTMLOptions.Print does,
	// with its scripts in it.
	Print bool
}

// PrintCSP is a printed page's policy: it runs only its own two scripts,
// which draw formulas and diagrams, and shows pictures of its own and of
// the web.
var PrintCSP = rendered.ScriptsCSP(rendered.PageMathJS, rendered.PrintDiagramJS) + "; img-src data: https:"

// HTML writes the page c as a web page. Only the page's own files and
// recordings are shown: a block may name any, as one an editor of the page
// wrote could.
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
		Print:  o.Print,
		Image: func(n *doc.Node) string {
			local, ok := strings.CutPrefix(n.Str("url"), "file://")
			if !ok {
				return ""
			}
			// An uploaded file's url is a path on the device that uploaded
			// it: this device keeps its copy elsewhere, or none.
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
				// Another page's file is named as the block names it, and
				// is neither served nor said to be missing.
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
				href = o.Serve.File(path, f.Name)
				detail = FileSize(f.SizeBytes)
				if kind := strings.ToUpper(strings.TrimPrefix(filepath.Ext(f.Name), ".")); kind != "" {
					detail = locales.CardFileDetail(kind, detail)
				}
				return href, name, detail
			}
			if name == "" {
				name = locales.CardFile()
			}
			return "", name, locales.CardNotOnDevice()
		},
		Recording: func(n *doc.Node) doc.RecordingView {
			rec, err := o.Store.Recording(n.Str("recording_id"))
			// Another page's recording shows its block's title alone.
			if err == nil && rec.PageID != o.Page {
				return doc.RecordingView{Title: recording.BlockTitle(n.Str("title"), time.Time{}, false, o.Title)}
			}
			v := doc.RecordingView{Title: recording.BlockTitle(n.Str("title"), rec.CreatedAt, err == nil, o.Title)}
			if err != nil {
				return v
			}
			v.Length = recording.FormatDuration(time.Duration(rec.DurationMs) * time.Millisecond)
			// Audio still being recorded, or not on this device, has no player.
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
		// A printed page is loaded from a string: its scripts are in it,
		// and its policy, as no server sends one, before them.
		opts.Script = nil
		opts.ScriptSource = func(name string) string {
			switch name {
			case "math":
				return rendered.PageMathJS
			case "diagram":
				return rendered.PrintDiagramJS
			}
			return ""
		}
		opts.Head = `<meta http-equiv="Content-Security-Policy" content="` + PrintCSP + `">` + o.Head
	}
	return doc.PageHTML(o.Title, c, opts)
}

// Inline serves a printed page's pictures in the page itself, as data
// URLs, and its files and audio nowhere: paper neither downloads nor
// plays.
var Inline Server = inline{}

type inline struct{}

func (inline) Image(path, typ string) string {
	data, err := os.ReadFile(path) // #nosec G304 -- a picture of the page's own, which HTML checked.
	if err != nil {
		return ""
	}
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func (inline) File(string, string) string { return "" }

func (inline) Audio(string) string { return "" }

// DueText names a to-do's due date for a web page: as the editor does,
// but as a date, not Today or Tomorrow, which go stale while the page
// stays as it was written; "" for none.
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

// FileSize is a file's size as people read it.
func FileSize(n int64) string {
	switch {
	case n >= 1<<30:
		return locales.CardSizeGB(math.Round(float64(n)/(1<<30)*10) / 10)
	case n >= 1<<20:
		return locales.CardSizeMB(math.Round(float64(n)/(1<<20)*10) / 10)
	case n >= 1<<10:
		return locales.CardSizeKB(int(math.Round(float64(n) / (1 << 10))))
	}
	return locales.CardSizeBytes(int(n)) // #nosec G115 -- under a kilobyte here.
}
