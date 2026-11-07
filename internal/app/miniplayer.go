package app

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/rendered"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// A small player under the page, for audio whose own has scrolled away: a
// transcript is read below it, and the block may still be on screen when
// its player no longer is. It takes a row of its own, not a place over the
// page, as a web view in the page would show over it.

// nowPlaying is the audio of the open page that started last: file block
// block, or the recording rec, in block.
type nowPlaying struct {
	block, rec string
}

// lineKey is a line of a block's transcript.
type lineKey struct {
	block string
	line  int
}

// fileStatus is where a file's player is, as it last said.
type fileStatus struct {
	at, length, rate float64
	paused           bool
}

// playerHead is how far down a block its player ends: past that, it has
// left the page's view, whatever of the block still shows.
const playerHead = 110

// headGone reports whether the head of a block that starts at top is out
// of the page's view, from viewTop to viewBottom.
func headGone(top, viewTop, viewBottom float32) bool {
	return top+playerHead <= viewTop || top >= viewBottom
}

// setNowPlaying notes the audio that starts: a file's audio or a recording,
// or nothing for a video, which has picture in picture.
func (a *App) setNowPlaying(video, rec string) {
	a.watchSystemAudio()
	// Going on, it stays where it says it is.
	if video != "" && a.now == (nowPlaying{block: video}) {
		return
	}
	a.now = nowPlaying{}
	if a.page == nil {
		return
	}
	switch {
	case rec != "":
		a.page.editor.Content.Walk(func(n, _ *doc.Node) {
			if n.Type == "recording" && n.Str("recording_id") == rec && a.now.block == "" {
				a.now = nowPlaying{block: n.ID, rec: rec}
			}
		})
	case a.fileMedia[video] != "":
		n := a.page.editor.Content.Find(video)
		if n != nil && doc.FilePreviewKind(n.Str("name"), n.Str("mime")) == doc.FileAudio {
			a.now, a.fileNow, a.fileNowAt = nowPlaying{block: video}, fileStatus{}, time.Now()
		}
	}
}

// miniPlayer draws the player under the page, while the audio's own is out
// of view.
func (a *App) miniPlayer(c *ui.Context) {
	ps := a.page
	if ps == nil || a.now.block == "" {
		return
	}
	id := a.now.block
	n := ps.editor.Content.Find(id)
	if n == nil {
		a.now = nowPlaying{}
		return
	}
	view := ps.view.Bounds(c)
	if top, ok := ps.editor.BlockTop(id); ok && (view.H == 0 || !headGone(top, view.Y, view.Y+view.H)) {
		return
	}
	audio, ok := a.heardAudio()
	if !ok {
		return
	}
	title, pos, length, playing := audio.title, audio.pos, audio.length, audio.playing
	toggle, seek, stop := audio.toggle, audio.seek, audio.stop
	if playing {
		c.After(lineTick)
	}
	pal := theme.Of(c)
	ui.Row(c.Key("mini-player")).Height(48).Padding(0, 14).Gap(10).AlignItems(ui.Center).
		Background(pal.Surface).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
		icon, label := "play", locales.RecPlay()
		if playing {
			icon, label = "pause", locales.CommonPause()
		}
		if iconButton(c, "toggle", icon, label).Clicked() {
			toggle()
		}
		back := ui.ButtonBase(c.Key("back")).MaxWidth(240).Shrink(1).Height(30).Padding(0, 6).Radius(6).AlignItems(ui.Center).
			Label(locales.CardGoToPlayer(title)).Cursor(ui.CursorPointer)
		if back.Hovered() {
			back.Background(pal.Hover)
		}
		back.Children(func() {
			ui.Text(c, strings.TrimSpace(title)).FontSize(13).FontWeight(600).TextColor(pal.Text).SingleLine()
		})
		if back.Clicked() {
			a.goToBlock(id, false)
		}
		ui.Box(c).Grow(1).Children(func() { a.playerBar(c, pos, length, seek) })
		follow := iconButton(c, "follow", "list", locales.CardFollowAlong())
		if a.followOn() {
			follow.Background(pal.AccentSoft).TextColor(pal.Accent)
		}
		if follow.Clicked() {
			a.setFollow(!a.followOn())
		}
		if iconButton(c, "stop", "x", locales.CommonClose()).Clicked() {
			stop()
		}
	})
}

// heard is the audio that started last, as what shows it and what controls
// it need it.
type heard struct {
	title        string
	pos, length  time.Duration
	playing      bool
	toggle, stop func()
	seek         func(time.Duration)
}

// heardAudio is the audio that started last, if it is still there.
func (a *App) heardAudio() (heard, bool) {
	id := a.now.block
	if a.page == nil || id == "" {
		return heard{}, false
	}
	n := a.page.editor.Content.Find(id)
	if n == nil {
		return heard{}, false
	}
	if a.now.rec != "" {
		pl := a.players[a.now.rec]
		if pl == nil || pl.p == nil {
			return heard{}, false
		}
		rec, err := a.st.Recording(a.now.rec)
		return heard{
			title: a.recordingTitle(n, rec, err == nil),
			pos:   pl.p.Position(), length: pl.p.Duration(), playing: pl.p.Playing(),
			toggle: func() { a.play(rec) },
			seek: func(t time.Duration) {
				pl.p.Seek(t)
				a.watchSystemAudio()
			},
			stop: func() {
				pl.p.Pause()
				a.now = nowPlaying{}
			},
		}, true
	}
	if a.videos[id] == nil {
		return heard{}, false
	}
	return heard{
		title: n.Str("name"),
		pos:   seconds(a.fileAt()), length: seconds(a.fileNow.length), playing: !a.fileNow.paused,
		toggle: func() { a.toggleFile(n) },
		seek:   func(t time.Duration) { a.seekFile(id, t) },
		stop:   func() { a.closePlayer(id) },
	}, true
}

// lineTick is how often lines redraw as audio plays, for the line being
// said to keep up.
const lineTick = 250 * time.Millisecond

// playhead is where block id's audio is, and how long it lasts, in seconds;
// false for a block not playing. A file's player says where it is twice a
// second, and goes on from there in between. It asks for the next frame
// while it plays: only a block on screen asks.
func (a *App) playhead(c *ui.Context, id string) (sec, length float64, ok bool) {
	if a.now.block != id {
		return 0, 0, false
	}
	if a.now.rec != "" {
		pl := a.players[a.now.rec]
		if pl == nil || pl.p == nil {
			return 0, 0, false
		}
		if pl.p.Playing() {
			c.After(lineTick)
		}
		return pl.p.Position().Seconds(), pl.p.Duration().Seconds(), true
	}
	if !a.fileNow.paused && a.fileNow.length > 0 {
		c.After(lineTick)
	}
	return a.fileAt(), a.fileNow.length, true
}

// fileAt is where the file playing is: where its player last said, and on
// from there as it plays.
func (a *App) fileAt() float64 {
	st := a.fileNow
	if st.paused || st.length <= 0 {
		return st.at
	}
	return min(st.at+time.Since(a.fileNowAt).Seconds()*a.speed(), st.length)
}

// toggleFile plays audio file block n, or pauses it: its player comes first,
// and one left paused as another started goes on.
func (a *App) toggleFile(n *doc.Node) {
	wv := a.videos[n.ID]
	if wv == nil {
		a.playFile(n, 0)
		return
	}
	on := true
	if a.now.block == n.ID {
		a.fileNow.at, a.fileNowAt = a.fileAt(), time.Now()
		a.fileNow.paused = !a.fileNow.paused
		on = !a.fileNow.paused
	} else {
		a.playAlone(n.ID, "")
	}
	page := wv.Page()
	// Eval waits for the page, so not on the UI thread.
	go func() { _, _ = page.Eval(rendered.FilePlaying(on)) }()
}

// seekFile takes block id's file to t, playing or not as it was.
func (a *App) seekFile(id string, t time.Duration) {
	wv := a.videos[id]
	if wv == nil {
		return
	}
	a.fileNow.at, a.fileNowAt = t.Seconds(), time.Now()
	page := wv.Page()
	go func() { _, _ = page.Eval(rendered.FileSeekTo(t.Seconds())) }()
}

// skipBy is how far a skip goes.
const skipBy = 15 * time.Second

// skipped is where audio of length dur at pos goes on a skip of by: nowhere
// until its length is known.
func skipped(pos, by, dur time.Duration) time.Duration {
	if dur <= 0 {
		return pos
	}
	return min(max(pos+by, 0), dur)
}

// playerBar is the controls of audio of length dur, at pos, that seek moves:
// a skip each way, where it is, and its speed.
func (a *App) playerBar(c *ui.Context, pos, dur time.Duration, seek func(time.Duration)) {
	ui.Row(c.Key("player-bar")).Gap(8).AlignItems(ui.Center).Children(func() {
		tools(c, func() {
			if skipButton(c, "back", "rotate-ccw", locales.CardSkipBack()).Clicked() {
				seek(skipped(pos, -skipBy, dur))
			}
			if skipButton(c, "forward", "rotate-cw", locales.CardSkipForward()).Clicked() {
				seek(skipped(pos, skipBy, dur))
			}
		})
		ui.Box(c).Grow(1).Children(func() { a.scrubBar(c, pos, dur, seek) })
		var speed ui.Element
		tools(c, func() {
			speed = ui.Button(c.Key("speed"), speedLabel(a.speed())).Label(locales.CardSpeed()).Tooltip(locales.CardSpeed()).
				Padding(4, 6).FontSize(12).FontFeatures("tnum")
		})
		speed.PopoverMenuButton(func(m *ui.Menu) {
			for _, s := range playSpeeds {
				if m.Item(speedLabel(s)).Checked(s == a.speed()).Chosen() {
					a.setSpeed(s)
				}
			}
		}, endMenu)
	})
}

// skipButton is a skip's button: its arrow around the seconds it goes.
func skipButton(c *ui.Context, key, icon, label string) ui.Element {
	b := ui.Button(c.Key(key), "").Label(label).Tooltip(label).Padding(6)
	b.Children(func() {
		ui.Box(c).Size(20, 20).Center().Children(func() {
			ui.Icon(c, theme.Icon(icon)).FontSize(20)
			ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Children(func() {
				ui.Text(c, strconv.Itoa(int(skipBy.Seconds()))).FontSize(7.5).FontWeight(600)
			})
		})
	})
	return b
}

// playSpeeds are the speeds audio plays at.
var playSpeeds = []float64{0.75, 1, 1.25, 1.5, 1.75, 2}

// speedKey keeps how fast audio plays.
const speedKey = "player.speed"

// speedLabel is a speed as its button says it: 1.5×.
func speedLabel(s float64) string { return strconv.FormatFloat(s, 'f', -1, 64) + "×" }

// speed is how fast audio plays, files' and recordings' alike.
func (a *App) speed() float64 {
	if a.playSpeed == 0 {
		v, _ := a.st.Value(speedKey)
		s, _ := strconv.ParseFloat(v, 64)
		if !slices.Contains(playSpeeds, s) {
			s = 1
		}
		a.playSpeed = s
	}
	return a.playSpeed
}

// setSpeed has audio play at s, what plays now and what plays next.
func (a *App) setSpeed(s float64) {
	a.fileNow.at, a.fileNowAt = a.fileAt(), time.Now()
	a.playSpeed = s
	if err := a.st.SetValue(speedKey, strconv.FormatFloat(s, 'f', -1, 64)); err != nil {
		a.toast(err.Error())
	}
	a.watchSystemAudio()
	for _, pl := range a.players {
		if pl.p != nil {
			pl.p.SetSpeed(s)
		}
	}
	if a.page == nil {
		return
	}
	for id := range a.fileMedia {
		if n := a.page.editor.Content.Find(id); n != nil && audioFile(n) && a.videos[id] != nil {
			a.tellSpeed(a.videos[id])
		}
	}
}

// tellSpeed tells a file's player how fast to play.
func (a *App) tellSpeed(wv *mygo.WebView) {
	page, script := wv.Page(), rendered.FileSpeed(a.speed())
	// Eval waits for the page, so not on the UI thread.
	go func() { _, _ = page.Eval(script) }()
}

// followKey keeps whether transcripts follow the audio.
const followKey = "player.follow"

// followOn reports whether transcripts follow the audio.
func (a *App) followOn() bool {
	if !a.followRead {
		v, _ := a.st.Value(followKey)
		a.followLines, a.followRead, a.followAt = v == "1", true, -1
	}
	return a.followLines
}

// setFollow says whether transcripts follow the audio.
func (a *App) setFollow(on bool) {
	a.followLines, a.followRead, a.followAt = on, true, -1
	v := ""
	if on {
		v = "1"
	}
	if err := a.st.SetValue(followKey, v); err != nil {
		a.toast(err.Error())
	}
}

// markLine lights row, the line of block being said, and, once it is laid
// out where it now is, brings it into view for those who follow along.
func (a *App) markLine(c *ui.Context, row ui.Element, block string, line int) {
	lightLine(c, row).Bind(&a.lineRow)
	switch key := (lineKey{block, line}); {
	case a.line != key:
		a.line, a.lineMoved = key, true
		c.After(0)
	case a.lineMoved:
		a.lineMoved = false
		a.followLine(c)
	}
}

// lineInset and lineLift are how far the light on the line being said
// reaches past its text, to its sides and above and below it, into the gap
// between lines: the text stays where it was.
const (
	lineInset = 6
	lineLift  = 3
)

// lightLine lights row, the line being said.
func lightLine(c *ui.Context, row ui.Element) ui.Element {
	return row.Background(theme.Of(c).AccentSoft).Radius(6).Padding(lineLift, lineInset).Margin(-lineLift, -lineInset)
}

// followLine scrolls the page to keep the line being said a third of the
// way down. Someone who scrolled since is left to read: it goes on from
// the line after next, once they have stopped.
func (a *App) followLine(c *ui.Context) {
	ps := a.page
	if ps == nil || !a.followOn() {
		return
	}
	if a.followAt >= 0 && ps.scroll.Y != a.followAt && !ps.scroll.Scrolling() {
		a.followAt = -1
		return
	}
	line, view := a.lineRow.Bounds(c), ps.view.Bounds(c)
	if line.H == 0 || view.H == 0 {
		return
	}
	y := min(max(ps.scroll.Y+line.Y-view.Y-view.H/3, 0), ps.scroll.MaxY)
	ps.scroll.ScrollTo(0, y, ui.Smooth)
	a.followAt = y
}

// seconds is a time a web page's player says as a duration.
func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
