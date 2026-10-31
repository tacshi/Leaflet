// Page content helpers (protocol §1–§2): node JSON with stable IDs, inline
// markdown ⇄ delta, BlockSpec → node JSON, and BlockView for read_page.
// Mirrors internal/agents/ and internal/doc/ on the client.

export type Attrs = Record<string, unknown>;

export interface DeltaOp {
  insert: string;
  attributes?: Attrs;
}

export interface NodeJSON {
  id: string;
  type: string;
  data?: Record<string, unknown>;
  children?: NodeJSON[];
}

export interface PageContent {
  document: NodeJSON;
  meta: {
    v: number;
    blocks: Record<string, number>;
    deleted: Record<string, number>;
  };
}

export interface ChartSpec {
  chart_type: "bar" | "line" | "pie";
  title?: string;
  labels: string[];
  series: { name: string; values: number[] }[];
  caption?: string;
}

export interface EmbedSpec {
  title?: string;
  html: string;
  height?: number;
}

export interface BlockSpec {
  type: string;
  text?: string;
  /** For code: the language, such as "go" or "python". */
  language?: string;
  /** For callout: info, tip, warning or danger. */
  tone?: string;
  /** For toggle_list: whether it starts folded. */
  collapsed?: boolean;
  /** For math_equation: the TeX formula. */
  formula?: string;
  /** For diagram: the Mermaid source. */
  source?: string;
  video?: { url: string; title?: string };
  link?: { url: string };
  level?: number;
  checked?: boolean;
  /** For todo_list: the day it is due, YYYY-MM-DD, or the day and time, YYYY-MM-DDTHH:MM. */
  due?: string;
  children?: BlockSpec[];
  rows?: string[][];
  chart?: ChartSpec;
  embed?: EmbedSpec;
  image?: { url: string; alt?: string };
}

export type Position =
  | { after: string }
  | { before: string }
  | { parent: string }
  | { at: "start" | "end" };

export interface BlockView {
  id: string;
  type: string;
  text: string;
  depth: number;
  parent_id?: string;
  level?: number;
  checked?: boolean;
  due?: string;
  language?: string;
  tone?: string;
  collapsed?: boolean;
  formula?: string;
  source?: string;
  url?: string;
  rows?: string[][];
  chart?: Record<string, unknown>;
  embed?: { title: string; height: number; html_length: number };
  recording?: Record<string, unknown>;
}

export const TEXT_TYPES = new Set([
  "paragraph",
  "heading",
  "bulleted_list",
  "numbered_list",
  "todo_list",
  "quote",
  "code",
  "callout",
  "toggle_list",
]);

/** Block types that may hold nested blocks. */
export const LIST_TYPES = new Set(["bulleted_list", "numbered_list", "todo_list", "toggle_list"]);

/** Block types an agent may create. */
export const SPEC_TYPES = new Set([
  ...TEXT_TYPES,
  "divider",
  "image",
  "table",
  "chart",
  "embed",
  "video",
  "link_preview",
  "math_equation",
  "diagram",
]);

// ---------------------------------------------------------------------------
// IDs and documents
// ---------------------------------------------------------------------------

const ID_ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

export function randomId(length = 6): string {
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  let id = "";
  for (const b of bytes) id += ID_ALPHABET[b % ID_ALPHABET.length];
  return id;
}

export function emptyContent(): PageContent {
  return {
    document: { id: "root", type: "page", children: [] },
    meta: { v: 1, blocks: {}, deleted: {} },
  };
}

/**
 * Whether stored content is a page's: an object with its document, of a
 * version this code knows. What is not would read as an empty page, which
 * saving would put in place of the real one.
 */
export function isPageContent(raw: unknown): boolean {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return false;
  const r = raw as { document?: unknown; meta?: { v?: unknown } };
  if (!r.document || typeof r.document !== "object" || Array.isArray(r.document)) return false;
  const v = r.meta && typeof r.meta === "object" ? r.meta.v : undefined;
  return v === undefined || (typeof v === "number" && v <= 1);
}

/** Accepts whatever is stored and returns a well-formed content object. */
export function normalizeContent(raw: unknown): PageContent {
  const content = emptyContent();
  if (!raw || typeof raw !== "object") return content;
  const r = raw as Partial<PageContent> & { document?: NodeJSON };
  if (r.document && typeof r.document === "object") {
    content.document = r.document;
  }
  content.document.id ||= "root";
  content.document.type ||= "page";
  content.document.children ??= [];
  if (r.meta && typeof r.meta === "object") {
    content.meta = {
      v: r.meta.v ?? 1,
      blocks: { ...(r.meta.blocks ?? {}) },
      deleted: { ...(r.meta.deleted ?? {}) },
    };
  }
  const seen = new Set<string>();
  walk(content.document, (node) => {
    if (!node.id || seen.has(node.id)) node.id = randomId();
    seen.add(node.id);
  });
  return content;
}

export function walk(
  node: NodeJSON,
  visit: (node: NodeJSON, parent: NodeJSON | null, depth: number) => void,
  parent: NodeJSON | null = null,
  depth = -1,
): void {
  visit(node, parent, depth);
  for (const child of node.children ?? []) walk(child, visit, node, depth + 1);
}

export function collectIds(node: NodeJSON, into: string[] = []): string[] {
  walk(node, (n) => into.push(n.id));
  return into;
}

export function clone<T>(value: T): T {
  return structuredClone(value);
}

export function deltaOf(node: NodeJSON): DeltaOp[] {
  const delta = node.data?.delta;
  return Array.isArray(delta) ? (delta as DeltaOp[]) : [];
}

// ---------------------------------------------------------------------------
// Inline markdown ⇄ delta
// ---------------------------------------------------------------------------
//
// A port of internal/doc/markdown.go: what one side writes, the other reads
// back as it was, so a page an agent reads and writes here or through the
// app keeps its text.

interface MentionAttr {
  kind?: string;
  id?: string;
  name?: string;
}

/** Attributes as one string, whatever order their keys are in. */
function attrsKey(a?: object): string {
  const sorted = (v: unknown): unknown =>
    v && typeof v === "object" && !Array.isArray(v)
      ? Object.fromEntries(Object.keys(v).sort().map((k) => [k, sorted((v as Record<string, unknown>)[k])]))
      : v;
  return JSON.stringify(sorted(a ?? {}));
}

function sameAttrs(a?: Attrs, b?: Attrs): boolean {
  return attrsKey(a) === attrsKey(b);
}

/** Appends text, joining the run before it in the same formats; a mention is a unit of its own. */
function pushText(out: DeltaOp[], text: string, attrs: Attrs): void {
  if (!text) return;
  const attributes = Object.keys(attrs).length ? { ...attrs } : undefined;
  const last = out[out.length - 1];
  if (last && !attrs.mention && !last.attributes?.mention && sameAttrs(last.attributes, attributes)) {
    last.insert += text;
    return;
  }
  out.push(attributes ? { insert: text, attributes } : { insert: text });
}

function isWordChar(ch: string | undefined): boolean {
  return !!ch && /[\p{L}\p{N}]/u.test(ch);
}

function isSpace(ch: string): boolean {
  return /\s/u.test(ch);
}

/** Whether a backslash escapes ch: ASCII punctuation. */
function isPunct(ch: string | undefined): boolean {
  return !!ch && /^[!-\/:-@\[-`{-~]$/.test(ch);
}

// A link to one of the user's pages, as doc.PageLink makes it: a page
// mention is the page's title linked to it. One to a block of a page,
// leaflet://page/<id>#<block>, stays a link.
const PAGE_LINK_PREFIX = "leaflet://page/";

/** The page a page link points to and the block, "" for the page; null for other links. */
function pageLink(href: string): { id: string; block: string } | null {
  if (!href.startsWith(PAGE_LINK_PREFIX)) return null;
  const rest = href.slice(PAGE_LINK_PREFIX.length);
  const hash = rest.indexOf("#");
  const id = hash < 0 ? rest : rest.slice(0, hash);
  return id ? { id, block: hash < 0 ? "" : rest.slice(hash + 1) } : null;
}

/** A person or agent mentioned, written [@Name](mention:user|agent:<id>). */
function mentionLink(href: string): { kind: string; id: string } | null {
  const m = href.match(/^mention:(user|agent):(.+)$/s);
  return m ? { kind: m[1], id: m[2] } : null;
}

/** A run in the formats Markdown writes; rest holds the others, which keep runs apart. */
interface MdRun {
  text: string;
  bold: boolean;
  italic: boolean;
  strike: boolean;
  code: boolean;
  href: string;
  mention?: MentionAttr;
  rest: string;
}

function mdRun(text: string, a: Attrs): MdRun {
  const { bold, italic, strikethrough, code, href, mention, ...rest } = a;
  return {
    text,
    bold: bold === true,
    italic: italic === true,
    strike: strikethrough === true,
    code: code === true,
    href: typeof href === "string" ? href : "",
    mention: mention && typeof mention === "object" ? (mention as MentionAttr) : undefined,
    rest: attrsKey(rest),
  };
}

function sameFormats(a: MdRun, b: MdRun): boolean {
  return a.bold === b.bold && a.italic === b.italic && a.strike === b.strike && a.code === b.code &&
    a.href === b.href && a.rest === b.rest && attrsKey(a.mention) === attrsKey(b.mention);
}

/** Drops empty runs and joins neighbours in equal formats, but mentions (Delta.Compact). */
function compactRuns(runs: MdRun[]): MdRun[] {
  const out: MdRun[] = [];
  for (const r of runs) {
    if (!r.text) continue;
    const last = out[out.length - 1];
    if (last && !r.mention && sameFormats(last, r)) {
      last.text += r.text;
      continue;
    }
    out.push({ ...r });
  }
  return out;
}

/**
 * Takes bold, italic and strike off the spaces at either end of the text
 * they span, outside code: markers must touch text to read as markers.
 */
function trimSpans(runs: MdRun[]): MdRun[] {
  const cs: { ch: string; r: MdRun; run: number }[] = [];
  runs.forEach((r, i) => {
    for (const ch of r.text) cs.push({ ch, r, run: i });
  });
  const marks: ("bold" | "italic" | "strike")[] = ["bold", "italic", "strike"];
  const space = (i: number) => !cs[i].r.code && isSpace(cs[i].ch);
  for (const mark of marks) {
    for (let i = 0; i < cs.length;) {
      if (!cs[i].r[mark]) {
        i++;
        continue;
      }
      let j = i;
      while (j < cs.length && cs[j].r[mark]) j++;
      let lo = i, hi = j;
      while (lo < hi && space(lo)) {
        cs[lo].r = { ...cs[lo].r, [mark]: false };
        lo++;
      }
      while (hi > lo && space(hi - 1)) {
        cs[hi - 1].r = { ...cs[hi - 1].r, [mark]: false };
        hi--;
      }
      i = j;
    }
  }
  const out: MdRun[] = [];
  for (let i = 0; i < cs.length;) {
    let j = i + 1;
    while (j < cs.length && cs[j].run === cs[i].run && sameFormats(cs[j].r, cs[i].r)) j++;
    out.push({ ...cs[i].r, text: cs.slice(i, j).map((c) => c.ch).join("") });
    i = j;
  }
  return compactRuns(out);
}

/** A format Markdown writes around text: a link to href, bold ("*"), italic ("i") or strike ("~"). */
interface MdMark {
  kind: "[" | "*" | "i" | "~";
  href: string;
}

/** A mark opening or closing, or text: written out, or code. */
interface MdToken {
  mark?: MdMark;
  open?: boolean;
  close?: boolean;
  text: string;
  code?: boolean;
}

function sameMark(a?: MdMark, b?: MdMark): boolean {
  return !!a && !!b && a.kind === b.kind && a.href === b.href;
}

/** The marks of a run, outermost first when they open together: a link holds the others. */
function marksOf(r: MdRun): MdMark[] {
  const out: MdMark[] = [];
  if (r.href) out.push({ kind: "[", href: r.href });
  if (r.bold) out.push({ kind: "*", href: "" });
  if (r.italic) out.push({ kind: "i", href: "" });
  if (r.strike) out.push({ kind: "~", href: "" });
  return out;
}

const hasMark = (marks: MdMark[], m: MdMark) => marks.some((k) => sameMark(k, m));
const isMarker = (t: MdToken) => !!(t.open || t.close);

/**
 * Moves spaces off the inner side of bold, italic and strike markers,
 * where a marker no longer reads as one: those before a closing marker go
 * after it, those after an opening one before it (doc.spacesOutside).
 */
function spacesOutside(toks: MdToken[]): MdToken[] {
  const plain = (t: MdToken) => !t.open && !t.close && !t.code;
  const emphasis = (t: MdToken) => isMarker(t) && t.mark!.kind !== "[";
  const out: MdToken[] = [];
  for (let i = 0; i < toks.length;) {
    if (!isMarker(toks[i])) {
      out.push(toks[i]);
      i++;
      continue;
    }
    // The markers here, and the spaces among them, each at the number of
    // markers before it.
    const marks: MdToken[] = [];
    const spaces: { text: string; at: number }[] = [];
    let j = i;
    for (; j < toks.length; j++) {
      const t = toks[j];
      if (isMarker(t)) marks.push(t);
      else if (plain(t) && t.text.trim() === "") spaces.push({ text: t.text, at: marks.length });
      else break;
    }
    const prev = out[out.length - 1];
    if (prev && plain(prev)) {
      const t = prev.text.replace(/\s+$/u, "");
      spaces.unshift({ text: prev.text.slice(t.length), at: 0 });
      prev.text = t;
    }
    if (j < toks.length && plain(toks[j])) {
      const t = toks[j].text.replace(/^\s+/u, "");
      spaces.push({ text: toks[j].text.slice(0, toks[j].text.length - t.length), at: marks.length });
      toks[j].text = t;
    }
    for (const s of spaces) {
      while (s.at < marks.length && emphasis(marks[s.at]) && marks[s.at].close) s.at++;
      while (s.at > 0 && emphasis(marks[s.at - 1]) && marks[s.at - 1].open) s.at--;
    }
    const seq: MdToken[] = [];
    for (let k = 0; k <= marks.length; k++) {
      for (const s of spaces) if (s.at === k && s.text) seq.push({ text: s.text });
      if (k < marks.length) seq.push(marks[k]);
    }
    // With the spaces out, a format can open and close on nothing, or
    // close and open again on the spot: it needs neither.
    for (let k = 0; k + 1 < seq.length;) {
      const a = seq[k], b = seq[k + 1];
      if (isMarker(a) && isMarker(b) && sameMark(a.mark, b.mark) && !!a.open !== !!b.open) {
        seq.splice(k, 2);
        k = Math.max(k - 1, 0);
        continue;
      }
      k++;
    }
    out.push(...seq);
    i = j;
  }
  return out;
}

/** Whether the bold mark toks[i] opens or closes has an italic mark next to either of its ends. */
function boldTouchesItalic(toks: MdToken[], i: number): boolean {
  const at = (j: number) => j >= 0 && j < toks.length && isMarker(toks[j]) && toks[j].mark!.kind === "i";
  // The other end: bold doesn't nest in bold, so it is the next bold mark
  // after an open, or before a close.
  const step = toks[i].close ? -1 : 1;
  let j = i + step;
  while (!(isMarker(toks[j]) && toks[j].mark!.kind === "*")) j += step;
  return at(i - 1) || at(i + 1) || at(j - 1) || at(j + 1);
}

/** Escapes the characters that would read as markup: all the markers but a _ inside a word. */
function escapeText(s: string): string {
  const chars = [...s];
  let out = "";
  chars.forEach((c, i) => {
    if ("\\*`~[]".includes(c)) out += "\\";
    else if (
      c === "_" && (i === 0 || i + 1 === chars.length || !isWordChar(chars[i - 1]) || !isWordChar(chars[i + 1]))
    ) out += "\\";
    out += c;
  });
  return out;
}

/**
 * Writes code between more backticks than any run of them in it, with a
 * space inside each end where it starts or ends with a backtick or a
 * space, as CommonMark reads code spans.
 */
function codeSpan(s: string): string {
  let fence = 1, run = 0;
  for (const c of s) {
    if (c === "`") {
      run++;
      fence = Math.max(fence, run + 1);
    } else run = 0;
  }
  const ticks = "`".repeat(fence);
  if (s.startsWith("`") || s.endsWith("`") || (s.startsWith(" ") && s.endsWith(" ") && s.trim() !== "")) {
    s = ` ${s} `;
  }
  return ticks + s + ticks;
}

/** A link's url, in angle brackets when it holds what would end it or read as markup. */
function linkDestination(href: string): string {
  if (!/[()<>\\ \t\n]/.test(href)) return href;
  return "<" + href.replace(/[\\<>]/g, "\\$&").replace(/\n/g, "%0A") + ">";
}

/**
 * Writes rich text in the inline markdown dialect agents read and write
 * (doc.Delta.ToMarkdown): a format over several runs is one span, the
 * others nested in it, so runs side by side read back as they were.
 */
export function deltaToMarkdown(delta: DeltaOp[]): string {
  const runs = trimSpans(compactRuns(
    delta.filter((op) => typeof op.insert === "string").map((op) => mdRun(op.insert, op.attributes ?? {})),
  ));
  let toks: MdToken[] = [];
  let open: MdMark[] = [];
  runs.forEach((r, i) => {
    const want = marksOf(r);
    // The marks open that this run still has stay open, as far as the
    // first it hasn't: closing one to open it again inside another would
    // put its marker after a space, where it no longer reads as one.
    let k = 0;
    while (k < open.length && hasMark(want, open[k])) k++;
    while (open.length > k) toks.push({ mark: open.pop(), close: true, text: "" });
    // Of the marks to open, the one that spans the most runs goes
    // outermost, so it closes last; in marksOf's order otherwise.
    const spans = (m: MdMark) => {
      let n = 1;
      while (n < runs.length - i && hasMark(marksOf(runs[i + n]), m)) n++;
      return n;
    };
    const opening = want.filter((m) => !hasMark(open, m)).map((m) => ({ m, n: spans(m) }));
    opening.sort((a, b) => b.n - a.n);
    for (const { m } of opening) {
      toks.push({ mark: m, open: true, text: "" });
      open = [...open, m];
    }
    const mention = r.mention;
    if (mention?.id && mention.kind !== "page") {
      // A page mention is a link to the page, which reads back as one;
      // a person or agent is a link to mention:<kind>:<id>.
      const name = mention.name || r.text.replace(/^@/, "");
      const kind = mention.kind === "agent" ? "agent" : "user";
      toks.push({ text: `[@${escapeText(name)}](${linkDestination(`mention:${kind}:${mention.id}`)})` });
    } else if (r.code) {
      // Code spans side by side differ only in what Markdown can't say:
      // they are one span, as backticks touching would not read back.
      const last = toks[toks.length - 1];
      if (last?.code) last.text += r.text;
      else toks.push({ text: r.text, code: true });
    } else {
      toks.push({ text: escapeText(r.text) });
    }
  });
  for (let i = open.length - 1; i >= 0; i--) toks.push({ mark: open[i], close: true, text: "" });
  toks = spacesOutside(toks);
  let out = "";
  toks.forEach((t, i) => {
    if (t.code) out += codeSpan(t.text);
    else if (!isMarker(t)) out += t.text;
    else if (t.mark!.kind === "[") out += t.open ? "[" : `](${linkDestination(t.mark!.href)})`;
    // Bold beside an italic's * is __, as *** reads either way.
    else if (t.mark!.kind === "*") out += boldTouchesItalic(toks, i) ? "__" : "**";
    else if (t.mark!.kind === "i") out += "*";
    else out += "~~";
  });
  return out;
}

/**
 * Reads [text](url) at the start of s: the text, the url and how many
 * characters they take. As in CommonMark, the url may be in angle
 * brackets or hold balanced parentheses, and backslashes escape in both.
 */
function link(s: string[]): { text: string[]; href: string; n: number } | null {
  let i = 1, depth = 0;
  for (; i < s.length; i++) {
    if (s[i] === "\\") i++;
    else if (s[i] === "`") {
      // A ] in code is the code's.
      const span = codeSpanAt(s.slice(i));
      if (span) i += span.n - 1;
    } else if (s[i] === "[") depth++;
    else if (s[i] === "]") {
      if (depth === 0) break;
      depth--;
    }
  }
  if (i === 1 || i + 1 >= s.length || s[i + 1] !== "(") return null;
  const text = s.slice(1, i);
  let href = "";
  let j = i + 2;
  let n: number;
  if (j < s.length && s[j] === "<") {
    for (j++; j < s.length && s[j] !== ">"; j++) {
      if (s[j] === "<" || s[j] === "\n") return null;
      if (s[j] === "\\" && j + 1 < s.length && isPunct(s[j + 1])) j++;
      href += s[j];
    }
    if (j + 1 >= s.length || s[j + 1] !== ")") return null;
    n = j + 2;
  } else {
    depth = 0;
    for (; j < s.length; j++) {
      let c = s[j];
      if (isSpace(c)) return null;
      if (c === "\\" && j + 1 < s.length && isPunct(s[j + 1])) c = s[++j];
      else if (c === "(") depth++;
      else if (c === ")") {
        if (depth === 0) break;
        depth--;
      }
      href += c;
    }
    if (j >= s.length) return null;
    n = j + 1;
  }
  return href ? { text, href, n } : null;
}

/**
 * Reads a code span at the start of s, between runs of as many backticks,
 * and how many characters it takes; null when there is none. A space
 * inside each end comes off, as CommonMark has it.
 */
function codeSpanAt(s: string[]): { code: string; n: number } | null {
  let open = 0;
  while (open < s.length && s[open] === "`") open++;
  for (let j = open; j < s.length;) {
    if (s[j] !== "`") {
      j++;
      continue;
    }
    let end = j;
    while (end < s.length && s[end] === "`") end++;
    if (end - j === open) {
      let code = s.slice(open, j).join("");
      if (code.length >= 2 && code[0] === " " && code[code.length - 1] === " " && code.trim() !== "") {
        code = code.slice(1, -1);
      }
      return code ? { code, n: end } : null;
    }
    j = end;
  }
  return null;
}

/** Where a code span or a link at s[j] ends, or j when none starts there. */
function skipSpan(s: string[], j: number): number {
  if (s[j] === "`") {
    const span = codeSpanAt(s.slice(j));
    if (span) return j + span.n;
  } else if (s[j] === "[") {
    const l = link(s.slice(j));
    if (l) return j + l.n;
  }
  return j;
}

function findSingle(s: string[], marker: string, from: number): number {
  for (let j = from; j < s.length; j++) {
    if (s[j] === "\\") {
      j++;
      continue;
    }
    const k = skipSpan(s, j);
    if (k > j) {
      j = k - 1;
      continue;
    }
    if (s[j] !== marker) continue;
    if (j + 1 < s.length && s[j + 1] === marker) {
      j++;
      continue;
    }
    if (marker === "_" && j + 1 < s.length && isWordChar(s[j + 1])) continue;
    if (j > from && s[j - 1] === " ") continue;
    return j;
  }
  return -1;
}

function indexOfMarker(s: string[], marker: string, from: number): number {
  const m = [...marker];
  for (let j = from; j + m.length <= s.length; j++) {
    if (s[j] === "\\") {
      j++;
      continue;
    }
    const k = skipSpan(s, j);
    if (k > j) {
      j = k - 1;
      continue;
    }
    if (s.slice(j, j + m.length).join("") === marker) return j;
  }
  return -1;
}

function parseInline(s: string[], attrs: Attrs, out: DeltaOp[]): void {
  let plain = "";
  const flush = () => {
    pushText(out, plain, attrs);
    plain = "";
  };
  for (let i = 0; i < s.length;) {
    const c = s[i];
    if (c === "\\" && i + 1 < s.length && isPunct(s[i + 1])) {
      plain += s[i + 1];
      i += 2;
      continue;
    }
    if (c === "`") {
      const span = codeSpanAt(s.slice(i));
      if (span) {
        flush();
        pushText(out, span.code, { ...attrs, code: true });
        i += span.n;
        continue;
      }
    }
    if (c === "[") {
      const l = link(s.slice(i));
      if (l) {
        flush();
        const page = pageLink(l.href);
        const mention = mentionLink(l.href);
        if (page && !page.block) {
          // A link to a page is a mention of it; one to a block of a page
          // stays a link, with its own text.
          const name = deltaToPlain(markdownToDelta(l.text.join("")));
          pushText(out, name, {
            ...attrs,
            href: PAGE_LINK_PREFIX + page.id,
            mention: { kind: "page", id: page.id, name },
          });
        } else if (mention) {
          const name = deltaToPlain(markdownToDelta(l.text.join(""))).replace(/^@/, "");
          pushText(out, `@${name}`, { ...attrs, mention: { ...mention, name } });
        } else {
          parseInline(l.text, { ...attrs, href: l.href }, out);
        }
        i += l.n;
        continue;
      }
    }
    // ***both*** is bold and italic, as agents write it.
    if (i + 2 < s.length && (c === "*" || c === "_") && s[i + 1] === c && s[i + 2] === c) {
      const end = indexOfMarker(s, c.repeat(3), i + 3);
      if (end > i + 3) {
        flush();
        parseInline(s.slice(i + 3, end), { ...attrs, bold: true, italic: true }, out);
        i = end + 3;
        continue;
      }
    }
    if (i + 1 < s.length && (c === "*" || c === "~" || c === "_") && s[i + 1] === c) {
      const end = indexOfMarker(s, c.repeat(2), i + 2);
      if (end > i + 2) {
        flush();
        parseInline(s.slice(i + 2, end), { ...attrs, ...(c === "~" ? { strikethrough: true } : { bold: true }) }, out);
        i = end + 2;
        continue;
      }
    }
    if ((c === "*" || c === "_") && (c !== "_" || !(i > 0 && isWordChar(s[i - 1])))) {
      const end = findSingle(s, c, i + 1);
      if (end > i + 1) {
        flush();
        parseInline(s.slice(i + 1, end), { ...attrs, italic: true }, out);
        i = end + 1;
        continue;
      }
    }
    plain += c;
    i++;
  }
  flush();
}

/**
 * Reads the inline markdown dialect (doc.FromMarkdown): **bold** or
 * __bold__, *italic* or _italic_, `code`, ~~strike~~, [text](url), page
 * mentions [Title](leaflet://page/<id>) and person or agent mentions
 * [@Name](mention:user|agent:<id>).
 */
export function markdownToDelta(src: string, attrs: Attrs = {}): DeltaOp[] {
  const out: DeltaOp[] = [];
  parseInline([...src], attrs, out);
  return out;
}

export function deltaToPlain(delta: DeltaOp[]): string {
  return delta.map((op) => (typeof op.insert === "string" ? op.insert : "")).join("");
}

// ---------------------------------------------------------------------------
// BlockSpec → node JSON
// ---------------------------------------------------------------------------

export class SpecError extends Error {}

export type IdFactory = () => string;

const LANGUAGE_ALIASES: Record<string, string> = {
  sh: "bash",
  shell: "bash",
  zsh: "bash",
  console: "bash",
  "c++": "cpp",
  cc: "cpp",
  cs: "csharp",
  "c#": "csharp",
  golang: "go",
  htm: "html",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  node: "javascript",
  kt: "kotlin",
  md: "markdown",
  py: "python",
  python3: "python",
  rb: "ruby",
  rs: "rust",
  ts: "typescript",
  tsx: "typescript",
  yml: "yaml",
  docker: "dockerfile",
  text: "",
  txt: "",
  plain: "",
  plaintext: "",
  none: "",
};

const CALLOUT_TONES = new Set(["info", "tip", "warning", "danger"]);

/** A callout's tone, or info (doc.CalloutTone). */
export function calloutTone(tone: unknown): string {
  const t = String(tone ?? "").trim().toLowerCase();
  return CALLOUT_TONES.has(t) ? t : "info";
}

/** Video files a video block plays, by extension (doc.VideoExts). */
const VIDEO_EXTS = new Set([".mp4", ".m4v", ".mov", ".webm", ".ogv"]);

/** Whether a link is to a YouTube or Vimeo video, or a video file (doc.ParseVideoURL). */
export function isVideoURL(link: string): boolean {
  let u: URL;
  try {
    u = new URL(link.trim());
  } catch {
    return false;
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") return false;
  const host = u.hostname.toLowerCase().replace(/^www\./, "");
  const parts = u.pathname.split("/").filter(Boolean);
  const youtubeId = /^[A-Za-z0-9_-]{11}$/;
  switch (host) {
    case "youtu.be":
      return parts.length === 1 && youtubeId.test(parts[0]);
    case "youtube.com":
    case "m.youtube.com":
    case "music.youtube.com":
    case "youtube-nocookie.com":
      if (parts.length === 1 && parts[0] === "watch") return youtubeId.test(u.searchParams.get("v") ?? "");
      return parts.length === 2 && ["embed", "shorts", "live", "v"].includes(parts[0]) && youtubeId.test(parts[1]);
    case "vimeo.com":
    case "player.vimeo.com":
      if (parts.some((p) => /^[0-9]+$/.test(p))) return true;
  }
  const ext = u.pathname.match(/\.[^./]*$/)?.[0].toLowerCase() ?? "";
  return VIDEO_EXTS.has(ext);
}

/** A code block's language as the ID it keeps (doc.CodeLanguageID). */
export function codeLanguage(name: unknown): string {
  const id = String(name ?? "").trim().toLowerCase();
  return Object.hasOwn(LANGUAGE_ALIASES, id) ? LANGUAGE_ALIASES[id] : id;
}

function textNode(id: string, type: string, text: string, extra: Record<string, unknown> = {}): NodeJSON {
  return { id, type, data: { ...extra, delta: markdownToDelta(text ?? "") } };
}

function clampInt(value: unknown, min: number, max: number, fallback: number): number {
  const n = typeof value === "number" ? Math.round(value) : Number.NaN;
  return Number.isFinite(n) ? Math.min(max, Math.max(min, n)) : fallback;
}

function validateChart(chart: unknown): ChartSpec {
  if (!chart || typeof chart !== "object") {
    throw new SpecError("A chart block needs a `chart` object");
  }
  const c = chart as Partial<ChartSpec>;
  const chartType = c.chart_type ?? "bar";
  if (!["bar", "line", "pie"].includes(chartType)) {
    throw new SpecError("chart.chart_type must be bar, line or pie");
  }
  if (!Array.isArray(c.labels) || c.labels.length === 0) {
    throw new SpecError("chart.labels must be a non-empty array of strings");
  }
  if (!Array.isArray(c.series) || c.series.length === 0) {
    throw new SpecError("chart.series must be a non-empty array");
  }
  const labels = c.labels.map((l) => String(l));
  const series = c.series.map((s, i) => {
    const values = Array.isArray(s?.values) ? s.values.map(Number) : [];
    if (values.length !== labels.length || values.some((v) => !Number.isFinite(v))) {
      throw new SpecError(
        `chart.series[${i}].values must hold ${labels.length} numbers, one per label`,
      );
    }
    return { name: String(s?.name ?? `Series ${i + 1}`), values };
  });
  if (chartType === "pie" && series.length > 1) {
    throw new SpecError("A pie chart takes exactly one series");
  }
  return {
    chart_type: chartType,
    title: c.title ? String(c.title) : "",
    labels,
    series,
    caption: c.caption ? String(c.caption) : "",
  };
}

export interface SpecOptions {
  newId: IdFactory;
  /** Shown on chart blocks ("Inserted by …"). */
  author?: string;
  /**
   * Blocks made so far with these options: specs converted with the same
   * options share it, so a call's blocks stay under MAX_SPEC_NODES in all.
   */
  made?: { count: number };
}

/** How deep a spec's blocks may nest, and how many one call may make. */
export const MAX_SPEC_DEPTH = 16;
export const MAX_SPEC_NODES = 2000;

/** Whether a line is a Markdown horizontal rule: three or more of -, * or _. */
export function isRule(line: string): boolean {
  return /^([-*_])(\s*\1){2,}$/.test(line.trim());
}

/**
 * Whether a to-do's due is one doc.ParseDue reads: a day, YYYY-MM-DD, or a
 * day and its time, YYYY-MM-DDTHH:MM, in the user's own time.
 */
export function validDue(due: unknown): due is string {
  const m = typeof due === "string" ? due.match(/^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2}))?$/) : null;
  if (!m) return false;
  const [year, month, day] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1];
  return days !== undefined && day >= 1 && day <= days &&
    (m[4] === undefined || (Number(m[4]) < 24 && Number(m[5]) < 60));
}

export function specToNode(spec: BlockSpec, opts: SpecOptions, id = opts.newId(), depth = 0): NodeJSON {
  if (!spec || typeof spec !== "object") throw new SpecError("Each block must be an object");
  if (depth >= MAX_SPEC_DEPTH) throw new SpecError(`Blocks nest at most ${MAX_SPEC_DEPTH} deep`);
  const made = opts.made ??= { count: 0 };
  if (++made.count > MAX_SPEC_NODES) throw new SpecError(`At most ${MAX_SPEC_NODES} blocks in all per call`);
  if (spec.children != null && !Array.isArray(spec.children)) {
    throw new SpecError("children must be an array of blocks");
  }
  let type = String(spec.type ?? "paragraph");
  // A Markdown rule written as text is the divider it stands for.
  if (type === "paragraph" && isRule(spec.text ?? "")) type = "divider";
  if (!SPEC_TYPES.has(type)) {
    throw new SpecError(
      `Unknown block type "${type}". Use one of: ${[...SPEC_TYPES].join(", ")}`,
    );
  }
  if (spec.children?.length && !LIST_TYPES.has(type)) {
    throw new SpecError(`Only list blocks take children, not "${type}"`);
  }

  let node: NodeJSON;
  switch (type) {
    case "heading":
      node = textNode(id, type, spec.text ?? "", { level: clampInt(spec.level, 1, 6, 2) });
      break;
    case "todo_list":
      node = textNode(id, type, spec.text ?? "", {
        checked: spec.checked === true,
        ...(validDue(spec.due) ? { due: spec.due } : {}),
      });
      break;
    case "paragraph":
    case "bulleted_list":
    case "numbered_list":
    case "quote":
      node = textNode(id, type, spec.text ?? "");
      break;
    case "callout":
      node = textNode(id, type, spec.text ?? "", { tone: calloutTone(spec.tone) });
      break;
    case "toggle_list":
      node = textNode(id, type, spec.text ?? "", { collapsed: spec.collapsed === true });
      break;
    case "video": {
      const url = String(spec.video?.url ?? "").trim();
      if (!isVideoURL(url)) {
        throw new SpecError("A video block needs `video.url`, a YouTube or Vimeo link or a link to a video file");
      }
      node = { id, type, data: { url, title: String(spec.video?.title ?? "").trim() } };
      break;
    }
    case "link_preview": {
      const url = String(spec.link?.url ?? "").trim();
      if (!/^https?:\/\/[^\s/]+/i.test(url) || /\s/.test(url)) {
        throw new SpecError("A link_preview block needs `link.url`, an http(s) link");
      }
      node = { id, type, data: { url } };
      break;
    }
    case "math_equation": {
      const formula = String(spec.formula ?? "").trim();
      if (!formula) throw new SpecError("A math_equation block needs `formula`, in TeX");
      node = { id, type, data: { formula } };
      break;
    }
    case "diagram": {
      const source = String(spec.source ?? "").replace(/^\n+|\n+$/g, "");
      if (!source.trim()) throw new SpecError("A diagram block needs `source`, in Mermaid");
      node = { id, type, data: { source } };
      break;
    }
    case "code": {
      // The code as it is, not markdown; data.delta is its plain text.
      const code = String(spec.text ?? "").replace(/\n$/, "");
      node = {
        id,
        type,
        data: { delta: code ? [{ insert: code }] : [], language: codeLanguage(spec.language) },
      };
      break;
    }
    case "divider":
      node = { id, type };
      break;
    case "image": {
      const url = spec.image?.url ?? "";
      if (!/^https?:\/\//i.test(url)) throw new SpecError("image.url must be an http(s) URL");
      const alt = typeof spec.image?.alt === "string" ? spec.image.alt : "";
      node = { id, type, data: { url, align: "center", alt } };
      break;
    }
    case "table":
      node = tableNode(id, spec.rows, opts.newId);
      break;
    case "chart":
      node = {
        id,
        type,
        data: { ...validateChart(spec.chart), author: opts.author ?? "" },
      };
      break;
    case "embed": {
      const html = spec.embed?.html;
      if (typeof html !== "string" || !html.trim()) {
        throw new SpecError("An embed block needs `embed.html`");
      }
      if (html.length > 200_000) throw new SpecError("embed.html must be under 200 KB");
      // As internal/doc/spec.go builds it.
      const title = typeof spec.embed?.title === "string" && spec.embed.title ? spec.embed.title : "Interactive embed";
      node = {
        id,
        type,
        data: {
          title,
          html,
          height: clampInt(spec.embed?.height, 120, 1200, 320),
          author: opts.author ?? "",
        },
      };
      break;
    }
    default:
      throw new SpecError(`Unsupported block type "${type}"`);
  }

  if (spec.children?.length) {
    node.children = spec.children.map((child) => specToNode(child, opts, opts.newId(), depth + 1));
  }
  return node;
}

/** appflowy_editor's TableNode: col-major `table/cell` children. */
export function tableNode(id: string, rows: unknown, newId: IdFactory): NodeJSON {
  if (!Array.isArray(rows) || rows.length === 0 || !rows.every(Array.isArray)) {
    throw new SpecError("A table block needs `rows`: an array of rows, each an array of cell strings");
  }
  const rowsLen = rows.length;
  const colsLen = Math.max(...rows.map((r) => (r as unknown[]).length));
  if (colsLen === 0) throw new SpecError("Table rows must have at least one cell");
  if (rowsLen * colsLen > 400) throw new SpecError("Tables are limited to 400 cells");
  const children: NodeJSON[] = [];
  for (let col = 0; col < colsLen; col++) {
    for (let row = 0; row < rowsLen; row++) {
      const value = (rows[row] as unknown[])[col];
      children.push({
        id: newId(),
        type: "table/cell",
        data: { colPosition: col, rowPosition: row },
        children: [textNode(newId(), "paragraph", value == null ? "" : String(value))],
      });
    }
  }
  return {
    id,
    type: "table",
    data: {
      colsLen,
      rowsLen,
      colDefaultWidth: 160,
      rowDefaultHeight: 40,
      colMinimumWidth: 40,
    },
    children,
  };
}

/** The most cells tableSize makes room for (doc.maxTableCells). */
const MAX_TABLE_CELLS = 1 << 16;

/** A JSON number as an integer, as doc.AsInt reads one; def for anything else. */
function asInt(v: unknown, def: number): number {
  return typeof v === "number" && Number.isFinite(v) ? Math.trunc(v) : def;
}

/**
 * A stored table's rows and columns, as far as it has cells for them
 * (doc.TableSize): a size from another client or a newer app, negative or
 * huge, never makes room for more cells than are there, or than
 * MAX_TABLE_CELLS.
 */
export function tableSize(node: NodeJSON): { rows: number; cols: number } {
  let lastRow = -1, lastCol = -1;
  for (const cell of node.children ?? []) {
    const row = asInt(cell.data?.rowPosition, -1), col = asInt(cell.data?.colPosition, -1);
    if (row >= 0 && col >= 0) {
      lastRow = Math.max(lastRow, row);
      lastCol = Math.max(lastCol, col);
    }
  }
  const rows = Math.min(Math.max(asInt(node.data?.rowsLen, 0), 0), lastRow + 1);
  const cols = Math.min(Math.max(asInt(node.data?.colsLen, 0), 0), lastCol + 1, MAX_TABLE_CELLS);
  if (rows === 0 || cols === 0) return { rows: 0, cols: 0 };
  return { rows: Math.min(rows, Math.floor(MAX_TABLE_CELLS / cols)), cols };
}

export function tableRows(node: NodeJSON): string[][] {
  const { rows: rowsLen, cols: colsLen } = tableSize(node);
  const rows: string[][] = Array.from({ length: rowsLen }, () => Array(colsLen).fill(""));
  for (const cell of node.children ?? []) {
    const col = asInt(cell.data?.colPosition, -1);
    const row = asInt(cell.data?.rowPosition, -1);
    if (row >= 0 && row < rowsLen && col >= 0 && col < colsLen) {
      rows[row][col] = (cell.children ?? []).map((p) => deltaToMarkdown(deltaOf(p))).join(" ");
    }
  }
  return rows;
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

export interface RecordingInfo {
  id: string;
  title?: string;
  duration_ms?: number;
  status?: string;
  transcript?: { start_ms?: number; text?: string; speaker?: string }[];
}

function clock(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
}

export function nodeView(
  node: NodeJSON,
  depth: number,
  parentId: string | undefined,
  recordings: Map<string, RecordingInfo>,
): BlockView {
  const view: BlockView = { id: node.id, type: node.type, text: "", depth };
  if (parentId) view.parent_id = parentId;
  const data = node.data ?? {};
  if (TEXT_TYPES.has(node.type)) view.text = deltaToMarkdown(deltaOf(node));
  switch (node.type) {
    case "heading":
      view.level = Number(data.level ?? 1);
      break;
    case "code":
      view.text = deltaToPlain(deltaOf(node));
      view.language = String(data.language ?? "");
      break;
    case "callout":
      view.tone = calloutTone(data.tone);
      break;
    case "toggle_list":
      view.collapsed = data.collapsed === true;
      break;
    case "video":
    case "link_preview":
      view.text = String(data.title ?? data.url ?? "");
      view.url = String(data.url ?? "");
      break;
    case "file":
      view.text = String(data.name ?? "");
      break;
    case "math_equation":
      view.formula = String(data.formula ?? "");
      view.text = view.formula;
      break;
    case "diagram":
      view.source = String(data.source ?? "");
      view.text = view.source;
      break;
    case "todo_list":
      view.checked = data.checked === true;
      if (typeof data.due === "string") view.due = data.due;
      break;
    case "table":
      view.rows = tableRows(node);
      break;
    case "chart":
      view.text = String(data.title ?? "");
      view.chart = {
        chart_type: data.chart_type,
        title: data.title,
        labels: data.labels,
        series: data.series,
      };
      break;
    case "embed":
      view.text = String(data.title ?? "");
      view.embed = {
        title: String(data.title ?? ""),
        height: Number(data.height ?? 360),
        html_length: String(data.html ?? "").length,
      };
      break;
    case "image":
      view.text = String(data.url ?? "");
      break;
    case "recording": {
      const rid = String(data.recording_id ?? "");
      const info = recordings.get(rid);
      view.text = String(data.title ?? info?.title ?? "Recording");
      view.recording = {
        recording_id: rid,
        duration_ms: data.duration_ms ?? info?.duration_ms,
        status: info?.status,
        transcript: (info?.transcript ?? [])
          .map((s) => `${clock(s.start_ms ?? 0)}${s.speaker ? ` ${s.speaker}:` : ""} ${s.text ?? ""}`.trim())
          .join("\n"),
      };
      break;
    }
    case "subpage":
      view.text = String(data.title ?? data.page_id ?? "");
      break;
  }
  return view;
}

/** Every addressable block, in reading order. Table cells are folded into their table. */
export function pageViews(
  content: PageContent,
  recordings: Map<string, RecordingInfo> = new Map(),
): BlockView[] {
  const views: BlockView[] = [];
  const visit = (nodes: NodeJSON[], depth: number, parentId?: string) => {
    for (const node of nodes) {
      views.push(nodeView(node, depth, parentId, recordings));
      if (node.type !== "table" && node.children?.length) {
        visit(node.children, depth + 1, node.id);
      }
    }
  };
  visit(content.document.children ?? [], 0);
  return views;
}

export function plainText(content: PageContent): string {
  const lines: string[] = [];
  walk(content.document, (node) => {
    if (TEXT_TYPES.has(node.type)) {
      const text = deltaToPlain(deltaOf(node));
      if (text) lines.push(text);
    } else if (["chart", "embed", "video", "link_preview"].includes(node.type)) {
      const title = String(node.data?.title ?? "");
      if (title) lines.push(title);
    } else if (node.type === "file" && node.data?.name) {
      lines.push(String(node.data.name));
    } else if (node.type === "math_equation" || node.type === "diagram") {
      const source = String(node.data?.formula ?? node.data?.source ?? "");
      if (source) lines.push(source);
    }
  });
  return lines.join("\n");
}
