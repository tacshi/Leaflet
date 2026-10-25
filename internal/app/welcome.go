package app

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// welcomeImage is a dawn over mountains and a lake, drawn for the guide.
//
//go:embed welcome/dawn.jpg
var welcomeImage []byte

// createWelcome gives a new space one page that shows what pages can do:
// writing, lists, tables, images, charts, interactive embeds and subpages.
func (a *App) createWelcome() error {
	if _, ok := a.st.Value("welcome.created"); ok {
		return nil
	}
	if pages, _ := a.st.Pages(); len(pages) > 0 {
		return a.st.SetValue("welcome.created", "true")
	}
	var blocks []*doc.Node
	add := func(m map[string]any) {
		n, err := doc.SpecToNode(m, "", "Leaflet")
		if err != nil {
			panic(err)
		}
		blocks = append(blocks, n)
	}
	text := func(typ, s string) { add(map[string]any{"type": typ, "text": s}) }
	heading := func(s string) { add(map[string]any{"type": "heading", "level": 2.0, "text": s}) }
	embed := func(title, html string, height int) {
		add(map[string]any{"type": "embed", "embed": map[string]any{"title": title, "html": html, "height": float64(height)}})
	}

	text("paragraph", "Pages are living documents you write with people and agents. Everything here is saved on this device first and syncs when you are online. This page shows what a page can hold; change anything, it is yours.")

	heading("Try it")
	for _, s := range []string{
		"Type **/** on an empty line to add a heading, list, table, chart, or embed",
		"Type **@** to ask an agent to write here, or to mention someone",
		"Select some text, then choose **Ask agent** or **Comment**",
		welcomeRecordTodo,
		welcomeProviderTodo,
	} {
		text("todo_list", s)
	}

	heading("Writing")
	text("paragraph", "Text takes **bold**, *italic*, `code`, ~~strikethrough~~ and [links](https://github.com/egoist/mygo). Type them as markdown and they turn into formats as you go.")
	text("quote", "The best way to have a good idea is to have a lot of ideas, and throw the bad ones away.")
	add(map[string]any{"type": "numbered_list", "text": "Write a rough outline", "children": []any{
		map[string]any{"type": "bulleted_list", "text": "Press **Tab** to nest an item, **Shift-Tab** to lift it back"},
	}})
	text("numbered_list", "Ask an agent to fill in a section")
	text("numbered_list", welcomeReviewStep)

	heading("Tables")
	add(map[string]any{"type": "table", "rows": []any{
		[]any{"Milestone", "Owner", "Due"},
		[]any{"Outline the guide", "You", "Monday"},
		[]any{"First draft", "Agent", "Wednesday"},
		[]any{"Review and publish", "You", "Friday"},
	}})

	heading("Charts")
	text("paragraph", "Charts are blocks too. Agents make them from your numbers, and **Edit data** on a chart changes them by hand.")
	add(map[string]any{"type": "chart", "chart": map[string]any{
		"chart_type": "bar", "title": "What people keep in pages", "labels": []any{"Notes", "Plans", "Research", "Recordings"},
		"series": []any{map[string]any{"name": "Pages", "values": []any{12.0, 7.0, 5.0, 3.0}}}, "caption": "Example data",
	}})
	add(map[string]any{"type": "chart", "chart": map[string]any{
		"chart_type": "line", "title": "Words written per day", "labels": []any{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"},
		"series": []any{
			map[string]any{"name": "You", "values": []any{420.0, 380.0, 650.0, 540.0, 720.0, 210.0, 300.0}},
			map[string]any{"name": "Agents", "values": []any{150.0, 260.0, 300.0, 480.0, 390.0, 120.0, 90.0}},
		},
	}})
	add(map[string]any{"type": "chart", "chart": map[string]any{
		"chart_type": "pie", "title": "Where the week went", "labels": []any{"Writing", "Meetings", "Research", "Email"},
		"series": []any{map[string]any{"name": "Hours", "values": []any{14.0, 9.0, 8.0, 5.0}}},
	}})

	heading("Interactive embeds")
	text("paragraph", "Embeds are small apps inside a page. They run sandboxed, with no network and no access to your data. Ask an agent for one, as “@ make a pomodoro timer”, or try these:")
	embed("The solar system, at your speed", solarSystemEmbed, 380)
	embed("Savings calculator", savingsEmbed, 270)
	embed("A piano to play", pianoEmbed, 250)

	p, err := a.st.CreatePage(store.NewPageOptions{Title: welcomeTitle, Content: doc.FromBlocks(blocks)})
	if err != nil {
		return err
	}
	// The picture is a file of the page and the subpage is a page of its
	// own, so they go in once the page exists.
	if c := a.st.Content(p.ID); c != nil {
		image, _ := a.addWelcomeImage(p.ID, c)
		sub, _ := a.addWelcomeSubpage(p.ID, c)
		if image || sub {
			if _, _, err := a.st.SaveContent(p.ID, c); err != nil {
				return err
			}
		}
	}
	if err := a.st.SetValue("welcome.page", p.ID); err != nil {
		return err
	}
	if err := a.st.SetValue("welcome.version", welcomeVersion); err != nil {
		return err
	}
	return a.st.SetValue("welcome.created", "true")
}

const (
	welcomeTitle   = "Welcome to Leaflet"
	welcomeVersion = "7"

	welcomeProviderTodo = "Add an agent provider in **Settings › Agent providers**: sign in with ChatGPT, paste an API key, or use a model server such as Ollama"
	welcomeRecordTodo   = "Press the microphone in the header to add a recording with its transcript"
	welcomeReviewStep   = "Review its edits, and **Undo** if you don't like them"
)

// updateWelcome rewrites the steps of an earlier welcome page that point
// at settings or buttons that have since moved, and adds the image and
// subpage examples it lacked. Other edits stay.
func (a *App) updateWelcome() error {
	if v, _ := a.st.Value("welcome.version"); v == welcomeVersion {
		return nil
	}
	if _, ok := a.st.Value("welcome.created"); !ok {
		return nil
	}
	var ids []string
	if id, ok := a.st.Value("welcome.page"); ok {
		ids = append(ids, id)
	} else {
		pages, _ := a.st.Pages()
		for _, p := range pages {
			if p.Title == welcomeTitle {
				ids = append(ids, p.ID)
			}
		}
	}
	for _, id := range ids {
		c := a.st.Content(id)
		if c == nil {
			continue
		}
		changed := false
		c.Walk(func(n, _ *doc.Node) {
			// Version 6 named the run bar's button Undo.
			if n.Type == "numbered_list" && n.Delta().Plain() == "Review its edits, and Undo run if you don't like them" {
				n.SetDelta(doc.FromMarkdown(welcomeReviewStep))
				changed = true
				return
			}
			if n.Type != "todo_list" {
				return
			}
			text := n.Delta().Plain()
			var next string
			switch {
			case strings.Contains(text, "Settings › Models"):
				next = welcomeProviderTodo
			case text == "Press Record to add a recording with its transcript":
				next = welcomeRecordTodo
			default:
				return
			}
			n.SetDelta(doc.FromMarkdown(next))
			changed = true
		})
		// Version 5 gave the embeds a dark theme: the ones still as the
		// guide made them take it.
		c.Walk(func(n, _ *doc.Node) {
			if n.Type != "embed" {
				return
			}
			src := n.Str("html")
			for _, e := range []struct{ was, now string }{
				{"html,body{margin:0;background:#fff;color:#16181d;font:14px", savingsEmbed},
				{"html,body{margin:0;height:100%;background:#f5f6f8;font:12px", pianoEmbed},
			} {
				if strings.Contains(src, e.was) && !strings.Contains(src, "data-theme") {
					n.Data["html"] = e.now
					changed = true
				}
			}
		})
		// Version 7 styled the sliders, kept the planets' names apart and
		// fit the piano to a narrow frame.
		c.Walk(func(n, _ *doc.Node) {
			if n.Type != "embed" {
				return
			}
			src := n.Str("html")
			for _, e := range []struct{ was, now, mark string }{
				{"var planets = [", solarSystemEmbed, "min-width:0;"},
				{"Saved every month", savingsEmbed, "--p"},
				{`var whites = [[0, "A"]`, pianoEmbed, "kb.style.zoom"},
			} {
				if strings.Contains(src, e.was) && !strings.Contains(src, e.mark) {
					n.Data["html"] = e.now
					changed = true
				}
			}
		})
		for _, add := range []func(string, *doc.Content) (bool, error){a.addWelcomeImage, a.addWelcomeSubpage} {
			if added, err := add(id, c); err != nil {
				return err
			} else if added {
				changed = true
			}
		}
		if changed {
			if _, _, err := a.st.SaveContent(id, c); err != nil {
				return err
			}
		}
	}
	return a.st.SetValue("welcome.version", welcomeVersion)
}

// addWelcomeImage adds the Images section to a welcome page that has no
// image, before its charts: the picture becomes a file of the page.
func (a *App) addWelcomeImage(pageID string, c *doc.Content) (bool, error) {
	var before string
	for _, n := range c.All() {
		if n.Type == "image" {
			return false, nil
		}
		text := n.Delta().Plain()
		if before == "" && (n.Type == "heading" && (text == "Charts" || text == "Interactive embeds" || text == "Subpages") || text == welcomeNestText) {
			before = n.ID
		}
	}
	dir, err := os.MkdirTemp("", "leaflet-welcome")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "Dawn over the lake.jpg")
	if err := os.WriteFile(path, welcomeImage, 0o600); err != nil {
		return false, err
	}
	f, err := a.st.AddFile(path, "", pageID, "image/jpeg")
	if err != nil {
		return false, err
	}
	var nodes []*doc.Node
	for _, spec := range []map[string]any{
		{"type": "heading", "level": 2.0, "text": "Images"},
		{"type": "paragraph", "text": "Add a picture with **/** › **Image**. It is saved with the page, so it shows offline too."},
	} {
		n, err := doc.SpecToNode(spec, "", "Leaflet")
		if err != nil {
			return false, err
		}
		nodes = append(nodes, n)
	}
	nodes = append(nodes, &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
		"url": "file://" + f.LocalPath, "align": "center", "alt": "A dawn over mountains and a still lake", "file_id": f.ID,
	}})
	// With no heading to go before, the section goes at the end.
	if err := c.Insert(doc.Position{Before: before}, nodes...); err != nil {
		return false, err
	}
	return true, nil
}

// welcomeNestText closed earlier guides; the Subpages section replaces it.
const welcomeNestText = "Make a new page with the pencil at the top of the sidebar, and nest pages inside pages from the + beside them."

// addWelcomeSubpage ends a welcome page that has no subpage with the
// Subpages section, linking to an example page made inside it.
func (a *App) addWelcomeSubpage(pageID string, c *doc.Content) (bool, error) {
	var old string
	for _, n := range c.All() {
		if n.Type == "subpage" {
			return false, nil
		}
		if n.Type == "paragraph" && n.Delta().Plain() == welcomeNestText {
			old = n.ID
		}
	}
	var child []*doc.Node
	var nodes []*doc.Node
	add := func(list *[]*doc.Node, m map[string]any) error {
		n, err := doc.SpecToNode(m, "", "Leaflet")
		if err == nil {
			*list = append(*list, n)
		}
		return err
	}
	for _, m := range []map[string]any{
		{"type": "paragraph", "text": "Notes from the week's sync. This page lives inside **Welcome to Leaflet**: the sidebar shows it under its parent, and sharing the parent shares it too."},
		{"type": "heading", "level": 2.0, "text": "Agenda"},
		{"type": "numbered_list", "text": "What shipped since last week"},
		{"type": "numbered_list", "text": "Blockers"},
		{"type": "numbered_list", "text": "Plans for next week"},
		{"type": "heading", "level": 2.0, "text": "Decisions"},
		{"type": "bulleted_list", "text": "Keep the release on **Friday**"},
		{"type": "bulleted_list", "text": "Move the design review to Thursday"},
		{"type": "heading", "level": 2.0, "text": "Action items"},
		{"type": "table", "rows": []any{
			[]any{"Task", "Owner", "Due"},
			[]any{"Write the release notes", "You", "Thursday"},
			[]any{"Sum up beta feedback", "Agent", "Wednesday"},
		}},
		{"type": "todo_list", "text": "Ask an agent to turn these notes into an email"},
	} {
		if err := add(&child, m); err != nil {
			return false, err
		}
	}
	for _, m := range []map[string]any{
		{"type": "heading", "level": 2.0, "text": "Subpages"},
		{"type": "paragraph", "text": "Pages nest inside pages. Add one with **/** › **Subpage**, or the **+** beside a page in the sidebar. Make a page of its own with the pencil at the top of the sidebar."},
	} {
		if err := add(&nodes, m); err != nil {
			return false, err
		}
	}
	p, err := a.st.CreatePage(store.NewPageOptions{ParentID: pageID, Title: "Weekly team sync", Content: doc.FromBlocks(child)})
	if err != nil {
		return false, err
	}
	nodes = append(nodes, &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": p.ID}})
	if err := c.Insert(doc.Position{Before: old}, nodes...); err != nil {
		return false, err
	}
	if old != "" {
		c.Remove(old)
	}
	return true, nil
}

// solarSystemEmbed draws the planets orbiting the sun, with a speed slider
// and a pause button.
const solarSystemEmbed = `<!doctype html>
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
<div class="bar"><button id="p">Pause</button><label for="s">Speed</label><input id="s" type="range" min="1" max="500" step="1" value="60"><span id="v">60 days / s</span></div>
<script>
var c = document.getElementById("c"), x = c.getContext("2d"), W = 0, H = 0;
function size() { var d = window.devicePixelRatio || 1; W = c.clientWidth; H = c.clientHeight; c.width = W * d; c.height = H * d; x.setTransform(d, 0, 0, d, 0, 0); }
size(); window.addEventListener("resize", size);
var planets = [
  {n: "Mercury", p: 88, r: 0.16, s: 2.5, c: "#b5b0a8"},
  {n: "Venus", p: 225, r: 0.24, s: 4, c: "#e8c27a"},
  {n: "Earth", p: 365, r: 0.32, s: 4.2, c: "#4f9dff"},
  {n: "Mars", p: 687, r: 0.40, s: 3.2, c: "#e0603e"},
  {n: "Jupiter", p: 4333, r: 0.56, s: 9, c: "#d9a66b"},
  {n: "Saturn", p: 10759, r: 0.70, s: 7.5, c: "#e6d39b", ring: true},
  {n: "Uranus", p: 30687, r: 0.82, s: 5.5, c: "#8fe0e6"},
  {n: "Neptune", p: 60190, r: 0.94, s: 5.5, c: "#5a73ff"}
];
var stars = [];
for (var i = 0; i < 220; i++) stars.push([Math.random(), Math.random(), Math.random() * 1.2 + 0.2]);
var days = 0, speed = 60, running = true, last = performance.now();
var s = document.getElementById("s"), v = document.getElementById("v"), b = document.getElementById("p");
function fill(el) { el.style.setProperty("--p", (el.value - el.min) / (el.max - el.min) * 100 + "%"); }
s.oninput = function () { speed = +s.value; v.textContent = speed + (speed == 1 ? " day / s" : " days / s"); fill(s); };
fill(s);
b.onclick = function () { running = !running; b.textContent = running ? "Pause" : "Play"; };
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
  x.fillText("Year " + (days / 365.25).toFixed(1), 14, 22);
  requestAnimationFrame(frame);
}
requestAnimationFrame(frame);
</script></body></html>`

// savingsEmbed works out savings from a monthly deposit, a rate and the
// years, with a bar for each year.
const savingsEmbed = `<!doctype html>
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
<div class="row"><label for="m">Saved every month</label><div><input id="m" type="range" min="50" max="3000" step="50" value="500"><output id="mo"></output></div></div>
<div class="row"><label for="r">Yearly return</label><div><input id="r" type="range" min="0" max="12" step="0.25" value="5"><output id="ro"></output></div></div>
<div class="row"><label for="y">Years</label><div><input id="y" type="range" min="1" max="40" step="1" value="20"><output id="yo"></output></div></div>
</div>
<div>
<div class="total" id="t"></div>
<div class="sub" id="d"></div>
<div class="bars" id="b"></div>
<div class="legend"><span><i style="background:#c9d0f3"></i>Saved</span><span><i style="background:#3446c9"></i>Earned</span></div>
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
  document.getElementById("yo").textContent = years + (years == 1 ? " year" : " years");
  var balance = 0, rows = [];
  for (var i = 1; i <= years * 12; i++) {
    balance = balance * (1 + rate) + monthly;
    if (i % 12 == 0) rows.push([balance, monthly * i]);
  }
  var paid = monthly * years * 12;
  document.getElementById("t").textContent = money(balance);
  document.getElementById("d").textContent = money(paid) + " saved, " + money(balance - paid) + " earned";
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
// keyboard, through the Web Audio API.
const pianoEmbed = `<!doctype html>
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
<div class="wrap" tabindex="0" id="wrap"><div class="kb" id="kb"></div><div class="hint">Click the keys, or click here and type the letters on them</div></div>
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
