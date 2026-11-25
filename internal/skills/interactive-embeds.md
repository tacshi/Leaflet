---
name: interactive-embeds
description: Build an embed block, a self-contained interactive HTML widget such as a simulation, a calculator, a visual explainer or a small game, that works offline, fits the page on a phone and a desktop, and follows its light and dark look. Read it before you write or change an embed.
---

# Interactive embeds

An embed is a small web page inside a Leaflet page: something the reader plays with to understand an idea, such as orbits under gravity, a loan calculator, a sorting algorithm step by step, a chord on a keyboard, or a quiz. Make one when trying it teaches more than reading about it. For numbers, a chart block is better; for a process, a diagram.

## What it can do

- It is one complete HTML document, CSS and JavaScript inline, up to 200,000 bytes, given as the block's `embed.html`, with a `title` and a `height` from 120 to 1200.
- It runs sandboxed with no network: no CDN scripts, web fonts, `fetch` or pictures by URL. Draw with canvas, SVG or the DOM; a picture or a sound it needs goes in as a `data:` URL.
- It has no storage: `localStorage` and cookies are refused. Keep state in variables.
- Sound plays with the Web Audio API, started from a click or a tap.
- Scrolling over it scrolls the page unless something in it scrolls, so zoom with buttons or a slider rather than the wheel.

## Fit the page

- The page is as narrow as a phone or as wide as a desktop window: lay it out to its width, with flex or grid and percentages, and size a canvas to its box, times `devicePixelRatio`, again on `resize`.
- Give `height` about what the content needs: the frame has that height while the embed loads, then fits the content, from 120 to 1200, as it grows or shrinks. Let the body take its content's height (no `height: 100%` or `100vh` on it) and keep a scrollbar out of it.
- Leaflet sets `data-theme="light"` or `"dark"` on `<html>`, with the matching `color-scheme`. Keep the background transparent, so it sits on the page, and take colors from CSS variables that the dark theme changes, as in the template. Leaflet loads the embed again when the look changes, so read the theme once, as it starts. The page's palette: red `#ef4444`, orange `#f97316`, yellow `#eab308`, green `#22c55e`, teal `#14b8a6`, blue `#3b82f6`, indigo `#6366f1`, purple `#a855f7`, pink `#ec4899`, brown `#92400e`, gray `#71717a`.
- Use the system font, `system-ui`, at about 15px, as the page does.

## Make it easy to use

- Show something at once: a meaningful start, running or ready, never a blank box.
- Few controls, each labeled: buttons, sliders with their value shown, a reset. Work by mouse, touch and keyboard: pointer events, and real `<button>` and `<input>` elements.
- A line of text in it, or a sentence above it in the page, says what to try.
- Respect `prefers-reduced-motion`: start paused, or animate less.
- Keep the code plain and short. A script error leaves the box empty, so check every name and bracket before you write it.

## Template

```html
<!doctype html>
<html>
<head>
<meta charset="utf-8">
<style>
  :root { --fg: #1f2328; --muted: #6b7280; --line: #e5e7eb; --accent: #3b82f6; }
  :root[data-theme="dark"] { --fg: #e6e6e6; --muted: #9ca3af; --line: #374151; --accent: #60a5fa; }
  html, body { margin: 0; background: transparent; color: var(--fg); font: 15px/1.5 system-ui, sans-serif; }
  .wrap { padding: 12px; display: grid; gap: 10px; }
  canvas { width: 100%; height: 260px; display: block; box-sizing: border-box; border: 1px solid var(--line); border-radius: 8px; }
  .row { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; color: var(--muted); }
  button { font: inherit; padding: 4px 12px; border-radius: 6px; border: 1px solid var(--line); background: transparent; color: var(--fg); }
</style>
</head>
<body>
<div class="wrap">
  <canvas id="c" aria-label="Simulation"></canvas>
  <div class="row">
    <button id="play">Pause</button>
    <label>Speed <input id="speed" type="range" min="1" max="10" value="5"></label>
    <span id="out">5</span>
  </div>
</div>
<script>
  const canvas = document.getElementById("c"), ctx = canvas.getContext("2d");
  const css = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  let running = !matchMedia("(prefers-reduced-motion: reduce)").matches, t = 0, raf = 0;
  const play = document.getElementById("play"), speed = document.getElementById("speed");
  function draw() {
    const w = canvas.clientWidth, h = canvas.clientHeight;
    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = css("--accent");
    ctx.beginPath(); ctx.arc(w / 2 + Math.cos(t) * w / 3, h / 2 + Math.sin(t) * h / 3, 10, 0, 2 * Math.PI); ctx.fill();
  }
  function tick() {
    t += speed.value / 200;
    draw();
    if (running) raf = requestAnimationFrame(tick);
  }
  function fit() {
    const w = canvas.clientWidth, h = canvas.clientHeight, d = devicePixelRatio || 1;
    canvas.width = Math.round(w * d); canvas.height = Math.round(h * d); ctx.setTransform(d, 0, 0, d, 0, 0);
    draw();
  }
  addEventListener("resize", fit);
  play.textContent = running ? "Pause" : "Play";
  play.onclick = () => {
    running = !running;
    play.textContent = running ? "Pause" : "Play";
    cancelAnimationFrame(raf);
    if (running) raf = requestAnimationFrame(tick);
  };
  speed.oninput = () => { document.getElementById("out").textContent = speed.value; };
  fit();
  if (running) raf = requestAnimationFrame(tick);
</script>
</body>
</html>
```

## Changing one

read_page leaves out an embed's html longer than 30,000 characters and gives its `html_length`: to change such an embed, write its html whole, with replace_block where you have it. Keep its title, and its height unless the content changes size.
