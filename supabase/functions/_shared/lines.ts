// Markdown as blocks, for write_markdown, as the app's doc.ParseLines
// reads it: a line each, with Markdown block prefixes. A line ending in a
// hard break, a backslash, goes on in the block on the next line, a line
// of a quote or an alert after another is more of it, and a line right
// after a list item's, indented under it, more of the item. A list item
// holds the blocks indented under it, a <details> those up to its
// </details>, the lines of a table make a table, a fenced block a code
// block (a Mermaid fence a diagram, lines between $$ a formula), a GitHub
// alert a callout, and a line that is only a picture an image.

import { type BlockSpec, codeLanguage, deltaToPlain, isRule, markdownToDelta } from "./blocks.ts";

const ALERT_TONES: Record<string, string> = {
  "[!NOTE]": "info",
  "[!IMPORTANT]": "info",
  "[!TIP]": "tip",
  "[!WARNING]": "warning",
  "[!CAUTION]": "danger",
};

/** The tone of a line opening a GitHub alert, as "> [!TIP]". */
function alertTone(line: string): string | null {
  const t = line.trim();
  if (!t.startsWith(">")) return null;
  return ALERT_TONES[t.slice(1).trim().toUpperCase()] ?? null;
}

/** The language of a line opening a code fence, as the ID a code block keeps; null for another line. */
function codeFence(line: string): string | null {
  const t = line.trim();
  const rest = t.replace(/^`+/, "");
  if (t.length - rest.length < 3 || rest.includes("`")) return null;
  return codeLanguage(rest.trim().split(/\s+/)[0] ?? "");
}

/**
 * A line of a table, its cells, or null for the line under the header.
 * As in GitHub's tables, a backslash keeps the character after it in the
 * cell, and \| is a | of the cell's text; a <br> is a line break.
 */
export function tableRow(line: string): string[] | null {
  const s = line.trim().replace(/^\|/, "");
  const cells: string[] = [];
  let cell = "";
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "\\" && i + 1 < s.length) {
      i++;
      if (s[i] !== "|") cell += "\\";
      cell += s[i];
    } else if (s[i] === "|") {
      cells.push(cell);
      cell = "";
    } else {
      cell += s[i];
    }
  }
  // A closing | ends the last cell; without one, what is after the last
  // | is a cell.
  if (cell !== "" || cells.length === 0) cells.push(cell);
  const trimmed = cells.map((c) => c.trim());
  const rule = trimmed.every((c) => c !== "" && c.replace(/[-: ]/g, "") === "");
  return rule ? null : trimmed.map(cellBreaks);
}

const BR_TAG = /^<br(?: ?\/)?>/i;

/** A table cell's Markdown with its <br>s as line breaks; one in a code span, or escaped, is text. */
function cellBreaks(md: string): string {
  if (!/<br/i.test(md)) return md;
  let out = "";
  for (let i = 0; i < md.length;) {
    if (md[i] === "\\") {
      out += md.slice(i, i + 2);
      i += 2;
    } else if (md[i] === "`") {
      const n = codeSpanLength(md, i);
      out += md.slice(i, i + n);
      i += n;
    } else if (md[i] === "<" && BR_TAG.test(md.slice(i))) {
      out += "\n";
      i += md.slice(i).match(BR_TAG)![0].length;
    } else {
      out += md[i++];
    }
  }
  return out;
}

/**
 * How long the code span at md[i] is, between runs of as many backticks;
 * or, when none closes, the run of backticks there.
 */
function codeSpanLength(md: string, i: number): number {
  const open = md.slice(i).match(/^`+/)![0].length;
  for (let j = i + open; j < md.length;) {
    if (md[j] !== "`") {
      j++;
      continue;
    }
    const run = md.slice(j).match(/^`+/)![0].length;
    if (run === open && md.slice(i + open, j) !== "") return j + run - i;
    j += run;
  }
  return open;
}

/** Whether a link is to a picture on the web, as doc.WebURL has it. */
function webURL(link: string): boolean {
  if (/\s/.test(link)) return false;
  try {
    const u = new URL(link);
    return (u.protocol === "http:" || u.protocol === "https:") && u.host !== "";
  } catch {
    return false;
  }
}

const unescape = (s: string) => s.replace(/\\([!-/:-@[-`{-~])/g, "$1");

/**
 * A line that is only a picture from the web, as ![alt](https://…/a.png)
 * or with a title, its link in angle brackets or holding balanced
 * parentheses, as CommonMark reads one; null for another line.
 */
function imageLine(line: string): { url: string; alt: string } | null {
  if (!line.startsWith("![")) return null;
  // The alt text, to its ], with brackets in it balanced.
  let i = 2;
  for (let depth = 0; i < line.length; i++) {
    if (line[i] === "\\") i++;
    else if (line[i] === "[") depth++;
    else if (line[i] === "]" && depth-- === 0) break;
  }
  if (line[i] !== "]" || line[i + 1] !== "(") return null;
  const alt = line.slice(2, i);
  let rest = line.slice(i + 2).replace(/^[ \t]+/, "");
  let url: string;
  const angle = rest.match(/^<((?:[^<>\n\\]|\\.)*)>/);
  if (angle) {
    url = angle[1];
    rest = rest.slice(angle[0].length);
  } else {
    let j = 0;
    for (let depth = 0; j < rest.length && rest[j] > " "; j++) {
      if (rest[j] === "\\") j++;
      else if (rest[j] === "(") depth++;
      else if (rest[j] === ")" && depth-- === 0) break;
    }
    url = rest.slice(0, j);
    rest = rest.slice(j);
  }
  // An optional title, and the ) that ends the line.
  if (!/^(?:\s+(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|\((?:[^()\\]|\\.)*\)))?\s*\)$/.test(rest)) return null;
  url = unescape(url).trim();
  if (!webURL(url)) return null;
  return { url, alt: deltaToPlain(markdownToDelta(alt)) };
}

/** A list item's marker at the start of a line: its type, its text, and whether a to-do is checked. */
function listItem(s: string): { type: string; rest: string; checked: boolean } | null {
  const bullet = s.match(/^[-*+](?: |$)(.*)$/);
  if (bullet) {
    const todo = bullet[1].match(/^\[([ xX])\](?: |$)(.*)$/);
    if (todo) return { type: "todo_list", rest: todo[2], checked: todo[1] !== " " };
    return { type: "bulleted_list", rest: bullet[1], checked: false };
  }
  const numbered = s.match(/^\d{1,3}[.)](?: |$)(.*)$/);
  return numbered ? { type: "numbered_list", rest: numbered[1], checked: false } : null;
}

/** A heading's #s at the start of a line: its level and text. */
function headingLine(s: string): { level: number; rest: string } | null {
  const m = s.match(/^(#{1,6})(?: |$)(.*)$/);
  return m ? { level: m[1].length, rest: m[2] } : null;
}

/** A line opening a toggle, <details> or <details open>: whether it is open, and what follows on the line. */
function detailsOpen(s: string): { open: boolean; rest: string } | null {
  const m = s.match(/^<details( open)?>(.*)$/i);
  return m ? { open: m[1] !== undefined, rest: m[2].trim() } : null;
}

const isDetailsClose = (s: string) => s.trim().toLowerCase() === "</details>";

/** Where the </summary> that ends a toggle's summary is in s, one not escaped, or -1. */
function summaryEnd(s: string): number {
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "\\") i++;
    else if (s.slice(i, i + 10).toLowerCase() === "</summary>") return i;
  }
  return -1;
}

/** Whether a line starts a block of its own, so it cannot go on in the block before it. */
function startsBlock(s: string): boolean {
  return listItem(s) !== null || headingLine(s) !== null || detailsOpen(s) !== null || codeFence(s) !== null ||
    s.startsWith(">") || s.startsWith("|") || s.trim() === "$$" || isRule(s) || isDetailsClose(s) ||
    imageLine(s) !== null;
}

/** Whether a line of Markdown ends in a hard break, a backslash not itself escaped. */
const hardBreak = (s: string) => (s.length - s.replace(/\\+$/, "").length) % 2 === 1;

/** A list item or a toggle that holds the blocks after it. */
interface Open {
  spec: BlockSpec;
  /** A toggle holds all up to its </details>. */
  toggle: boolean;
  /**
   * How far a line is indented to be in it: two columns past a list
   * item's marker, or as far as a toggle's <details>.
   */
  min: number;
}

/** A text block being read, a line of Markdown at a time. */
interface Text {
  spec: BlockSpec;
  lines: string[];
  /** A quote or an alert, which lines starting with > go on. */
  quoted: boolean;
  /** A list item, which lines indented under it right after its own go on. */
  item: boolean;
  /** How far a line is indented to go on in it. */
  min: number;
  /** Its last line ends in a hard break, so the next goes on in it. */
  hard: boolean;
}

/** Reads Markdown as block specs, as pasting it in the app does. */
export function markdownSpecs(markdown: string): BlockSpec[] {
  const text = markdown.replace(/\r\n/g, "\n").replace(/^\n+|\n+$/g, "");
  const out: BlockSpec[] = [];
  let open: Open[] = [];
  // The text blocks read, their text set at the end, and the one the
  // next line may go on in.
  const texts: Text[] = [];
  let last: Text | null = null;
  // The toggle whose <summary> is being read, over lines, and one just
  // opened, whose summary may follow.
  let summary: Text | null = null;
  let awaiting: Text | null = null;
  let table: string[][] = [];
  let tableIn: BlockSpec[] | null = null;
  // The fenced block open: its fence, its language, its indent and lines,
  // and the blocks it goes last in.
  let code: { fence: string; language: string; indent: string; lines: string[]; into: BlockSpec[] } | null = null;

  const kids = (): BlockSpec[] => open.length ? (open[open.length - 1].spec.children ??= []) : out;
  const min = () => open.length ? open[open.length - 1].min : 0;
  // Closes the list items a line at indent is not under, and returns the
  // blocks the line's block goes in.
  const container = (indent: number): BlockSpec[] => {
    while (open.length && !open[open.length - 1].toggle && indent < open[open.length - 1].min) open.pop();
    return kids();
  };
  const add = (spec: BlockSpec, indent: number) => container(indent).push(spec);
  const newText = (spec: BlockSpec, quoted: boolean, item: boolean, min: number): Text => {
    const t: Text = { spec, lines: [], quoted, item, min, hard: false };
    texts.push(t);
    return t;
  };
  // Puts a line in a block: a hard break that ended the line before is the
  // line break between them.
  const addLine = (t: Text, s: string) => {
    if (t.hard) t.lines[t.lines.length - 1] = t.lines[t.lines.length - 1].slice(0, -1);
    t.lines.push(s);
    t.hard = hardBreak(s);
  };
  const flushTable = () => {
    if (table.length) tableIn!.push({ type: "table", rows: table });
    table = [];
    tableIn = null;
  };
  const closeCode = () => {
    const c = code!;
    const body = c.lines.join("\n");
    // An empty formula or diagram has nothing to draw: it makes no block,
    // as a spec of one would be refused.
    if ((c.fence === "$$" || c.language === "mermaid") && body.trim() === "") {
      // nothing
    } else if (c.fence === "$$") c.into.push({ type: "math_equation", formula: body });
    else if (c.language === "mermaid") c.into.push({ type: "diagram", source: body });
    else c.into.push({ type: "code", text: body, language: c.language });
    code = null;
  };
  // Reads a line of a toggle's <summary>, up to its </summary>; what
  // follows that on the line is a line of its own.
  const summaryLine = (s: string) => {
    const t = summary!;
    const end = summaryEnd(s);
    if (end < 0) {
      t.lines.push(s);
      return;
    }
    t.lines.push(s.slice(0, end));
    summary = null;
    const rest = s.slice(end + "</summary>".length);
    if (rest.trim() !== "") readLine(rest);
  };
  // Puts a line in the text block before it when it goes on there.
  const continues = (trimmed: string, indent: number): boolean => {
    const t = last;
    if (!t) return false;
    if (t.quoted && trimmed.startsWith(">") && indent >= t.min) {
      if (alertTone(trimmed)) return false;
      addLine(t, trimmed.slice(1).replace(/^ /, ""));
      return true;
    }
    if (startsBlock(trimmed)) return false;
    if (t.hard || t.item && indent >= t.min) {
      addLine(t, trimmed);
      return true;
    }
    return false;
  };

  const readLine = (line: string) => {
    const trimmed = line.replace(/^[ \t]+/, "");
    if (code) {
      const t = trimmed.trim();
      if (code.fence === "$$" ? t === "$$" : t.startsWith(code.fence) && t.replace(/[` ]/g, "") === "") {
        closeCode();
      } else {
        code.lines.push(line.startsWith(code.indent) ? line.slice(code.indent.length) : line);
      }
      return;
    }
    if (summary) {
      summaryLine(trimmed);
      return;
    }
    if (line.trim() === "") {
      // A blank line only separates blocks; a list goes on across it.
      flushTable();
      last = null;
      return;
    }
    const lead = line.slice(0, line.length - trimmed.length);
    const indent = (lead.match(/ /g)?.length ?? 0) + 2 * (lead.match(/\t/g)?.length ?? 0);
    if (awaiting) {
      const t = awaiting;
      awaiting = null;
      if (/^<summary>/i.test(trimmed)) {
        summary = t;
        summaryLine(trimmed.slice("<summary>".length));
        return;
      }
    }
    if (continues(trimmed, indent)) return;
    last = null;
    const tone = alertTone(trimmed);
    if (tone) {
      flushTable();
      const spec: BlockSpec = { type: "callout", tone, text: "" };
      add(spec, indent);
      last = newText(spec, true, false, min());
      return;
    }
    const language = codeFence(trimmed);
    if (language !== null || trimmed.trim() === "$$") {
      flushTable();
      const fence = language !== null ? trimmed.match(/^`+/)![0] : "$$";
      code = { fence, language: language ?? "", indent: lead, lines: [], into: container(indent) };
      return;
    }
    if (trimmed.startsWith("|")) {
      tableIn ??= container(indent);
      const row = tableRow(trimmed);
      if (row) table.push(row);
      return;
    }
    flushTable();
    const details = detailsOpen(trimmed);
    if (details) {
      const spec: BlockSpec = { type: "toggle_list", text: "", collapsed: !details.open };
      add(spec, indent);
      open.push({ spec, toggle: true, min: indent });
      const t = newText(spec, false, false, 0);
      if (/^<summary>/i.test(details.rest)) {
        summary = t;
        summaryLine(details.rest.slice("<summary>".length));
      } else {
        awaiting = t;
        if (details.rest !== "") readLine(details.rest);
      }
      return;
    }
    if (isDetailsClose(trimmed)) {
      const i = open.findLastIndex((o) => o.toggle);
      if (i >= 0) {
        open = open.slice(0, i);
        return;
      }
    }
    if (isRule(trimmed)) {
      add({ type: "divider" }, indent);
      return;
    }
    const item = listItem(trimmed);
    if (item) {
      const spec: BlockSpec = item.type === "todo_list"
        ? { type: item.type, checked: item.checked, text: "" }
        : { type: item.type, text: "" };
      add(spec, indent);
      open.push({ spec, toggle: false, min: indent + 2 });
      last = newText(spec, false, true, indent + 2);
      addLine(last, item.rest);
      return;
    }
    const heading = headingLine(trimmed);
    if (heading) {
      const spec: BlockSpec = { type: "heading", level: heading.level, text: "" };
      add(spec, indent);
      // A heading is one line.
      newText(spec, false, false, 0).lines.push(heading.rest);
      return;
    }
    if (trimmed === ">" || trimmed.startsWith("> ")) {
      const spec: BlockSpec = { type: "quote", text: "" };
      add(spec, indent);
      last = newText(spec, true, false, min());
      addLine(last, trimmed.slice(2));
      return;
    }
    const image = imageLine(trimmed);
    if (image) {
      add({ type: "image", image }, indent);
      return;
    }
    const spec: BlockSpec = { type: "paragraph", text: "" };
    add(spec, indent);
    last = newText(spec, false, false, 0);
    addLine(last, trimmed);
  };

  for (const line of text ? text.split("\n") : []) readLine(line);
  flushTable();
  // A fence left open runs to the end.
  if (code) closeCode();
  for (const t of texts) t.spec.text = t.lines.join("\n");
  return out;
}
