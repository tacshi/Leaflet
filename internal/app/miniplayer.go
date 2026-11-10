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

type nowPlaying struct {
	block, rec string
}

type lineKey struct {
	block string
	line  int
}

type fileStatus struct {
	at, length, rate float64
	paused           bool
}

const playerHead = 110

func headGone(top, viewTop, viewBottom float32) bool {
	return top+playerHead <= viewTop || top >= viewBottom
}

func (a *App) setNowPlaying(video, rec string) {
	a.watchSystemAudio()
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

type heard struct {
	title        string
	pos, length  time.Duration
	playing      bool
	toggle, stop func()
	seek         func(time.Duration)
}

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
		v := a.recordingView(a.now.rec)
		rec := v.rec
		return heard{
			title: a.recordingTitle(n, rec, v.err == nil),
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

const lineTick = 250 * time.Millisecond

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

func (a *App) fileAt() float64 {
	st := a.fileNow
	if st.paused || st.length <= 0 {
		return st.at
	}
	return min(st.at+time.Since(a.fileNowAt).Seconds()*a.speed(), st.length)
}

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
	go func() { _, _ = page.Eval(rendered.FilePlaying(on)) }()
}

func (a *App) seekFile(id string, t time.Duration) {
	wv := a.videos[id]
	if wv == nil {
		return
	}
	a.fileNow.at, a.fileNowAt = t.Seconds(), time.Now()
	page := wv.Page()
	go func() { _, _ = page.Eval(rendered.FileSeekTo(t.Seconds())) }()
}

const skipBy = 15 * time.Second

func skipped(pos, by, dur time.Duration) time.Duration {
	if dur <= 0 {
		return pos
	}
	return min(max(pos+by, 0), dur)
}

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

var playSpeeds = []float64{0.75, 1, 1.25, 1.5, 1.75, 2}

const speedKey = "player.speed"

func speedLabel(s float64) string { return strconv.FormatFloat(s, 'f', -1, 64) + "×" }

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

func (a *App) tellSpeed(wv *mygo.WebView) {
	page, script := wv.Page(), rendered.FileSpeed(a.speed())
	go func() { _, _ = page.Eval(script) }()
}

const followKey = "player.follow"

func (a *App) followOn() bool {
	if !a.followRead {
		v, _ := a.st.Value(followKey)
		a.followLines, a.followRead, a.followAt = v == "1", true, -1
	}
	return a.followLines
}

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

const (
	lineInset = 6
	lineLift  = 3
)

func lightLine(c *ui.Context, row ui.Element) ui.Element {
	return row.Background(theme.Of(c).AccentSoft).Radius(6).Padding(lineLift, lineInset).Margin(-lineLift, -lineInset)
}

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

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
