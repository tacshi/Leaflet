// Random inputs for the parity corpora (_shared/corpus_test.ts): text,
// Markdown, links, block specs, pages, ops and tool calls, drawn to reach
// where the server and the app could read them apart: Go's spaces and
// JavaScript's, UTF-16 units and bytes, numbers past what an int holds,
// values of the wrong kind, and blocks the tools may not name.

import type { DeltaOp, NodeJSON, PageContent, Position } from "../_shared/blocks.ts";
import type { Random } from "./random.ts";

/** Runs of text to draw from. */
export const CHARS = [
  "a",
  "b",
  "Plan",
  "x",
  "0",
  "42",
  " ",
  " ",
  "  ",
  "\t",
  "\n",
  "*",
  "**",
  '<span style="color:#ff0000">',
  '<span style="background:#00ff00;color:#FF000080">',
  '<span style="color:red">',
  "</span>",
  "_",
  "__",
  "`",
  "~",
  "~~",
  "[",
  "]",
  "(",
  ")",
  "!",
  "\\",
  "#",
  ">",
  "-",
  "+",
  "|",
  "<",
  "/",
  ":",
  ".",
  "=",
  "$",
  "&",
  '"',
  "'",
  "@",
  "%",
  "{",
  " ",
  "\u0085",
  " ",
  " ",
  "　",
  "​",
  "﻿",
  "\v",
  "\f",
  "\r",
  "\u0001",
  "\u007f",
  "é",
  "İ",
  "ß",
  "Σ",
  "K",
  "字",
  "日本語",
  "😀",
  "𝔸",
  "é",
  "。",
  "？",
  "！",
  "«",
];

/** Inline Markdown, besides the characters, to draw from. */
const INLINE = [
  ...CHARS,
  "word",
  "snake_case",
  "**bold**",
  "*it*",
  "_it_",
  "~~gone~~",
  "`code`",
  "``a`b``",
  "[link](https://example.com/a)",
  "[a](<https://x.com/a b>)",
  "[a](https://x.com/(b))",
  "![pic](https://x.com/a.png)",
  "[@Ann](mention:user:u1)",
  "[@Bot](mention:agent:a1)",
  "[Plan](leaflet://page/p1)",
  "[x](leaflet://page/p2#b1)",
  "[1:23](leaflet://page/p2#b1?t=83)",
  "[](leaflet://page/p3)",
  "https://example.com/x?y=1",
  "\\*",
  "\\_",
  "\\`",
  "\\[",
  "\\\\",
  "<br>",
  "***",
  "* ",
  " *",
  "**a*",
  "*a**",
  "_a_b_",
];

/** Links, as an agent may write one, or a page may hold one. */
const URLS = [
  "https://example.com/a.png",
  "http://example.com/b",
  " https://example.com/c.jpg ",
  "HTTPS://Example.com/D.PNG",
  "https://",
  "https:///a.png",
  "http:/example.com/c.png",
  "ftp://example.com/a.png",
  "javascript:alert(1)",
  "example.com/a.png",
  "https://exa mple.com/a.png",
  "https://example.com/a b.png",
  "https://example.com/a b.png",
  "https://example.com/\u0085",
  "https://example.com:99999/a.png",
  "https://example.com:/a.png",
  "https://:80/a.png",
  "https://256.0.0.1/a.png",
  "https://[::1]:8080/a.png",
  "https://[::1%25en0]/a.png",
  "https://[1.2.3.4]/a.png",
  "https://%41.com/a.png",
  "https://%c3%a9.com/a.png",
  "https://exa\\mple.com/a.png",
  "https://u:p@example.com/a.png",
  "https://u@p@example.com/a.png",
  "https://example.com/%zz.png",
  "https://example.com/%2e.png",
  "https://example.com/a.png?x=1#y",
  "https://example.com/a.png#%zz",
  "https://例え.jp/a.png",
  "https://youtu.be/dQw4w9WgXcQ",
  "https://youtu.be/dQw4w9WgXc",
  "https://youtu.be/a/b.mp4",
  "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
  "https://youtube.com/watch?v=dQw4w9WgXcQ&t=1",
  "https://youtube.com/watch?v=%64Qw4w9WgXcQ",
  "https://m.youtube.com/shorts/dQw4w9WgXcQ",
  "https://youtube-nocookie.com/embed/dQw4w9WgXcQ",
  "https://youtube.com/watch?x=1;v=dQw4w9WgXcQ",
  "https://vimeo.com/76979871",
  "https://player.vimeo.com/video/76979871",
  "https://vimeo.com/76979871/8272103f6e",
  "https://player.vimeo.com/video/76979871?h=8272103f6e",
  "https://vimeo.com/76979871?h=Nope",
  "https://vimeo.com/channels/staffpicks/76979871/abcdef",
  "https://vİmeo.com/76979871",
  "https://vimeo.com/channels/staff",
  "https://www.bilibili.com/video/BV1KofNYjEuK/?p=2",
  "https://www.bilibili.com/zh-Hans/video/BV1e5nv6GEZJ",
  "https://m.bilibili.com/video/av573719854",
  "https://www.bilibili.com/video/BV0KofNYjEuK",
  "https://b23.tv/BV1KofNYjEuK",
  "https://b23.tv/AbCdEfG",
  "https://player.bilibili.com/player.html?aid=573719854",
  "https://player.bilibili.com/player.html?bvid=%42V1KofNYjEuK",
  "https://player.bilibili.com/player.html?aid=0",
  "https://www.bilibili.com/video/bv1KofNYjEuK/",
  "https://b23.tv/bV1KofNYjEuK",
  "https://player.bilibili.com/player.html?bvid=Bv1KofNYjEuK&page=2",
  "https://www.bilibili.com/video/bv",
  "https://www.dailymotion.com/video/x7tgad0_a-title",
  "https://dai.ly/x8j5v1k",
  "https://geo.dailymotion.com/player.html?video=x8j5v1k",
  "https://www.dailymotion.com/embed/video/",
  "https://www.nicovideo.jp/watch/sm9",
  "https://nico.ms/so38016254",
  "https://www.nicovideo.jp/watch/lv1",
  "https://example.com/clip.MP4",
  "https://example.com/clip.webm?x=1",
  "https://example.com/a%2Emp4",
  "https://example.com/.mov",
  "http://example.com/clip.ogv/",
  "https://go.dev/doc",
  "https://go.dev/a\u007fb",
];

/** A link: one of URLS, or one put together of their parts. */
export function url(r: Random): string {
  if (r.chance(0.6)) return r.pick(URLS);
  const scheme = r.pick(["https://", "http://", "HTTP://", "https:", "https:/", "mailto:", ""]);
  const user = r.pick(["", "", "", "u@", "u:p@", "@"]);
  const host = r.pick([
    "example.com",
    "Example.COM",
    "www.youtube.com",
    "youtu.be",
    "vimeo.com",
    "www.bilibili.com",
    "player.bilibili.com",
    "b23.tv",
    "dai.ly",
    "nico.ms",
    "1.2.3.4",
    "[::1]",
    "",
    "a_b.com",
    "例え.jp",
    "%41",
    "x y",
  ]);
  const port = r.pick(["", "", "", ":8080", ":0", ":99999", ":", ":x"]);
  const path = r.text([
    "/",
    "a",
    ".png",
    ".mp4",
    "%20",
    "%zz",
    "%2F",
    " ",
    "　",
    "watch",
    "dQw4w9WgXcQ",
    "é",
    "video",
    "BV1KofNYjEuK",
    "av2",
    "player.html",
    "x8j5v1k",
    "sm9",
  ], 5);
  const query = r.pick([
    "",
    "",
    "?v=dQw4w9WgXcQ",
    "?x=%zz",
    "?a=1&v=x",
    "?aid=2",
    "?bvid=BV1KofNYjEuK",
    "?video=x8j5v1k",
  ]);
  const fragment = r.pick(["", "", "#top", "#%zz", "#a b"]);
  return scheme + user + host + port + path + query + fragment;
}

/** Inline Markdown, for text an agent writes. */
export function inline(r: Random, max = 12): string {
  return r.text(INLINE, max);
}

/** A value of any JSON kind, as a field of the wrong one may be. */
export function anyValue(r: Random): unknown {
  switch (r.int(8)) {
    case 0:
      return r.text(CHARS, 4);
    case 1:
      return r.pick([0, 1, 2, 2.5, -3, 1e21, -1e21, 1e300, 1e-7, 250000, 2 ** 53 + 2]);
    case 2:
      return r.chance(0.5);
    case 3:
      return null;
    case 4:
      return [];
    case 5:
      return {};
    case 6:
      return [r.text(CHARS, 3), 1];
    default:
      return { x: r.text(CHARS, 3) };
  }
}

/** A value: mostly of the kind the field takes, from these, else of any kind. */
function field(r: Random, values: readonly unknown[]): unknown {
  return r.chance(0.85) ? r.pick(values) : anyValue(r);
}

/** A mention, as a delta's attribute has one. */
function mention(r: Random): unknown {
  const m: Record<string, unknown> = {};
  if (r.chance(0.9)) m.kind = r.pick(["user", "agent", "page", "", "other"]);
  if (r.chance(0.9)) m.id = r.pick(["u1", "p1", "", "a b", "x)y"]);
  if (r.chance(0.8)) m.name = r.text(CHARS, 3);
  return m;
}

/** Rich text, as a block's delta holds it. */
export function delta(r: Random, max = 5): DeltaOp[] {
  return r.times(0, max, () => {
    const op: DeltaOp = { insert: r.text(CHARS, 6) };
    const a: Record<string, unknown> = {};
    if (r.chance(0.3)) a.bold = r.chance(0.9) ? true : r.pick([false, 1, "true"]);
    if (r.chance(0.3)) a.italic = true;
    if (r.chance(0.15)) a.strikethrough = true;
    if (r.chance(0.2)) a.code = true;
    if (r.chance(0.15)) {
      a.href = r.chance(0.5)
        ? url(r)
        : r.pick(["leaflet://page/p1", "leaflet://page/p2#b", "leaflet://page/p2#b?t=83", "#x", ""]);
    }
    if (r.chance(0.08)) a.mention = mention(r);
    if (r.chance(0.05)) a.underline = true;
    if (r.chance(0.05)) a.color = r.pick(["#ff0000", "#FF000080", "red"]);
    if (r.chance(0.05)) a.background = r.pick(["#00ff00", "green"]);
    if (Object.keys(a).length) op.attributes = a;
    return op;
  });
}

/** A delta as a stored block may have it: now and then with runs no reader takes. */
function storedDelta(r: Random): unknown {
  if (r.chance(0.05)) return r.pick(["text", null, {}]);
  const ops: unknown[] = delta(r);
  if (r.chance(0.05)) ops.push(r.pick([{ insert: 5 }, "x", null, { attributes: { bold: true } }]));
  return ops;
}

/** Lines of Markdown, as write_markdown takes them. */
const LINES = [
  "# Title",
  "## Heading",
  "###### Six",
  "####### Seven",
  "#NoSpace",
  "- item",
  "* item",
  "+ item",
  "  - nested",
  "    - deeper",
  "\t- tabbed",
  "1. one",
  "2) two",
  "1000. year",
  "0. zero",
  "- [ ] todo",
  "- [x] done",
  "- [X] Done",
  "> quote",
  ">",
  "> more",
  "> [!NOTE]",
  "> [!warning]",
  "> [!TIP] title",
  "```",
  "```go",
  "````python",
  "``` mermaid",
  "~~~",
  "$$",
  "E = mc^2",
  "graph TD",
  "| a | b |",
  "| --- | --- |",
  "|:-|-:|",
  "| x | `y|z` | \\| |",
  "|",
  "<details>",
  "<details open>",
  "<summary>Sum</summary>",
  "<summary>",
  "</summary>",
  "</details>",
  "---",
  "***",
  "_ _ _",
  "- - -",
  "![a](https://example.com/a.png)",
  '![a](https://example.com/a.png "t")',
  "![](http:/x)",
  "text with a break\\",
  "two spaces  ",
  "",
  "",
  "   ",
  " indented",
];

/** Markdown of several lines, as write_markdown takes it. */
export function markdown(r: Random): string {
  const lines = r.times(1, 10, () => {
    switch (r.int(4)) {
      case 0:
        return inline(r, 6);
      case 1:
        return r.pick(LINES) + (r.chance(0.5) ? " " + inline(r, 4) : "");
      default:
        return r.pick(LINES);
    }
  });
  return lines.join(r.pick(["\n", "\n", "\n", "\r\n", "\r", "\n\n"]));
}

/** Code languages, as a fence or a spec names one. */
const LANGUAGES = [
  "go",
  "Go",
  "golang",
  "JS",
  "c++",
  "C#",
  "py",
  "Visual Basic",
  "  Objective   C ",
  "text",
  "none",
  "İNI",
  "ΑΣ",
  "mermaid",
  "",
];

/** A video's notes, as an agent writes them: lines after their times, with spaces and line breaks of each kind. */
const NOTES = [
  "0:00 Intro\n1:23 Caches",
  " \r\n[1:02:03] - Wrap-up\r\n",
  "A talk.\rsee 4:56",
  "\u0085 2:05 字 \u3000",
  " ",
  "",
];

/** A block spec, as an agent writes one: of each type, with fields of the wrong kind now and then. */
export function spec(r: Random, depth = 0): Record<string, unknown> {
  const s: Record<string, unknown> = {};
  const type = r.pick([
    "paragraph",
    "heading",
    "bulleted_list",
    "numbered_list",
    "todo_list",
    "toggle_list",
    "quote",
    "code",
    "callout",
    "divider",
    "image",
    "table",
    "chart",
    "embed",
    "video",
    "link_preview",
    "math_equation",
    "diagram",
    "",
    "file",
    "subpage",
    "Paragraph",
  ]);
  if (r.chance(0.95)) s.type = r.chance(0.97) ? type : anyValue(r);
  if (r.chance(0.8)) {
    s.text = r.chance(0.9) ? (r.chance(0.1) ? r.pick(["---", " * * *", "-*-", "__\t_"]) : inline(r)) : anyValue(r);
  }
  if (r.chance(0.3)) s.level = field(r, [1, 2, 3, 6, 0, 7, -1, 2.7, 1e21, "3"]);
  if (r.chance(0.3)) s.checked = field(r, [true, false, "true", 1]);
  if (r.chance(0.3)) {
    s.due = field(r, [
      "2026-10-30",
      "2026-10-30T09:15",
      "2026-02-29",
      "2024-02-29",
      "2026-13-01",
      "2026-10-30T24:00",
      "2026-10-30T5:04",
      "x",
    ]);
  }
  if (r.chance(0.3)) s.collapsed = field(r, [true, false, "yes"]);
  if (r.chance(0.3)) s.language = field(r, LANGUAGES);
  if (r.chance(0.3)) {
    s.tone = field(r, ["info", "tip", "warning", "danger", " TIP ", "Danger", "loud", "\u0085warning"]);
  }
  if (r.chance(0.2)) s.formula = field(r, ["E = mc^2", " ", "\u0085x　", ""]);
  if (r.chance(0.2)) s.source = field(r, ["graph TD\n  A-->B", "\n\ngraph\n\n", " \n ", ""]);
  if (r.chance(0.25)) s.url = url(r);
  if (r.chance(0.2)) {
    s.image = r.chance(0.9) ? { url: url(r), ...(r.chance(0.5) ? { alt: field(r, ["A", ""]) } : {}) } : anyValue(r);
  }
  if (r.chance(0.2)) {
    s.video = r.chance(0.9)
      ? {
        url: url(r),
        ...(r.chance(0.5) ? { title: field(r, [" Song ", ""]) } : {}),
        ...(r.chance(0.4) ? { notes: field(r, NOTES) } : {}),
      }
      : anyValue(r);
  }
  if (r.chance(0.2)) s.link = r.chance(0.9) ? { url: url(r) } : anyValue(r);
  if (r.chance(0.25)) {
    s.rows = r.chance(0.9)
      ? r.times(
        0,
        3,
        () => r.chance(0.9) ? r.times(0, 3, () => field(r, ["a", "", 1, 1250000, true, null, "*b*"])) : anyValue(r),
      )
      : anyValue(r);
  }
  if (r.chance(0.25)) s.chart = r.chance(0.9) ? chartSpec(r) : anyValue(r);
  if (r.chance(0.2)) {
    s.embed = r.chance(0.9)
      ? {
        html: field(r, ["<p>hi</p>", " ", "", "<b>字</b>"]),
        ...(r.chance(0.5) ? { title: field(r, ["Timer", ""]) } : {}),
        ...(r.chance(0.5) ? { height: field(r, [100, 120, 500.5, 1200, 5000, -1, "300"]) } : {}),
      }
      : anyValue(r);
  }
  if (r.chance(depth < 2 ? 0.2 : 0.05)) {
    s.children = r.chance(0.9) ? r.times(0, 3, () => r.chance(0.9) ? spec(r, depth + 1) : anyValue(r)) : anyValue(r);
  }
  return s;
}

function chartSpec(r: Random): Record<string, unknown> {
  const labels = r.times(0, 3, () => field(r, ["a", "b", "", 1, 1e21, null]));
  const n = r.chance(0.8) ? labels.length : r.int(4);
  const c: Record<string, unknown> = {
    labels: r.chance(0.9) ? labels : anyValue(r),
    series: r.times(0, 2, () => {
      const s: Record<string, unknown> = {
        values: r.chance(0.9)
          ? Array.from({ length: n }, () => field(r, [1, 2.5, -3, "4", " 3.5 ", "1e3", "0x10", "x", 1e21]))
          : anyValue(r),
      };
      if (r.chance(0.7)) s.name = field(r, ["Sales", "", 1e-7]);
      return s;
    }),
  };
  if (r.chance(0.9)) c.chart_type = field(r, ["bar", "line", "pie", "", "area"]);
  if (r.chance(0.5)) c.title = field(r, ["Risks", ""]);
  if (r.chance(0.3)) c.caption = field(r, ["By phase", ""]);
  return c;
}

/** Makes IDs for a page's blocks, in order. */
export class Ids {
  private n = 0;
  constructor(private readonly prefix: string) {}
  next(): string {
    return `${this.prefix}${++this.n}`;
  }
}

/** Block types a stored page may hold, the server's and others. */
const BLOCK_TYPES = [
  "paragraph",
  "paragraph",
  "heading",
  "bulleted_list",
  "bulleted_list",
  "numbered_list",
  "todo_list",
  "toggle_list",
  "quote",
  "code",
  "callout",
  "divider",
  "image",
  "table",
  "chart",
  "embed",
  "video",
  "link_preview",
  "file",
  "math_equation",
  "diagram",
  "subpage",
  "recording",
  "mystery",
];

const LISTS = ["bulleted_list", "numbered_list", "todo_list", "toggle_list"];

/** A table's cells, as stored: now and then at a place out of its size, twice at one, or of another kind. */
function cells(r: Random, ids: Ids, rows: number, cols: number, odd: boolean): NodeJSON[] {
  const out: NodeJSON[] = [];
  for (let c = 0; c < cols; c++) {
    for (let row = 0; row < rows; row++) {
      const data: Record<string, unknown> = { colPosition: c, rowPosition: row };
      if (odd && r.chance(0.1)) data.rowPosition = r.pick([-1, rows + 1, 1e19, 1.5, "0", null]);
      if (odd && r.chance(0.05)) data.colPosition = r.pick([0, cols, 2 ** 53]);
      out.push({
        id: ids.next(),
        type: "table/cell",
        data,
        children: r.times(
          odd ? 0 : 1,
          odd ? 2 : 1,
          () => ({ id: ids.next(), type: "paragraph", data: { delta: delta(r, 2) } }),
        ),
      });
    }
  }
  return odd && r.chance(0.2) ? out.reverse() : out;
}

/**
 * A block as a page stores it. odd ones have data of the wrong kind now
 * and then, as another client or an older app may write it; the rest
 * are as the apps write them.
 */
export function block(r: Random, ids: Ids, odd: boolean, depth = 0): NodeJSON {
  const type = odd ? r.pick(BLOCK_TYPES) : r.pick(BLOCK_TYPES.slice(0, -1));
  const node: NodeJSON = { id: ids.next(), type };
  const v = (values: readonly unknown[]) => odd ? field(r, values) : r.pick(values);
  const data: Record<string, unknown> = {};
  const text = ["paragraph", "heading", "quote", "code", "callout", ...LISTS].includes(type);
  if (text) data.delta = odd ? storedDelta(r) : delta(r);
  switch (type) {
    case "heading":
      data.level = v([1, 2, 3, 6, 0, 7, 2.5, 1e21, -1e21, 1e300]);
      break;
    case "todo_list":
      data.checked = v([true, false]);
      if (r.chance(0.4)) data.due = v(["2026-10-30", "2026-10-30T09:15", ""]);
      break;
    case "toggle_list":
      data.collapsed = v([true, false]);
      break;
    case "code":
      data.language = v(["go", "", "python"]);
      break;
    case "callout":
      data.tone = v(["info", "tip", "warning", "danger", "Warning", " tip ", "loud"]);
      break;
    case "image":
      data.url = v(["https://example.com/a.png", "https://example.com/b.png"]);
      if (r.chance(0.7)) data.align = "center";
      if (r.chance(0.7)) data.alt = v(["A chart", ""]);
      break;
    case "table": {
      const rows = r.between(1, 3), cols = r.between(1, 3);
      data.rowsLen = odd && r.chance(0.2) ? r.pick([0, rows + 2, -1, 1e21, "2"]) : rows;
      data.colsLen = odd && r.chance(0.2) ? r.pick([0, cols + 2, -1, 2 ** 60]) : cols;
      node.children = cells(r, ids, rows, cols, odd);
      break;
    }
    case "chart":
      data.chart_type = v(["bar", "line", "pie"]);
      if (r.chance(0.7)) data.title = v(["Risks", ""]);
      data.labels = v([["A", "B"], ["x"]]);
      data.series = v([[{ name: "n", values: [1, 4] }], [{ name: "m", values: [2.5] }]]);
      if (r.chance(0.5)) data.caption = v(["By phase"]);
      data.author = "Claude";
      break;
    case "embed":
      data.title = v(["Timer", ""]);
      data.height = v([320, 120.5, 1e21]);
      data.html = v(["<p>hi</p>", "<b>字</b>"]);
      break;
    case "video":
      data.url = v(["https://youtu.be/dQw4w9WgXcQ", "https://example.com/a.mp4"]);
      if (r.chance(0.7)) data.title = v(["Song", ""]);
      if (r.chance(0.4)) data.notes = v(NOTES);
      break;
    case "link_preview":
      data.url = v(["https://go.dev/", "https://example.com/untitled"]);
      if (r.chance(0.5)) data.title = v(["Go", "  Docs  ", " "]);
      if (r.chance(0.5)) data.description = v(["The language"]);
      break;
    case "file":
      if (r.chance(0.7)) data.name = v(["notes.pdf", " ", ""]);
      if (r.chance(0.5)) data.title = v([" Report ", ""]);
      if (r.chance(0.5)) data.url = v(["file:///x/r.pdf"]);
      if (r.chance(0.5)) data.mime = v(["application/pdf"]);
      if (r.chance(0.5)) data.size = v([1234, 0]);
      if (r.chance(0.5)) data.notes = v(["0:00 Intro\n1:23 Caches", "Said 字"]);
      break;
    case "math_equation":
      data.formula = v(["e^{i\\pi}+1=0", " ", ""]);
      break;
    case "diagram":
      data.source = v(["graph TD\n  A-->B", "\n"]);
      break;
    case "subpage":
      if (r.chance(0.8)) data.page_id = v(["page-2", ""]);
      break;
    case "recording":
      data.recording_id = v(["rec-1"]);
      if (r.chance(0.7)) data.title = v(["Standup"]);
      if (r.chance(0.7)) data.duration_ms = v([60000, null]);
      break;
  }
  if (type !== "divider" || r.chance(0.5)) node.data = data;
  // A list holds blocks; a block of another kind may, from another
  // editor, when odd.
  if (type !== "table" && depth < 3 && (LISTS.includes(type) ? r.chance(0.4) : odd && r.chance(0.05))) {
    node.children = r.times(1, 3, () => block(r, ids, odd, depth + 1));
  }
  return node;
}

/** A page of blocks. */
export function page(r: Random, ids: Ids, odd: boolean, max = 6): PageContent {
  return {
    document: { id: "root", type: "page", children: r.times(0, max, () => block(r, ids, odd)) },
    meta: { v: 1, blocks: {}, deleted: {} },
  };
}

/** Every block of a page in reading order, a table's cells and what they hold among them. */
export function allBlocks(content: PageContent): NodeJSON[] {
  const out: NodeJSON[] = [];
  const visit = (nodes: NodeJSON[]) => {
    for (const n of nodes) {
      out.push(n);
      visit(n.children ?? []);
    }
  };
  visit(content.document.children ?? []);
  return out;
}

/** A position, as an op or a call names one, next to or in any of ids. */
export function position(r: Random, ids: readonly string[]): Position {
  const id = r.pick(ids);
  switch (r.int(5)) {
    case 0:
      return { after: id };
    case 1:
      return { before: id };
    case 2:
      return { parent: id };
    default:
      return { at: r.pick(["start", "end"] as const) };
  }
}

/** Runs fn with IDs drawn from r, so the blocks it makes are the same each time. */
export async function withSeededIds<T>(r: Random, fn: () => Promise<T>): Promise<T> {
  const random = crypto.getRandomValues;
  crypto.getRandomValues = (<A extends ArrayBufferView | null>(array: A): A => {
    if (array) {
      const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
      for (let i = 0; i < bytes.length; i++) bytes[i] = r.int(256);
    }
    return array;
  }) as typeof crypto.getRandomValues;
  try {
    return await fn();
  } finally {
    crypto.getRandomValues = random;
  }
}
