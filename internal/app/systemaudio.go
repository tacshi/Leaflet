package app

import (
	_ "embed"
	"time"

	"github.com/egoist/mygo"
)

// A recording where the system shows media: Now Playing on a Mac, the media
// controls on Windows. Those, and the media keys, drive it. It counts on by
// itself while audio plays, so it only hears of what it can't work out. A
// file plays in a web view, which tells the system itself (nameFile): told
// here too, it would show twice.

// leafletCover is the app's icon, beside what plays where it has no cover.
//
//go:embed cover.png
var leafletCover []byte

const (
	// systemPoll is how often the audio playing is checked on.
	systemPoll = 500 * time.Millisecond
	// systemLost is how far the audio can be from where the system thinks
	// before it is told again.
	systemLost = 1500 * time.Millisecond
)

// systemAudio is what the system was last told.
type systemAudio struct {
	block, title string
	length, pos  time.Duration
	playing      bool
	rate         float64
	at           time.Time
}

// watchSystemAudio has the audio checked on soon: from then on while it
// plays, as a change of its own says nothing. Paused, nothing runs, for the
// window not to draw for nothing.
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

// syncSystemAudio tells the system what plays, if that changed, and reports
// whether it plays.
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

// systemDrift is how far pos is from where the system, told s, thinks the
// audio is.
func systemDrift(s systemAudio, pos time.Duration) time.Duration {
	thinks := s.pos
	if s.playing {
		thinks += time.Duration(float64(time.Since(s.at)) * s.rate)
	}
	return max(thinks-pos, pos-thinks)
}

// mediaCommand does what the system's media controls or the media keys ask.
// They have no skip of their own: next and previous are it.
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
