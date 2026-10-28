package app

import (
	_ "embed"
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// welcomeImage is a dawn over mountains and a lake, drawn for the guide.
//
//go:embed welcome/dawn.jpg
var welcomeImage []byte

// createWelcome gives a new space one page that shows what pages can do,
// with an example subpage inside it: writing, blocks, media, charts,
// embeds, to-dos, recordings, agents, organizing and finding pages,
// comments, history, sync and shortcuts. A comment shows a thread, and the
// subpage is also kept as a template. The guide and the meeting carry a tag
// each, so tags show from the start: in their bylines, the sidebar's Tags
// tab and search. They are written in the language the app shows when they
// are made.
func (a *App) createWelcome() error {
	if _, ok := a.st.Value("welcome.created"); ok {
		return nil
	}
	if pages, _ := a.st.Pages(); len(pages) > 0 {
		return a.st.SetValue("welcome.created", "true")
	}
	m := locales.In(i18n.Locale())
	// The picture is a file of the page and the subpage a page inside it,
	// so the page comes first and its blocks after.
	p, err := a.st.CreatePage(store.NewPageOptions{Title: m.WelcomeTitle(), Tags: []string{m.WelcomeTagGuide()}})
	if err != nil {
		return err
	}
	image, err := a.welcomeImageFile(p.ID, m.WelcomeImageName()+".jpg")
	if err != nil {
		return err
	}
	meetings := []string{m.WelcomeTagMeetings()}
	sub, err := a.st.CreatePage(store.NewPageOptions{ParentID: p.ID, Title: m.WelcomeSubTitle(), Tags: meetings, Content: welcomeSubpage(m)})
	if err != nil {
		return err
	}
	blocks, commentsID, err := welcomeBlocks(m, image, sub.ID)
	if err != nil {
		return err
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks(blocks)); err != nil {
		return err
	}
	if _, err := a.st.AddComment(p.ID, commentsID, m.WelcomeComment(), "", "", "agent", "Leaflet"); err != nil {
		return err
	}
	if _, err := a.st.CreatePage(store.NewPageOptions{Title: m.WelcomeSubTitle(), Template: true, Tags: meetings, Content: doc.FromBlocks(welcomeMeeting(m))}); err != nil {
		return err
	}
	if err := a.st.SetValue("welcome.page", p.ID); err != nil {
		return err
	}
	return a.st.SetValue("welcome.created", "true")
}

// welcomeImageFile adds the guide's picture as a file of its page, named
// name.
func (a *App) welcomeImageFile(pageID, name string) (store.File, error) {
	dir, err := os.MkdirTemp("", "leaflet-welcome")
	if err != nil {
		return store.File{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, welcomeImage, 0o600); err != nil {
		return store.File{}, err
	}
	return a.st.AddFile(path, "", pageID, "image/jpeg")
}

// welcomeBlocks are the guide's blocks in m's language, with its picture
// and a card for and a link to its subpage, and the ID of the block about
// comments, which the guide's example comment is on.
func welcomeBlocks(m locales.Messages, image store.File, subID string) ([]*doc.Node, string, error) {
	var blocks []*doc.Node
	var bad error
	add := func(spec map[string]any) {
		n, err := doc.SpecToNode(spec, "", "Leaflet")
		if err != nil && bad == nil {
			bad = err
		}
		if n != nil {
			blocks = append(blocks, n)
		}
	}
	text := func(typ, s string) { add(map[string]any{"type": typ, "text": s}) }
	heading := func(s string) { add(map[string]any{"type": "heading", "level": 2.0, "text": s}) }
	chart := func(spec map[string]any) { add(map[string]any{"type": "chart", "chart": spec}) }
	embed := func(title, page string, height int) {
		add(map[string]any{"type": "embed", "embed": map[string]any{"title": title, "html": page, "height": float64(height)}})
	}

	text("paragraph", m.WelcomeIntro())

	heading(m.WelcomeTryHeading())
	for _, s := range []string{
		m.WelcomeTrySlash(),
		m.WelcomeTryAt(),
		m.WelcomeTrySelect(),
		m.WelcomeTryRecord(),
		m.WelcomeTryProvider(),
		m.WelcomeTryTag(),
	} {
		text("todo_list", s)
	}

	heading(m.WelcomeWritingHeading())
	text("paragraph", m.WelcomeWritingMarkdown())
	text("quote", m.WelcomeQuote())
	add(map[string]any{"type": "numbered_list", "text": m.WelcomeListOutline(), "children": []any{
		map[string]any{"type": "bulleted_list", "text": m.WelcomeListNest()},
	}})
	text("numbered_list", m.WelcomeListAsk())
	text("numbered_list", m.WelcomeListReview())

	heading(m.WelcomeBlocksHeading())
	text("paragraph", m.WelcomeBlocksTable())
	you, agent := m.WelcomeTableYou(), m.WelcomeTableAgent()
	add(map[string]any{"type": "table", "rows": []any{
		[]any{m.WelcomeTableMilestone(), m.WelcomeTableOwner(), m.WelcomeTableDue()},
		[]any{m.WelcomeTableOutline(), you, m.WelcomeMonday()},
		[]any{m.WelcomeTableDraft(), agent, m.WelcomeWednesday()},
		[]any{m.WelcomeTableReview(), you, m.WelcomeFriday()},
	}})
	text("paragraph", m.WelcomeCodeIntro())
	add(map[string]any{"type": "code", "language": "javascript", "text": welcomeCode(m)})
	add(map[string]any{"type": "callout", "tone": "tip", "text": m.WelcomeCallout()})
	add(map[string]any{"type": "toggle_list", "text": m.WelcomeToggle(), "children": []any{
		map[string]any{"type": "paragraph", "text": m.WelcomeToggleInside()},
	}})
	text("paragraph", m.WelcomeMathDiagram())
	add(map[string]any{"type": "math_equation", "formula": `\int_0^1 x^2\,dx = \frac{1}{3} \qquad e^{i\pi} + 1 = 0`})
	add(map[string]any{"type": "diagram", "source": "flowchart LR\n  Idea([" + m.WelcomeDiagramIdea() + "]) --> Draft[" + m.WelcomeDiagramDraft() + "]\n  Draft --> Review{" + m.WelcomeDiagramReview() +
		"}\n  Review -->|" + m.WelcomeDiagramChanges() + "| Draft\n  Review -->|" + m.WelcomeDiagramOk() + "| Publish([" + m.WelcomeDiagramPublish() + "])"})

	heading(m.WelcomeMediaHeading())
	text("paragraph", m.WelcomeMediaImages())
	blocks = append(blocks, &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
		"url": "file://" + image.LocalPath, "align": "center", "alt": m.WelcomeImageAlt(), "file_id": image.ID,
	}})
	add(map[string]any{"type": "video", "video": map[string]any{"url": "https://www.youtube.com/watch?v=aqz-KE-bpKQ", "title": "Big Buck Bunny · Blender Foundation"}})
	text("paragraph", m.WelcomeMediaCharts())
	research := m.WelcomeChartResearch()
	chart(map[string]any{
		"chart_type": "bar", "title": m.WelcomeChartPagesTitle(), "labels": []any{m.WelcomeChartNotes(), m.WelcomeChartPlans(), research, m.WelcomeChartRecordings()},
		"series": []any{map[string]any{"name": m.WelcomeChartPages(), "values": []any{12.0, 7.0, 5.0, 3.0}}}, "caption": m.WelcomeChartCaption(),
	})
	chart(map[string]any{
		"chart_type": "line", "title": m.WelcomeChartWordsTitle(), "labels": []any{
			m.WelcomeChartMon(), m.WelcomeChartTue(), m.WelcomeChartWed(), m.WelcomeChartThu(), m.WelcomeChartFri(), m.WelcomeChartSat(), m.WelcomeChartSun(),
		},
		"series": []any{
			map[string]any{"name": you, "values": []any{420.0, 380.0, 650.0, 540.0, 720.0, 210.0, 300.0}},
			map[string]any{"name": m.WelcomeChartAgents(), "values": []any{150.0, 260.0, 300.0, 480.0, 390.0, 120.0, 90.0}},
		},
	})
	chart(map[string]any{
		"chart_type": "pie", "title": m.WelcomeChartWeekTitle(), "labels": []any{m.WelcomeChartWriting(), m.WelcomeChartMeetings(), research, m.WelcomeChartEmail()},
		"series": []any{map[string]any{"name": m.WelcomeChartHours(), "values": []any{14.0, 9.0, 8.0, 5.0}}},
	})

	heading(m.WelcomeEmbedsHeading())
	text("paragraph", m.WelcomeEmbedsIntro())
	embed(m.WelcomeSolarTitle(), solarSystemEmbed(m), 380)
	embed(m.WelcomeSavingsTitle(), savingsEmbed(m), 270)
	embed(m.WelcomePianoTitle(), pianoEmbed(m), 250)

	heading(m.WelcomeTodosHeading())
	text("todo_list", m.WelcomeDateTodo())
	text("paragraph", m.WelcomeTodosDue())
	text("paragraph", m.WelcomeRecordings())

	heading(m.WelcomeAgentsHeading())
	text("paragraph", m.WelcomeAgentsIntro())
	for _, s := range []string{
		m.WelcomeAgentsAt(),
		m.WelcomeAgentsSelect(),
		m.WelcomeAgentsPanel(),
		m.WelcomeAgentsSettings(),
		m.WelcomeAgentsExternal(),
	} {
		text("bulleted_list", s)
	}

	heading(m.WelcomeOrganizingHeading())
	text("paragraph", m.WelcomeOrganizingNest())
	blocks = append(blocks, &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": subID}})
	// The message says where the link to the subpage goes, by a mark in
	// its place.
	const mark = "￼"
	if link, err := doc.SpecToNode(map[string]any{"type": "paragraph", "text": m.WelcomeOrganizingLink(mark)}, "", "Leaflet"); err == nil {
		title := m.WelcomeSubTitle()
		link.SetDelta(withRun(link.Delta(), mark, doc.Run{Text: title, Attrs: doc.PageMention(subID, title)}))
		blocks = append(blocks, link)
	} else if bad == nil {
		bad = err
	}
	text("paragraph", m.WelcomeOrganizingFolders())
	text("paragraph", m.WelcomeOrganizingTags(m.WelcomeTagGuide(), m.WelcomeTagMeetings()))
	text("paragraph", m.WelcomeOrganizingHeadings())

	heading(m.WelcomeTogetherHeading())
	text("paragraph", m.WelcomeTogetherComments())
	commentsID := blocks[len(blocks)-1].ID
	text("paragraph", m.WelcomeTogetherHistory())
	text("paragraph", m.WelcomeTogetherSync())

	heading(m.WelcomeShortcutsHeading())
	add(map[string]any{"type": "table", "rows": []any{
		[]any{m.WelcomeShortcutsKeys(), m.WelcomeShortcutsDoes()},
		[]any{"⌘P", m.WelcomeShortcutSearch()},
		[]any{"⌘N", m.WelcomeShortcutNewPage()},
		[]any{"⌘J", m.WelcomeShortcutAgentPanel()},
		[]any{"⌘\\", m.WelcomeShortcutSidebar()},
		[]any{"⌘,", m.WelcomeShortcutSettings()},
		[]any{"⌘[  ⌘]", m.WelcomeShortcutBackForward()},
		[]any{"⌘B  ⌘I  ⌘U", m.WelcomeShortcutFormat()},
		[]any{"⌘E  ⇧⌘X", m.WelcomeShortcutCodeStrike()},
		[]any{"⌘K", m.WelcomeShortcutLink()},
		[]any{"⇧Enter", m.WelcomeShortcutLineBreak()},
		[]any{"Tab  ⇧Tab", m.WelcomeShortcutTab()},
		[]any{"⌘Z  ⇧⌘Z", m.WelcomeShortcutUndo()},
	}})
	return blocks, commentsID, bad
}

// withRun puts run in d where mark is, in place of the mark.
func withRun(d doc.Delta, mark string, run doc.Run) doc.Delta {
	var out doc.Delta
	for _, r := range d {
		before, after, ok := strings.Cut(r.Text, mark)
		if !ok {
			out = append(out, r)
			continue
		}
		if before != "" {
			out = append(out, doc.Run{Text: before, Attrs: r.Attrs})
		}
		out = append(out, run)
		if after != "" {
			out = append(out, doc.Run{Text: after, Attrs: r.Attrs})
		}
	}
	return out
}

// welcomeSubpage is the content of the guide's example subpage, in m's
// language: where it lives, that it is a template too, and the meeting.
func welcomeSubpage(m locales.Messages) *doc.Content {
	var blocks []*doc.Node
	for _, spec := range []map[string]any{
		{"type": "paragraph", "text": m.WelcomeSubIntro(m.WelcomeTitle())},
		{"type": "callout", "tone": "tip", "text": m.WelcomeSubTemplate(m.WelcomeSubTitle())},
	} {
		if n, err := doc.SpecToNode(spec, "", "Leaflet"); err == nil {
			blocks = append(blocks, n)
		}
	}
	return doc.FromBlocks(append(blocks, welcomeMeeting(m)...))
}

// welcomeMeeting are the blocks of the example meeting's notes, which the
// subpage shows and the template it is kept as starts with.
func welcomeMeeting(m locales.Messages) []*doc.Node {
	var blocks []*doc.Node
	for _, spec := range []map[string]any{
		{"type": "heading", "level": 2.0, "text": m.WelcomeSubAgenda()},
		{"type": "numbered_list", "text": m.WelcomeSubShipped()},
		{"type": "numbered_list", "text": m.WelcomeSubBlockers()},
		{"type": "numbered_list", "text": m.WelcomeSubPlans()},
		{"type": "heading", "level": 2.0, "text": m.WelcomeSubDecisions()},
		{"type": "bulleted_list", "text": m.WelcomeSubRelease()},
		{"type": "bulleted_list", "text": m.WelcomeSubDesignReview()},
		{"type": "heading", "level": 2.0, "text": m.WelcomeSubActions()},
		{"type": "table", "rows": []any{
			[]any{m.WelcomeTableTask(), m.WelcomeTableOwner(), m.WelcomeTableDue()},
			[]any{m.WelcomeSubReleaseNotes(), m.WelcomeTableYou(), m.WelcomeThursday()},
			[]any{m.WelcomeSubFeedback(), m.WelcomeTableAgent(), m.WelcomeWednesday()},
		}},
		{"type": "todo_list", "text": m.WelcomeSubEmail()},
	} {
		if n, err := doc.SpecToNode(spec, "", "Leaflet"); err == nil {
			blocks = append(blocks, n)
		}
	}
	return blocks
}

// welcomeCode is the guide's code example, its comment in m's language.
func welcomeCode(m locales.Messages) string {
	return "// " + m.WelcomeCodeComment() + `
function wordCount(text) {
  return text.split(/\s+/).filter(Boolean).length;
}

console.log(wordCount("Pages are living documents")); // 4`
}

// fillEmbed puts an embed's texts in its HTML: {{key}} as HTML text and
// {{js:key}} as a JavaScript string.
func fillEmbed(page string, texts map[string]string) string {
	pairs := make([]string, 0, 4*len(texts))
	for k, v := range texts {
		js, _ := json.Marshal(v)
		pairs = append(pairs, "{{"+k+"}}", html.EscapeString(v), "{{js:"+k+"}}", string(js))
	}
	return strings.NewReplacer(pairs...).Replace(page)
}

// The embeds' texts that count a number say it with "%1" in its place,
// which their scripts fill in.
const embedNumber = "%1"

// solarSystemEmbed draws the planets orbiting the sun, with a speed slider
// and a pause button, in m's language.
func solarSystemEmbed(m locales.Messages) string {
	return fillEmbed(solarSystemPage, map[string]string{
		"pause": m.WelcomeSolarPause(), "play": m.WelcomeSolarPlay(), "speed": m.WelcomeSolarSpeed(),
		"speedNow": m.WelcomeSolarDaysPerSecond(60, "60"),
		"speedOne": m.WelcomeSolarDaysPerSecond(1, embedNumber), "speedOther": m.WelcomeSolarDaysPerSecond(2, embedNumber),
		"year":    m.WelcomeSolarYear(embedNumber),
		"mercury": m.WelcomeSolarMercury(), "venus": m.WelcomeSolarVenus(), "earth": m.WelcomeSolarEarth(), "mars": m.WelcomeSolarMars(),
		"jupiter": m.WelcomeSolarJupiter(), "saturn": m.WelcomeSolarSaturn(), "uranus": m.WelcomeSolarUranus(), "neptune": m.WelcomeSolarNeptune(),
	})
}

const solarSystemPage = `<!doctype html>
<html><head><meta charset="utf-8"><style>
html,body{margin:0;height:100%;background:#070914;color:#d8ddff;font:13px -apple-system,system-ui,sans-serif;overflow:hidden}
canvas{display:block;width:100%;height:100%}
.bar{position:absolute;left:12px;right:12px;bottom:12px;display:flex;gap:12px;align-items:center;background:rgba(18,22,48,.72);padding:8px 12px;border-radius:10px}
.bar input{-webkit-appearance:none;appearance:none;flex:1;min-width:0;height:4px;border-radius:2px;outline:none;background:linear-gradient(#7b8cff,#7b8cff) 0/var(--p,0%) 100% no-repeat,rgba(255,255,255,.18)}
.bar input::-webkit-slider-thumb{-webkit-appearance:none;width:16px;height:16px;border-radius:50%;background:#e9ebff;box-shadow:0 1px 3px rgba(0,0,0,.4);cursor:pointer}
.bar button{background:#3446c9;color:#fff;border:0;border-radius:6px;padding:5px 12px;font:inherit;cursor:pointer}
.bar span{min-width:96px;text-align:right;font-variant-numeric:tabular-nums;color:#aab3ff;white-space:nowrap}
</style></head><body>
<canvas id="c"></canvas>
<div class="bar"><button id="p">{{pause}}</button><label for="s">{{speed}}</label><input id="s" type="range" min="1" max="500" step="1" value="60"><span id="v">{{speedNow}}</span></div>
<script>
var c = document.getElementById("c"), x = c.getContext("2d"), W = 0, H = 0;
function size() { var d = window.devicePixelRatio || 1; W = c.clientWidth; H = c.clientHeight; c.width = W * d; c.height = H * d; x.setTransform(d, 0, 0, d, 0, 0); }
size(); window.addEventListener("resize", size);
var planets = [
  {n: {{js:mercury}}, p: 88, r: 0.16, s: 2.5, c: "#b5b0a8"},
  {n: {{js:venus}}, p: 225, r: 0.24, s: 4, c: "#e8c27a"},
  {n: {{js:earth}}, p: 365, r: 0.32, s: 4.2, c: "#4f9dff"},
  {n: {{js:mars}}, p: 687, r: 0.40, s: 3.2, c: "#e0603e"},
  {n: {{js:jupiter}}, p: 4333, r: 0.56, s: 9, c: "#d9a66b"},
  {n: {{js:saturn}}, p: 10759, r: 0.70, s: 7.5, c: "#e6d39b", ring: true},
  {n: {{js:uranus}}, p: 30687, r: 0.82, s: 5.5, c: "#8fe0e6"},
  {n: {{js:neptune}}, p: 60190, r: 0.94, s: 5.5, c: "#5a73ff"}
];
var stars = [];
for (var i = 0; i < 220; i++) stars.push([Math.random(), Math.random(), Math.random() * 1.2 + 0.2]);
var days = 0, speed = 60, running = true, last = performance.now();
var s = document.getElementById("s"), v = document.getElementById("v"), b = document.getElementById("p");
function fill(el) { el.style.setProperty("--p", (el.value - el.min) / (el.max - el.min) * 100 + "%"); }
s.oninput = function () { speed = +s.value; v.textContent = (speed == 1 ? {{js:speedOne}} : {{js:speedOther}}).split("%1").join(speed); fill(s); };
fill(s);
b.onclick = function () { running = !running; b.textContent = running ? {{js:pause}} : {{js:play}}; };
function frame(now) {
  var dt = Math.min(now - last, 60) / 1000; last = now;
  if (running) days += dt * speed;
  x.fillStyle = "#070914"; x.fillRect(0, 0, W, H);
  for (var i = 0; i < stars.length; i++) { x.fillStyle = "rgba(255,255,255," + (0.25 + stars[i][2] / 3) + ")"; x.fillRect(stars[i][0] * W, stars[i][1] * H, stars[i][2], stars[i][2]); }
  var cx = W / 2, cy = (H - 44) / 2, R = Math.min(W / 2 - 16, (H - 60) / 2 / 0.55);
  var g = x.createRadialGradient(cx, cy, 0, cx, cy, 18);
  g.addColorStop(0, "#fff6c8"); g.addColorStop(0.4, "#ffc44d"); g.addColorStop(1, "rgba(255,150,40,0)");
  x.fillStyle = g; x.beginPath(); x.arc(cx, cy, 18, 0, 7); x.fill();
  // Each name goes beside its planet where it overlaps no other name, or
  // is left out.
  var placed = [];
  x.font = "11px -apple-system,system-ui";
  for (var j = 0; j < planets.length; j++) {
    var p = planets[j], a = days / p.p * Math.PI * 2, rr = p.r * R;
    x.strokeStyle = "rgba(150,160,255,0.13)"; x.beginPath(); x.ellipse(cx, cy, rr, rr * 0.55, 0, 0, 7); x.stroke();
    var px = cx + Math.cos(a) * rr, py = cy + Math.sin(a) * rr * 0.55;
    if (p.ring) { x.strokeStyle = "rgba(230,211,155,0.7)"; x.beginPath(); x.ellipse(px, py, p.s * 1.9, p.s * 0.7, -0.3, 0, 7); x.stroke(); }
    x.fillStyle = p.c; x.beginPath(); x.arc(px, py, p.s, 0, 7); x.fill();
    if (W > 420) {
      var tw = x.measureText(p.n).width;
      var spots = [[px + p.s + 4, py - p.s - 2], [px + p.s + 4, py + p.s + 11], [px - p.s - 4 - tw, py - p.s - 2], [px - p.s - 4 - tw, py + p.s + 11]];
      for (var k = 0; k < spots.length; k++) {
        var lx = spots[k][0], ly = spots[k][1], free = true;
        for (var q = 0; q < placed.length && free; q++) {
          var o = placed[q];
          free = lx > o[0] + o[2] + 4 || lx + tw + 4 < o[0] || Math.abs(ly - o[1]) > 12;
        }
        if (free) { x.fillStyle = "rgba(216,221,255,0.7)"; x.fillText(p.n, lx, ly); placed.push([lx, ly, tw]); break; }
      }
    }
  }
  x.fillStyle = "#aab3ff"; x.font = "12px -apple-system,system-ui";
  x.fillText({{js:year}}.split("%1").join((days / 365.25).toFixed(1)), 14, 22);
  requestAnimationFrame(frame);
}
requestAnimationFrame(frame);
</script></body></html>`

// savingsEmbed works out savings from a monthly deposit, a rate and the
// years, with a bar for each year, in m's language.
func savingsEmbed(m locales.Messages) string {
	return fillEmbed(savingsPage, map[string]string{
		"monthly": m.WelcomeSavingsMonthly(), "return": m.WelcomeSavingsReturn(), "years": m.WelcomeSavingsYears(),
		"yearOne": m.WelcomeSavingsYearCount(1, embedNumber), "yearOther": m.WelcomeSavingsYearCount(2, embedNumber),
		"saved": m.WelcomeSavingsSaved(), "earned": m.WelcomeSavingsEarned(), "summary": m.WelcomeSavingsSummary(embedNumber, "%2"),
	})
}

const savingsPage = `<!doctype html>
<html><head><meta charset="utf-8"><style>
html,body{margin:0;background:#fff;color:#16181d;font:14px -apple-system,system-ui,sans-serif}
.wrap{display:grid;grid-template-columns:minmax(200px,1fr) 1.3fr;gap:20px;padding:18px}
label{display:block;font-size:12px;color:#565c67;margin:0 0 4px}
.row{margin-bottom:14px}
.row div{display:flex;gap:10px;align-items:center}
input{-webkit-appearance:none;appearance:none;flex:1;height:4px;border-radius:2px;outline:none;background:linear-gradient(#3446c9,#3446c9) 0/var(--p,0%) 100% no-repeat,#dfe2ea}
input::-webkit-slider-thumb{-webkit-appearance:none;width:16px;height:16px;border-radius:50%;background:#fff;border:1px solid #c9ccd4;box-shadow:0 1px 3px rgba(0,0,0,.15);cursor:pointer}
output{min-width:70px;text-align:right;font-weight:600;font-variant-numeric:tabular-nums}
.total{font-size:30px;font-weight:700;letter-spacing:-.5px;font-variant-numeric:tabular-nums}
.sub{color:#565c67;font-size:12px;margin-top:2px}
.bars{display:flex;align-items:flex-end;gap:3px;height:120px;margin-top:14px}
.bars div{flex:1;border-radius:3px 3px 0 0;background:linear-gradient(#3446c9 var(--g),#c9d0f3 var(--g))}
.legend{display:flex;gap:14px;margin-top:8px;font-size:12px;color:#565c67}
.legend i{display:inline-block;width:9px;height:9px;border-radius:2px;margin-right:5px;vertical-align:-1px}
@media (max-width:480px){.wrap{grid-template-columns:1fr}}
:root[data-theme=dark],:root[data-theme=dark] body{background:#1b1e24;color:#e9ebef}
:root[data-theme=dark] label,:root[data-theme=dark] .sub,:root[data-theme=dark] .legend{color:#9ea4af}
:root[data-theme=dark] input{background:linear-gradient(#7b8cff,#7b8cff) 0/var(--p,0%) 100% no-repeat,#3a3f4a}
:root[data-theme=dark] input::-webkit-slider-thumb{background:#e9ebef;border-color:#e9ebef}
:root[data-theme=dark] .bars div{background:linear-gradient(#6a7bff var(--g),#3a4377 var(--g))}
:root[data-theme=dark] .legend span:first-child i{background:#3a4377!important}
:root[data-theme=dark] .legend span:last-child i{background:#6a7bff!important}
</style></head><body><div class="wrap">
<div>
<div class="row"><label for="m">{{monthly}}</label><div><input id="m" type="range" min="50" max="3000" step="50" value="500"><output id="mo"></output></div></div>
<div class="row"><label for="r">{{return}}</label><div><input id="r" type="range" min="0" max="12" step="0.25" value="5"><output id="ro"></output></div></div>
<div class="row"><label for="y">{{years}}</label><div><input id="y" type="range" min="1" max="40" step="1" value="20"><output id="yo"></output></div></div>
</div>
<div>
<div class="total" id="t"></div>
<div class="sub" id="d"></div>
<div class="bars" id="b"></div>
<div class="legend"><span><i style="background:#c9d0f3"></i>{{saved}}</span><span><i style="background:#3446c9"></i>{{earned}}</span></div>
</div>
</div>
<script>
var m = document.getElementById("m"), r = document.getElementById("r"), y = document.getElementById("y");
function money(v) { return "$" + Math.round(v).toLocaleString("en-US"); }
function update() {
  [m, r, y].forEach(function (el) { el.style.setProperty("--p", (el.value - el.min) / (el.max - el.min) * 100 + "%"); });
  var monthly = +m.value, rate = +r.value / 100 / 12, years = +y.value;
  document.getElementById("mo").textContent = money(monthly);
  document.getElementById("ro").textContent = (+r.value).toFixed(2) + "%";
  document.getElementById("yo").textContent = (years == 1 ? {{js:yearOne}} : {{js:yearOther}}).split("%1").join(years);
  var balance = 0, rows = [];
  for (var i = 1; i <= years * 12; i++) {
    balance = balance * (1 + rate) + monthly;
    if (i % 12 == 0) rows.push([balance, monthly * i]);
  }
  var paid = monthly * years * 12;
  document.getElementById("t").textContent = money(balance);
  document.getElementById("d").textContent = {{js:summary}}.split("%1").join(money(paid)).split("%2").join(money(balance - paid));
  var b = document.getElementById("b"); b.innerHTML = "";
  var top = rows[rows.length - 1][0];
  rows.forEach(function (row) {
    var bar = document.createElement("div");
    bar.style.height = (row[0] / top * 100) + "%";
    bar.style.setProperty("--g", ((1 - row[1] / row[0]) * 100) + "%");
    bar.title = money(row[0]);
    b.appendChild(bar);
  });
}
[m, r, y].forEach(function (el) { el.oninput = update; });
update();
</script></body></html>`

// pianoEmbed is an octave and a half to play with the pointer or the
// keyboard, through the Web Audio API, in m's language.
func pianoEmbed(m locales.Messages) string {
	return fillEmbed(pianoPage, map[string]string{"hint": m.WelcomePianoHint()})
}

const pianoPage = `<!doctype html>
<html><head><meta charset="utf-8"><style>
html,body{margin:0;height:100%;background:#f5f6f8;font:12px -apple-system,system-ui,sans-serif;user-select:none;-webkit-user-select:none}
.wrap{display:flex;flex-direction:column;align-items:center;gap:12px;padding:16px;outline:none}
.kb{position:relative;display:flex}
.w{width:44px;height:160px;background:#fff;border:1px solid #c9ccd4;border-radius:0 0 7px 7px;margin-right:-1px;display:flex;align-items:flex-end;justify-content:center;padding-bottom:9px;box-sizing:border-box;color:#9aa1ad;cursor:pointer;box-shadow:inset 0 -4px 0 #eceef2}
.b{position:absolute;top:0;width:28px;height:100px;background:#1b1e24;border-radius:0 0 5px 5px;z-index:2;cursor:pointer;color:#8a909b;display:flex;align-items:flex-end;justify-content:center;padding-bottom:6px;box-sizing:border-box;font-size:10px}
.w.on{background:#dfe3f6;box-shadow:inset 0 -2px 0 #c9d0f3}
.b.on{background:#3446c9;color:#dfe3f6}
.hint{color:#6b717c}
:root[data-theme=dark],:root[data-theme=dark] body{background:#1b1e24}
:root[data-theme=dark] .w{background:#e4e6ea;border-color:#2e333c;box-shadow:inset 0 -4px 0 #c9ccd4;color:#6b717c}
:root[data-theme=dark] .b{background:#0b0c0f}
:root[data-theme=dark] .w.on{background:#bac3ff}
:root[data-theme=dark] .b.on{background:#4f5fd9}
:root[data-theme=dark] .hint{color:#9ea4af}
</style></head><body>
<div class="wrap" tabindex="0" id="wrap"><div class="kb" id="kb"></div><div class="hint">{{hint}}</div></div>
<script>
var whites = [[0, "A"], [2, "S"], [4, "D"], [5, "F"], [7, "G"], [9, "H"], [11, "J"], [12, "K"], [14, "L"], [16, ";"]];
var blacks = [[1, "W", 0], [3, "E", 1], [6, "T", 3], [8, "Y", 4], [10, "U", 5], [13, "O", 7], [15, "P", 8]];
var kb = document.getElementById("kb"), keys = {}, ctx = null;
function audio() {
  if (!ctx) ctx = new (window.AudioContext || window.webkitAudioContext)();
  if (ctx.state === "suspended") ctx.resume();
  return ctx;
}
function play(semi) {
  var a = audio(), f = 261.63 * Math.pow(2, semi / 12), t = a.currentTime;
  var out = a.createGain(); out.connect(a.destination);
  out.gain.setValueAtTime(0, t); out.gain.linearRampToValueAtTime(0.3, t + 0.008); out.gain.exponentialRampToValueAtTime(0.001, t + 1.6);
  [[1, "triangle", 1], [2, "sine", 0.35], [3, "sine", 0.12]].forEach(function (h) {
    var o = a.createOscillator(), g = a.createGain();
    o.type = h[1]; o.frequency.value = f * h[0]; g.gain.value = h[2];
    o.connect(g); g.connect(out); o.start(t); o.stop(t + 1.7);
  });
}
function key(semi, letter, cls) {
  var el = document.createElement("div");
  el.className = cls; el.textContent = letter;
  el.onpointerdown = function (e) { e.preventDefault(); press(semi); };
  el.onpointerup = el.onpointerleave = function () { release(semi); };
  keys[semi] = el; keys[letter.toLowerCase()] = semi;
  return el;
}
function press(semi) { if (keys[semi].className.indexOf(" on") < 0) { keys[semi].className += " on"; play(semi); } }
function release(semi) { keys[semi].className = keys[semi].className.replace(" on", ""); }
whites.forEach(function (w) { kb.appendChild(key(w[0], w[1], "w")); });
blacks.forEach(function (b) {
  var el = key(b[0], b[1], "b");
  el.style.left = (b[2] * 43 + 43 - 14) + "px";
  kb.appendChild(el);
});
// The keyboard shrinks to fit a narrow frame.
function fit() { kb.style.zoom = Math.min(1, (innerWidth - 32) / 431); }
fit(); addEventListener("resize", fit);
document.addEventListener("keydown", function (e) {
  var semi = keys[e.key.toLowerCase()];
  if (typeof semi === "number" && !e.repeat) { press(semi); e.preventDefault(); }
});
document.addEventListener("keyup", function (e) {
  var semi = keys[e.key.toLowerCase()];
  if (typeof semi === "number") release(semi);
});
</script></body></html>`
