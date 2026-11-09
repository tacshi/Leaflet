// Markdown as blocks, for write_markdown, as the app's doc.ParseLines
// reads it: a line each, with Markdown block prefixes. A line ending in a
// hard break, a backslash, goes on in the block on the next line, a line
// of a quote or an alert after another is more of it, and a line right
// after a list item's, indented under it, more of the item. A list item
// holds the blocks indented under it, a <details> those up to its
// </details>, the lines of a table make a table, a fenced block a code
// block (a Mermaid fence a diagram, lines between $$ a formula), a GitHub
// alert a callout, and a line that is only a picture an image.

import {
  type BlockSpec,
  codeLanguage,
  deltaToPlain,
  fields,
  InlineText,
  isRule,
  markdownToDelta,
  trimSpace,
} from "./blocks.ts";
import { agentURL } from "./url.ts";

const ALERT_TONES: Record<string, string> = {
  "[!NOTE]": "info",
  "[!IMPORTANT]": "info",
  "[!TIP]": "tip",
  "[!WARNING]": "warning",
  "[!CAUTION]": "danger",
};

/** The tone of a line opening a GitHub alert, as "> [!TIP]". */
function alertTone(line: string): string | null {
  const t = trimSpace(line);
  if (!t.startsWith(">")) return null;
  return ALERT_TONES[trimSpace(t.slice(1)).toUpperCase()] ?? null;
}

/**
 * The language of a line opening a code fence, as the ID a code block
 * keeps; null for another line. Only spaces and tabs indent a fence, as
 * CommonMark has it: one after another space is text.
 */
function codeFence(line: string): string | null {
  const t = line.replace(/^[ \t]+|[ \t]+$/g, "");
  const rest = t.replace(/^`+/, "");
  if (t.length - rest.length < 3 || rest.includes("`")) return null;
  return codeLanguage(fields(rest)[0] ?? "");
}

/**
 * A line of a table: its cells, and whether it is a line of dashes, as the
 * one under the header ("| --- | --- |") (doc.tableRow). As in GitHub's
 * tables, a backslash keeps the character after it in the cell, and \| is
 * a | of the cell's text, even in a code span; there, where a backslash is
 * the code's, one before anything else is only itself. A <br> is a line
 * break.
 */
export function tableRow(line: string): { cells: string[]; rule: boolean } {
  const s = [...trimSpace(line).replace(/^\|/, "")];
  const text = new InlineText(s).span();
  const cells: string[] = [];
  let cell = "";
  // Where the code span the line is read in ends.
  let code = 0;
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "\\" && i + 1 < s.length && s[i + 1] === "|") {
      i++;
      cell += "|";
    } else if (s[i] === "\\" && i + 1 < s.length && i >= code) {
      cell += s[i] + s[i + 1];
      i++;
    } else if (s[i] === "|") {
      cells.push(cell);
      cell = "";
    } else if (s[i] === "`" && i >= code) {
      const span = text.codeSpan(i);
      if (span) code = i + span.n;
      cell += s[i];
    } else {
      cell += s[i];
    }
  }
  // A closing | ends the last cell; without one, what is after the last
  // | is a cell.
  if (cell !== "" || cells.length === 0) cells.push(cell);
  const trimmed = cells.map(trimSpace);
  const rule = trimmed.every((c) => c !== "" && c.replace(/[-: ]/g, "") === "");
  return { cells: trimmed.map(cellBreaks), rule };
}

/** How a line break is written in a table's cell (doc.brTags). */
const BR_TAGS = ["<br>", "<br/>", "<br />"];

/**
 * A table cell's Markdown with its <br>s as line breaks; one in a code
 * span, or escaped, is text (doc.cellBreaks). A link's address holds no
 * code span, nor a <br>.
 */
function cellBreaks(md: string): string {
  if (!md.toLowerCase().includes("<br")) return md;
  const s = [...md];
  const text = new InlineText(s).span();
  let out = "";
  for (let i = 0; i < s.length;) {
    if (s[i] === "\\") {
      out += s.slice(i, i + 2).join("");
      i += 2;
      continue;
    }
    if (s[i] === "[") {
      const l = text.link(i);
      if (l) {
        out += "[" + cellBreaks(s.slice(i + 1, l.close).join("")) + s.slice(l.close, i + l.n).join("");
        i += l.n;
        continue;
      }
    }
    if (s[i] === "`") {
      const span = text.codeSpan(i);
      let j = i + (span?.n ?? 0);
      if (!span) { while (j < s.length && s[j] === "`") j++; }
      out += s.slice(i, j).join("");
      i = j;
      continue;
    }
    if (s[i] === "<") {
      const rest = s.slice(i, i + 6).join("").toLowerCase();
      const tag = BR_TAGS.find((t) => rest.startsWith(t));
      if (tag) {
        out += "\n";
        i += tag.length;
        continue;
      }
    }
    out += s[i++];
  }
  return out;
}

const unescape = (s: string) => s.replace(/\\([!-/:-@[-`{-~])/g, "$1");

/**
 * An image's optional title, and the ) that ends the line, with spaces or
 * tabs after it. As in CommonMark, only spaces and tabs, and at most one
 * line break, separate the title; another space is no separator.
 */
const titleEnd = new RegExp(
  `^(?:(?:[ \\t]+|[ \\t]*\\n[ \\t]*)(?:"(?:[^"\\\\]|\\\\.)*"|'(?:[^'\\\\]|\\\\.)*'|\\((?:[^()\\\\]|\\\\.)*\\)))?[ \\t]*\\n?[ \\t]*\\)[ \\t]*$`,
  "su",
);

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
  if (!titleEnd.test(rest)) return null;
  // A link the image block takes, as a spec's image.url: one it would
  // refuse would fail the whole call (agentURL).
  url = unescape(url);
  if (!agentURL(url)) return null;
  return { url, alt: deltaToPlain(markdownToDelta(alt)) };
}

/** A list item's marker at the start of a line: its type, its text, and whether a to-do is checked. */
function listItem(s: string): { type: string; rest: string; checked: boolean } | null {
  // The text may hold any character, U+2028 and U+2029 among them.
  const bullet = s.match(/^[-*+](?: |$)(.*)$/s);
  if (bullet) {
    const todo = bullet[1].match(/^\[([ xX])\](?: |$)(.*)$/s);
    if (todo) return { type: "todo_list", rest: todo[2], checked: todo[1] !== " " };
    return { type: "bulleted_list", rest: bullet[1], checked: false };
  }
  // A number of up to nine digits, as CommonMark's.
  const numbered = s.match(/^\d{1,9}[.)](?: |$)(.*)$/s);
  return numbered ? { type: "numbered_list", rest: numbered[1], checked: false } : null;
}

/** The number a numbered item's line starts with. */
const itemNumber = (line: string) => parseInt(line, 10);

/** Whether a numbered item's line starts with a number of four digits or more. */
const longNumber = (line: string) => /^\d{4}/.test(line);

/** A heading's #s at the start of a line: its level and text. */
function headingLine(s: string): { level: number; rest: string } | null {
  const m = s.match(/^(#{1,6})(?: |$)(.*)$/s);
  return m ? { level: m[1].length, rest: m[2] } : null;
}

/** A line opening a toggle, <details> or <details open>: whether it is open, and what follows on the line. */
function detailsOpen(s: string): { open: boolean; rest: string } | null {
  const m = s.match(/^<details( open)?>(.*)$/is);
  return m ? { open: m[1] !== undefined, rest: trimSpace(m[2]) } : null;
}

const isDetailsClose = (s: string) => trimSpace(s).toLowerCase() === "</details>";

/** Where the </summary> that ends a toggle's summary is in s, one not escaped nor in a code span, or -1. */
function summaryEnd(s: string): number {
  const r = [...s];
  const text = new InlineText(r).span();
  for (let i = 0; i < r.length; i++) {
    if (r[i] === "\\") i++;
    else if (r[i] === "`") {
      const span = text.codeSpan(i);
      if (span) i += span.n - 1;
    } else if (r[i] === "<" && r.slice(i, i + 10).join("").toLowerCase() === "</summary>") {
      return r.slice(0, i).join("").length;
    }
  }
  return -1;
}

/** Whether a line starts a block of its own, so it cannot go on in the block before it. */
function startsBlock(s: string): boolean {
  // A long number starts a list item only after the one before it.
  const item = listItem(s);
  return item !== null && (item.type !== "numbered_list" || !longNumber(s)) || headingLine(s) !== null ||
    detailsOpen(s) !== null || codeFence(s) !== null ||
    s.startsWith(">") || s.startsWith("|") || trimSpace(s) === "$$" || isRule(s) || isDetailsClose(s) ||
    imageLine(s) !== null;
}

/** Whether a line of Markdown ends in a hard break, a backslash not itself escaped. */
const hardBreak = (s: string) => (s.length - s.replace(/\\+$/, "").length) % 2 === 1;

/**
 * Whether a line of Markdown ends in a web address written out
 * (doc.endsInAddress): one starts in its last word, which a backslash
 * right after would be part of.
 */
const endsInAddress = (s: string) =>
  /[Hh][Tt][Tt][Pp][Ss]?:\/\/|[Ww][Ww][Ww]\./.test(s.slice(Math.max(s.lastIndexOf(" "), s.lastIndexOf("\t")) + 1));

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
  // The ends of lines Markdown has, \r\n and \r, are \n.
  const text = markdown.replace(/\r\n?/g, "\n").replace(/^\n+|\n+$/g, "");
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
  // The rows of the table being read, into tableIn, and whether the line
  // under its header has been.
  let table: string[][] = [];
  let tableIn: BlockSpec[] | null = null;
  let ruled = false;
  // The numbered items read, by their numbers.
  const numbers = new Map<BlockSpec, number>();
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
  // line break between them, with the space the app puts before one to
  // keep it out of a web address.
  const addLine = (t: Text, s: string) => {
    if (t.hard) {
      let line = t.lines[t.lines.length - 1].slice(0, -1);
      if (line.endsWith(" ") && endsInAddress(line.replace(/ +$/, ""))) line = line.slice(0, -1);
      t.lines[t.lines.length - 1] = line;
    }
    t.lines.push(s);
    t.hard = hardBreak(s);
  };
  const flushTable = () => {
    if (table.length) tableIn!.push({ type: "table", rows: table });
    table = [];
    tableIn = null;
    ruled = false;
  };
  const closeCode = () => {
    const c = code!;
    const body = c.lines.join("\n");
    // An empty formula or diagram has nothing to draw: it makes no block,
    // as a spec of one would be refused.
    if ((c.fence === "$$" || c.language === "mermaid") && trimSpace(body) === "") {
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
    if (trimSpace(rest) !== "") readLine(rest);
  };
  // Whether a numbered item's line is one: a number of four digits or
  // more, as a year that starts a line, is one only right after the item
  // before it, as the app numbers a long list.
  const counts = (line: string, indent: number): boolean => {
    if (!longNumber(line)) return true;
    const kids = container(indent);
    return kids.length > 0 && numbers.get(kids[kids.length - 1]) === itemNumber(line) - 1;
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
      // As the client closes one: a $$ line, or the fence's backticks
      // with only more of them and spaces.
      if (
        code.fence === "$$" ? trimSpace(trimmed) === "$$" : trimmed.startsWith(code.fence) && /^[` ]*$/.test(trimmed)
      ) {
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
    if (trimSpace(line) === "") {
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
    if (language !== null || trimSpace(trimmed) === "$$") {
      flushTable();
      const fence = language !== null ? trimmed.match(/^`+/)![0] : "$$";
      code = { fence, language: language ?? "", indent: lead, lines: [], into: container(indent) };
      return;
    }
    if (trimmed.startsWith("|")) {
      tableIn ??= container(indent);
      // The line of dashes under the header is no row; one further down
      // is, of cells that are dashes.
      const row = tableRow(trimmed);
      if (row.rule && table.length === 1 && !ruled) ruled = true;
      else table.push(row.cells);
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
    if (item && (item.type !== "numbered_list" || counts(trimmed, indent))) {
      const spec: BlockSpec = item.type === "todo_list"
        ? { type: item.type, checked: item.checked, text: "" }
        : { type: item.type, text: "" };
      if (item.type === "numbered_list") numbers.set(spec, itemNumber(trimmed));
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
