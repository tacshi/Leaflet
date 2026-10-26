package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/audio"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/keychain"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/internal/theme"
)

// startRecording records into the open page.
func (a *App) startRecording(pageID, _ string) {
	ed := a.Editor(pageID)
	err := a.rec.Start(pageID, a.settings.Stt, a.online && !a.settings.WorkOffline, func(n *doc.Node) {
		if ed != nil {
			ed.InsertBlocks(n)
			a.savePage()
			return
		}
		if c := a.st.Content(pageID); c != nil {
			_ = c.Insert(doc.Position{At: "end"}, n)
			if _, _, err := a.st.SaveContent(pageID, c); err != nil {
				a.toast("Could not save: " + err.Error())
			}
		}
	})
	if err != nil {
		a.toast(err.Error())
	}
}

func fmtDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
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
		label := fmtDuration(a.rec.Elapsed())
		if st.Starting {
			label = "Starting…"
		}
		ui.Text(c, label).FontSize(12.5).FontWeight(600).TextColor(pal.DangerText).FontFeatures("tnum")
		c.After(500 * time.Millisecond)
		if v := a.rec.View(); v != nil {
			// The capture page must be in the window to keep running.
			ui.WebView(c, v).Size(1, 1).Opacity(0.01)
		}
		pause := ui.ButtonBase(c.Key("pause")).Size(22, 22).Center().Radius(999).Label("Pause").TextColor(pal.DangerText)
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
		stop := ui.ButtonBase(c.Key("stop")).Height(22).Padding(0, 8).Radius(999).Background(pal.Danger).TextColor(pal.OnAccent).Label("Stop")
		stop.Children(func() { ui.Text(c, "Stop").FontSize(12).FontWeight(600) })
		if stop.Clicked() {
			a.rec.Stop()
		}
	})
}

// player is the playback of one recording; from is where it starts once
// its audio loads, as a time chosen before then asks.
type player struct {
	sound   *audio.Sound
	p       *audio.Player
	loading bool
	from    time.Duration
	err     string
}

var players = map[string]*player{}

func (a *App) play(rec store.Recording) {
	pl := players[rec.ID]
	if pl == nil {
		pl = &player{}
		players[rec.ID] = pl
	}
	if pl.p != nil {
		if pl.p.Playing() {
			pl.p.Pause()
		} else {
			if pl.p.Position() >= pl.p.Duration() {
				pl.p.Seek(0)
			}
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
			if err != nil {
				pl.err = err.Error()
				return
			}
			pl.sound = snd
			pl.p = snd.NewPlayer()
			if pl.from > 0 {
				pl.p.Seek(pl.from)
			}
			pl.p.Play()
		})
	}()
}

func (a *App) recordingBlock(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	id := n.Str("recording_id")
	rec, err := a.st.Recording(id)
	live := a.rec.Active() && a.rec.State.ID == id
	ui.Column(c.Key("rec:"+n.ID)).Padding(14, 16).Gap(12).Radius(10).Border(1, pal.Divider).Background(pal.Surface).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(30, 30).Radius(999).Center().Background(map[bool]ui.Color{true: pal.DangerBg, false: pal.AccentSoft}[live]).Children(func() {
				ui.Icon(c, theme.Icon("mic")).FontSize(15).TextColor(map[bool]ui.Color{true: pal.Danger, false: pal.AccentText}[live])
			})
			ui.Column(c).Grow(1).Children(func() {
				ui.Text(c, a.recordingTitle(n, rec, err == nil)).FontSize(14).FontWeight(600).TextColor(pal.Text)
				status := ""
				switch {
				case err != nil:
					status = "Recording not found on this device"
				case live:
					status = a.rec.State.Engine
				case a.rec.Finishing[id]:
					status = "Adding timestamps…"
				case rec.Status == "transcribing" || a.rec.Transcribing[id]:
					status = "Transcribing…"
				case rec.Status == "failed":
					status = "Transcription failed"
				case rec.SttProvider == "":
					status = fmtDuration(time.Duration(rec.DurationMs)*time.Millisecond) + " · Audio only"
				default:
					status = fmtDuration(time.Duration(rec.DurationMs)*time.Millisecond) + " · " + map[bool]string{true: "Soniox", false: "Endpoint"}[rec.SttProvider == "soniox"]
				}
				ui.Text(c, status).FontSize(12).TextColor(pal.TextMuted)
			})
			if err == nil && !live && rec.Status != "recording" {
				pl := players[id]
				icon, label := "play", "Play"
				if pl != nil && pl.p != nil && pl.p.Playing() {
					icon, label = "pause", "Pause"
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
					more = iconButton(c, "more", "ellipsis", "Recording actions")
				})
				more.Menu(func(m *ui.Menu) {
					if m.Item("Copy transcript").Disabled(len(rec.Transcript) == 0).Chosen() {
						c.WriteClipboard(transcriptPlain(rec))
						c.Toast("Copied")
					}
					if m.Item("Insert transcript as text").Disabled(len(rec.Transcript) == 0).Chosen() {
						a.insertTranscript(n.ID, rec)
					}
					if m.Item("Summarize with agent").Disabled(len(rec.Transcript) == 0 || !a.hub.Available()).Chosen() {
						a.panelOpen, a.panelTab = true, 0
						a.hub.Chat(a.page.id, "Summarize the recording “"+n.Str("title")+"” below it on the page. Its transcript:\n"+transcriptText(rec), []string{n.ID})
					}
					label := "Transcribe again"
					if len(rec.Transcript) == 0 {
						label = "Transcribe"
					}
					if m.Item(label).Chosen() {
						if err := a.rec.Retranscribe(id, a.settings.Stt, a.online && !a.settings.WorkOffline); err != nil {
							a.toast(err.Error())
						}
					}
				})
			}
		})
		if live {
			a.levelMeter(c, a.rec.State.Levels)
			if t := a.rec.State.LiveText; t != "" {
				ui.Text(c, t).FontSize(13.5).TextColor(pal.TextSecondary).Font(theme.PageFont)
			}
			return
		}
		if err != nil {
			return
		}
		if pl := players[id]; pl != nil && pl.p != nil {
			a.scrubber(c, pl)
		}
		if rec.Status == "failed" && rec.Error != "" {
			notice(c, "danger", "circle-alert", rec.Error, nil)
		}
		if len(rec.Transcript) > 0 {
			// A click on a line plays from it, so its text is not selectable;
			// the menu's Copy transcript copies it.
			ui.Column(c).Gap(8).Children(func() {
				for i, s := range recording.Tidy(rec.Transcript) {
					// The time sits on the line's baseline, though its type is smaller.
					ui.Row(c.Key(i)).Gap(12).AlignItems(ui.Baseline).Children(func() {
						ts := ui.ButtonBase(c.Key("ts")).Width(46)
						ts.Children(func() {
							ui.Text(c, fmtDuration(time.Duration(s.StartMs)*time.Millisecond)).FontSize(12).TextColor(pal.Accent).FontFeatures("tnum")
						})
						if ts.Clicked() {
							a.seek(rec, time.Duration(s.StartMs)*time.Millisecond)
						}
						line := ui.Column(c.Key("line")).Grow(1).Padding(0, 6).Margin(0, -6).Radius(6)
						if line.Hovered() {
							line.Background(pal.Hover)
						}
						line.Children(func() {
							if s.Speaker != "" {
								ui.Text(c, "Speaker "+s.Speaker).FontSize(11.5).FontWeight(600).TextColor(pal.TextMuted)
							}
							ui.Text(c, s.Text).FontSize(14).Font(theme.PageFont).TextColor(pal.Text)
						})
						if line.Clicked() {
							a.seek(rec, time.Duration(s.StartMs)*time.Millisecond)
						}
					})
				}
			})
		}
	})
}

// recordingTitle is the name a recording block shows: its title, with the
// time it was made where the title, as older blocks', gives only the day,
// or just "Recording" on a page of the same name, as the menu bar makes.
func (a *App) recordingTitle(n *doc.Node, rec store.Recording, found bool) string {
	title := n.Str("title")
	if day := rec.CreatedAt.Local(); found && !rec.CreatedAt.IsZero() && title == "Recording · "+day.Format("Jan 2") {
		title = "Recording · " + day.Format("Jan 2, 15:04")
	}
	if a.page != nil && strings.TrimSpace(a.page.title) == title {
		return "Recording"
	}
	return title
}

// transcriptPlain is a recording's transcript as text to copy: a line
// each, after its speaker's name when it has one.
func transcriptPlain(rec store.Recording) string {
	var lines []string
	for _, s := range recording.Tidy(rec.Transcript) {
		if s.Speaker != "" {
			lines = append(lines, "Speaker "+s.Speaker+": "+s.Text)
		} else {
			lines = append(lines, s.Text)
		}
	}
	return strings.Join(lines, "\n")
}

func transcriptText(rec store.Recording) string {
	var lines []string
	for _, s := range recording.Tidy(rec.Transcript) {
		lines = append(lines, "["+fmtDuration(time.Duration(s.StartMs)*time.Millisecond)+"] "+s.Text)
	}
	return strings.Join(lines, "\n")
}

// seek plays a recording from t, once its audio loads if it has not.
func (a *App) seek(rec store.Recording, t time.Duration) {
	pl := players[rec.ID]
	if pl == nil || pl.p == nil {
		a.play(rec)
		players[rec.ID].from = t
		return
	}
	pl.p.Seek(t)
	if !pl.p.Playing() {
		pl.p.Play()
	}
}

func (a *App) scrubber(c *ui.Context, pl *player) {
	pal := theme.Of(c)
	pos, dur := pl.p.Position(), pl.p.Duration()
	frac := 0.0
	if dur > 0 {
		frac = float64(pos) / float64(dur)
	}
	ui.Row(c.Key("scrub")).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmtDuration(pos)).FontSize(12).TextColor(pal.TextMuted).FontFeatures("tnum")
		v := frac
		if ui.SliderBase(c.Key("bar"), &v, 0, 1).Grow(1).Height(14).Draw(func(p *ui.Painter, r ui.Rect) {
			y := r.Y + r.H/2 - 2
			p.Fill(ui.Rect{X: r.X, Y: y, W: r.W, H: 4}, pal.Divider, 2)
			p.Fill(ui.Rect{X: r.X, Y: y, W: r.W * float32(v), H: 4}, pal.Accent, 2)
			p.Fill(ui.Rect{X: r.X + r.W*float32(v) - 6, Y: r.Y + r.H/2 - 6, W: 12, H: 12}, pal.Accent, 6)
		}).Changed() {
			pl.p.Seek(time.Duration(v * float64(dur)))
		}
		ui.Text(c, fmtDuration(dur)).FontSize(12).TextColor(pal.TextMuted).FontFeatures("tnum")
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
	var nodes []*doc.Node
	for _, s := range recording.Tidy(rec.Transcript) {
		n := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
		n.SetDelta(doc.Delta{{Text: fmtDuration(time.Duration(s.StartMs)*time.Millisecond) + "  ", Attrs: doc.Attrs{Bold: true}}, {Text: s.Text}})
		nodes = append(nodes, n)
	}
	ed.Edit(func(c *doc.Content) { _ = c.Insert(doc.Position{After: blockID}, nodes...) })
}

// Speech to text ---------------------------------------------------------

type speechState struct {
	key       string
	keyLoaded bool
	soniox    string
	// sonioxSaved is whether this device's keychain has a Soniox key.
	sonioxSaved bool
	saving      bool
	testing     bool
	testResult  string
	testOK      bool
	// micTesting is whether Record a test is running; micText is what it
	// heard, or why it heard nothing.
	micTesting bool
	micText    string
}

var speech speechState

// sttControlW is the width of Speech to text's fields and lists; a
// field's input is that less its box's padding.
const sttControlW = 280

func (a *App) speechSettings(c *ui.Context) {
	pal := theme.Of(c)
	s := &speech
	stt := &a.settings.Stt
	if !s.keyLoaded {
		s.keyLoaded = true
		if k, err := keychain.Get("stt.endpoint.key"); err == nil {
			s.key = string(k)
		}
		s.sonioxSaved = recording.LocalSonioxKey() != ""
	}
	changed := false
	heading(c, "Speech to text", "How recordings become transcripts.")
	prov := map[string]int{"openai-compatible": 0, "soniox": 1}[stt.Provider]
	// One card for the engine and its settings, their controls one width
	// so their left edges line up.
	card(c, "engine", func() {
		settingRow(c, "provider", "Transcription", engineStatus(*stt, s.sonioxSaved || a.session.SignedIn), true, func() {
			if ui.Segmented(c.Key("provider"), &prov, "Endpoint", "Soniox"); true {
				want := []string{"openai-compatible", "soniox"}[prov]
				if want != stt.Provider {
					stt.Provider = want
					changed = true
				}
			}
		})
		if stt.Provider == "openai-compatible" {
			settingRow(c, "url", "Endpoint", "OpenAI-compatible; one on your network works offline", false, func() {
				if field(c, "url", &stt.BaseURL, "http://localhost:8000/v1").Width(sttControlW - 20).Changed() {
					changed = true
				}
			})
			settingRow(c, "model", "Model", "", false, func() {
				if field(c, "model", &stt.Model, "gpt-transcribe").Width(sttControlW - 20).Changed() {
					changed = true
				}
			})
			if strings.TrimSpace(stt.BaseURL) == "" {
				// The key row comes with an address: where it is says
				// where the key is kept.
			} else if recording.IsLocalEndpoint(stt.BaseURL) {
				settingRow(c, "key", "API key", "Kept in this device's keychain", false, func() {
					ui.Row(c).Width(sttControlW).Gap(8).AlignItems(ui.Center).Children(func() {
						if growPassword(c, "key", &s.key, "Optional").Changed() {
							if err := keychain.Set("stt.endpoint.key", []byte(s.key)); err != nil {
								a.toast("Could not save the key: " + err.Error())
							}
						}
						a.sttTestButton(c, func(ctx context.Context) error {
							_, err := recording.TranscribeEndpoint(ctx, stt.BaseURL, stt.Model, recording.WavFromPCM(make([]byte, recording.SampleRate)), s.key, "auto")
							return err
						})
					})
				})
			} else {
				// The server calls a cloud endpoint with the key of one of
				// the agent providers.
				var ids []string
				names := map[string]string{"": "No key"}
				for _, p := range a.providers {
					if !p.IsLocal {
						ids = append(ids, p.ID)
						names[p.ID] = p.Name
					}
				}
				desc := "The server calls the endpoint with this provider's key"
				if len(ids) == 0 {
					desc = "Add a cloud agent provider, such as OpenAI, to use its key here"
				}
				settingRow(c, "key", "API key from", desc, false, func() {
					choice := stt.ProviderID
					if selectBox(c, "key-from", &choice, append([]string{""}, ids...), func(s string) string { return names[s] }, sttControlW); choice != stt.ProviderID {
						stt.ProviderID = choice
						changed = true
					}
				})
			}
			return
		}
		where, ph := "Live transcripts; key kept on the server", "Paste a key"
		if !a.session.SignedIn {
			where = "Live transcripts; key kept on this device"
		}
		if s.sonioxSaved {
			ph = "Saved"
		}
		settingRow(c, "key", "Soniox API key", where, false, func() {
			ui.Row(c).Width(sttControlW).Gap(8).AlignItems(ui.Center).Children(func() {
				if growPassword(c, "key", &s.soniox, ph).Changed() {
					s.testResult = ""
				}
				key := strings.TrimSpace(s.soniox)
				if key == "" && s.sonioxSaved {
					key = recording.LocalSonioxKey()
				}
				a.sttTestButton(c, func(ctx context.Context) error { return recording.TestSonioxKey(ctx, key) })
				if button(c, "save-key", "", "Save").Disabled(s.saving || strings.TrimSpace(s.soniox) == "").Clicked() {
					a.saveSonioxKey()
				}
			})
		})
		settingRow(c, "model", "Model", "", false, func() {
			if field(c, "model", &stt.SonioxModel, "stt-rt-v5").Width(sttControlW - 20).Changed() {
				changed = true
			}
		})
		sp := stt.SpeakerLabels
		settingRow(c, "speakers", "Label speakers", "", false, func() {
			if ui.Switch(c.Key("speakers"), &sp).Changed() {
				stt.SpeakerLabels = sp
				changed = true
			}
		})
	})
	// Both engines' Test buttons report here.
	if s.testResult != "" {
		notice(c, map[bool]string{true: "ok", false: "danger"}[s.testOK], map[bool]string{true: "check", false: "circle-alert"}[s.testOK], s.testResult, nil)
	}
	label(c, "Recording")
	card(c, "recording", func() {
		lang := stt.Language
		settingRow(c, "lang", "Language", "", true, func() {
			if _, ok := recording.Languages[lang]; !ok {
				lang = "auto"
			}
			if selectBox(c, "lang", &lang, recording.LanguageCodes(), recording.LanguageName, sttControlW); lang != stt.Language {
				stt.Language = lang
				changed = true
			}
		})
		keep := stt.KeepAudio
		settingRow(c, "keep", "Keep the audio", "Otherwise only the transcript stays", false, func() {
			if ui.Switch(c.Key("keep"), &keep).Changed() {
				stt.KeepAudio = keep
				changed = true
			}
		})
		desc := s.micText
		if s.micTesting {
			switch {
			case a.rec.State.Starting:
				desc = "Starting the microphone…"
			case a.rec.Active() && a.rec.State.LiveText != "":
				desc = a.rec.State.LiveText
			case a.rec.Active():
				desc = "Listening for 4 seconds; say something"
			default:
				desc = "Transcribing…"
			}
		}
		settingRow(c, "test", "Test with microphone", desc, false, func() {
			if v := a.rec.View(); s.micTesting && v != nil {
				// The capture page must be in the window to keep running.
				ui.WebView(c, v).Size(1, 1).Opacity(0.01)
			}
			label := "Record a test"
			if s.micTesting {
				label = "Testing…"
			}
			if button(c, "test", "mic", label).Disabled(a.rec.Active() || s.micTesting).Clicked() {
				s.micTesting, s.micText = true, ""
				err := a.rec.Test(a.settings.Stt, a.online && !a.settings.WorkOffline, 4*time.Second, func(text string, err error) {
					s.micTesting, s.micText = false, "“"+text+"”"
					if err != nil {
						s.micText = err.Error()
					}
				})
				if err != nil {
					s.micTesting, s.micText = false, err.Error()
				}
			}
		})
	})
	if changed {
		a.saveSettings()
		a.syncSttSettings()
	}
	_ = pal
}

// engineStatus says where transcription runs, or that it is not set up.
func engineStatus(stt store.SttSettings, hasSonioxKey bool) string {
	if stt.Provider == "soniox" && !hasSonioxKey || stt.Provider != "soniox" && strings.TrimSpace(stt.BaseURL) == "" {
		return "Not set up"
	}
	return recording.EngineLabel(stt)
}

// sttTestButton is a Test button that runs test and shows how it went.
func (a *App) sttTestButton(c *ui.Context, test func(ctx context.Context) error) {
	s := &speech
	label := "Test"
	if s.testing {
		label = "Testing…"
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
				s.testResult = "It works"
			}
		})
	}()
}

// saveSonioxKey keeps the Soniox key on the server, or in this device's
// keychain when there is no server to keep it.
func (a *App) saveSonioxKey() {
	s := &speech
	key := strings.TrimSpace(s.soniox)
	if a.session.SignedIn {
		a.saveSttKey("soniox", key)
		return
	}
	if err := recording.SetLocalSonioxKey(key); err != nil {
		a.toast("Could not save the key: " + err.Error())
		return
	}
	s.soniox, s.sonioxSaved, s.testResult = "", true, ""
	a.toast("Saved the key on this device")
}

func (a *App) saveSttKey(provider, key string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	speech.saving = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := a.backend.Invoke(ctx, "stt", map[string]any{"action": "save_key", "provider": provider, "api_key": key})
		a.update(func() {
			speech.saving = false
			if err != nil {
				a.toast("Could not save the key: " + err.Error())
				return
			}
			speech.soniox, speech.key = "", ""
			a.toast("Saved the key on the server")
		})
	}()
}

// syncSttSettings mirrors the speech settings to the server.
func (a *App) syncSttSettings() {
	if !a.backend.SignedIn() {
		return
	}
	stt := a.settings.Stt
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		row := map[string]any{"user_id": a.backend.UserID(), "provider": stt.Provider, "language": stt.Language,
			"live_transcript": stt.LiveTranscript, "keep_audio": stt.KeepAudio, "speaker_labels": stt.SpeakerLabels,
			"base_url": stt.BaseURL, "model": stt.Model, "provider_id": nilIfEmpty(stt.ProviderID)}
		if _, err := a.backend.Upsert(ctx, "stt_settings", row, "user_id"); err != nil {
			fmt.Println("stt settings:", err)
		}
	}()
}

var _ = agents.Describe

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
