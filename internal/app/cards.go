package app

import (
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/internal/webpage"
	"leaflet/locales"
)

type webAnswers[T any] struct {
	answers map[string]T
	asking  map[string]bool
	failed  map[string]time.Time
}

func (w *webAnswers[T]) get(a *App, key string, ask func() (T, error), got func(T)) T {
	var zero T
	if v, ok := w.answers[key]; ok {
		return v
	}
	if w.asking[key] || time.Since(w.failed[key]) < imageRetry {
		return zero
	}
	if w.answers == nil {
		w.answers, w.asking, w.failed = map[string]T{}, map[string]bool{}, map[string]time.Time{}
	}
	w.asking[key] = true
	var v T
	var err error
	a.offMain(func() { v, err = ask() }, func() {
		delete(w.asking, key)
		if err != nil {
			w.failed[key] = time.Now()
			a.later(imageRetry, func() {})
			return
		}
		delete(w.failed, key)
		w.answers[key] = v
		got(v)
	})
	return zero
}

func (w *webAnswers[T]) settled(key string) bool {
	_, answered := w.answers[key]
	_, failed := w.failed[key]
	return answered || failed
}

func (a *App) linkInfoOf(link string) web.LinkInfo {
	pageID := ""
	if a.page != nil {
		pageID = a.page.id
	}
	return a.linkInfo.get(a, link, func() (web.LinkInfo, error) {
		info, err := web.LookUp(link)
		if err != nil {
			log.Printf("link %s: %v", link, err)
		}
		return info, err
	}, func(info web.LinkInfo) { a.keepLinkInfo(pageID, link, info) })
}

func (a *App) keepLinkInfo(pageID, link string, info web.LinkInfo) {
	if info.Title == "" || !a.canEdit(pageID) {
		return
	}
	if ed := a.openEditor(pageID); ed != nil {
		if ed.ReadOnly {
			return
		}
		var ids []string
		ed.Content.Walk(func(n, _ *doc.Node) {
			if web.Lacking(n, link) {
				ids = append(ids, n.ID)
			}
		})
		for _, id := range ids {
			for k, v := range info.Data() {
				ed.SetData(id, k, v)
			}
		}
		return
	}
	err := a.pageCore().EditPage(pageID, func(c *doc.Content) bool { return info.Fill(c, link) })
	if err != nil {
		log.Printf("link %s: %v", link, err)
	}
}

func cardButton(c *ui.Context, key, label string) ui.Element {
	pal := theme.Of(c)
	b := ui.ButtonBase(c.Key(key)).FillWidth().Radius(8).Border(1, pal.Divider).Background(pal.Surface).
		Cursor(ui.CursorPointer).AlignItems(ui.Center).Clip().Label(label)
	if b.Hovered() {
		b.Background(pal.Hover.Over(pal.Surface))
	}
	return b
}

func (a *App) linkPreviewBlock(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	link := n.Str("url")
	info := web.LinkInfo{Title: n.Str("title"), Description: n.Str("description"), Image: n.Str("image"), Site: n.Str("site")}
	if info.Title == "" {
		info = a.linkInfoOf(link)
	}
	site := info.Site
	if u, err := url.Parse(link); err == nil && site == "" {
		site = strings.TrimPrefix(u.Hostname(), "www.")
	}
	title := info.Title
	if title == "" {
		title = link
	}
	height := float32(64)
	if info.Description != "" || info.Image != "" {
		height = 96
	}
	b := cardButton(c, "link:"+n.ID, title).Height(height)
	b.Children(func() {
		ui.Column(c).Grow(1).FillHeight().Padding(12, 14).Gap(4).Children(func() {
			ui.Text(c, title).FontWeight(600).FontSize(14.5).TextColor(pal.Text).SingleLine()
			if info.Description != "" {
				ui.Text(c, info.Description).FontSize(13).TextColor(pal.TextMuted).MaxLines(2)
			}
			ui.Spacer(c)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, theme.Icon("globe")).FontSize(12).TextColor(pal.TextMuted)
				ui.Text(c, site).FontSize(12).TextColor(pal.TextMuted).SingleLine()
			})
		})
		if info.Image != "" {
			if bmp := a.images.get(info.Image, func() { a.update(func() {}) }); bmp != nil {
				ui.Image(c, bmp).Size(150, 96).Fit(ui.Cover)
			}
		}
	})
	if b.Clicked() {
		openLink(link)
	}
}

func humanSize(n int64) string { return webpage.FileSize(n) }

func audioFile(n *doc.Node) bool {
	return n.Type == "file" && doc.FilePreviewKind(n.Str("name"), n.Str("mime")) == doc.FileAudio
}

func (a *App) audioBlock(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	id := n.ID
	file, found := a.fileByID(n.Str("file_id"))
	name := n.Str("name")
	if name == "" {
		name = locales.CardFile()
	}
	failed := a.fileFailed[id]
	heard := a.now.block == id && a.videos[id] != nil
	ui.Column(c.Key("file-block:"+id)).FillWidth().Padding(14, 16).Gap(12).Radius(10).Border(1, pal.Divider).Background(pal.Surface).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(30, 30).Radius(999).Center().Background(pal.AccentSoft).Children(func() {
				ui.Icon(c, theme.Icon("audio-lines")).FontSize(15).TextColor(pal.AccentText)
			})
			ui.Column(c).Grow(1).Children(func() {
				ui.Text(c, name).FontSize(14).FontWeight(600).TextColor(pal.Text).SingleLine()
				status := locales.CardNotOnDevice()
				switch {
				case a.rec.TranscribingFiles[id] != "":
					status = locales.RecTranscribing()
				case found:
					status = fileDetail(file.Name, file.SizeBytes)
					if engine := recording.EngineName(n.Str(doc.TranscribedBy)); engine != "" && strings.TrimSpace(n.Str("notes")) != "" {
						status = locales.CardTranscribedBy(status, engine)
					}
				}
				ui.Text(c, status).FontSize(12).TextColor(pal.TextMuted)
			})
			var more ui.Element
			tools(c, func() {
				icon, label := "play", locales.CardPlayFile(name)
				switch {
				case failed:
					icon, label = "paperclip", locales.CardOpenFile(name)
				case heard && !a.fileNow.paused:
					icon, label = "pause", locales.CommonPause()
				}
				play := iconButton(c, "play", icon, label).Disabled(!found)
				if play.Clicked() {
					if failed {
						a.openFile(file)
					} else {
						a.toggleFile(n)
					}
				}
				more = iconButton(c, "more", "ellipsis", locales.CardActions())
			})
			more.PopoverMenuButton(func(m *ui.Menu) { a.fileMenu(c, m, n) }, endMenu)
		})
		if failed {
			ui.Text(c, locales.CardCannotPlay()).FontSize(12.5).TextColor(pal.TextMuted)
		}
		if heard {
			sec, length, _ := a.playhead(c, id)
			a.playerBar(c, seconds(sec), seconds(length), func(t time.Duration) { a.seekFile(id, t) })
		}
		a.videoNotes(c, n, false)
	})
}

func (a *App) fileBlock(c *ui.Context, n *doc.Node, width float32) {
	if audioFile(n) {
		a.audioBlock(c, n)
		return
	}
	block := ui.Column(c.Key("file-block:" + n.ID)).Gap(12).FillWidth()
	hovered := block.Hovered()
	block.Children(func() {
		a.fileHead(c, n, width, hovered)
		a.videoNotes(c, n, hovered)
	})
}

func (a *App) fileHead(c *ui.Context, n *doc.Node, width float32, hovered bool) {
	pal := theme.Of(c)
	file, found := store.File{}, false
	for _, f := range a.files {
		if f.ID == n.Str("file_id") {
			file, found = f, true
			break
		}
	}
	if !found {
		name := n.Str("name")
		if name == "" {
			name = locales.CardFile()
		}
		a.fileCard(c, cardButton(c, "file:"+n.ID, name).Opacity(0.7), "paperclip", name, -1, pal)
		return
	}
	kind := doc.FilePreviewKind(file.Name, file.MimeType)
	media := kind == doc.FileAudio || kind == doc.FileVideo
	wv := a.videos[n.ID]
	failed := a.fileFailed[n.ID]
	icon, label := "paperclip", locales.CardOpenFile(file.Name)
	if media && !failed {
		icon, label = "play", locales.CardPlayFile(file.Name)
	}
	if wv == nil || kind != doc.FileVideo {
		ui.Row(c.Key("file-head")).FillWidth().Children(func() {
			b := cardButton(c, "file:"+n.ID, label)
			a.fileCard(c, b, icon, file.Name, file.SizeBytes, pal)
			if b.Clicked() {
				switch {
				case failed:
					a.openFile(file)
				case media:
					a.playFile(n, 0)
				default:
					a.previewFile(file)
				}
			}
			more := iconButton(c, "more", "ellipsis", locales.CardActions()).Absolute().Top(10).Right(10).
				Opacity(map[bool]float32{true: 1, false: 0}[hovered])
			more.PopoverMenuButton(func(m *ui.Menu) { a.fileMenu(c, m, n) }, endMenu)
		})
	}
	if failed {
		ui.Text(c, locales.CardCannotPlay()).FontSize(12.5).TextColor(pal.TextMuted)
	}
	if a.rec.TranscribingFiles[n.ID] != "" {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(12, 12)
			ui.Text(c, locales.RecTranscribing()).FontSize(12.5).TextColor(pal.TextMuted)
		})
	}
	if wv != nil {
		ui.WebView(c, wv).Height(width * 9 / 16).FillWidth().Radius(8)
	}
}

func (a *App) fileMenu(c *ui.Context, m *ui.Menu, n *doc.Node) {
	file, found := a.fileByID(n.Str("file_id"))
	if m.Item(locales.CardOpenExternally()).Disabled(!found).Chosen() {
		a.openFile(file)
	}
	label := locales.VideoAddNotes()
	if strings.TrimSpace(n.Str("notes")) != "" {
		label = locales.VideoEditNotes()
	}
	if m.Item(label).Disabled(!a.canChangePage()).Chosen() {
		a.dialogs.openVideoNotes(n)
	}
	if doc.HasSpeakers(n.Str("notes"), doc.SpeakerNamesOf(n)) {
		hidden := n.Bool(doc.SpeakersHidden)
		label := locales.CardHideSpeakers()
		if hidden {
			label = locales.CardShowSpeakers()
		}
		if m.Item(label).Disabled(!a.canChangePage()).Chosen() {
			a.setSpeakersHidden(n.ID, !hidden)
		}
	}
	if found && strings.TrimSpace(n.Str("notes")) != "" {
		if kind := doc.FilePreviewKind(file.Name, file.MimeType); kind == doc.FileAudio || kind == doc.FileVideo {
			a.transcriptMenu(c, m, n, file.Name)
		}
	}
	if !found || !doc.Transcribable(file.Name, file.MimeType) {
		return
	}
	notes := strings.TrimSpace(n.Str("notes")) != ""
	label = locales.RecTranscribe()
	if notes {
		label = locales.RecTranscribeAgain()
	}
	a.transcribeItem(m, a.openPageID(), label, !a.canChangePage() || a.rec.TranscribingFiles[n.ID] != "", func(provider string) {
		if notes {
			a.fileToTranscribe, a.fileTranscribeWith = n.ID, provider
		} else {
			a.transcribeFile(n.ID, provider)
		}
	})
}

func (a *App) setSpeakersHidden(id string, hidden bool) {
	if !a.canChangePage() {
		return
	}
	a.page.editor.Edit(func(content *doc.Content) {
		if n := content.Find(id); n != nil {
			if hidden {
				n.Set(doc.SpeakersHidden, true)
			} else {
				delete(n.Data, doc.SpeakersHidden)
			}
		}
	})
}

func (a *App) transcriptMenu(c *ui.Context, m *ui.Menu, n *doc.Node, name string) {
	notes, names, hidden := n.Str("notes"), doc.SpeakerNamesOf(n), n.Bool(doc.SpeakersHidden)
	if m.Item(locales.RecCopyTranscript()).Chosen() {
		c.WriteClipboard(doc.NotesText(notes, names, hidden))
		c.Toast(locales.CommonCopied())
	}
	edit := a.canChangePage()
	if m.Item(locales.RecInsertTranscript()).Disabled(!edit).Chosen() {
		nodes := doc.NotesBlocks(notes, names, hidden)
		a.page.editor.Edit(func(content *doc.Content) { _ = content.Insert(doc.Position{After: n.ID}, nodes...) })
	}
	if m.Item(locales.RecSummarize()).Disabled(!edit || !a.hub.Available()).Chosen() {
		a.openPageAgent()
		a.hub.Chat(a.page.id, locales.RecSummarizePrompt(name), []string{n.ID})
	}
	if m.Item(locales.CardFollowAlong()).Checked(a.followOn()).Chosen() {
		a.setFollow(!a.followOn())
	}
}

func (a *App) speakerMenu(m *ui.Menu, n *doc.Node, l doc.NoteLine) {
	speakers := doc.NoteSpeakers(n.Str("notes"), doc.SpeakerNamesOf(n))
	if len(speakers) == 0 || !a.canChangePage() {
		return
	}
	m.Submenu(locales.CardChangeSpeaker(), func(m *ui.Menu) {
		for _, s := range speakers {
			if m.Item(s).Checked(s == l.Speaker).Chosen() {
				a.editSpeakers(n.ID, func(notes string, names []string) (string, []string) {
					return doc.SetLineSpeaker(notes, names, l.Line, s)
				})
			}
		}
		m.Separator()
		if m.Item(locales.CardNewSpeaker()).Chosen() {
			a.dialogs.openSpeaker(n.ID, "", l.Line)
		}
	})
	if l.Speaker != "" && m.Item(locales.CardRenameSpeaker(l.Speaker)).Chosen() {
		a.dialogs.openSpeaker(n.ID, l.Speaker, l.Line)
	}
	m.Separator()
}

func (a *App) editSpeakers(id string, edit func(notes string, names []string) (string, []string)) {
	if !a.canChangePage() {
		return
	}
	a.page.editor.Edit(func(content *doc.Content) {
		if n := content.Find(id); n != nil && n.Type == "file" {
			notes, names := edit(n.Str("notes"), doc.SpeakerNamesOf(n))
			n.Set("notes", notes)
			doc.SetSpeakerNames(n, names)
		}
	})
}

func (d *dialogs) openSpeaker(block, from string, line int) {
	d.speakerOpen, d.speakerBlock, d.speakerFrom, d.speakerLine, d.speakerName = true, block, from, line, from
}

func (a *App) speakerDialog(c *ui.Context) {
	d := &a.dialogs
	title := locales.CardNewSpeaker()
	if d.speakerFrom != "" {
		title = locales.CardRenameSpeaker(d.speakerFrom)
	}
	dialog(c, &d.speakerOpen, 380, func() {
		dialogHeader(c, title, "", &d.speakerOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			save := func() {
				d.speakerOpen = false
				block, from, line, name := d.speakerBlock, d.speakerFrom, d.speakerLine, d.speakerName
				a.editSpeakers(block, func(notes string, names []string) (string, []string) {
					if from != "" {
						return doc.RenameSpeaker(notes, names, from, name)
					}
					return doc.SetLineSpeaker(notes, names, line, name)
				})
			}
			in := field(c, "speaker", &d.speakerName, locales.CardSpeakerName()).AutoFocus()
			if in.Submitted() {
				save()
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.speakerOpen = false
				}
				if primary(c, "save", "", locales.CommonSave()).Disabled(doc.CleanSpeakerName(d.speakerName) == "").Clicked() {
					save()
				}
			})
		})
	})
}

func (a *App) transcribeFile(id, provider string) {
	if a.page == nil {
		return
	}
	if err := a.pageCore().TranscribeFile(a.page.id, id, a.sttFor(a.page.id, provider), a.online && !a.settings.WorkOffline); err != nil {
		a.toast(err.Error())
	}
}

func (a *App) replaceNotesAlert(c *ui.Context) {
	if a.fileToTranscribe == "" {
		return
	}
	open := true
	if ui.AlertDialog(c, &open, locales.RecReplaceNotesTitle(), locales.RecReplaceNotesMessage(),
		locales.CommonCancel(), locales.RecReplaceNotesAction()) == 1 {
		a.transcribeFile(a.fileToTranscribe, a.fileTranscribeWith)
	}
	if !open {
		a.fileToTranscribe = ""
	}
}

func (a *App) fileCard(c *ui.Context, b ui.Element, icon, name string, size int64, pal *theme.Palette) {
	b.Padding(10, 14).Gap(12).Children(func() {
		ui.Row(c).Size(36, 36).Center().Radius(8).Background(pal.Ground).Children(func() {
			ui.Icon(c, theme.Icon(icon)).FontSize(17).TextColor(pal.Icon)
		})
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, name).FontWeight(600).FontSize(14).TextColor(pal.Text).SingleLine()
			detail := locales.CardNotOnDevice()
			if size >= 0 {
				detail = fileDetail(name, size)
			}
			ui.Text(c, detail).FontSize(12).TextColor(pal.TextMuted)
		})
	})
}

func fileDetail(name string, size int64) string {
	kind := strings.ToUpper(strings.TrimPrefix(filepath.Ext(name), "."))
	if kind == "" {
		return humanSize(size)
	}
	return locales.CardFileDetail(kind, humanSize(size))
}
