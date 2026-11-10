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

type Server interface {
	Image(path, typ string) string
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
}

var PrintCSP = rendered.ScriptsCSP(rendered.PageMathJS, rendered.PrintDiagramJS) + "; img-src data: https:"

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

var Inline Server = inline{}

type inline struct{}

func (inline) Image(path, typ string) string {
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return ""
	}
	if small, smallTyp, ok := forPrint(data); ok {
		data, typ = small, smallTyp
	}
	return "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(data)
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

func FileSize(n int64) string {
	switch {
	case n >= 1<<30:
		return locales.CardSizeGB(math.Round(float64(n)/(1<<30)*10) / 10)
	case n >= 1<<20:
		return locales.CardSizeMB(math.Round(float64(n)/(1<<20)*10) / 10)
	case n >= 1<<10:
		return locales.CardSizeKB(int(math.Round(float64(n) / (1 << 10))))
	}
	return locales.CardSizeBytes(int(n)) // #nosec G115
}
