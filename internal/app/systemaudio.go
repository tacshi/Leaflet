package app

import (
	_ "embed"
	"time"

	"github.com/egoist/mygo"
)

//go:embed cover.png
var leafletCover []byte

const (
	systemPoll = 500 * time.Millisecond
	systemLost = 1500 * time.Millisecond
)

type systemAudio struct {
	block, title string
	length, pos  time.Duration
	playing      bool
	rate         float64
	at           time.Time
}

func (a *App) watchSystemAudio() {
	if a.win == nil || a.systemWatch {
		return
	}
	a.systemWatch = true
	time.AfterFunc(systemPoll, func() {
		a.update(func() {
			a.systemWatch = false
			if a.syncSystemAudio() {
				a.watchSystemAudio()
			}
		})
	})
}

func (a *App) syncSystemAudio() (playing bool) {
	audio, ok := a.heardAudio()
	if !ok {
		a.now = nowPlaying{}
	}
	if !ok || a.now.rec == "" {
		if a.systemAudio.block != "" {
			mygo.NowPlaying.Clear()
			a.systemAudio = systemAudio{}
		}
		return false
	}
	if !a.systemHeard {
		a.systemHeard = true
		mygo.NowPlaying.OnCommand(func(cmd mygo.MediaCommand, pos time.Duration) {
			a.update(func() { a.mediaCommand(cmd, pos) })
		})
	}
	s, rate := a.systemAudio, a.speed()
	state := mygo.PlaybackPaused
	if audio.playing {
		state = mygo.PlaybackPlaying
	}
	next := systemAudio{block: a.now.block, title: audio.title, length: audio.length, pos: audio.pos,
		playing: audio.playing, rate: rate, at: time.Now()}
	switch {
	case s.block != next.block || s.title != next.title || s.length != next.length || s.rate != rate:
		mygo.NowPlaying.Set(mygo.NowPlayingInfo{Title: audio.title, Artist: "Leaflet", Artwork: leafletCover,
			Duration: audio.length, Position: audio.pos, Rate: rate, State: state,
			Commands: mygo.MediaPlay | mygo.MediaPause | mygo.MediaStop | mygo.MediaSeek | mygo.MediaNext | mygo.MediaPrevious})
	case s.playing != audio.playing || systemDrift(s, audio.pos) >= systemLost:
		mygo.NowPlaying.SetPlayback(state, audio.pos)
	default:
		return audio.playing
	}
	a.systemAudio = next
	return audio.playing
}

func systemDrift(s systemAudio, pos time.Duration) time.Duration {
	thinks := s.pos
	if s.playing {
		thinks += time.Duration(float64(time.Since(s.at)) * s.rate)
	}
	return max(thinks-pos, pos-thinks)
}

func (a *App) mediaCommand(cmd mygo.MediaCommand, pos time.Duration) {
	audio, ok := a.heardAudio()
	if !ok {
		return
	}
	switch cmd {
	case mygo.MediaPlay:
		if !audio.playing {
			audio.toggle()
		}
	case mygo.MediaPause:
		if audio.playing {
			audio.toggle()
		}
	case mygo.MediaStop:
		audio.stop()
	case mygo.MediaSeek:
		audio.seek(pos)
	case mygo.MediaNext:
		audio.seek(skipped(audio.pos, skipBy, audio.length))
	case mygo.MediaPrevious:
		audio.seek(skipped(audio.pos, -skipBy, audio.length))
	}
	if a.syncSystemAudio() {
		a.watchSystemAudio()
	}
}
