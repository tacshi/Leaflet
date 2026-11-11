package core

import (
	_ "embed"
	"encoding/json"
	"errors"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

//go:embed welcome/dawn.jpg
var welcomeImage []byte

func (c *Core) createWelcome() (err error) {
	if _, ok := c.Store.Value("welcome.created"); ok {
		return nil
	}
	if pages, _ := c.Store.Pages(); len(pages) > 0 {
		return c.Store.SetValue("welcome.created", "true")
	}
	var made []string
	defer func() {
		if err != nil {
			err = errors.Join(err, c.Store.RemovePages(made))
		}
	}()
	m := locales.In(i18n.Locale())
	p, err := c.Store.CreatePage(store.NewPageOptions{Title: m.WelcomeTitle(), Tags: []string{m.WelcomeTagGuide()}})
	if err != nil {
		return err
	}
	made = append(made, p.ID)
	image, err := c.welcomeImageFile(p.ID, m.WelcomeImageName()+".jpg")
	if err != nil {
		return err
	}
	meetings := []string{m.WelcomeTagMeetings()}
	sub, err := c.Store.CreatePage(store.NewPageOptions{ParentID: p.ID, Title: m.WelcomeSubTitle(), Tags: meetings, Content: welcomeSubpage(m)})
	if err != nil {
		return err
	}
	made = append(made, sub.ID)
	blocks, commentsID, err := welcomeBlocks(m, image, sub.ID)
	if err != nil {
		return err
	}
	if _, _, err := c.Store.SaveContent(p.ID, doc.FromBlocks(blocks)); err != nil {
		return err
	}
	if _, err := c.Store.AddComment(p.ID, commentsID, m.WelcomeComment(), "", "", "agent", "Leaflet"); err != nil {
		return err
	}
	tmpl, err := c.Store.CreatePage(store.NewPageOptions{Title: m.WelcomeSubTitle(), Template: true, Tags: meetings, Content: doc.FromBlocks(welcomeMeeting(m))})
	if err != nil {
		return err
	}
	made = append(made, tmpl.ID)
	if err := c.Store.SetValue("welcome.page", p.ID); err != nil {
		return err
	}
	if err := c.Store.MarkStarterPages([]string{p.ID, sub.ID, tmpl.ID}); err != nil {
		return err
	}
	return c.Store.SetValue("welcome.created", "true")
}

func (c *Core) welcomeImageFile(pageID, name string) (store.File, error) {
	dir, err := os.MkdirTemp("", "leaflet-welcome")
	if err != nil {
		return store.File{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, welcomeImage, 0o600); err != nil {
		return store.File{}, err
	}
	return c.Store.AddFile(path, "", pageID, "image/jpeg")
}

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
	text("paragraph", m.WelcomeWritingLines())

	heading(m.WelcomeBlocksHeading())
	text("paragraph", m.WelcomeBlocksTable())
	you, agent := m.WelcomeTableYou(), m.WelcomeTableAgent()
	add(map[string]any{"type": "table", "rows": []any{
		[]any{m.WelcomeTableMilestone(), m.WelcomeTableOwner(), m.WelcomeTableDue()},
		[]any{m.WelcomeTableOutline(), you, m.WelcomeMonday()},
		[]any{m.WelcomeTableDraft(), agent, m.WelcomeWednesday()},
		[]any{m.WelcomeTableReview(), you, m.WelcomeFriday()},
	}})
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
		"url": store.FileURL(image.ID, image.Name), "align": "center", "alt": m.WelcomeImageAlt(), "file_id": image.ID,
	}})
	add(map[string]any{"type": "video", "video": map[string]any{
		"url": "https://www.youtube.com/watch?v=aqz-KE-bpKQ", "title": "Big Buck Bunny · Blender Foundation", "notes": m.WelcomeVideoNotes(),
	}})
	text("paragraph", m.WelcomeMediaFiles())
	text("paragraph", m.WelcomeMediaCharts())
	chart(map[string]any{
		"chart_type": "bar", "title": m.WelcomeChartPagesTitle(), "labels": []any{m.WelcomeChartNotes(), m.WelcomeChartPlans(), m.WelcomeChartResearch(), m.WelcomeChartRecordings()},
		"series": []any{map[string]any{"name": m.WelcomeChartPages(), "values": []any{12.0, 7.0, 5.0, 3.0}}}, "caption": m.WelcomeChartCaption(),
	})
	text("paragraph", m.WelcomeEmbedsIntro())
	embed(m.WelcomePianoTitle(), pianoEmbed(m), 220)

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
		m.WelcomeAgentsWorkspace(),
		m.WelcomeAgentsSettings(),
		m.WelcomeAgentsExternal(),
	} {
		text("bulleted_list", s)
	}

	heading(m.WelcomeOrganizingHeading())
	text("paragraph", m.WelcomeOrganizingNest())
	blocks = append(blocks, &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": subID}})
	const mark = "￼"
	if link, err := doc.SpecToNode(map[string]any{"type": "paragraph", "text": m.WelcomeOrganizingLink(mark)}, "", "Leaflet"); err == nil {
		title := m.WelcomeSubTitle()
		link.SetDelta(withRun(link.Delta(), mark, doc.Run{Text: title, Attrs: doc.PageMention(subID, title)}))
		blocks = append(blocks, link)
	} else if bad == nil {
		bad = err
	}
	text("paragraph", m.WelcomeOrganizingFolders())
	text("paragraph", m.WelcomeOrganizingMenu())
	text("paragraph", m.WelcomeOrganizingTags(m.WelcomeTagGuide(), m.WelcomeTagMeetings()))
	text("paragraph", m.WelcomeOrganizingHeadings())

	heading(m.WelcomeTogetherHeading())
	text("paragraph", m.WelcomeTogetherShare())
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

func welcomeCode(m locales.Messages) string {
	return "// " + m.WelcomeCodeComment() + `
function wordCount(text) {
  return text.split(/\s+/).filter(Boolean).length;
}

console.log(wordCount("Pages are living documents")); // 4`
}

func fillEmbed(page string, texts map[string]string) string {
	pairs := make([]string, 0, 4*len(texts))
	for k, v := range texts {
		js, _ := json.Marshal(v)
		pairs = append(pairs, "{{"+k+"}}", html.EscapeString(v), "{{js:"+k+"}}", string(js))
	}
	return strings.NewReplacer(pairs...).Replace(page)
}

func pianoEmbed(m locales.Messages) string {
	return fillEmbed(pianoPage, map[string]string{"hint": m.WelcomePianoHint()})
}

const pianoPage = `<!doctype html>
<html><head><meta charset="utf-8"><style>
html,body{margin:0;height:100%;background:#f5f6f8;font:12px -apple-system,system-ui,sans-serif;user-select:none;-webkit-user-select:none}
.wrap{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:12px;padding:16px;min-height:100%;box-sizing:border-box;outline:none}
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
