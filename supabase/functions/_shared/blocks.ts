// Page content helpers (protocol §1–§2): node JSON with stable IDs, inline
// markdown ⇄ delta, BlockSpec → node JSON, and BlockView for read_page.
// Mirrors internal/agents/ and internal/doc/ on the client.

import { agentURL, goLower, hostname, parseURL, pathExt, queryValue } from "./url.ts";

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
  /** For video: its link, its title, and its notes, a plain-text line each, a time starting a line playing it from there. */
  video?: { url: string; title?: string; notes?: string };
  /** For replace_block on a file block: its notes, the file itself as it is. */
  file?: { notes?: string };
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

/**
 * A block as agents read it, with the keys doc.BlockViews gives it on the
 * client: an agent sees the same page in the cloud as on a device.
 */
export interface BlockView {
  id: string;
  type: string;
  /** For text blocks, as inline markdown; for code, the code. */
  text?: string;
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
  rows?: string[][];
  chart?: Record<string, unknown>;
  /** The source too, so an agent can change an embed with replace_block rather than write a new one blind. */
  embed?: { title: unknown; height: unknown; html?: unknown; html_length?: number };
  image?: { url: unknown; alt: unknown };
  video?: { url: unknown; title: unknown; notes?: unknown };
  link?: { url: unknown; title: unknown; description: unknown };
  file?: { name: unknown; mime: unknown; size: unknown; notes?: unknown };
  recording?: Record<string, unknown>;
  page_id?: unknown;
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

/** A page with one empty paragraph to type in, stamped now (doc.Empty). */
export function blankContent(now = Date.now()): PageContent {
  const content = emptyContent();
  const id = randomId();
  content.document.children = [{ id, type: "paragraph", data: { delta: [] } }];
  content.meta.blocks[id] = now;
  return content;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

/**
 * Text as the database can keep it: without NUL, which text and jsonb
 * refuse, and with half a surrogate pair, which jsonb refuses too, as
 * U+FFFD, as Go reads one from JSON. One such character a model wrote
 * would otherwise fail the save of all a run did.
 */
export function storable(text: string): string {
  return text.replaceAll("\u0000", "").toWellFormed();
}

/** A value parsed from JSON with every string value in it storable. */
export function storableValue(value: unknown): unknown {
  if (typeof value === "string") return storable(value);
  if (Array.isArray(value)) return value.map(storableValue);
  if (isRecord(value)) return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, storableValue(v)]));
  return value;
}

/**
 * Whether a list of blocks reads as the client's doc.Node reads one: each
 * an object, or null, which the client drops; its id and type text, its
 * data an object and its children such a list, each of them null or not
 * there too. The client cannot read a page with anything else in it.
 */
function blocksDecode(raw: unknown): boolean {
  const lists = [raw];
  while (lists.length) {
    const list = lists.pop();
    if (list == null) continue;
    if (!Array.isArray(list)) return false;
    for (const node of list) {
      if (node === null) continue;
      if (!isRecord(node)) return false;
      const { id, type, data, children } = node;
      if (id != null && typeof id !== "string") return false;
      if (type != null && typeof type !== "string") return false;
      if (data != null && !isRecord(data)) return false;
      lists.push(children);
    }
  }
  return true;
}

/**
 * Whether stored content is a page's, as the client reads one: an object
 * with its document, of a version this code knows, with blocks it can read
 * (blocksDecode) and edit times that are numbers. What is not would read
 * as an empty page, or part of one, which saving would put in place of the
 * real one.
 */
export function isPageContent(raw: unknown): boolean {
  if (!isRecord(raw) || !isRecord(raw.document)) return false;
  const meta = raw.meta;
  if (meta != null) {
    if (!isRecord(meta)) return false;
    if (meta.v != null && !(Number.isInteger(meta.v) && (meta.v as number) <= 1)) return false;
    for (const times of [meta.blocks, meta.deleted]) {
      if (times == null) continue;
      if (!isRecord(times) || Object.values(times).some((t) => t !== null && typeof t !== "number")) return false;
    }
  }
  return blocksDecode(raw.document.children);
}

/**
 * Accepts whatever is stored and returns a well-formed content object:
 * nulls among blocks dropped, as the client drops them, and each block
 * with an id of its own.
 */
export function normalizeContent(raw: unknown): PageContent {
  const content = emptyContent();
  if (!raw || typeof raw !== "object") return content;
  const r = raw as Partial<PageContent> & { document?: NodeJSON };
  if (r.document && typeof r.document === "object") {
    content.document = r.document;
  }
  content.document.id ||= "root";
  content.document.type ||= "page";
  if (r.meta && typeof r.meta === "object") {
    content.meta = {
      v: r.meta.v ?? 1,
      blocks: { ...(r.meta.blocks ?? {}) },
      deleted: { ...(r.meta.deleted ?? {}) },
    };
  }
  // A block at a time, not by recursion: a stored page may nest deeper
  // than the stack goes. blocks are the page's blocks in document order.
  const blocks: NodeJSON[] = [];
  const nodes = [content.document];
  while (nodes.length) {
    const node = nodes.pop()!;
    if (node !== content.document) blocks.push(node);
    if (typeof node.type !== "string") node.type = "";
    if (Array.isArray(node.children)) node.children = node.children.filter(isRecord) as unknown as NodeJSON[];
    else if (node === content.document) node.children = [];
    else delete node.children;
    const kids = node.children ?? [];
    for (let i = kids.length - 1; i >= 0; i--) nodes.push(kids[i]);
  }
  repairIds(blocks);
  return content;
}

/**
 * Gives stored content's blocks without an ID, or with one an earlier
 * block or the page has, IDs every reader gives them (doc's repairIDs):
 * its ID and how many blocks had it so far, "a-2" for the second "a" and
 * "block-1" for the first without one. Every read of the page makes the
 * same, here and on each device, so a merge matches the blocks up. Only
 * what is read goes through it: a block an edit makes takes a new random
 * ID (randomId). blocks are in document order.
 */
function repairIds(blocks: NodeJSON[]): void {
  const idOf = (node: NodeJSON) => typeof node.id === "string" ? node.id : "";
  const taken = new Set(["root", ...blocks.map(idOf)]);
  const seen = new Set(["root"]);
  const count = new Map<string, number>();
  for (const node of blocks) {
    const was = idOf(node);
    count.set(was, (count.get(was) ?? 0) + 1);
    if (was && !seen.has(was)) {
      seen.add(was);
      continue;
    }
    for (;;) {
      const id = `${was || "block"}-${count.get(was)}`;
      if (!taken.has(id) && !seen.has(id)) {
        node.id = id;
        break;
      }
      count.set(was, count.get(was)! + 1);
    }
    seen.add(node.id);
  }
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

/**
 * A block's runs of text, as the client reads its delta (doc.DeltaFromJSON):
 * an op that is no object, or that inserts no text, is none of them.
 */
export function deltaOf(node: NodeJSON): DeltaOp[] {
  const delta = node.data?.delta;
  return Array.isArray(delta) ? delta.filter((op) => isRecord(op) && typeof op.insert === "string") : [];
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

/**
 * The characters Go's unicode.IsSpace takes for spaces, as a regular
 * expression's class: what the client trims or reads as space, this side
 * does too. U+0085 is one; U+FEFF, which JavaScript's \s has, is not.
 */
export const SPACES = "\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000";
const spaceRe = new RegExp(`[${SPACES}]`, "u");
const spaceRunRe = new RegExp(`[${SPACES}]+`, "u");
const edgeSpacesRe = new RegExp(`^[${SPACES}]+|[${SPACES}]+$`, "gu");
const leadSpacesRe = new RegExp(`^[${SPACES}]+`, "u");
const tailSpacesRe = new RegExp(`[${SPACES}]+$`, "u");

/** Whether s holds a space, as unicode.IsSpace has one; for one character, whether it is one. */
export function isSpace(s: string): boolean {
  return spaceRe.test(s);
}

/** s without the spaces at its ends (strings.TrimSpace). */
export function trimSpace(s: string): string {
  return s.replace(edgeSpacesRe, "");
}

/** The words of s between runs of spaces (strings.Fields). */
export function fields(s: string): string[] {
  return s.split(spaceRunRe).filter(Boolean);
}

/** Whether a backslash escapes ch: ASCII punctuation. */
function isPunct(ch: string | undefined): boolean {
  return !!ch && /^[!-\/:-@\[-`{-~]$/.test(ch);
}

// A link to one of the user's pages, as doc.PageLink makes it: a page
// mention is the page's title linked to it. One to a block of a page,
// leaflet://page/<id>#<block>, stays a link.
const PAGE_LINK_PREFIX = "leaflet://page/";

/**
 * The page a page link points to and the block, "" for the page; null for
 * other links. A link to a moment of a video or a recording names its time
 * after the block, as #<block>?t=83 (doc.MomentLink).
 */
function pageLink(href: string): { id: string; block: string } | null {
  if (!href.startsWith(PAGE_LINK_PREFIX)) return null;
  const rest = href.slice(PAGE_LINK_PREFIX.length);
  const hash = rest.indexOf("#");
  const id = hash < 0 ? rest : rest.slice(0, hash);
  return id ? { id, block: hash < 0 ? "" : rest.slice(hash + 1).split("?")[0] } : null;
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
  style: string;
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
    style: colorStyle(a),
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
  // Off line breaks in code too: a line can't start with a marker that
  // closes, where it can read as a list item's.
  const space = (i: number) => (!cs[i].r.code || cs[i].ch === "\n") && isSpace(cs[i].ch);
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
  kind: "[" | "c" | "*" | "i" | "~";
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

/**
 * The marks of a run, outermost first when they open together: a link
 * holds the others, and a color the formats.
 */
function marksOf(r: MdRun): MdMark[] {
  const out: MdMark[] = [];
  if (r.href) out.push({ kind: "[", href: r.href });
  if (r.style) out.push({ kind: "c", href: r.style });
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
  const emphasis = (t: MdToken) => isMarker(t) && t.mark!.kind !== "[" && t.mark!.kind !== "c";
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
      else if (plain(t) && trimSpace(t.text) === "") spaces.push({ text: t.text, at: marks.length });
      else break;
    }
    const prev = out[out.length - 1];
    if (prev && plain(prev)) {
      const t = prev.text.replace(tailSpacesRe, "");
      spaces.unshift({ text: prev.text.slice(t.length), at: 0 });
      prev.text = t;
    }
    if (j < toks.length && plain(toks[j])) {
      const t = toks[j].text.replace(leadSpacesRe, "");
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

/** The tags a color is written between (deltaToMarkdown). */
const COLOR_OPEN = '<span style="';
const COLOR_CLOSE = "</span>";

/**
 * The longest style a color's tag is read with: a text of tags that end
 * nowhere is not looked through to its end from each (doc.maxColorStyle).
 */
const MAX_COLOR_STYLE = 64;

const colorRe = /^#(?:[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/;

/** Whether v is a color as runs keep one (doc.ValidColor). */
function validColor(v: unknown): v is string {
  return typeof v === "string" && colorRe.test(v);
}

/** How a run's color and highlight are written in a tag, "" without them (doc.colorStyle). */
function colorStyle(a: Attrs): string {
  const parts: string[] = [];
  if (validColor(a.color)) parts.push(`color:${a.color}`);
  if (validColor(a.background)) parts.push(`background:${a.background}`);
  return parts.join(";");
}

/** Whether the characters of s at i spell text. */
function hasChars(s: string[], i: number, text: string): boolean {
  const t = [...text];
  return t.every((c, k) => s[i + k] === c);
}

/**
 * Reads a color's opening tag at s[i], before hi: the formats it sets on
 * attrs and how many characters it takes, or null when there is none, as a
 * style of anything but hex colors, which reads as the text it is
 * (doc.colorTagAt).
 */
function colorTagAt(s: string[], i: number, hi: number, attrs: Attrs): { attrs: Attrs; n: number } | null {
  if (!hasChars(s, i, COLOR_OPEN)) return null;
  const start = i + COLOR_OPEN.length;
  const limit = Math.min(hi, start + MAX_COLOR_STYLE);
  let end = start;
  while (end < limit && s[end] !== '"') end++;
  if (end >= limit || end + 1 >= hi || s[end + 1] !== ">") return null;
  const out = { ...attrs };
  let found = false;
  for (const part of s.slice(start, end).join("").split(";")) {
    if (trimSpace(part) === "") continue;
    const at = part.indexOf(":");
    const name = trimSpace(at < 0 ? part : part.slice(0, at));
    const value = at < 0 ? "" : trimSpace(part.slice(at + 1));
    if (!validColor(value)) return null;
    if (name === "color") out.color = value;
    else if (name === "background" || name === "background-color") out.background = value;
    else return null;
    found = true;
  }
  return found ? { attrs: out, n: end + 2 - i } : null;
}

/**
 * Escapes the characters that would read as markup: all the markers but a
 * _ inside a word, and the < of a color's tags.
 */
function escapeText(s: string): string {
  const chars = [...s];
  let out = "";
  chars.forEach((c, i) => {
    if ("\\*`~[]".includes(c)) out += "\\";
    else if (c === "<" && (hasChars(chars, i, COLOR_OPEN) || hasChars(chars, i, COLOR_CLOSE))) out += "\\";
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
  if (s.startsWith("`") || s.endsWith("`") || (s.startsWith(" ") && s.endsWith(" ") && trimSpace(s) !== "")) {
    s = ` ${s} `;
  }
  return ticks + s + ticks;
}

/**
 * A link's url, in angle brackets when it holds what would end it or read
 * as markup, and a backtick as a url can hold one (doc.LinkDestination).
 */
function linkDestination(href: string): string {
  href = href.replaceAll("`", "%60");
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
    // A ! before a link would make it a picture: it is escaped.
    if ((t.open && t.mark!.kind === "[" || !isMarker(t) && !t.code && t.text.startsWith("[")) && out.endsWith("!")) {
      out = out.slice(0, -1) + "\\!";
    }
    if (t.code) out += codeSpan(t.text);
    else if (!isMarker(t)) out += t.text;
    else if (t.mark!.kind === "[") out += t.open ? "[" : `](${linkDestination(t.mark!.href)})`;
    else if (t.mark!.kind === "c") out += t.open ? `${COLOR_OPEN}${t.mark!.href}">` : COLOR_CLOSE;
    // Bold beside an italic's * is __, as *** reads either way.
    else if (t.mark!.kind === "*") out += boldTouchesItalic(toks, i) ? "__" : "**";
    else if (t.mark!.kind === "i") out += "*";
    else out += "~~";
  });
  return out;
}

// ---------------------------------------------------------------------------
// Reading inline markdown
// ---------------------------------------------------------------------------
//
// The reader looks ahead from a marker for where it ends: a code span's
// closing backticks, a link's ] and its url's ), a * or ** that closes.
// Looked for again from each marker, as doc.FromMarkdown looks, a text of
// markers that close nowhere takes time in the square of its length: 60 KB
// of "_a " takes seconds, past what an Edge Function may spend. Here each
// is found once for each place in a span of text: what a scan finds from a
// place it passes on to every place it went through, and the runs of
// backticks are found once for the whole text. A span is what one reading
// of it covers, the text or the inside of a link or a format, as it ends
// where it does: the scans of what is inside one stop at its end.

/** A code span read: its code, and how many characters it takes from its first backtick. */
interface CodeSpan {
  code: string;
  n: number;
}

/** A link read, [text](url): its text is s[from + 1, close), its url in s[urlFrom, urlTo), and it takes n characters. */
interface LinkSpan {
  close: number;
  urlFrom: number;
  urlTo: number;
  n: number;
}

/**
 * Formats and links nest at most this deep: past it, text is read as it
 * is written. Each level reads its span again, and the stack runs out a
 * few thousand deep.
 */
const MAX_INLINE_DEPTH = 32;

/** The characters of a text read as inline markdown, and its runs of backticks. */
export class InlineText {
  /** For a backtick, where the run of them it is in starts and ends. */
  private readonly runStart: Int32Array;
  private readonly runEnd: Int32Array;
  /** The runs of backticks of each length, by where they start, in order. */
  private readonly runs = new Map<number, number[]>();

  constructor(readonly s: string[]) {
    this.runStart = new Int32Array(s.length);
    this.runEnd = new Int32Array(s.length);
    for (let i = 0; i < s.length;) {
      if (s[i] !== "`") {
        i++;
        continue;
      }
      let end = i;
      while (end < s.length && s[end] === "`") end++;
      this.runStart.fill(i, i, end);
      this.runEnd.fill(end, i, end);
      const list = this.runs.get(end - i) ?? [];
      list.push(i);
      this.runs.set(end - i, list);
      i = end;
    }
  }

  /** The whole text, as one span. */
  span(): Span {
    return new Span(this, 0, this.s.length);
  }

  /**
   * The code span at j, in a span ending at hi: between runs of as many
   * backticks, the first such run after j's (codeSpanAt). A run the span
   * cuts at hi counts as long as it is inside it.
   */
  codeSpan(j: number, hi: number): CodeSpan | null {
    const open = Math.min(this.runEnd[j], hi) - j;
    if (open >= hi - j) return null;
    let close = -1;
    // The first whole run of as many after j's.
    const list = this.runs.get(open) ?? [];
    let a = 0, b = list.length;
    while (a < b) {
      const m = (a + b) >> 1;
      if (list[m] <= j) a = m + 1;
      else b = m;
    }
    if (a < list.length && list[a] + open <= hi) close = list[a];
    else if (this.s[hi - 1] === "`" && this.runEnd[hi - 1] > hi) {
      // Else the run the span cuts, if it is as long inside it.
      const start = this.runStart[hi - 1];
      if (start > j && hi - start === open) close = start;
    }
    if (close < 0) return null;
    let code = this.s.slice(j + open, close).join("");
    if (code.length >= 2 && code[0] === " " && code[code.length - 1] === " " && trimSpace(code) !== "") {
      code = code.slice(1, -1);
    }
    return code ? { code, n: close + open - j } : null;
  }
}

/** Part of an InlineText, s[lo, hi), read on its own, with what its scans found. */
export class Span {
  private readonly s: string[];
  private codes = new Map<number, CodeSpan | null>();
  private links = new Map<number, LinkSpan | null>();
  /** For each place, where the first ] at its depth is from there (link), or -1. */
  private closes: Int32Array | null = null;
  /** For each place, where the first ) at its depth is from there, in a url (link), or -1. */
  private urlCloses: Int32Array | null = null;
  /** Of each marker, where it is first found from each place passed (findSingle, indexOfMarker). */
  private found = new Map<string, Map<number, number>>();

  constructor(readonly text: InlineText, readonly lo: number, readonly hi: number) {
    this.s = text.s;
  }

  /** The code span at j, or null. */
  codeSpan(j: number): CodeSpan | null {
    if (!this.codes.has(j)) this.codes.set(j, this.text.codeSpan(j, this.hi));
    return this.codes.get(j)!;
  }

  /** Where a code span or a link at j ends, or j when none starts there. */
  skip(j: number): number {
    if (this.s[j] === "`") {
      const span = this.codeSpan(j);
      if (span) return j + span.n;
    } else if (this.s[j] === "[") {
      const l = this.link(j);
      if (l) return j + l.n;
    }
    return j;
  }

  /**
   * Where the first ] is from each place at the depth of that place, a [
   * and its ] in between, a code span's or an escaped one not counting:
   * filled from the end, each from the places after it.
   */
  private closeAt(p: number): number {
    if (!this.closes) {
      const { s, lo, hi } = this;
      const m = new Int32Array(hi - lo + 2).fill(-1);
      const at = (q: number) => (q < hi ? m[q - lo] : -1);
      for (let q = hi - 1; q >= lo; q--) {
        const c = s[q];
        if (c === "\\") m[q - lo] = at(q + 2);
        else if (c === "`") {
          const span = this.codeSpan(q);
          m[q - lo] = at(span ? q + span.n : q + 1);
        } else if (c === "[") {
          const inner = at(q + 1);
          m[q - lo] = inner < 0 ? -1 : at(inner + 1);
        } else if (c === "]") m[q - lo] = q;
        else m[q - lo] = at(q + 1);
      }
      this.closes = m;
    }
    return p < this.hi ? this.closes[p - this.lo] : -1;
  }

  /**
   * Where a url's ) is from each place at the depth of that place, a (
   * and its ) in between, an escaped one not counting; -1 past a space.
   */
  private urlCloseAt(p: number): number {
    if (!this.urlCloses) {
      const { s, lo, hi } = this;
      const m = new Int32Array(hi - lo + 2).fill(-1);
      const at = (q: number) => (q < hi ? m[q - lo] : -1);
      for (let q = hi - 1; q >= lo; q--) {
        const c = s[q];
        if (isSpace(c)) m[q - lo] = -1;
        else if (c === "\\" && q + 1 < hi && isPunct(s[q + 1])) m[q - lo] = at(q + 2);
        else if (c === "(") {
          const inner = at(q + 1);
          m[q - lo] = inner < 0 ? -1 : at(inner + 1);
        } else if (c === ")") m[q - lo] = q;
        else m[q - lo] = at(q + 1);
      }
      this.urlCloses = m;
    }
    return p < this.hi ? this.urlCloses[p - this.lo] : -1;
  }

  /**
   * Reads [text](url) at j: as in CommonMark, the url may be in angle
   * brackets or hold balanced parentheses, and backslashes escape in
   * both (doc.link).
   */
  link(j: number): LinkSpan | null {
    if (this.links.has(j)) return this.links.get(j)!;
    const { s, hi } = this;
    let found: LinkSpan | null = null;
    const close = this.closeAt(j + 1);
    if (close > j + 1 && close + 1 < hi && s[close + 1] === "(") {
      const from = close + 2;
      if (from < hi && s[from] === "<") {
        let k = from + 1;
        for (; k < hi && s[k] !== ">"; k++) {
          if (s[k] === "<" || s[k] === "\n") {
            k = -1;
            break;
          }
          if (s[k] === "\\" && k + 1 < hi && isPunct(s[k + 1])) k++;
        }
        if (k > from + 1 && k + 1 < hi && s[k + 1] === ")") {
          found = { close, urlFrom: from + 1, urlTo: k, n: k + 2 - j };
        }
      } else {
        const end = this.urlCloseAt(from);
        if (end > from) found = { close, urlFrom: from, urlTo: end, n: end + 1 - j };
      }
    }
    this.links.set(j, found);
    return found;
  }

  /** A link's url, its escapes undone. */
  href(l: LinkSpan): string {
    const { s, hi } = this;
    let href = "";
    for (let k = l.urlFrom; k < l.urlTo; k++) {
      if (s[k] === "\\" && k + 1 < hi && isPunct(s[k + 1])) k++;
      href += s[k];
    }
    return href;
  }

  /**
   * Where scanning from p for a marker first finds one, a place at a time
   * as next has it, or -1: each place passed is told what was found, so
   * no place is scanned from twice.
   */
  private first(
    key: string,
    p: number,
    end: number,
    next: (q: number) => number,
    takes: (q: number) => boolean,
  ): number {
    let memo = this.found.get(key);
    if (!memo) this.found.set(key, memo = new Map());
    const passed: number[] = [];
    let result = -1;
    while (p < end) {
      const known = memo.get(p);
      if (known !== undefined) {
        result = known;
        break;
      }
      if (takes(p)) {
        result = p;
        break;
      }
      passed.push(p);
      p = next(p);
    }
    for (const q of passed) memo.set(q, result);
    return result;
  }

  /**
   * Where a single * or _ closes from `from`: one not doubled, not before
   * a word for _, nor after a space but right at from, outside code spans
   * and links (doc.findSingle).
   */
  findSingle(marker: string, from: number): number {
    const { s, hi } = this;
    const next = (j: number) => {
      if (s[j] === "\\") return j + 2;
      const k = this.skip(j);
      if (k > j) return k;
      return s[j] === marker && j + 1 < hi && s[j + 1] === marker ? j + 2 : j + 1;
    };
    const closes = (j: number) =>
      s[j] === marker && !(j + 1 < hi && s[j + 1] === marker) &&
      !(marker === "_" && j + 1 < hi && isWordChar(s[j + 1]));
    if (from >= hi) return -1;
    // Right at from, a space before it does not count.
    if (closes(from)) return from;
    return this.first(`1${marker}`, next(from), hi, next, (j) => closes(j) && s[j - 1] !== " ");
  }

  /** Where marker is first from `from`, outside code spans and links (doc.indexString). */
  indexOf(marker: string, from: number): number {
    const { s } = this;
    const m = [...marker];
    const next = (j: number) => {
      if (s[j] === "\\") return j + 2;
      const k = this.skip(j);
      return k > j ? k : j + 1;
    };
    // A marker's characters are never a backslash nor what opens a span.
    const at = (j: number) => m.every((c, k) => s[j + k] === c);
    return this.first(marker, from, this.hi - m.length + 1, next, at);
  }
}

/** The formats a run keeps of its own (doc.Attrs), which sharedAttrs looks at. */
const FLAGS = ["bold", "italic", "underline", "strikethrough", "code"];
const COLORS = ["color", "background"];

/** The formats every run of d has, or attrs when it has none (doc.sharedAttrs). */
function sharedAttrs(d: DeltaOp[], attrs: Attrs): Attrs {
  // Runs of spaces only have no say: Markdown writes no format over them.
  const words = d.filter((r) => trimSpace(r.insert) !== "");
  if (words.length) d = words;
  if (d.length === 0) return attrs;
  const out: Attrs = {};
  for (const k of FLAGS) {
    if (d.every((r) => r.attributes?.[k] === true)) out[k] = true;
  }
  for (const k of COLORS) {
    const v = d[0].attributes?.[k];
    if (v !== undefined && d.every((r) => r.attributes?.[k] === v)) out[k] = v;
  }
  return out;
}

function parseInline(span: Span, attrs: Attrs, out: DeltaOp[], depth = 0): void {
  const { s } = span.text;
  const { lo, hi } = span;
  if (depth > MAX_INLINE_DEPTH) {
    pushText(out, s.slice(lo, hi).join(""), attrs);
    return;
  }
  const inner = (from: number, to: number) => new Span(span.text, from, to);
  let plain = "";
  const flush = () => {
    pushText(out, plain, attrs);
    plain = "";
  };
  for (let i = lo; i < hi;) {
    const c = s[i];
    if (c === "\\" && i + 1 < hi && isPunct(s[i + 1])) {
      plain += s[i + 1];
      i += 2;
      continue;
    }
    if (c === "`") {
      const code = span.codeSpan(i);
      if (code) {
        flush();
        pushText(out, code.code, { ...attrs, code: true });
        i += code.n;
        continue;
      }
    }
    if (c === "[") {
      const l = span.link(i);
      if (l) {
        flush();
        const href = span.href(l);
        const page = pageLink(href);
        const mention = mentionLink(href);
        const text = inner(i + 1, l.close);
        if (page && !page.block) {
          // A link to a page is a mention of it, in the formats around the
          // link and those all its text has, as deltaToMarkdown writes a
          // mention's own; one to a block of a page stays a link, with its
          // own text.
          const kids: DeltaOp[] = [];
          parseInline(text, attrs, kids, depth + 1);
          const name = deltaToPlain(kids);
          pushText(out, name, {
            ...sharedAttrs(kids, attrs),
            href: PAGE_LINK_PREFIX + page.id,
            mention: { kind: "page", id: page.id, name },
          });
        } else if (mention) {
          // By the name it shows, its text read on its own.
          const kids: DeltaOp[] = [];
          parseInline(text, {}, kids, depth + 1);
          const name = deltaToPlain(kids).replace(/^@/, "");
          pushText(out, `@${name}`, { ...attrs, mention: { ...mention, name } });
        } else {
          parseInline(text, { ...attrs, href }, out, depth + 1);
        }
        i += l.n;
        continue;
      }
    }
    if (c === "<") {
      const tag = colorTagAt(s, i, hi, attrs);
      const end = tag ? span.indexOf(COLOR_CLOSE, i + tag.n) : -1;
      if (tag && end > i + tag.n) {
        flush();
        parseInline(inner(i + tag.n, end), tag.attrs, out, depth + 1);
        i = end + COLOR_CLOSE.length;
        continue;
      }
    }
    // ***both*** is bold and italic, as agents write it.
    if (i + 2 < hi && (c === "*" || c === "_") && s[i + 1] === c && s[i + 2] === c) {
      const end = span.indexOf(c.repeat(3), i + 3);
      if (end > i + 3) {
        flush();
        parseInline(inner(i + 3, end), { ...attrs, bold: true, italic: true }, out, depth + 1);
        i = end + 3;
        continue;
      }
    }
    if (i + 1 < hi && (c === "*" || c === "~" || c === "_") && s[i + 1] === c) {
      const end = span.indexOf(c.repeat(2), i + 2);
      if (end > i + 2) {
        flush();
        const format = c === "~" ? { strikethrough: true } : { bold: true };
        parseInline(inner(i + 2, end), { ...attrs, ...format }, out, depth + 1);
        i = end + 2;
        continue;
      }
    }
    if ((c === "*" || c === "_") && (c !== "_" || !(i > lo && isWordChar(s[i - 1])))) {
      const end = span.findSingle(c, i + 1);
      if (end > i + 1) {
        flush();
        parseInline(inner(i + 1, end), { ...attrs, italic: true }, out, depth + 1);
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
 * __bold__, *italic* or _italic_, `code`, ~~strike~~, colors in
 * <span style="color:#rrggbb;background:#rrggbb">, [text](url), page
 * mentions [Title](leaflet://page/<id>) and person or agent mentions
 * [@Name](mention:user|agent:<id>).
 */
export function markdownToDelta(src: string, attrs: Attrs = {}): DeltaOp[] {
  const out: DeltaOp[] = [];
  parseInline(new InlineText([...src]).span(), attrs, out);
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
  const t = goLower(trimSpace(String(tone ?? "")));
  return CALLOUT_TONES.has(t) ? t : "info";
}

/** Video files a video block plays, by extension (doc.VideoExts). */
const VIDEO_EXTS = new Set([".mp4", ".m4v", ".mov", ".webm", ".ogv"]);

/** A video a link points to (doc.Video): its site, and on YouTube and Vimeo, whose captions agents read, its ID and Vimeo's key. */
export interface VideoRef {
  provider: string;
  id: string;
  hash: string;
}

/**
 * Whether a link is to a video on YouTube, Vimeo, Bilibili, Dailymotion or
 * Niconico, or a video file (doc.ParseVideoURL): a link to one of those
 * sites that names no video may still be to a file.
 */
export function isVideoURL(link: string): boolean {
  return videoOf(link) !== null;
}

/**
 * The video a link points to, as doc.ParseVideoURL reads it, or null: the
 * ID and key of one on YouTube or Vimeo, the site alone of the others'.
 */
export function videoOf(link: string): VideoRef | null {
  const u = parseURL(trimSpace(link));
  if (!u || (u.scheme !== "http" && u.scheme !== "https")) return null;
  const ref = (provider: string, id = "", hash = ""): VideoRef => ({ provider, id, hash });
  const host = goLower(hostname(u.host)).replace(/^www\./, "");
  const parts = u.path.split("/").filter(Boolean);
  const youtubeId = /^[A-Za-z0-9_-]{11}$/;
  const one = parts.length === 1 ? parts[0] : "";
  switch (host) {
    case "youtu.be":
      if (youtubeId.test(one)) return ref("youtube", one);
      break;
    case "youtube.com":
    case "m.youtube.com":
    case "music.youtube.com":
    case "youtube-nocookie.com": {
      const id = parts.length === 1 && parts[0] === "watch"
        ? queryValue(u.rawQuery, "v")
        : parts.length === 2 && ["embed", "shorts", "live", "v"].includes(parts[0])
        ? parts[1]
        : "";
      if (youtubeId.test(id)) return ref("youtube", id);
      break;
    }
    case "vimeo.com":
    case "player.vimeo.com": {
      // The ID is the last number in the path; an unlisted video's key
      // follows it, or is the query's h.
      const hex = /^[0-9a-f]{6,}$/;
      for (let i = parts.length - 1; i >= 0; i--) {
        if (!/^[0-9]+$/.test(parts[i])) continue;
        let hash = queryValue(u.rawQuery, "h");
        if (i + 1 < parts.length && hex.test(parts[i + 1])) hash = parts[i + 1];
        return ref("vimeo", parts[i], hex.test(hash) ? hash : "");
      }
      break;
    }
    case "bilibili.com":
    case "m.bilibili.com":
    case "player.bilibili.com":
    case "b23.tv": {
      const i = parts.indexOf("video");
      let id = "";
      if (host === "b23.tv") id = one;
      else if (one === "player.html") {
        id = queryValue(u.rawQuery, "bvid");
        const aid = queryValue(u.rawQuery, "aid");
        if (id === "" && aid !== "") id = "av" + aid;
      } else if (i >= 0 && i + 1 < parts.length) id = parts[i + 1];
      // Bilibili takes a BV ID's prefix in any case, sending on to BV.
      if (id.length > 2 && goLower(id.slice(0, 2)) === "bv") id = "BV" + id.slice(2);
      if (/^(?:BV[1-9A-HJ-NP-Za-km-z]{10}|av[1-9][0-9]*)$/.test(id)) return ref("bilibili");
      break;
    }
    case "dailymotion.com":
    case "geo.dailymotion.com":
    case "dai.ly": {
      const id = host === "dai.ly"
        ? one
        : one === "player.html"
        ? queryValue(u.rawQuery, "video")
        : parts.length === 2 && parts[0] === "video"
        ? parts[1]
        : parts.length === 3 && parts[0] === "embed" && parts[1] === "video"
        ? parts[2]
        : "";
      if (/^[xk][0-9A-Za-z]+$/.test(id.split("_")[0])) return ref("dailymotion");
      break;
    }
    case "nicovideo.jp":
    case "sp.nicovideo.jp":
    case "embed.nicovideo.jp":
    case "nico.ms": {
      const id = host === "nico.ms" ? one : parts.length === 2 && parts[0] === "watch" ? parts[1] : "";
      if (/^(?:sm|so|nm)[1-9][0-9]*$/.test(id)) return ref("niconico");
    }
  }
  return VIDEO_EXTS.has(goLower(pathExt(u.path))) && u.host !== "" ? ref("file") : null;
}

/** The most characters a block's notes hold (doc.MaxNotes). */
export const MAX_NOTES = 200000;

/** File block previous with the notes a replace_block's spec gives it, its file's own fields as they are (doc.FileWithNotes). */
export function fileWithNotes(previous: NodeJSON, spec: BlockSpec): NodeJSON {
  const part = spec.file ?? (spec as { notes?: string });
  const notes = normalizeNotes(str(part.notes));
  if ([...notes].length > MAX_NOTES) throw new SpecError(`A file's notes take at most ${MAX_NOTES} characters`);
  const { notes: _, ...data } = previous.data ?? {};
  return { ...structuredClone(previous), data: { ...structuredClone(data), ...(notes ? { notes } : {}) } };
}

/** A video's notes as a block keeps them (doc.NormalizeNotes): a line break for each of any kind, and no space around them. */
export function normalizeNotes(s: string): string {
  return trimSpace(s.replaceAll("\r\n", "\n").replaceAll("\r", "\n"));
}

/**
 * A code block's language as the ID it keeps (doc.CodeLanguageID): its
 * spaces dashes, as a fence, which ends a language at a space, can write it.
 */
export function codeLanguage(name: unknown): string {
  const id = goLower(fields(String(name ?? "")).join("-"));
  return Object.hasOwn(LANGUAGE_ALIASES, id) ? LANGUAGE_ALIASES[id] : id;
}

function textNode(id: string, type: string, text: string, extra: Record<string, unknown> = {}): NodeJSON {
  return { id, type, data: { ...extra, delta: markdownToDelta(text ?? "") } };
}

/** A JSON number as an integer within min and max, as doc.AsInt reads one: cut to its whole part. */
function clampInt(value: unknown, min: number, max: number, fallback: number): number {
  const n = typeof value === "number" ? Math.trunc(value) : Number.NaN;
  return Number.isFinite(n) ? Math.min(max, Math.max(min, n)) : fallback;
}

/**
 * A JSON value as text (doc.JSONText): a string as it is, null as nothing,
 * a number or a boolean as it reads. A spec's cells and labels are read
 * so, and a transcript's speakers.
 */
export function jsonText(value: unknown): string {
  // A list or an object is no text, as on the client.
  return value == null || typeof value === "object" ? "" : String(value);
}

/** A spec's string, or nothing for a value of another kind, as doc.SpecToNode reads one. */
function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/**
 * A chart's value (doc.chartValue): a JSON number, or a string of a
 * decimal one. What is not one, NaN and infinity among them, is no value: 0.
 */
function specNumber(value: unknown): number {
  const s = typeof value === "string" ? trimSpace(value) : "";
  const n = typeof value === "number"
    ? value
    : /^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$/.test(s)
    ? Number(s)
    : Number.NaN;
  return Number.isFinite(n) ? n : 0;
}

/** A spec's own object for a block, or the spec itself when it has none, as doc.SpecToNode reads image, video and link. */
function specPart(spec: BlockSpec, key: "image" | "video" | "link"): Record<string, unknown> {
  const part = spec[key];
  return part && typeof part === "object" && !Array.isArray(part)
    ? part as Record<string, unknown>
    : spec as unknown as Record<string, unknown>;
}

function validateChart(chart: unknown): ChartSpec {
  if (!chart || typeof chart !== "object") {
    throw new SpecError("A chart block needs a `chart` object");
  }
  const c = chart as Partial<ChartSpec>;
  // No type, or an empty one, is a bar chart, as doc.ChartData has it.
  const chartType = typeof c.chart_type === "string" && c.chart_type ? c.chart_type : "bar";
  if (!["bar", "line", "pie"].includes(chartType)) {
    throw new SpecError("chart.chart_type must be bar, line or pie");
  }
  if (!Array.isArray(c.labels) || c.labels.length === 0) {
    throw new SpecError("chart.labels must be a non-empty array of strings");
  }
  if (!Array.isArray(c.series) || c.series.length === 0) {
    throw new SpecError("chart.series must be a non-empty array");
  }
  const labels = c.labels.map(jsonText);
  const series = c.series.map((s, i) => {
    const values = Array.isArray(s?.values) ? s.values.map(specNumber) : [];
    if (values.length !== labels.length) {
      throw new SpecError(
        `chart.series[${i}].values must hold ${labels.length} numbers, one per label`,
      );
    }
    return { name: s?.name == null ? `Series ${i + 1}` : jsonText(s.name), values };
  });
  if (chartType === "pie" && series.length > 1) {
    throw new SpecError("A pie chart takes exactly one series");
  }
  return {
    chart_type: chartType,
    title: str(c.title),
    labels,
    series,
    caption: str(c.caption),
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
  /**
   * Specs read from Markdown (markdownSpecs): a code block's text is its
   * fence's lines, a blank one at the end too, as the app keeps them.
   */
  markdown?: boolean;
}

/** How deep a spec's blocks may nest, and how many one call may make. */
export const MAX_SPEC_DEPTH = 16;
export const MAX_SPEC_NODES = 2000;

/**
 * Whether a line is a Markdown horizontal rule (doc.IsRule): three or more
 * of the same one of -, * or _, with spaces or tabs between them and around
 * them, as CommonMark has it.
 */
export function isRule(line: string): boolean {
  return /^[ \t]*([-*_])(?:[ \t]*\1){2,}[ \t]*$/.test(line);
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
  // No type, or an empty one, is a paragraph, as doc.SpecToNode has it.
  let type = typeof spec.type === "string" && spec.type ? spec.type : "paragraph";
  // A Markdown rule written as text is the divider it stands for.
  const text = str(spec.text);
  if (type === "paragraph" && isRule(text)) type = "divider";
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
      node = textNode(id, type, text, { level: clampInt(spec.level, 1, 6, 2) });
      break;
    case "todo_list":
      node = textNode(id, type, text, {
        checked: spec.checked === true,
        ...(validDue(spec.due) ? { due: spec.due } : {}),
      });
      break;
    case "paragraph":
    case "bulleted_list":
    case "numbered_list":
    case "quote":
      node = textNode(id, type, text);
      break;
    case "callout":
      node = textNode(id, type, text, { tone: calloutTone(spec.tone) });
      break;
    case "toggle_list":
      node = textNode(id, type, text, { collapsed: spec.collapsed === true });
      break;
    case "video": {
      const video = specPart(spec, "video");
      const url = trimSpace(str(video.url));
      if (!isVideoURL(url)) {
        throw new SpecError(
          "A video block needs `video.url`, a link to a YouTube, Vimeo, Bilibili, Dailymotion or Niconico video, or to a video file",
        );
      }
      const notes = normalizeNotes(str(video.notes));
      if ([...notes].length > MAX_NOTES) throw new SpecError(`A video's notes take at most ${MAX_NOTES} characters`);
      node = { id, type, data: { url, title: trimSpace(str(video.title)), ...(notes ? { notes } : {}) } };
      break;
    }
    case "link_preview": {
      const url = trimSpace(str(specPart(spec, "link").url));
      if (!agentURL(url)) {
        throw new SpecError("A link_preview block needs `link.url`, an http(s) link");
      }
      node = { id, type, data: { url } };
      break;
    }
    case "math_equation": {
      const formula = trimSpace(str(spec.formula));
      if (!formula) throw new SpecError("A math_equation block needs `formula`, in TeX");
      node = { id, type, data: { formula } };
      break;
    }
    case "diagram": {
      const source = str(spec.source).replace(/^\n+|\n+$/g, "");
      if (!trimSpace(source)) throw new SpecError("A diagram block needs `source`, in Mermaid");
      node = { id, type, data: { source } };
      break;
    }
    case "code": {
      // The code as it is, not markdown; data.delta is its plain text. An
      // agent's code may end in a line break, which is no line of it.
      const code = opts.markdown ? text : text.replace(/\n$/, "");
      node = {
        id,
        type,
        data: { delta: code ? [{ insert: code }] : [], language: codeLanguage(str(spec.language)) },
      };
      break;
    }
    case "divider":
      node = { id, type };
      break;
    case "image": {
      const image = specPart(spec, "image");
      // The app loads it by itself.
      const url = typeof image.url === "string" ? trimSpace(image.url) : "";
      if (!agentURL(url)) throw new SpecError("image.url must be an http(s) URL");
      const alt = typeof image.alt === "string" ? image.alt : "";
      node = { id, type, data: { url, align: "center", alt } };
      break;
    }
    case "table": {
      // Each cell is a block, and counts as one, as in doc.SpecToNode.
      const rows = Array.isArray(spec.rows) ? spec.rows as unknown[] : [];
      const cols = rows.reduce((n: number, r) => Math.max(n, Array.isArray(r) ? r.length : 0), 0);
      made.count += rows.length * cols;
      if (made.count > MAX_SPEC_NODES) throw new SpecError(`At most ${MAX_SPEC_NODES} blocks in all per call`);
      node = tableNode(id, spec.rows, opts.newId);
      break;
    }
    case "chart":
      node = {
        id,
        type,
        data: { ...validateChart(spec.chart), author: opts.author ?? "" },
      };
      break;
    case "embed": {
      const html = spec.embed?.html;
      if (typeof html !== "string" || !trimSpace(html)) {
        throw new SpecError("An embed block needs `embed.html`");
      }
      // In bytes, as the client counts them: UTF-16 units would let CJK
      // through at three times the size.
      if (new TextEncoder().encode(html).length > 200_000) throw new SpecError("embed.html must be under 200 KB");
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
  const colsLen = rows.reduce((n: number, r) => Math.max(n, (r as unknown[]).length), 0);
  if (colsLen === 0) throw new SpecError("Table rows must have at least one cell");
  const children: NodeJSON[] = [];
  for (let col = 0; col < colsLen; col++) {
    for (let row = 0; row < rowsLen; row++) {
      children.push({
        id: newId(),
        type: "table/cell",
        data: { colPosition: col, rowPosition: row },
        children: [textNode(newId(), "paragraph", jsonText((rows[row] as unknown[])[col]))],
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

/**
 * A JSON number as an integer, as doc.AsInt reads one; def for anything
 * else, and for one beyond the integers a number holds exactly (±2^53).
 */
function asInt(v: unknown, def: number): number {
  return typeof v === "number" && Number.isFinite(v) && Math.abs(v) <= 2 ** 53 ? Math.trunc(v) : def;
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

/** A table's cells as Markdown; of two at one place, the first (doc.TableCells). */
export function tableRows(node: NodeJSON): string[][] {
  const { rows: rowsLen, cols: colsLen } = tableSize(node);
  const rows: string[][] = Array.from({ length: rowsLen }, () => Array(colsLen).fill(""));
  const taken = new Set<number>();
  for (const cell of node.children ?? []) {
    const col = asInt(cell.data?.colPosition, -1);
    const row = asInt(cell.data?.rowPosition, -1);
    if (row >= 0 && row < rowsLen && col >= 0 && col < colsLen && !taken.has(row * colsLen + col)) {
      taken.add(row * colsLen + col);
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

/** How much of a recording's transcript an agent reads. */
const MAX_TRANSCRIPT = 40_000;

/**
 * A transcript as an agent reads it (agents.TranscriptText): a line of
 * "[m:ss] text" each, a line of only punctuation joined to the one before.
 */
export function transcriptText(segments: RecordingInfo["transcript"]): string {
  const lines: string[] = [];
  for (const s of segments ?? []) {
    const text = trimSpace(String(s?.text ?? ""));
    if (!text) continue;
    if (/^\p{P}+$/u.test(text) && lines.length) {
      lines[lines.length - 1] += text;
      continue;
    }
    const at = Math.trunc((s.start_ms ?? 0) / 1000);
    let line = `[${Math.trunc(at / 60)}:${String(at % 60).padStart(2, "0")}] `;
    const speaker = s.speaker == null ? "" : String(s.speaker);
    if (speaker) line += `Speaker ${speaker}: `;
    lines.push(line + text);
  }
  const out = [...lines.join("\n")];
  return out.length > MAX_TRANSCRIPT
    ? out.slice(0, MAX_TRANSCRIPT).join("") + "\n[The rest of the transcript is cut off.]"
    : out.join("");
}

export function nodeView(
  node: NodeJSON,
  depth: number,
  parentId: string | undefined,
  recordings: Map<string, RecordingInfo>,
): BlockView {
  const view: BlockView = { id: node.id, type: node.type, depth };
  if (parentId) view.parent_id = parentId;
  const data = node.data ?? {};
  if (TEXT_TYPES.has(node.type)) view.text = deltaToMarkdown(deltaOf(node));
  switch (node.type) {
    case "code":
      view.text = deltaToPlain(deltaOf(node));
      view.language = typeof data.language === "string" ? data.language : "";
      break;
    case "callout":
      view.tone = calloutTone(data.tone);
      break;
    case "toggle_list":
      view.collapsed = data.collapsed === true;
      break;
    case "heading":
      // Within 1 and 6, however far past them it is stored, as on the client.
      view.level = clampInt(data.level, 1, 6, 1);
      break;
    case "todo_list":
      view.checked = data.checked === true;
      if (typeof data.due === "string" && data.due) view.due = data.due;
      break;
    case "table":
      view.rows = tableRows(node);
      break;
    case "chart":
      view.chart = {
        chart_type: data.chart_type,
        title: data.title,
        labels: data.labels,
        series: data.series,
        caption: data.caption,
      };
      break;
    case "embed":
      view.embed = { title: data.title, height: data.height, html: data.html };
      break;
    case "image":
      view.image = { url: data.url, alt: data.alt };
      break;
    case "video":
      view.video = { url: data.url, title: data.title, ...("notes" in data ? { notes: data.notes } : {}) };
      break;
    case "math_equation":
      view.formula = typeof data.formula === "string" ? data.formula : "";
      break;
    case "diagram":
      view.source = typeof data.source === "string" ? data.source : "";
      break;
    case "link_preview":
      view.link = { url: data.url, title: data.title, description: data.description };
      break;
    case "file":
      view.file = {
        name: data.name,
        mime: data.mime,
        size: data.size,
        ...("notes" in data ? { notes: data.notes } : {}),
      };
      break;
    case "recording": {
      // What the block has, as doc.BlockViews gives it, and what the
      // server knows of the recording besides: how long it is, whether its
      // transcript is done, and the transcript, which the client adds too.
      const rid = String(data.recording_id ?? "");
      const info = recordings.get(rid);
      const transcript = info ? transcriptText(info.transcript) : "";
      view.recording = {
        recording_id: data.recording_id,
        title: data.title,
        // The block's own, null too, as on the client; else the server's.
        duration_ms: "duration_ms" in data ? data.duration_ms : info?.duration_ms,
        ...(info ? { status: info.status } : {}),
        // As the client adds it: only when there is one.
        ...(transcript ? { transcript } : {}),
      };
      break;
    }
    case "subpage":
      view.page_id = data.page_id;
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

/** A block's own text without formats: for a block with none, the title or source it shows (doc.blockText). */
function blockText(node: NodeJSON): string {
  const data = node.data ?? {};
  const str = (key: string) => typeof data[key] === "string" ? data[key] as string : "";
  switch (node.type) {
    case "chart":
    case "embed":
      return str("title");
    case "video": {
      // Its notes too, for search to find what is said in it.
      const notes = str("notes");
      return notes ? trimSpace(str("title") + "\n" + notes) : str("title");
    }
    case "link_preview":
    case "file": {
      // Its title or name, else its link (doc.cardTitle), and a file's
      // notes, for what is said in a recording it holds.
      const title = ["title", "name", "url"].map((k) => trimSpace(str(k))).find(Boolean) ?? "";
      const notes = node.type === "file" ? str("notes") : "";
      return notes ? trimSpace(title + "\n" + notes) : title;
    }
    case "math_equation":
      return str("formula");
    case "diagram":
      return str("source");
  }
  return deltaToPlain(deltaOf(node));
}

/** The page's text, a line per block that has some, for search (doc.Content.PlainText). */
export function plainText(content: PageContent): string {
  const lines: string[] = [];
  walk(content.document, (node) => {
    if (node === content.document) return;
    const text = blockText(node);
    if (trimSpace(text) !== "") lines.push(text);
  });
  return lines.join("\n");
}
