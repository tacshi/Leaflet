package app

import (
	"context"
	"errors"
	"log"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/guard"
	"leaflet/internal/mediaserver"
	"leaflet/internal/rendered"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

// Video blocks show the video's picture until play is clicked, and then
// its site's player, or a player of the file, in a web view of its own.

// videoSession is the website data of the videos' sites' players, which
// goes as the first of them opens and as the last closes. It isn't kept in
// memory alone: a web view keeps what plays in such a session from the
// system, which then shows its own name in place of the video's.
var videoSession = sync.OnceValue(func() *mygo.Session {
	s := mygo.NewSession("video-player")
	clearVideoData(s)
	return s
})

// clearVideoData lets go of what s's sites stored.
func clearVideoData(s *mygo.Session) {
	if err := s.ClearData(mygo.SiteDataAll); err != nil {
		log.Printf("video players' data: %v", err)
	}
}

// fileSession is the website data of the players of files, which have none,
// as their pages run no script. It isn't kept in memory as a video's is: a
// web view keeps what plays in such a session from the system, which then
// shows its own name in place of the file's.
var fileSession = sync.OnceValue(func() *mygo.Session { return mygo.NewSession("file-player") })

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
		if v.Provider == "bilibili" {
			// Its part, which its player then plays given, says them.
			_, p := web.VideoPart(v)
			if p.CID == 0 {
				return info, errors.New("bilibili did not list it")
			}
			return videoInfo{title: p.Title, thumbnail: p.Picture}, nil
		}
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
	// The player's cookies and storage stay apart from the other web
	// views', until it closes. Its videos may float in picture in picture, which
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
	page.LoadHTML(rendered.VideoPage(web.KnownVideoPart(v)), rendered.VideoOrigin)
	a.videos[id] = wv
	a.sitePlayers = true
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

// fileVideo is an audio or video file's player, as the rendered package's
// scripts tell one.
var fileVideo = doc.Video{Provider: "file"}

// playFile puts file block n's player in its block, playing from at
// seconds in: it comes first, where it is on another device. A file the
// web view can't play says so in its block, which then opens it in
// another app.
func (a *App) playFile(n *doc.Node, at int) {
	file, ok := a.fileByID(n.Str("file_id"))
	if !ok || a.win == nil {
		return
	}
	id := n.ID
	kind := doc.FilePreviewKind(file.Name, file.MimeType)
	if kind != doc.FileAudio && kind != doc.FileVideo {
		return
	}
	delete(a.downloadFails, file.ID)
	a.lost(file.LocalPath)
	path := a.localFile(file, func(ok bool) {
		if ok && a.page != nil && a.page.editor.HasBlock(id) && a.videos[id] == nil {
			if n := a.page.editor.Content.Find(id); n != nil {
				a.playFile(n, at)
			}
		}
	})
	if path == "" {
		a.toast(locales.HostDownloading(file.Name))
		return
	}
	address, err := a.media.Register(mediaserver.Item{Path: path, Type: doc.FileMediaType(file.Name, file.MimeType),
		Page: rendered.FilePlayerPage(kind == doc.FileVideo, at), Policy: rendered.FilePlayerCSP,
		Cover: leafletCover, CoverType: "image/png"})
	if err != nil {
		a.toast(locales.VideoCannotPlay(err.Error()))
		return
	}
	// A loopback page is the app's own to MyGo in a development build:
	// Untrusted keeps it from being one, as its file is anyone's.
	wv, err := a.win.NewWebView(mygo.WebViewOptions{BackgroundColor: mediaBackground(kind), ScrollToContainer: true,
		Page: mygo.PageOptions{Session: fileSession(), PictureInPicture: true, Untrusted: true, Autoplay: mygo.AutoplayAllow}})
	if err != nil {
		a.media.Revoke(address)
		a.toast(locales.VideoCannotPlay(err.Error()))
		return
	}
	page := wv.Page()
	page.OnWillNavigate(func(e *mygo.NavigateEvent) { e.PreventDefault() })
	page.SetWindowOpenHandler(func(mygo.WindowOpenRequest) *mygo.WindowOptions { return nil })
	nameFile(page, file.Name)
	if err := page.LoadURL(address); err != nil {
		wv.Destroy()
		a.media.Revoke(address)
		a.toast(locales.VideoCannotPlay(err.Error()))
		return
	}
	a.videos[id] = wv
	if a.fileMedia == nil {
		a.fileMedia = map[string]string{}
	}
	a.fileMedia[id] = address
	delete(a.fileFailed, id)
	a.playAlone(id, "")
	page.OnAudibleChanged(func(audible bool) {
		if audible && a.videos[id] == wv {
			a.playAlone(id, "")
			if a.win != nil {
				a.win.Invalidate()
			}
		}
	})
	a.watchPlayer(id, wv, kind == doc.FileAudio)
}

// nameFile has the player page is about to load tell the system its file's
// name, once it has loaded.
func nameFile(page *mygo.Page, name string) {
	page.OnDidFinishLoad(func() {
		// Eval waits for the page, so not on the UI thread.
		go func() { _, _ = page.Eval(rendered.FileMetadata(name)) }()
	})
}

// mediaBackground is the color behind a file's player: a video's black, or
// none for an audio bar.
func mediaBackground(kind string) string {
	if kind == doc.FileVideo {
		return "#000000"
	}
	return "#00000000"
}

// playerPoll is how often a file's player is asked how it is.
const playerPoll = 500 * time.Millisecond

// watchPlayer asks a file's player how it is as it plays: one that can't
// read its file shows nothing to say so, so its block does, and where
// audio has got to is for the player under the page.
func (a *App) watchPlayer(id string, wv *mygo.WebView, audio bool) {
	page := wv.Page()
	stop := make(chan struct{})
	if a.fileWatch == nil {
		a.fileWatch = map[string]chan struct{}{}
	}
	a.fileWatch[id] = stop
	guard.Go(func() {
		var last fileStatus
		for {
			select {
			case <-stop:
				return
			case <-time.After(playerPoll):
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			raw, err := page.EvalContext(ctx, rendered.FilePlayerStatus)
			cancel()
			said, ok := raw.(map[string]any)
			if err != nil || !ok {
				continue
			}
			if failed, _ := said["failed"].(bool); failed {
				a.update(func() {
					if a.videos[id] != wv {
						return
					}
					a.closePlayer(id)
					if a.fileFailed == nil {
						a.fileFailed = map[string]bool{}
					}
					a.fileFailed[id] = true
				})
				return
			}
			if !audio {
				continue
			}
			var st fileStatus
			st.at, _ = said["at"].(float64)
			st.length, _ = said["length"].(float64)
			st.paused, _ = said["paused"].(bool)
			st.rate, _ = said["rate"].(float64)
			if st != last {
				last = st
				a.update(func() {
					if a.now.block == id {
						a.fileNow, a.fileNowAt = st, time.Now()
					}
					// Loading a file takes it back to its own speed.
					if st.rate != a.speed() {
						a.tellSpeed(wv)
					}
				})
			}
		}
	})
}

// playAlone keeps one player of the open page playing, the one that starts
// now, as one person listens to one at a time: the video of block video,
// or the recording rec. The others pause.
func (a *App) playAlone(video, rec string) {
	a.setNowPlaying(video, rec)
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
		if n := a.page.editor.Content.Find(id); n != nil && n.Type == "file" {
			v = fileVideo
		} else if n != nil {
			v, _ = doc.ParseVideoURL(n.Str("url"))
		}
	}
	if script := rendered.VideoPause(v); script != "" {
		page := wv.Page()
		// Eval waits for the page, so not on the UI thread.
		go func() { _, _ = page.Eval(script) }()
		return
	}
	a.closePlayer(id)
}

// closePlayer takes away the player of block id, and for a file what
// served it.
func (a *App) closePlayer(id string) {
	if wv := a.videos[id]; wv != nil {
		wv.Destroy()
		delete(a.videos, id)
	}
	if address, ok := a.fileMedia[id]; ok {
		a.media.Revoke(address)
		delete(a.fileMedia, id)
	}
	if stop := a.fileWatch[id]; stop != nil {
		close(stop)
		delete(a.fileWatch, id)
	}
	if a.now.block == id {
		a.now = nowPlaying{}
	}
	a.clearSitePlayers()
}

// clearSitePlayers lets go of what the videos' sites stored, once none of
// their players is left.
func (a *App) clearSitePlayers() {
	if !a.sitePlayers {
		return
	}
	for id := range a.videos {
		if a.fileMedia[id] == "" {
			return
		}
	}
	a.sitePlayers = false
	// It waits for the web views, so not on the UI thread.
	go clearVideoData(videoSession())
}

// seekVideo plays video block id of the open page from sec seconds in:
// its player goes there, or loads again there where its site's can't be
// told to, or starts there if it has not.
func (a *App) seekVideo(id string, sec int) {
	if a.page == nil {
		return
	}
	n := a.page.editor.Content.Find(id)
	if n == nil {
		return
	}
	var v doc.Video
	ok := false
	switch n.Type {
	case "video":
		v, ok = doc.ParseVideoURL(n.Str("url"))
	case "file":
		v, ok = fileVideo, true
	}
	if !ok {
		return
	}
	wv := a.videos[id]
	if wv == nil {
		if n.Type == "file" {
			a.playFile(n, sec)
		} else {
			a.playVideo(id, v.At(sec))
		}
		return
	}
	a.playAlone(id, "")
	page := wv.Page()
	if script := rendered.VideoSeek(v, sec); script != "" {
		// Eval waits for the page, so not on the UI thread.
		go func() { _, _ = page.Eval(script) }()
		return
	}
	page.LoadHTML(rendered.VideoPage(web.KnownVideoPart(v.At(sec))), rendered.VideoOrigin)
}

// readNotes are a block's notes read in lines, and the longest of their
// times, for the column they stand in.
type readNotes struct {
	notes  string
	hidden bool
	names  []string
	lines  []doc.NoteLine
	widest string
}

// noteLines reads block id's notes, once for each text of them: the block
// is drawn each frame, and a transcript has thousands of lines.
func (a *App) noteLines(n *doc.Node) readNotes {
	notes, hidden := n.Str("notes"), n.Bool(doc.SpeakersHidden)
	if r, ok := a.notesRead[n.ID]; ok && r.notes == notes && r.hidden == hidden && slices.Equal(r.names, doc.SpeakerNamesOf(n)) {
		return r
	}
	r := readNotes{notes: notes, hidden: hidden, names: doc.SpeakerNamesOf(n)}
	// An audio file names its speakers over their lines, as a recording does.
	r.lines = doc.SpokenLines(notes, r.names, hidden || audioFile(n))
	for _, l := range r.lines {
		if t := doc.Clock(max(l.At, 0)); l.At >= 0 && len(t) > len(r.widest) {
			r.widest = t
		}
	}
	// What other notes the open pages had goes, now and then.
	if a.notesRead == nil || len(a.notesRead) > 64 {
		a.notesRead = map[string]readNotes{}
	}
	a.notesRead[n.ID] = r
	return r
}

// videoNotes draws a video's notes under its player, as a recording's
// transcript is drawn: a line's time beside it, and a time in its text,
// play the video from there. Long notes show their first lines until
// opened. While the block is hovered, a page that can be changed offers
// to edit them. An audio file's are its card's own, drawn as a recording's
// transcript, and edit from its menu.
func (a *App) videoNotes(c *ui.Context, n *doc.Node, hovered bool) {
	pal := theme.Of(c)
	audio := audioFile(n)
	read := a.noteLines(n)
	lines := read.lines
	if len(lines) == 0 {
		return
	}
	long := doc.FoldsTranscript(len(lines))
	open := a.notesOpen[n.ID]
	// What the audio is at, for a file's notes: a video's site doesn't say.
	current := -1
	if sec, _, heard := a.playhead(c, n.ID); heard {
		current = doc.CurrentLine(lines, sec)
	}
	shown := lines
	if long && !open {
		shown = lines[:doc.FoldedLines]
	}
	// The times' column is as wide as the longest of them all, shown or
	// folded away.
	col := timeColumn(c, []string{read.widest}, map[bool]float32{true: 12, false: 13}[audio])
	play := func(at int) { a.seekVideo(n.ID, at) }
	// In the page's text size, as its text.
	size := float32(a.settings.TextSize)
	if audio {
		size = 14
	}
	speakers := audio && !n.Bool(doc.SpeakersHidden)
	text := func(l doc.NoteLine) bool {
		if speakers && l.Speaker != "" {
			ui.Text(c, l.Speaker).FontSize(11.5).FontWeight(600).TextColor(pal.TextMuted)
		}
		return timedText(c, l.Parts, size, pal.Text, play)
	}
	// A click on a line plays from it, so its text is not selectable; the
	// notes copy from their dialog. They are in a panel of their own, as a
	// video's description is on its site, tinted as a code block is.
	panel := ui.Column(c.Key("notes:" + n.ID))
	timeSize := float32(13)
	if audio {
		panel.Gap(8)
		timeSize = 12
	} else {
		panel.Gap(4).Padding(10, 12).Radius(10).Background(pal.Code)
	}
	panel.Children(func() {
		line := func(i int, l doc.NoteLine) ui.Element {
			var row ui.Element
			if l.At < 0 {
				row = ui.Column(c.Key(i)).Children(func() { text(l) })
			} else {
				row = timedRow(c, i, doc.Clock(l.At), col, timeSize, func() bool { return text(l) }, func() { play(l.At) })
			}
			row.PopoverMenu(func(m *ui.Menu) { a.noteMenu(c, m, n, l) })
			return row
		}
		if audio && long && !open {
			a.rollingLines(c, n.ID, current, len(lines), func(i int) ui.Element {
				row := line(i, lines[i])
				if i == current {
					lightLine(c, row)
				}
				return row
			})
		} else {
			for i, l := range shown {
				row := line(i, l)
				if i == current {
					a.markLine(c, row, n.ID, i)
				}
			}
		}
		if long && foldButton(c, len(lines), open, col) {
			if a.notesOpen == nil {
				a.notesOpen = map[string]bool{}
			}
			a.notesOpen[n.ID] = !open
		}
		if audio || !a.canChangePage() {
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
	if n.Type == "file" {
		a.speakerMenu(m, n, l)
		a.fileMenu(c, m, n)
		return
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
			a.openPageAgent()
			a.hub.Chat(a.page.id, locales.VideoSummarizePrompt(title), []string{n.ID})
		}
	}
}

// openVideoNotes opens the dialog that writes the notes of video block n.
func (d *dialogs) openVideoNotes(n *doc.Node) {
	d.notesOpen, d.notesBlock, d.notesText, d.notesFile = true, n.ID, n.Str("notes"), n.Type == "file"
}

// videoNotesDialog writes the notes of a video block of the open page, as
// one step to undo.
func (a *App) videoNotesDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.notesOpen, 560, func() {
		title, hint := locales.VideoNotesTitle(), locales.VideoNotesHint()
		if d.notesFile {
			title, hint = locales.CardNotesTitle(), locales.CardNotesHint()
		}
		dialogHeader(c, title, hint, &d.notesOpen)
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
	if n == nil || n.Type != "video" && n.Type != "file" || n.Str("notes") == notes {
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
	for id := range a.videos {
		if a.page == nil || !a.page.editor.HasBlock(id) {
			a.closePlayer(id)
		}
	}
}

// openLink opens a web link in the browser.
func openLink(u string) {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		go func() { _ = mygo.Shell.OpenExternal(u) }()
	}
}
