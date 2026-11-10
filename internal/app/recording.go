package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/guard"
	"leaflet/internal/macspeech"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// exportRecordingAudio saves a recording's audio where the user chooses,
// as it comes, for one that is not on this device any more, and never all
// in memory.
func (a *App) exportRecordingAudio(rec store.Recording, title string) {
	a.saveExport(export.SafeName(title)+".wav", func(path string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		audio, err := a.rec.OpenAudio(ctx, rec)
		if err != nil {
			return err
		}
		defer func() { _ = audio.Close() }()
		return export.Save(path, audio)
	})
}

// exportTranscript saves a recording's transcript as a file of format.
func (a *App) exportTranscript(rec store.Recording, title, format string) {
	text := recording.TranscriptFile(rec.Transcript, format)
	a.saveExport(export.SafeName(title)+"."+format, func(path string) error {
		return export.Save(path, strings.NewReader(text))
	})
}

// saveExport asks where to save a file, by default named name, and has
// save write it there, off the main thread, saying how it went.
func (a *App) saveExport(name string, save func(path string) error) {
	guard.Go(func() {
		path, _ := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: locales.HostExportTitle(), DefaultPath: name})
		if path == "" {
			return
		}
		err := save(path)
		a.update(func() {
			if err != nil {
				a.toast(locales.HostErrorExport(err.Error()))
			} else {
				a.toast(locales.HostExported(filepath.Base(path)))
			}
		})
	})
}

// startRecording records into the open page, transcribed by provider, or
// the one marked for the page when it is "".
func (a *App) startRecording(pageID, provider string) {
	ed := a.openEditor(pageID)
	err := a.rec.Start(pageID, a.sttFor(pageID, provider), a.online && !a.settings.WorkOffline, func(n *doc.Node) {
		if ed != nil {
			ed.InsertBlocks(n)
			a.savePage()
			return
		}
		err := a.pageCore().EditPage(pageID, func(c *doc.Content) bool {
			return c.Insert(doc.Position{At: "end"}, n.Clone()) == nil
		})
		if err != nil {
			a.toast(locales.RecCouldNotSave(err.Error()))
		}
	})
	if err != nil {
		a.toast(err.Error())
	}
}

// recordingPill shows the recording in progress in the page's header.
func (a *App) recordingPill(c *ui.Context) {
	pal := theme.Of(c)
	st := &a.rec.State
	ui.Row(c.Key("rec-pill")).Height(28).Padding(0, 4, 0, 10).Gap(8).Radius(999).Background(pal.DangerBg).
		Border(1, pal.DangerBorder).AlignItems(ui.Center).Children(func() {
		dot := ui.Box(c).Size(8, 8).Radius(4).Background(pal.Danger)
		if !st.Paused {
			dot.Opacity(0.4 + 0.6*dot.Loop("pulse", 1200*time.Millisecond, ui.Bounce(ui.EaseInOut)))
		}
		label := recording.FormatDuration(a.rec.Elapsed())
		if st.Starting {
			label = locales.RecStarting()
		}
		ui.Text(c, label).FontSize(12.5).FontWeight(600).TextColor(pal.DangerText).FontFeatures("tnum")
		c.After(500 * time.Millisecond)
		// Before the microphone starts there is nothing to pause.
		pause := ui.ButtonBase(c.Key("pause")).Size(22, 22).Center().Radius(999).Label(locales.CommonPause()).TextColor(pal.DangerText).
			Disabled(st.Starting)
		pause.Children(func() {
			icon := "pause"
			if st.Paused {
				icon = "play"
			}
			ui.Icon(c, theme.Icon(icon)).FontSize(12)
		})
		if pause.Clicked() {
			a.rec.TogglePause()
		}
		// A square in a circle, as on the iPad.
		stop := ui.ButtonBase(c.Key("stop")).Size(22, 22).Center().Radius(999).Background(pal.Danger).Label(locales.RecStop()).Tooltip(locales.RecStop())
		stop.Children(func() { ui.Box(c).Size(8, 8).Radius(2).Background(pal.OnAccent) })
		if stop.Clicked() {
			a.rec.Stop()
		}
	})
}

// recView is the recording's capture page, if one is running.
func (a *App) recView() *mygo.WebView {
	if a.rec == nil || a.capture == nil || !a.rec.Active() {
		return nil
	}
	return a.capture.View()
}

// player is the playback of one recording; from is where it starts once
// its audio loads, as a time chosen before then asks.
type player struct {
	sound   *audio.Sound
	p       *audio.Player
	loading bool
	from    time.Duration
}

// dropGonePlayers stops the recordings playing whose block is no longer
// in the open page, as one deleted, and lets their audio go.
func (a *App) dropGonePlayers() {
	if len(a.players) == 0 {
		return
	}
	shown := map[string]bool{}
	if a.page != nil {
		a.page.editor.Content.Walk(func(n *doc.Node, _ *doc.Node) {
			if n.Type == "recording" {
				shown[n.Str("recording_id")] = true
			}
		})
	}
	for id, pl := range a.players {
		if !shown[id] {
			if pl.p != nil {
				pl.p.Close()
			}
			delete(a.players, id)
		}
	}
}

// dropPlayers stops the recordings playing and lets their audio go, as
// the page that shows them closes.
func (a *App) dropPlayers() {
	for id, pl := range a.players {
		if pl.p != nil {
			pl.p.Close()
		}
		delete(a.players, id)
	}
}

func (a *App) play(rec store.Recording) {
	pl := a.players[rec.ID]
	if pl == nil {
		if a.players == nil {
			a.players = map[string]*player{}
		}
		pl = &player{}
		a.players[rec.ID] = pl
	}
	if pl.p != nil {
		if pl.p.Playing() {
			pl.p.Pause()
		} else {
			if pl.p.Position() >= pl.p.Duration() {
				pl.p.Seek(0)
			}
			a.playAlone("", rec.ID)
			pl.p.Play()
		}
		return
	}
	if pl.loading {
		return
	}
	pl.loading = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		data, err := a.rec.Audio(ctx, rec)
		var snd *audio.Sound
		if err == nil {
			snd, err = audio.DecodeSound(data)
		}
		a.update(func() {
			pl.loading = false
			if a.players[rec.ID] != pl {
				return // the page closed meanwhile
			}
			if err != nil {
				a.toast(locales.RecCouldNotPlay(err.Error()))
				return
			}
			pl.sound = snd
			pl.p = snd.NewPlayer().SetSpeed(a.speed()).SetPreservesPitch(true)
			if pl.from > 0 {
				pl.p.Seek(pl.from)
			}
			a.playAlone("", rec.ID)
			pl.p.Play()
		})
	}()
}

// recView is a recording as its block shows it: its row, or why there is
// none, and its transcript tidied into lines.
type recView struct {
	rec   store.Recording
	err   error
	lines []store.Segment
}

// recordingView is recording id as its block shows it, read once until
// the recordings change: a block is drawn each frame, and each quarter
// second while it plays.
func (a *App) recordingView(id string) recView {
	if v, ok := a.recs[id]; ok {
		return v
	}
	rec, err := a.st.Recording(id)
	v := recView{rec: rec, err: err}
	if err == nil {
		v.lines = recording.Tidy(rec.Transcript)
	}
	if a.recs == nil {
		a.recs = map[string]recView{}
	}
	a.recs[id] = v
	return v
}

func (a *App) recordingBlock(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	id := n.Str("recording_id")
	v := a.recordingView(id)
	rec, err := v.rec, v.err
	live := a.rec.Active() && a.rec.State.ID == id
	// On a page that can't be changed, the transcript only copies.
	ro := a.page == nil || a.page.editor.ReadOnly || !a.canEdit(a.page.id)
	ui.Column(c.Key("rec:"+n.ID)).Padding(14, 16).Gap(12).Radius(10).Border(1, pal.Divider).Background(pal.Surface).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(30, 30).Radius(999).Center().Background(map[bool]ui.Color{true: pal.DangerBg, false: pal.AccentSoft}[live]).Children(func() {
				ui.Icon(c, theme.Icon("audio-lines")).FontSize(15).TextColor(map[bool]ui.Color{true: pal.Danger, false: pal.AccentText}[live])
			})
			ui.Column(c).Grow(1).Children(func() {
				ui.Text(c, a.recordingTitle(n, rec, err == nil)).FontSize(14).FontWeight(600).TextColor(pal.Text)
				var status string
				switch {
				case err != nil:
					status = locales.RecNotFound()
				case live:
					status = a.rec.State.Engine
				case a.rec.Finishing[id]:
					status = locales.RecAddingTimestamps()
				case a.rec.Transcribing[id]:
					status = locales.RecTranscribing()
				default:
					status = recording.Status(rec)
				}
				ui.Text(c, status).FontSize(12).TextColor(pal.TextMuted)
			})
			if err == nil && !live && rec.Status != "recording" {
				pl := a.players[id]
				icon, label := "play", locales.RecPlay()
				if pl != nil && pl.p != nil && pl.p.Playing() {
					icon, label = "pause", locales.CommonPause()
					c.After(250 * time.Millisecond)
				}
				var more ui.Element
				tools(c, func() {
					b := iconButton(c, "play", icon, label)
					if pl != nil && pl.loading {
						b.Disabled(true)
					}
					if b.Clicked() {
						a.play(rec)
					}
					more = iconButton(c, "more", "ellipsis", locales.RecActions())
				})
				more.PopoverMenuButton(func(m *ui.Menu) {
					if m.Item(locales.RecRename()).Disabled(ro).Chosen() {
						a.dialogs.openRenameRecording(n.ID, a.recordingTitle(n, rec, true))
					}
					if m.Item(locales.RecCopyTranscript()).Disabled(len(rec.Transcript) == 0).Chosen() {
						c.WriteClipboard(recording.TranscriptText(rec))
						c.Toast(locales.CommonCopied())
					}
					if m.Item(locales.RecInsertTranscript()).Disabled(len(rec.Transcript) == 0 || ro).Chosen() {
						a.insertTranscript(n.ID, rec)
					}
					name := a.recordingTitle(n, rec, true)
					if m.Item(locales.RecExportAudio()).Disabled(rec.LocalPath == "" && rec.AudioPath == "").Chosen() {
						a.exportRecordingAudio(rec, name)
					}
					m.Submenu(locales.RecExportTranscript(), func(m *ui.Menu) {
						for _, f := range []struct{ format, label string }{
							{recording.TranscriptTxt, locales.RecExportText()},
							{recording.TranscriptSrt, locales.RecExportSrt()},
							{recording.TranscriptVtt, locales.RecExportVtt()},
						} {
							if m.Item(f.label).Chosen() {
								a.exportTranscript(rec, name, f.format)
							}
						}
					}).Disabled(len(rec.Transcript) == 0)
					// The agent reads the transcript with the block.
					if m.Item(locales.RecSummarize()).Disabled(len(rec.Transcript) == 0 || !a.hub.Available() || ro).Chosen() {
						a.openPageAgent()
						a.hub.Chat(a.page.id, locales.RecSummarizePrompt(a.recordingTitle(n, rec, true)), []string{n.ID})
					}
					if m.Item(locales.CardFollowAlong()).Checked(a.followOn()).Disabled(len(rec.Transcript) == 0).Chosen() {
						a.setFollow(!a.followOn())
					}
					label := locales.RecTranscribeAgain()
					if len(rec.Transcript) == 0 {
						label = locales.RecTranscribe()
					}
					// Not while a transcription runs, which the new one would race.
					a.transcribeItem(m, a.openPageID(), label, ro || a.rec.Transcribing[id], func(provider string) {
						if err := a.rec.Retranscribe(id, a.sttFor(a.openPageID(), provider), a.online && !a.settings.WorkOffline); err != nil {
							a.toast(err.Error())
						}
					})
					m.Separator()
					// The block goes as one step to undo; its recording stays,
					// for the undo to bring back.
					if m.Item(locales.CommonDelete()).Danger().Disabled(ro).Chosen() {
						a.page.editor.Edit(func(content *doc.Content) { content.Remove(n.ID) })
					}
				}, endMenu)
			}
		})
		if live {
			a.levelMeter(c, a.rec.State.Levels)
			if t := a.rec.State.LiveText; t != "" {
				// Its last two lines, from the bottom up, so the block keeps
				// its height as the words come: the whole transcript shows
				// once it stops.
				ui.Column(c.Key("live")).MaxHeight(2 * liveLine).Reverse().Clip().Children(func() {
					ui.Text(c, t).FontSize(13.5).FixedLineHeight(liveLine).Shrink(0).TextColor(pal.TextSecondary).Font(theme.PageFamily(t))
				})
			}
			return
		}
		if err != nil {
			return
		}
		if pl := a.players[id]; pl != nil && pl.p != nil {
			a.scrubber(c, pl)
		}
		// What went wrong, until it is read and closed: a failure, or a
		// warning over a transcript that may be short.
		if rec.Error != "" {
			kind := "warn"
			if rec.Status == "failed" {
				kind = "danger"
			}
			var dismiss func()
			if !ro {
				dismiss = func() {
					if err := a.rec.DismissError(id); err != nil {
						a.toast(err.Error())
					}
				}
			}
			closableNotice(c, kind, "circle-alert", rec.Error, nil, dismiss)
		}
		if len(rec.Transcript) > 0 {
			// A long transcript shows its first lines until it is opened.
			lines := v.lines
			long := doc.FoldsTranscript(len(lines))
			open := a.recOpen[n.ID]
			current := -1
			sec, _, heard := a.playhead(c, n.ID)
			if heard {
				for i, s := range lines {
					if float64(s.StartMs)/1000 > sec {
						break
					}
					current = i
				}
			}
			// A click on a line plays from it, so its text is not selectable;
			// the menu's Copy transcript copies it.
			col := timeColumn(c, []string{recording.FormatDuration(time.Duration(rec.DurationMs) * time.Millisecond)}, 12)
			ui.Column(c).Gap(8).Children(func() {
				line := func(i int) ui.Element {
					s := lines[i]
					at := time.Duration(s.StartMs) * time.Millisecond
					return timedRow(c, i, recording.FormatDuration(at), col, 12, func() bool {
						if s.Speaker != "" {
							ui.Text(c, locales.RecSpeaker(s.Speaker)).FontSize(11.5).FontWeight(600).TextColor(pal.TextMuted)
						}
						ui.Text(c, s.Text).FontSize(14).Font(theme.PageFamily(s.Text)).TextColor(pal.Text)
						return false
					}, func() { a.seek(rec, at) })
				}
				if long && !open {
					a.rollingLines(c, n.ID, current, len(lines), func(i int) ui.Element {
						row := line(i)
						if i == current {
							lightLine(c, row)
						}
						return row
					})
				} else {
					for i := range lines {
						row := line(i)
						if i == current {
							a.markLine(c, row, n.ID, i)
						}
					}
				}
				if long && foldButton(c, len(lines), open, col) {
					if a.recOpen == nil {
						a.recOpen = map[string]bool{}
					}
					a.recOpen[n.ID] = !open
				}
			})
		}
	})
}

// liveLine is the height of a line of the words heard while recording,
// two of which show.
const liveLine = 20

// openRenameRecording opens the dialog renaming the recording block of
// id, named title.
func (d *dialogs) openRenameRecording(id, title string) {
	d.recOpen, d.recBlock, d.recName, d.recSelect = true, id, title, true
}

// recordingDialog renames a recording block of the open page; a name
// left empty keeps the one it has.
func (a *App) recordingDialog(c *ui.Context) {
	d := &a.dialogs
	dialog(c, &d.recOpen, 380, func() {
		dialogHeader(c, locales.RecRenameTitle(), "", &d.recOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(14).Children(func() {
			in := field(c, "name", &d.recName, locales.RecName()).AutoFocus()
			if d.recSelect && in.Focused() {
				d.recSelect = false
				in.SetTextSelection(0, utf8.RuneCountInString(d.recName))
			}
			save := func() {
				d.recOpen = false
				if a.page == nil || a.page.editor.ReadOnly {
					return
				}
				n := a.page.editor.Content.Find(d.recBlock)
				if n == nil || n.Type != "recording" {
					return
				}
				name := strings.TrimSpace(d.recName)
				if name != "" && name != n.Str("title") {
					id := n.ID
					a.page.editor.Edit(func(content *doc.Content) { content.Find(id).Set("title", name) })
				}
			}
			if in.Submitted() {
				save()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.recOpen = false
				}
				if primary(c, "save", "", locales.CommonRename()).Clicked() {
					save()
				}
			})
		})
	})
}

// recordingTitle is the name a recording block shows: its title, with the
// time it was made where the title, as older blocks', gives only the day,
// or just "Recording" on a page of the same name, as the menu bar makes.
// Older blocks were titled in English, "Recording · Jan 2".
func (a *App) recordingTitle(n *doc.Node, rec store.Recording, found bool) string {
	page := ""
	if a.page != nil {
		page = a.page.title
	}
	return recordingName(n, rec, found, page)
}

// recordingName is recordingTitle for a block on the page titled page;
// a block with no title is a Recording too.
func recordingName(n *doc.Node, rec store.Recording, found bool, page string) string {
	return recording.BlockTitle(n.Str("title"), rec.CreatedAt, found, page)
}

// seek plays a recording from t, once its audio loads if it has not.
func (a *App) seek(rec store.Recording, t time.Duration) {
	pl := a.players[rec.ID]
	if pl == nil || pl.p == nil {
		a.play(rec)
		a.players[rec.ID].from = t
		return
	}
	pl.p.Seek(t)
	if !pl.p.Playing() {
		a.playAlone("", rec.ID)
		pl.p.Play()
	}
}

func (a *App) scrubber(c *ui.Context, pl *player) {
	a.playerBar(c, pl.p.Position(), pl.p.Duration(), func(t time.Duration) {
		pl.p.Seek(t)
		a.watchSystemAudio()
	})
}

// scrubBar shows where audio of length dur is, at pos, for a drag to move
// by seek.
func (a *App) scrubBar(c *ui.Context, pos, dur time.Duration, seek func(time.Duration)) {
	pal := theme.Of(c)
	frac := 0.0
	if dur > 0 {
		frac = float64(pos) / float64(dur)
	}
	ui.Row(c.Key("scrub")).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, recording.FormatDuration(pos)).FontSize(12).TextColor(pal.TextMuted).FontFeatures("tnum")
		v := frac
		if ui.SliderBase(c.Key("bar"), &v, 0, 1).Grow(1).Height(14).Draw(func(p *ui.Painter, r ui.Rect) {
			y := r.Y + r.H/2 - 2
			p.Fill(ui.Rect{X: r.X, Y: y, W: r.W, H: 4}, pal.Divider, 2)
			p.Fill(ui.Rect{X: r.X, Y: y, W: r.W * float32(v), H: 4}, pal.Accent, 2)
			p.Fill(ui.Rect{X: r.X + r.W*float32(v) - 6, Y: r.Y + r.H/2 - 6, W: 12, H: 12}, pal.Accent, 6)
		}).Changed() {
			seek(time.Duration(v * float64(dur)))
		}
		ui.Text(c, recording.FormatDuration(dur)).FontSize(12).TextColor(pal.TextMuted).FontFeatures("tnum")
	})
}

func (a *App) levelMeter(c *ui.Context, levels []float64) {
	pal := theme.Of(c)
	// One thin bar per 50 ms, newest at the right, filling the width: about
	// seven seconds at the page's width.
	ui.Box(c.Key("meter")).Height(36).FillWidth().Draw(func(p *ui.Painter, r ui.Rect) {
		const bar, step = 2.5, 4.5
		n := int(r.W / step)
		for i := 0; i < n; i++ {
			v := 0.0
			if j := len(levels) - n + i; j >= 0 {
				v = levels[j]
			}
			h := max(float32(v)*r.H, 2.5)
			color := pal.Danger.Alpha(0.8)
			if j := len(levels) - n + i; j < 0 {
				color = pal.Divider
			}
			p.Fill(ui.Rect{X: r.X + float32(i)*step, Y: r.Y + (r.H-h)/2, W: bar, H: h}, color, bar/2)
		}
	})
}

// insertTranscript puts a recording's transcript after its block as text.
func (a *App) insertTranscript(blockID string, rec store.Recording) {
	ed := a.page.editor
	if ed.ReadOnly {
		return
	}
	nodes := recording.TranscriptBlocks(rec)
	ed.Edit(func(c *doc.Content) { _ = c.Insert(doc.Position{After: blockID}, nodes...) })
}

// Speech to text ---------------------------------------------------------

// findSpeech gives the recorder the Mac's own speech engine, where the app
// ships its helper and macOS can transcribe. A Mac that has one
// transcribes with it unless another engine is set up: speech settings
// left at an endpoint with no address turn to it, as on the iPad.
func (a *App) findSpeech() {
	// Finding it takes up to 10 s: a recording meanwhile waits for it to
	// transcribe, rather than keeping only its audio.
	a.rec.FindingEngine()
	dir, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		a.update(func() { a.rec.SetEngine(nil) })
		return
	}
	h := macspeech.Find(dir)
	if h == nil {
		a.update(func() { a.rec.SetEngine(nil) })
		return
	}
	a.update(func() {
		a.rec.SetEngine(h)
		if recording.PreferOnDevice(&a.settings.Stt) {
			a.saveSettings()
		}
	})
}

type speechState struct {
	key string
	// keyLoaded is set once the keys kept here are being read, off the
	// main thread; keyTyped once the endpoint key is typed, which keeps
	// what the keychain had from replacing it; keyTimer keeps it once
	// typing pauses (keepEndpointKey).
	keyLoaded, keyTyped bool
	keyTimer            *time.Timer
	soniox              string
	// sonioxSaved is whether this device's keychain has a Soniox key.
	sonioxSaved bool
	// moss is the MOSS key typed, and mossSaved whether this device's
	// keychain has one.
	moss      string
	mossSaved bool
	// open is the speech provider whose settings show under its row, and
	// adding one being added, listed until it is set up.
	open, adding string
	saving       bool
	testing      bool
	testResult   string
	testOK       bool
	// micTesting is whether Record a test is running; micText is what it
	// heard, or why it heard nothing.
	micTesting bool
	micText    string
}

// sttControlW is the width of Speech to text's fields and lists; a
// field's input is that less its box's padding.
const sttControlW = 280

// loadSpeechKeys reads the speech keys kept here, once, off the main
// thread: the keychain can take a while, or ask first.
func (a *App) loadSpeechKeys() {
	s := &a.speech
	if s.keyLoaded {
		return
	}
	s.keyLoaded = true
	var key string
	var soniox, moss bool
	a.offMain(func() {
		key, soniox, moss = recording.EndpointKey(), recording.LocalSonioxKey() != "", recording.MossKey() != ""
	}, func() {
		if !s.keyTyped {
			s.key = key
		}
		s.sonioxSaved, s.mossSaved = soniox, moss
	})
}

func (a *App) speechSettings(c *ui.Context) {
	s := &a.speech
	stt := &a.settings.Stt
	a.loadSpeechKeys()
	changed := false
	keys := recording.KeysOf(*stt, s.sonioxSaved, s.mossSaved, a.session.SignedIn, a.rec.Engine != nil)
	set, addable := recording.SpeechProviders(*stt, keys)
	// A provider open whose settings no longer set it up, as an endpoint's
	// address emptied to type another, is being added again: it would go
	// from the list with the field being typed in.
	if s.adding == "" && s.open != "" && !recording.SetUp(s.open, *stt, keys) {
		s.adding = s.open
	}
	// A provider being added shows with its settings until it is set up.
	if s.adding != "" {
		if recording.SetUp(s.adding, *stt, keys) {
			s.adding = ""
		} else {
			set = append(set, recording.SpeechProvider{ID: s.adding, Name: recording.ProviderName(s.adding)})
			addable = slices.DeleteFunc(addable, func(p recording.SpeechProvider) bool { return p.ID == s.adding })
		}
	}
	headingWith(c, locales.RecSttTitle(), locales.RecSttSubtitle(), func() {
		add := primary(c, "add", "plus", locales.SettingsAddProvider()).Disabled(len(addable) == 0)
		add.Menu(func(m *ui.Menu) {
			for _, p := range addable {
				if m.Item(p.Name).Chosen() {
					s.adding, s.open, s.testResult = p.ID, p.ID, ""
				}
			}
		})
	})
	// One card for the providers, each opening its settings, as Agent
	// providers has them, the controls one width so their left edges line
	// up. There is no default: each transcription picks its provider.
	pal := theme.Of(c)
	card(c, "engine", func() {
		if len(set) == 0 {
			settingRow(c, "none", locales.RecSttNoProviders(), locales.RecSttNoProvidersDesc(), true, nil)
		}
		for i, p := range set {
			open := s.open == p.ID
			row := ui.ButtonBase(c.Key("sp:" + p.ID)).Justify(ui.Start).Label(p.Name)
			if i > 0 {
				row.BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider)
			}
			if row.Hovered() {
				row.Background(pal.Hover)
			}
			row.Children(func() {
				icon := "chevron-right"
				if open {
					icon = "chevron-down"
				}
				settingRow(c, "row", p.Name, p.Place, true, func() {
					ui.Icon(c, theme.Icon(icon)).FontSize(14).TextColor(pal.TextMuted)
				})
			})
			if row.Clicked() {
				s.testResult = ""
				if open {
					s.open = ""
				} else {
					s.open = p.ID
				}
			}
			if open {
				ui.Column(c.Key("fields:" + p.ID)).FillWidth().Background(pal.Ground).Children(func() {
					a.speechFields(c, p, stt, &changed)
				})
			}
		}
		if len(set) == 0 {
			return
		}
		// Each transcription picks its provider: speakers are labelled by
		// those that can, Soniox and MOSS.
		if recording.SetUp("soniox", *stt, keys) || recording.SetUp(recording.Moss, *stt, keys) {
			sp := stt.SpeakerLabels
			settingRow(c, "speakers", locales.RecSttSpeakers(), "", false, func() {
				if ui.Switch(c.Key("speakers"), &sp).Changed() {
					stt.SpeakerLabels = sp
					changed = true
				}
			})
		}
	})
	// Both engines' Test buttons report here.
	if s.testResult != "" {
		notice(c, map[bool]string{true: "ok", false: "danger"}[s.testOK], map[bool]string{true: "check", false: "circle-alert"}[s.testOK], s.testResult, nil)
	}
	label(c, locales.CommonRecording())
	card(c, "recording", func() {
		lang := stt.Language
		settingRow(c, "lang", locales.RecSttLanguage(), "", true, func() {
			if _, ok := recording.Languages[lang]; !ok {
				lang = "auto"
			}
			if selectBox(c, "lang", &lang, recording.LanguageCodes(), recording.LanguageName, sttControlW); lang != stt.Language {
				stt.Language = lang
				changed = true
			}
		})
		desc := s.micText
		if s.micTesting {
			switch {
			case a.rec.State.Starting:
				desc = locales.RecSttMicStarting()
			case a.rec.Active() && a.rec.State.LiveText != "":
				desc = a.rec.State.LiveText
			case a.rec.Active():
				desc = locales.RecSttListening(int(recording.MicTest / time.Second))
			default:
				desc = locales.RecTranscribing()
			}
		}
		settingRow(c, "test", locales.RecSttTestMic(), desc, false, func() {
			if v := a.recView(); s.micTesting && v != nil {
				// The capture page must be in the window to keep running.
				ui.WebView(c, v).Size(1, 1).Opacity(0.01)
			}
			label := locales.RecSttRecordTest()
			if s.micTesting {
				label = locales.RecSttTesting()
			}
			if button(c, "test", "record", label).Disabled(a.rec.Active() || s.micTesting).Clicked() {
				s.micTesting, s.micText = true, ""
				err := a.rec.TestMicrophone(a.settings.Stt, a.online && !a.settings.WorkOffline, func(heard string) {
					s.micTesting, s.micText = false, heard
				})
				if err != nil {
					s.micTesting, s.micText = false, err.Error()
				}
			}
		})
	})
	if changed {
		// An endpoint's address typed sets it up.
		recording.PreferSetUp(stt, keys)
		a.saveSettings()
		a.rec.MirrorSettings(a.settings.Stt)
	}
}

// speechFields are provider p's own settings, under its row: its key,
// address or model, a test of them, and taking it off the list.
func (a *App) speechFields(c *ui.Context, p recording.SpeechProvider, stt *store.SttSettings, changed *bool) {
	s := &a.speech
	switch p.ID {
	case recording.OnDevice:
		settingRow(c, "on-device", locales.RecSttOnDevice(), locales.RecSttOnDeviceDesc(), true, nil)
	case recording.Endpoint:
		settingRow(c, "url", locales.CommonEndpoint(), locales.RecSttEndpointDesc(), true, func() {
			if field(c, "url", &stt.BaseURL, "http://localhost:8000/v1").Width(sttControlW - 20).Changed() {
				*changed = true
			}
		})
		settingRow(c, "model", locales.CommonModel(), "", false, func() {
			if field(c, "model", &stt.Model, "gpt-transcribe").Width(sttControlW - 20).Changed() {
				*changed = true
			}
		})
		if strings.TrimSpace(stt.BaseURL) == "" {
			// The key row comes with an address: where it is says
			// where the key is kept.
		} else if recording.IsLocalEndpoint(stt.BaseURL) {
			settingRow(c, "key", locales.RecSttApiKey(), locales.RecSttKeyInKeychain(), false, func() {
				ui.Row(c).Width(sttControlW).Gap(8).AlignItems(ui.Center).Children(func() {
					if growPassword(c, "key", &s.key, locales.RecSttOptional()).Changed() {
						a.keepEndpointKey(s.key)
					}
					// The test goes on while the fields change: it has
					// them as they are now.
					url, model, key := stt.BaseURL, stt.Model, s.key
					a.sttTestButton(c, func(ctx context.Context) error {
						return recording.TestEndpoint(ctx, url, model, key)
					})
				})
			})
		} else {
			// The server calls a cloud endpoint with the key of one of
			// the agent providers.
			var ids []string
			names := map[string]string{"": locales.RecSttNoKey()}
			for _, p := range a.providers {
				if !p.IsLocal {
					ids = append(ids, p.ID)
					names[p.ID] = p.Name
				}
			}
			desc := locales.RecSttKeyFromDesc()
			if len(ids) == 0 {
				desc = locales.RecSttAddProvider()
			}
			settingRow(c, "key", locales.RecSttKeyFrom(), desc, false, func() {
				choice := stt.ProviderID
				if selectBox(c, "key-from", &choice, append([]string{""}, ids...), func(s string) string { return names[s] }, sttControlW); choice != stt.ProviderID {
					stt.ProviderID = choice
					*changed = true
				}
			})
		}
	case recording.Moss:
		ph := locales.RecSttPasteKey()
		if s.mossSaved {
			ph = locales.RecSttKeySaved()
		}
		settingRow(c, "moss-key", locales.RecSttMossKey(), locales.RecSttMossDesc(), true, func() {
			ui.Row(c).Width(sttControlW).Gap(8).AlignItems(ui.Center).Children(func() {
				if growPassword(c, "moss-key", &s.moss, ph).Changed() {
					s.testResult = ""
				}
				key, saved := strings.TrimSpace(s.moss), s.mossSaved
				a.sttTestButton(c, func(ctx context.Context) error {
					if key == "" && saved {
						key = recording.MossKey()
					}
					return recording.TestMossKey(ctx, key)
				})
				if button(c, "save-moss-key", "", locales.CommonSave()).Disabled(s.saving || strings.TrimSpace(s.moss) == "").Clicked() {
					a.saveMossKey()
				}
			})
		})
	case "soniox":
		where, ph := locales.RecSttSonioxOnServer(), locales.RecSttPasteKey()
		if !a.session.SignedIn {
			where = locales.RecSttSonioxOnDevice()
		}
		if s.sonioxSaved {
			ph = locales.RecSttKeySaved()
		}
		settingRow(c, "key", locales.RecSttSonioxKey(), where, true, func() {
			ui.Row(c).Width(sttControlW).Gap(8).AlignItems(ui.Center).Children(func() {
				if growPassword(c, "key", &s.soniox, ph).Changed() {
					s.testResult = ""
				}
				// With none typed, the test reads the one kept here, as it
				// runs: not the keychain each frame.
				key, saved := strings.TrimSpace(s.soniox), s.sonioxSaved
				a.sttTestButton(c, func(ctx context.Context) error {
					if key == "" && saved {
						key = recording.LocalSonioxKey()
					}
					return recording.TestSonioxKey(ctx, key)
				})
				if button(c, "save-key", "", locales.CommonSave()).Disabled(s.saving || strings.TrimSpace(s.soniox) == "").Clicked() {
					a.saveSonioxKey()
				}
			})
		})
		settingRow(c, "model", locales.CommonModel(), "", false, func() {
			if field(c, "model", &stt.SonioxModel, "stt-rt-v5").Width(sttControlW - 20).Changed() {
				*changed = true
			}
		})
	}
	if p.Removable {
		settingRow(c, "remove", "", "", false, func() {
			if danger(c, "remove", locales.CommonRemove()).Disabled(s.saving).Clicked() {
				a.removeSpeechProvider(p.ID)
			}
		})
	}
}

// sttTestButton is a Test button that runs test and shows how it went.
func (a *App) sttTestButton(c *ui.Context, test func(ctx context.Context) error) {
	s := &a.speech
	label := locales.RecSttTest()
	if s.testing {
		label = locales.RecSttTesting()
	}
	// Wide enough for "Testing…", so the row keeps its layout.
	if !button(c, "test", "", label).MinWidth(82).Justify(ui.Center).Disabled(s.testing).Clicked() {
		return
	}
	s.testing, s.testResult = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := test(ctx)
		a.update(func() {
			s.testing, s.testOK = false, err == nil
			if err != nil {
				s.testResult = err.Error()
			} else {
				s.testResult = locales.RecSttItWorks()
			}
		})
	}()
}

// keepEndpointKey keeps the endpoint key typed in the keychain once typing
// pauses, off the main thread, as the keychain can take a while, or ask
// first; an empty one it removes. The writes go one at a time, and only
// the last.
func (a *App) keepEndpointKey(key string) {
	s := &a.speech
	s.keyTyped = true
	if s.keyTimer != nil {
		s.keyTimer.Stop()
	}
	n := a.endpointKeys.Add(1)
	s.keyTimer = time.AfterFunc(500*time.Millisecond, func() {
		defer guard.Recover()
		a.endpointKeyMu.Lock()
		defer a.endpointKeyMu.Unlock()
		if a.endpointKeys.Load() != n {
			return
		}
		if err := recording.SetEndpointKey(key); err != nil {
			a.update(func() { a.toast(locales.RecSttKeyNotSaved(err.Error())) })
		}
	})
}

// speechKeys are the speech keys kept, as last read.
func (a *App) speechKeys() recording.Keys {
	s := &a.speech
	a.loadSpeechKeys()
	return recording.KeysOf(a.settings.Stt, s.sonioxSaved, s.mossSaved, a.session.SignedIn, a.rec.Engine != nil)
}

// sttFor is the speech settings a transcription on page pageID takes, with
// provider picked, or the one marked for the page when it is "": there is
// no default. One picked is kept as the one last picked anywhere.
func (a *App) sttFor(pageID, provider string) store.SttSettings {
	stt, last := a.rec.For(pageID, provider, a.settings.Stt, a.speechKeys())
	if last {
		a.settings.Stt.Provider = provider
		a.saveSettings()
		a.rec.MirrorSettings(a.settings.Stt)
	}
	return stt
}

// openPageID is the open page's ID, or "" with none open.
func (a *App) openPageID() string {
	if a.page == nil {
		return ""
	}
	return a.page.id
}

// transcribeItems are menu m's items picking the provider a transcription
// on page pageID takes, the one last picked there checked; do runs with
// the one picked.
func (a *App) transcribeItems(m *ui.Menu, pageID string, do func(provider string)) {
	keys := a.speechKeys()
	cur := a.rec.Choice(pageID, a.settings.Stt, keys)
	for _, p := range recording.Choices(a.settings.Stt, keys) {
		if m.Item(p.Name).Checked(p.ID == cur).Chosen() {
			do(p.ID)
		}
	}
}

// transcribeItem is menu m's item that transcribes on page pageID, as
// label says: with more than one provider set up, a submenu picking it,
// the one last picked there checked; do runs with the provider picked, or
// "" for the one marked.
func (a *App) transcribeItem(m *ui.Menu, pageID, label string, disabled bool, do func(provider string)) {
	choices := recording.Choices(a.settings.Stt, a.speechKeys())
	if len(choices) < 2 {
		if m.Item(label).Disabled(disabled).Chosen() {
			do("")
		}
		return
	}
	m.Submenu(label, func(m *ui.Menu) { a.transcribeItems(m, pageID, do) }).Disabled(disabled)
}

// preferSetUp has the speech settings transcribe with a provider set up,
// when the one they name is not, saving them if so: the first provider
// set up transcribes.
func (a *App) preferSetUp() {
	if recording.PreferSetUp(&a.settings.Stt, a.speechKeys()) {
		a.saveSettings()
		a.rec.MirrorSettings(a.settings.Stt)
	}
}

// removeSpeechProvider takes a speech provider off Settings' list, its key
// off this device, off the main thread.
func (a *App) removeSpeechProvider(id string) {
	s := &a.speech
	s.saving = true
	stt, keys := a.settings.Stt, a.speechKeys()
	var err error
	a.offMain(func() { keys, err = recording.RemoveProvider(id, &stt, keys) }, func() {
		s.saving = false
		if err != nil {
			a.toast(err.Error())
			return
		}
		s.sonioxSaved, s.mossSaved, s.open, s.testResult = keys.SonioxHere, keys.MossHere, "", ""
		if id == recording.Endpoint {
			s.key = ""
		}
		a.settings.Stt = stt
		a.saveSettings()
		a.rec.MirrorSettings(a.settings.Stt)
	})
}

// saveMossKey keeps the MOSS key typed in this device's keychain, off the
// main thread.
func (a *App) saveMossKey() {
	s := &a.speech
	key := strings.TrimSpace(s.moss)
	if key == "" {
		return
	}
	s.saving = true
	var err error
	a.offMain(func() { err = recording.SetMossKey(key) }, func() {
		s.saving = false
		if err != nil {
			a.toast(locales.RecSttKeyNotSaved(err.Error()))
			return
		}
		s.moss, s.mossSaved, s.testResult = "", true, ""
		a.preferSetUp()
		a.toast(locales.RecSttSavedOnDevice())
	})
}

// saveSonioxKey keeps the Soniox key typed on the server, or in this
// device's keychain when signed out, off the main thread.
func (a *App) saveSonioxKey() {
	s := &a.speech
	key := strings.TrimSpace(s.soniox)
	if key == "" {
		return
	}
	s.saving = true
	rec := a.rec
	var onServer bool
	var err error
	a.offMain(func() { onServer, err = rec.SaveSonioxKey(key) }, func() {
		s.saving = false
		if err != nil {
			a.toast(err.Error())
			return
		}
		s.soniox, s.testResult = "", ""
		if onServer {
			a.settings.Stt.SonioxOnServer = true
			a.saveSettings()
			a.preferSetUp()
			a.toast(locales.RecSttSavedOnServer())
			return
		}
		s.sonioxSaved = true
		a.preferSetUp()
		a.toast(locales.RecSttSavedOnDevice())
	})
}
