package app

import (
	_ "embed"
	"os"
	"path/filepath"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// welcomeImage is a dawn over mountains and a lake, drawn for the guide.
//
//go:embed welcome/dawn.jpg
var welcomeImage []byte

const (
	welcomeTitle    = "Welcome to Leaflet"
	welcomeSubTitle = "Weekly team sync"

	welcomeDateTodo = "Hover this to-do and choose **Add date** to give it a due date"
)

// createWelcome gives a new space one page that shows what pages can do,
// with an example subpage inside it: writing, blocks, media, charts,
// embeds, to-dos, recordings, agents, organizing and finding pages,
// working together, sync and shortcuts.
func (a *App) createWelcome() error {
	if _, ok := a.st.Value("welcome.created"); ok {
		return nil
	}
	if pages, _ := a.st.Pages(); len(pages) > 0 {
		return a.st.SetValue("welcome.created", "true")
	}
	// The picture is a file of the page and the subpage a page inside it,
	// so the page comes first and its blocks after.
	p, err := a.st.CreatePage(store.NewPageOptions{Title: welcomeTitle})
	if err != nil {
		return err
	}
	image, err := a.welcomeImageFile(p.ID)
	if err != nil {
		return err
	}
	sub, err := a.st.CreatePage(store.NewPageOptions{ParentID: p.ID, Title: welcomeSubTitle, Content: welcomeSubpage()})
	if err != nil {
		return err
	}
	blocks, err := welcomeBlocks(image, sub.ID)
	if err != nil {
		return err
	}
	if _, _, err := a.st.SaveContent(p.ID, doc.FromBlocks(blocks)); err != nil {
		return err
	}
	if err := a.st.SetValue("welcome.page", p.ID); err != nil {
		return err
	}
	return a.st.SetValue("welcome.created", "true")
}

// welcomeImageFile adds the guide's picture as a file of its page.
func (a *App) welcomeImageFile(pageID string) (store.File, error) {
	dir, err := os.MkdirTemp("", "leaflet-welcome")
	if err != nil {
		return store.File{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "Dawn over the lake.jpg")
	if err := os.WriteFile(path, welcomeImage, 0o600); err != nil {
		return store.File{}, err
	}
	return a.st.AddFile(path, "", pageID, "image/jpeg")
}

// welcomeBlocks are the guide's blocks, with its picture and a card for
// and a link to its subpage.
func welcomeBlocks(image store.File, subID string) ([]*doc.Node, error) {
	var blocks []*doc.Node
	var bad error
	add := func(m map[string]any) {
		n, err := doc.SpecToNode(m, "", "Leaflet")
		if err != nil && bad == nil {
			bad = err
		}
		if n != nil {
			blocks = append(blocks, n)
		}
	}
	text := func(typ, s string) { add(map[string]any{"type": typ, "text": s}) }
	heading := func(s string) { add(map[string]any{"type": "heading", "level": 2.0, "text": s}) }
	chart := func(m map[string]any) { add(map[string]any{"type": "chart", "chart": m}) }
	embed := func(title, html string, height int) {
		add(map[string]any{"type": "embed", "embed": map[string]any{"title": title, "html": html, "height": float64(height)}})
	}

	text("paragraph", "Pages are living documents you write with people and agents. They are saved on this device first and sync when you are online. This page shows what a page can do; change anything, it is yours.")

	heading("Try it")
	for _, s := range []string{
		"Type **/** on an empty line to add any block",
		"Type **@** to ask an agent to write here, mention someone or link a page",
		"Select some text, then choose **Ask agent**, **Comment** or a color",
		"Press the microphone in the header to record, with a transcript",
		"Add an agent provider in **Settings › Agent providers**: sign in with ChatGPT, paste an API key, or use a model server such as Ollama",
	} {
		text("todo_list", s)
	}

	heading("Writing")
	text("paragraph", "Type **bold**, *italic*, `code`, ~~strikethrough~~ and [links](https://github.com/egoist/mygo) as Markdown and they format as you go. At the start of a line, `#` makes a heading, `-` a bullet, `1.` a numbered item, `[]` a to-do, `>` a quote and `---` a divider. Pasted Markdown keeps its headings, lists, tables and code.")
	text("quote", "The best way to have a good idea is to have a lot of ideas, and throw the bad ones away.")
	add(map[string]any{"type": "numbered_list", "text": "Write a rough outline", "children": []any{
		map[string]any{"type": "bulleted_list", "text": "Press **Tab** to nest an item, **⇧Tab** to lift it back"},
	}})
	text("numbered_list", "Ask an agent to fill in a section")
	text("numbered_list", "Review its edits, and **Undo** if you don't like them")

	heading("Blocks")
	text("paragraph", "**/** adds any block. In a table, **Tab** moves to the next cell and adds a row at the end, and cells take formatting.")
	add(map[string]any{"type": "table", "rows": []any{
		[]any{"Milestone", "Owner", "Due"},
		[]any{"Outline the guide", "You", "Monday"},
		[]any{"First draft", "Agent", "Wednesday"},
		[]any{"Review and publish", "You", "Friday"},
	}})
	text("paragraph", "Code colors its syntax: type three backticks and a language, or pick one in the bar above the block.")
	add(map[string]any{"type": "code", "language": "javascript", "text": welcomeCode})
	add(map[string]any{"type": "callout", "tone": "tip", "text": "A callout sets a note apart. Click its icon to make it a tip, a warning or a caution."})
	add(map[string]any{"type": "toggle_list", "text": "A toggle folds away what is inside it: click the arrow", "children": []any{
		map[string]any{"type": "paragraph", "text": "Press **Enter** at the end of an open toggle to write inside it."},
	}})
	text("paragraph", "**Math** is TeX and **Diagram** is Mermaid; **Edit** opens their source. **Link card** previews a web page, and **File** keeps any file with the page.")
	add(map[string]any{"type": "math_equation", "formula": `\int_0^1 x^2\,dx = \frac{1}{3} \qquad e^{i\pi} + 1 = 0`})
	add(map[string]any{"type": "diagram", "source": "flowchart LR\n  Idea([Idea]) --> Draft[Write a draft]\n  Draft --> Review{Review}\n  Review -->|changes| Draft\n  Review -->|ok| Publish([Publish])"})

	heading("Pictures, video and charts")
	text("paragraph", "Images are saved with the page, so they show offline. Paste a YouTube or Vimeo link on an empty line for a video that plays in place.")
	blocks = append(blocks, &doc.Node{ID: doc.NewBlockID(), Type: "image", Data: map[string]any{
		"url": "file://" + image.LocalPath, "align": "center", "alt": "A dawn over mountains and a still lake", "file_id": image.ID,
	}})
	add(map[string]any{"type": "video", "video": map[string]any{"url": "https://www.youtube.com/watch?v=aqz-KE-bpKQ", "title": "Big Buck Bunny · Blender Foundation"}})
	text("paragraph", "Charts are bar, line or pie. Agents make them from your numbers, and **Edit data** changes them by hand.")
	chart(map[string]any{
		"chart_type": "bar", "title": "What people keep in pages", "labels": []any{"Notes", "Plans", "Research", "Recordings"},
		"series": []any{map[string]any{"name": "Pages", "values": []any{12.0, 7.0, 5.0, 3.0}}}, "caption": "Example data",
	})
	chart(map[string]any{
		"chart_type": "line", "title": "Words written per day", "labels": []any{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"},
		"series": []any{
			map[string]any{"name": "You", "values": []any{420.0, 380.0, 650.0, 540.0, 720.0, 210.0, 300.0}},
			map[string]any{"name": "Agents", "values": []any{150.0, 260.0, 300.0, 480.0, 390.0, 120.0, 90.0}},
		},
	})
	chart(map[string]any{
		"chart_type": "pie", "title": "Where the week went", "labels": []any{"Writing", "Meetings", "Research", "Email"},
		"series": []any{map[string]any{"name": "Hours", "values": []any{14.0, 9.0, 8.0, 5.0}}},
	})

	heading("Interactive embeds")
	text("paragraph", "Embeds are small apps in a page, sandboxed with no network and no access to your data. Ask an agent for one, as “@ make a pomodoro timer”, or try these:")
	embed("The solar system, at your speed", solarSystemEmbed, 380)
	embed("Savings calculator", savingsEmbed, 270)
	embed("A piano to play", pianoEmbed, 250)

	heading("To-dos and recordings")
	text("todo_list", welcomeDateTodo)
	text("paragraph", "A due to-do reminds you on the day and turns red once overdue. The **To-dos** button at the top of the sidebar gathers the open ones from every page.")
	text("paragraph", "A recording comes with a transcript, a time on each line: click a time to play from there. Its **…** menu puts the transcript in the page or has an agent sum it up. **Settings › Speech to text** picks Soniox or an OpenAI-compatible server, which can run on your own network, and the language. The menu bar icon starts a recording without opening the window.")

	heading("Agents")
	text("paragraph", "Agents read the page and edit it with you: text, tables, charts, code and embeds. Each run is one step you can undo, and the blocks it changed stay tinted until the next.")
	for _, s := range []string{
		"**@** on a line asks about that spot: “@ write an intro for this page”",
		"Select text, then **Ask agent**: “make this shorter”, “translate it to French”",
		"The panel (**⌘J**) holds a chat about the page. Mention an agent in a comment and it answers in the thread",
		"**Settings › Agent providers** sets the default agent, a model on this machine for offline use, and whether agents may read your other pages",
		"Claude Code, Codex, Cursor and other MCP clients can work on your pages too: **Settings › External agents** sets them up",
	} {
		text("bulleted_list", s)
	}

	heading("Organizing pages")
	text("paragraph", "Pages nest inside pages: **/** › **Subpage**, or the **+** beside a page in the sidebar, adds one with a card for it here.")
	blocks = append(blocks, &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": subID}})
	link := &doc.Node{ID: doc.NewBlockID(), Type: "paragraph"}
	link.SetDelta(doc.Delta{
		{Text: "Type "}, {Text: "@", Attrs: doc.Attrs{Bold: true}}, {Text: " and a page's name to link to it, as to "},
		{Text: welcomeSubTitle, Attrs: doc.PageMention(subID, welcomeSubTitle)}, {Text: "; the link keeps up when the page is renamed."},
	})
	blocks = append(blocks, link)
	text("paragraph", "Folders, from the button beside **My space**, group pages. A page's **…** menu moves it, duplicates it with its subpages, exports it as Markdown, or saves it as a template, which a blank page then offers to start from.")
	text("paragraph", "Headings fold their section from the arrow beside them, and the panel's **Outline** tab lists them. Right-click a block to **Copy link to block**.")

	heading("Working together")
	text("paragraph", "Select text and choose **Comment** to start a thread. Threads live in the panel's **Comments** tab, where you reply and **Resolve** them. Once you sign in, **Share** in the header invites people to edit or view a page and its subpages.")
	text("paragraph", "**Page history** in the **…** menu keeps a version every ten minutes of editing and before each agent run. See what changed since one, inline or side by side, name the current one, or restore it.")
	text("paragraph", "Pages work offline and sync between devices when you sign in; **Settings › Offline and storage** chooses which stay on this device. A block changed on two devices while apart keeps one version, and the page says so, with the other a click away.")

	heading("Shortcuts")
	add(map[string]any{"type": "table", "rows": []any{
		[]any{"Keys", "Does"},
		[]any{"⌘P", "Search pages, templates, recordings and comments"},
		[]any{"⌘N", "Make a new page"},
		[]any{"⌘J", "Show or hide the agent panel"},
		[]any{"⌘\\", "Show or hide the sidebar"},
		[]any{"⌘,", "Open Settings: theme, text size, agents and speech"},
		[]any{"⌘[  ⌘]", "Go back and forward between pages"},
		[]any{"⌘B  ⌘I  ⌘U", "Bold, italic, underline"},
		[]any{"⌘E  ⇧⌘X", "Inline code, strikethrough"},
		[]any{"⌘K", "Link the selected text"},
		[]any{"⇧Enter", "A line break within a block"},
		[]any{"Tab  ⇧Tab", "Nest or lift a list item, indent code, or move between table cells"},
		[]any{"⌘Z  ⇧⌘Z", "Undo, redo"},
	}})
	return blocks, bad
}

// welcomeSubpage is the content of the guide's example subpage.
func welcomeSubpage() *doc.Content {
	var blocks []*doc.Node
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
		if n, err := doc.SpecToNode(m, "", "Leaflet"); err == nil {
			blocks = append(blocks, n)
		}
	}
	return doc.FromBlocks(blocks)
}

// welcomeCode is the guide's code example.
const welcomeCode = `// Count the words in a page's text, as a byline might.
function wordCount(text) {
  return text.split(/\s+/).filter(Boolean).length;
}

console.log(wordCount("Pages are living documents")); // 4`

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
