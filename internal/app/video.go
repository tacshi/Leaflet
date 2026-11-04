package app

import (
	"log"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/rendered"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

// Video blocks show the video's picture until play is clicked, and then
// its site's player, or a player of the file, in a web view of its own.

// videoSession is the website data of the video players, kept in memory
// only, which goes as the last player closes.
var videoSession = sync.OnceValue(mygo.NewEphemeralSession)

// videoInfo is what a video's site tells about it: its title and picture.
type videoInfo struct{ title, thumbnail string }

// videoKey names a video whatever link to it a block has.
func videoKey(v doc.Video) string { return v.Provider + ":" + v.ID }

// videoInfoOf returns what the video's site said about it, asking it the
// first time. Until it answers, or when it cannot, the info is empty; a
// file, with no site to ask, goes by its name.
func (a *App) videoInfoOf(v doc.Video) videoInfo {
	if v.Provider == "file" {
		return videoInfo{title: v.FileName()}
	}
	key := videoKey(v)
	return a.videoInfo.get(a, key, func() (videoInfo, error) {
		var info videoInfo
		data, err := web.Fetch(v.InfoURL())
		if err == nil {
			info.title, info.thumbnail, err = v.ReadInfo(data)
		}
		if err != nil {
			log.Printf("video %s: %v", v.WatchURL(), err)
		}
		return info, err
	}, func(info videoInfo) { a.nameVideos(key, info.title) })
}

// nameVideos gives the open page's blocks of a video without a title the
// one its site said, for search and exports to have it.
func (a *App) nameVideos(key, title string) {
	if title == "" || a.page == nil || a.page.editor.ReadOnly {
		return
	}
	var ids []string
	a.page.editor.Content.Walk(func(n, _ *doc.Node) {
		if v, ok := doc.ParseVideoURL(n.Str("url")); ok && n.Type == "video" && n.Str("title") == "" && videoKey(v) == key {
			ids = append(ids, n.ID)
		}
	})
	for _, id := range ids {
		a.page.editor.SetData(id, "title", title)
	}
}

func (a *App) videoBlock(c *ui.Context, n *doc.Node, width float32) {
	pal := theme.Of(c)
	v, ok := doc.ParseVideoURL(n.Str("url"))
	if !ok {
		ui.Text(c, locales.VideoNotALink(n.Str("url"))).TextColor(pal.TextMuted)
		return
	}
	block := ui.Column(c.Key("video-block:" + n.ID)).Gap(12).FillWidth()
	hovered := block.Hovered()
	block.Children(func() {
		a.videoPlayer(c, n, v, width)
		a.videoNotes(c, n, hovered)
	})
}

// videoPlayer draws a video's picture, which starts its player, or the
// player once it has.
func (a *App) videoPlayer(c *ui.Context, n *doc.Node, v doc.Video, width float32) {
	height := width * 9 / 16
	if wv := a.videos[n.ID]; wv != nil {
		ui.WebView(c, wv).Height(height).FillWidth().Radius(8)
		return
	}
	info := a.videoInfoOf(v)
	title := n.Str("title")
	if title == "" {
		title = info.title
	}
	if key := videoKey(v); title == "" && a.videoInfo.settled(key) {
		// The site does not know the video, or could not be reached: the
		// link at least says what it was.
		title = n.Str("url")
	}
	thumb := v.Thumbnail()
	if thumb == "" {
		thumb = info.thumbnail
	}
	white := ui.RGB(255, 255, 255)
	poster := ui.ButtonBase(c.Key("video:" + n.ID)).Height(height).FillWidth().Radius(8).Clip().
		Background(ui.RGB(0, 0, 0)).Cursor(ui.CursorPointer).Label(locales.VideoPlay(title))
	hovered := poster.Hovered()
	poster.Children(func() {
		if bmp := a.images.get(thumb, func() { a.update(func() {}) }); bmp != nil {
			ui.Image(c, bmp).Absolute().Top(0).Left(0).Size(width, height).Fit(ui.Cover)
		}
		if title != "" {
			ui.Row(c).Absolute().Top(0).Left(0).Width(width).Padding(14, 16, 32).
				LinearGradient(ui.LinearGradient{From: ui.RGBA(0, 0, 0, 0.6), To: ui.RGBA(0, 0, 0, 0), Angle: 180}).Children(func() {
				ui.Text(c, title).FontSize(15).FontWeight(600).TextColor(white).SingleLine().Grow(1)
			})
		}
		const play = 64
		ui.Row(c).Absolute().Top((height-play)/2).Left((width-play)/2).Size(play, play).Radius(play / 2).
			Background(ui.RGBA(0, 0, 0, map[bool]float32{true: 0.8, false: 0.55}[hovered])).Center().Children(func() {
			ui.Icon(c, theme.Icon("play")).FontSize(26).TextColor(white).Margin(0, 0, 0, 3)
		})
	})
	if poster.Clicked() {
		a.playVideo(n.ID, v)
	}
}

// playVideo puts the video's player in its block, playing.
func (a *App) playVideo(id string, v doc.Video) {
	if a.win == nil {
		return
	}
	// The player's cookies and storage stay in memory, apart from the
	// other web views'. Its videos may float in picture in picture, which
	// macOS otherwise keeps to Safari. The wheel over the player, which
	// does not scroll, scrolls the page around it.
	wv, err := a.win.NewWebView(mygo.WebViewOptions{BackgroundColor: "#000000", ScrollToContainer: true,
		Page: mygo.PageOptions{Session: videoSession(), PictureInPicture: true, UserAgentSuffix: videoAgent()}})
	if err != nil {
		a.toast(locales.VideoCannotPlay(err.Error()))
		return
	}
	page := wv.Page()
	// The player's links, as to the video on its site, open in the
	// browser rather than in the block; what its script opens on its own,
	// as an ad's pop-up, opens nowhere.
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		e.PreventDefault()
		if e.UserInitiated {
			openLink(e.URL)
		}
	})
	page.SetWindowOpenHandler(func(r mygo.WindowOpenRequest) *mygo.WindowOptions {
		if r.UserInitiated {
			openLink(r.URL)
		}
		return nil
	})
	page.LoadHTML(rendered.VideoPage(v), rendered.VideoOrigin)
	a.videos[id] = wv
	a.playAlone(id, "")
	// Started from its own controls, as after a pause, it plays alone too.
	page.OnAudibleChanged(func(audible bool) {
		if audible && a.videos[id] == wv {
			a.playAlone(id, "")
			if a.win != nil {
				a.win.Invalidate()
			}
		}
	})
}

// playAlone keeps one player of the open page playing, the one that starts
// now, as one person listens to one at a time: the video of block video,
// or the recording rec. The others pause.
func (a *App) playAlone(video, rec string) {
	for id := range a.videos {
		if id != video {
			a.pauseVideo(id)
		}
	}
	for id, pl := range a.players {
		if id != rec && pl.p != nil && pl.p.Playing() {
			pl.p.Pause()
		}
	}
}

// pauseVideo pauses the player of video block id, as its site's player is
// told, or, one whose site's player takes no word from the page, by going
// back to the video's picture, which plays it again.
func (a *App) pauseVideo(id string) {
	wv := a.videos[id]
	if wv == nil {
		return
	}
	var v doc.Video
	if a.page != nil {
		if n := a.page.editor.Content.Find(id); n != nil {
			v, _ = doc.ParseVideoURL(n.Str("url"))
		}
	}
	if script := rendered.VideoPause(v); script != "" {
		page := wv.Page()
		// Eval waits for the page, so not on the UI thread.
		go func() { _, _ = page.Eval(script) }()
		return
	}
	wv.Destroy()
	delete(a.videos, id)
}

// seekVideo plays video block id of the open page from sec seconds in:
// its player goes there, or loads again there where its site's can't be
// told to, or starts there if it has not.
func (a *App) seekVideo(id string, sec int) {
	if a.page == nil {
		return
	}
	n := a.page.editor.Content.Find(id)
	if n == nil || n.Type != "video" {
		return
	}
	v, ok := doc.ParseVideoURL(n.Str("url"))
	if !ok {
		return
	}
	wv := a.videos[id]
	if wv == nil {
		a.playVideo(id, v.At(sec))
		return
	}
	a.playAlone(id, "")
	page := wv.Page()
	if script := rendered.VideoSeek(v, sec); script != "" {
		// Eval waits for the page, so not on the UI thread.
		go func() { _, _ = page.Eval(script) }()
		return
	}
	page.LoadHTML(rendered.VideoPage(v.At(sec)), rendered.VideoOrigin)
}

// noteLines are a video's notes in lines, read once for each text of them
// as the block is drawn each frame.
func (a *App) noteLines(notes string) []doc.NoteLine {
	if lines, ok := a.notesRead[notes]; ok {
		return lines
	}
	// What other notes the open pages had goes, now and then.
	if a.notesRead == nil || len(a.notesRead) > 64 {
		a.notesRead = map[string][]doc.NoteLine{}
	}
	lines := doc.NoteLines(notes)
	a.notesRead[notes] = lines
	return lines
}

// videoNotes draws a video's notes under its player, as a recording's
// transcript is drawn: a line's time beside it, and a time in its text,
// play the video from there. Long notes show their first lines until
// opened. While the block is hovered, a page that can be changed offers
// to edit them.
func (a *App) videoNotes(c *ui.Context, n *doc.Node, hovered bool) {
	pal := theme.Of(c)
	lines := a.noteLines(n.Str("notes"))
	if len(lines) == 0 {
		return
	}
	long := doc.FoldsTranscript(len(lines))
	open := a.notesOpen[n.ID]
	shown := lines
	if long && !open {
		shown = lines[:doc.FoldedLines]
	}
	// The times' column is as wide as the longest of them all, shown or
	// folded away.
	var times []string
	for _, l := range lines {
		if l.At >= 0 {
			times = append(times, doc.Clock(l.At))
		}
	}
	col := timeColumn(c, times, 13)
	play := func(at int) { a.seekVideo(n.ID, at) }
	// In the page's text size, as its text.
	size := float32(a.settings.TextSize)
	text := func(l doc.NoteLine) bool { return timedText(c, l.Parts, size, pal.Text, play) }
	// A click on a line plays from it, so its text is not selectable; the
	// notes copy from their dialog. They are in a panel of their own, as a
	// video's description is on its site, tinted as a code block is.
	ui.Column(c.Key("notes:"+n.ID)).Gap(4).Padding(10, 12).Radius(10).Background(pal.Code).Children(func() {
		for i, l := range shown {
			var row ui.Element
			if l.At < 0 {
				row = ui.Row(c.Key(i)).Children(func() { text(l) })
			} else {
				row = timedRow(c, i, doc.Clock(l.At), col, 13, func() bool { return text(l) }, func() { play(l.At) })
			}
			row.PopoverMenu(func(m *ui.Menu) { a.noteMenu(c, m, n, l) })
		}
		if long && foldButton(c, len(lines), open, col) {
			if a.notesOpen == nil {
				a.notesOpen = map[string]bool{}
			}
			a.notesOpen[n.ID] = !open
		}
		if !a.canChangePage() {
			return
		}
		// In the panel's corner, over the first line's end, as a chart's
		// Edit data is in its head.
		edit := ui.ButtonBase(c.Key("edit")).Absolute().Top(8).Right(8).Height(24).Padding(0, 8).Radius(6).
			Background(pal.Surface).Border(1, pal.Divider).TextColor(pal.TextMuted).Label(locales.VideoEditNotes()).
			Opacity(map[bool]float32{true: 1, false: 0}[hovered])
		if edit.Hovered() {
			edit.Background(pal.Hover)
		}
		edit.Children(func() {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, theme.Icon("pencil")).FontSize(12)
				ui.Text(c, locales.VideoEditNotes()).FontSize(12)
			})
		})
		if edit.Clicked() {
			a.dialogs.openVideoNotes(n)
		}
	})
}

// canChangePage reports whether the open page can be changed here.
func (a *App) canChangePage() bool {
	return a.page != nil && !a.page.editor.ReadOnly && a.canEdit(a.page.id)
}

// noteMenu is the menu of a line of a video's notes: a link to the moment
// it is about, to put in another page or a chat, and the video's own.
func (a *App) noteMenu(c *ui.Context, m *ui.Menu, n *doc.Node, l doc.NoteLine) {
	at := l.At
	for _, p := range l.Parts {
		if at < 0 && p.At >= 0 {
			at = p.At
		}
	}
	if at >= 0 && a.page != nil {
		if m.Item(locales.VideoCopyMoment(doc.Clock(at))).Chosen() {
			c.WriteClipboard(doc.MomentLink(a.page.id, n.ID, at))
			c.Toast(locales.EditorCopiedBlockLink())
		}
		m.Separator()
	}
	a.videoMenu(m, n)
}

// videoMenu adds a video block's own items to a menu of it: its notes,
// and an agent's summary of them from what is said in the video, which
// it reads where the video's site gives its captions.
func (a *App) videoMenu(m *ui.Menu, n *doc.Node) {
	edit := a.canChangePage()
	label := locales.VideoAddNotes()
	if strings.TrimSpace(n.Str("notes")) != "" {
		label = locales.VideoEditNotes()
	}
	if m.Item(label).Disabled(!edit).Chosen() {
		a.dialogs.openVideoNotes(n)
	}
	v, ok := doc.ParseVideoURL(n.Str("url"))
	if ok && web.HasCaptions(v) {
		if m.Item(locales.VideoSummarize()).Disabled(!edit || !a.hub.Available()).Chosen() {
			title := n.Str("title")
			if title == "" {
				title = a.videoInfoOf(v).title
			}
			if title == "" {
				title = n.Str("url")
			}
			a.panelOpen, a.panelTab = true, 0
			a.hub.Chat(a.page.id, locales.VideoSummarizePrompt(title), []string{n.ID})
		}
	}
}

// openVideoNotes opens the dialog that writes the notes of video block n.
func (d *dialogs) openVideoNotes(n *doc.Node) {
	d.notesOpen, d.notesBlock, d.notesText = true, n.ID, n.Str("notes")
}

// videoNotesDialog writes the notes of a video block of the open page, as
// one step to undo.
func (a *App) videoNotesDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.notesOpen, 560, func() {
		dialogHeader(c, locales.VideoNotesTitle(), locales.VideoNotesHint(), &d.notesOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			ui.TextArea(c.Key("notes"), &d.notesText).Height(260).FontSize(14).Font(theme.PageFamily(d.notesText)).
				Placeholder(locales.VideoNotesPlaceholder()).AutoFocus()
			if over := utf8.RuneCountInString(doc.NormalizeNotes(d.notesText)) - doc.MaxNotes; over > 0 {
				ui.Text(c, locales.VideoNotesTooLong(doc.MaxNotes)).FontSize(12.5).TextColor(pal.DangerText)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.notesOpen = false
				}
				notes := doc.NormalizeNotes(d.notesText)
				if primary(c, "save", "", locales.CommonSave()).Disabled(utf8.RuneCountInString(notes) > doc.MaxNotes).Clicked() {
					d.notesOpen = false
					a.setVideoNotes(d.notesBlock, notes)
				}
			})
		})
	})
}

// setVideoNotes writes the notes of video block id of the open page, or
// takes them away for none, as one step to undo.
func (a *App) setVideoNotes(id, notes string) {
	if !a.canChangePage() {
		return
	}
	n := a.page.editor.Content.Find(id)
	if n == nil || n.Type != "video" || n.Str("notes") == notes {
		return
	}
	a.page.editor.Edit(func(content *doc.Content) {
		if n := content.Find(id); n != nil {
			if notes == "" {
				delete(n.Data, "notes")
			} else {
				n.Set("notes", notes)
			}
		}
	})
}

// videoAgent is what the players' web view adds to its user agent: on a
// Mac, Safari's, which WebKit's own lacks. Vimeo's player refuses to play
// in a browser it doesn't know, and asks for a supported one.
func videoAgent() string {
	if runtime.GOOS == "darwin" {
		return rendered.SafariAgent
	}
	return ""
}

// dropVideos stops the players of videos no longer in the open page.
func (a *App) dropVideos() {
	for id, v := range a.videos {
		if a.page == nil || a.page.editor.Content.Find(id) == nil {
			v.Destroy()
			delete(a.videos, id)
		}
	}
}

// openLink opens a web link in the browser.
func openLink(u string) {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		go func() { _ = mygo.Shell.OpenExternal(u) }()
	}
}
