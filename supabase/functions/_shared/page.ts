// Reading a web page for an agent: its text, with Markdown's headings, list
// items and links, the links made whole. Mirrors internal/web/read.go; both
// read the pages of testdata/pages.json alike.

import { type DefaultTreeAdapterMap, Parser } from "npm:parse5@8.0.1";
import { readBytes } from "./openai.ts";
import { publicFetch, type Resolver, UnsafeUrlError } from "./net.ts";

export interface WebPage {
  /** Where the page was read, after the redirects on the way. */
  url: string;
  title: string;
  text: string;
  /** The web links in text, in the order they come. */
  links: string[];
  /** A page whose text went on past MAX_PAGE_TEXT. */
  truncated: boolean;
}

/** The most of a page's text an agent reads, in characters (Go's MaxPageText). */
export const MAX_PAGE_TEXT = 30_000;

/** The most of a page read from the web, as on the device (Go's maxPageBytes). */
export const MAX_PAGE_BYTES = 2 << 20;

/**
 * The most elements open at once in a page that is read, as Go's parser
 * has it: a page nested deeper reads as nothing, as it does there.
 */
const MAX_OPEN_ELEMENTS = 512;

/** The most redirects followed to a page. */
const MAX_REDIRECTS = 5;

const HTML_NS = "http://www.w3.org/1999/xhtml";

/** The elements whose content is no text to read (Go's skipped). */
const SKIPPED = new Set([
  "head",
  "script",
  "style",
  "noscript",
  "template",
  "iframe",
  "object",
  "embed",
  "canvas",
  "video",
  "audio",
  "picture",
  "map",
  "select",
  "button",
  "textarea",
  "input",
  "dialog",
]);

/** The elements that start a line of their own (Go's blocks). */
const BLOCKS = new Set([
  "address",
  "article",
  "aside",
  "blockquote",
  "caption",
  "center",
  "dd",
  "details",
  "div",
  "dl",
  "dt",
  "fieldset",
  "figcaption",
  "figure",
  "footer",
  "form",
  "h1",
  "h2",
  "h3",
  "h4",
  "h5",
  "h6",
  "header",
  "hgroup",
  "li",
  "main",
  "nav",
  "ol",
  "p",
  "pre",
  "section",
  "summary",
  "table",
  "tbody",
  "tfoot",
  "thead",
  "tr",
  "ul",
]);

/** HTML's spaces and the no-break space, which run together into one. */
const SPACES = /[\t\n\f\r \u00a0]+/g;

interface Node {
  nodeName: string;
  namespaceURI?: string;
  value?: string;
  attrs?: { name: string; value: string; namespace?: string }[];
  childNodes?: Node[];
}

const isElement = (n: Node, name?: string) =>
  n.namespaceURI !== undefined && !n.nodeName.startsWith("#") && (name === undefined || n.nodeName === name);

/** The first HTML element named name in n, n itself or one inside it. */
function find(n: Node, name: string): Node | undefined {
  if (isElement(n, name) && n.namespaceURI === HTML_NS) return n;
  for (const c of n.childNodes ?? []) {
    const f = find(c, name);
    if (f) return f;
  }
  return undefined;
}

function attr(n: Node, name: string): string | undefined {
  return n.attrs?.find((a) => !a.namespace && a.name === name)?.value;
}

/** Whether a UTF-16 code is a space as Go's unicode.IsSpace has it. */
function isSpace(c: number): boolean {
  return c === 0x20 || (c >= 0x09 && c <= 0x0d) || c === 0x85 || c === 0xa0 || c === 0x1680 ||
    (c >= 0x2000 && c <= 0x200a) || c === 0x2028 || c === 0x2029 || c === 0x202f || c === 0x205f ||
    c === 0x3000;
}

/** s without the spaces it starts with (Go's strings.TrimLeftFunc with unicode.IsSpace). */
function trimLeft(s: string): string {
  let i = 0;
  while (i < s.length && isSpace(s.charCodeAt(i))) i++;
  return s.slice(i);
}

/** s without the spaces it ends with (Go's strings.TrimRightFunc with unicode.IsSpace). */
function trimRight(s: string): string {
  let i = s.length;
  while (i > 0 && isSpace(s.charCodeAt(i - 1))) i--;
  return s.slice(0, i);
}

/** Trims Go's unicode.IsSpace, as strings.TrimSpace does. */
function trimSpace(s: string): string {
  return trimRight(trimLeft(s));
}

/** s without the characters of cut it ends with (Go's strings.TrimRight). */
function trimRightOf(s: string, cut: string): string {
  let i = s.length;
  while (i > 0 && cut.includes(s[i - 1])) i--;
  return s.slice(0, i);
}

/** The characters in s, as Go counts a string's runes. */
function runes(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++, n++) {
    const c = s.charCodeAt(i);
    if (c >= 0xd800 && c < 0xdc00 && i + 1 < s.length) {
      const d = s.charCodeAt(i + 1);
      if (d >= 0xdc00 && d < 0xe000) i++;
    }
  }
  return n;
}

/** The characters in s that are not spaces. */
function marksIn(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (isSpace(c)) continue;
    n++;
    if (c >= 0xd800 && c < 0xdc00 && i + 1 < s.length) {
      const d = s.charCodeAt(i + 1);
      if (d >= 0xdc00 && d < 0xe000) i++;
    }
  }
  return n;
}

/** Whether s has a character that is not a space. */
function hasMark(s: string): boolean {
  for (let i = 0; i < s.length; i++) if (!isSpace(s.charCodeAt(i))) return true;
  return false;
}

/**
 * u as a page's text gives it: without its fragment, and its parentheses
 * escaped, which would end a Markdown link (Go's linkString).
 */
function linkString(u: URL): string {
  const c = new URL(u.href);
  c.hash = "";
  return c.href.replace(/\(/g, "%28").replace(/\)/g, "%29");
}

/** A link being read: its piece in the line, and the line's marks before it (Go's openLink). */
interface OpenLink {
  at: number;
  marks: number;
}

/**
 * Writes a page's text, as Go's pageReader: each piece of the page read
 * once, so a page takes as long as it is long, and no more once its text
 * has gone on past MAX_PAGE_TEXT for good.
 */
class PageReader {
  out: string[] = [];
  /** The characters in out, and those up to its last that is not a line break. */
  outRunes = 0;
  outText = 0;
  /** The line, piece by piece: a link holds a piece of its own before its text, for its [ once it ends in the line. */
  parts: string[] = [];
  /** The links being read, outermost first; the first decided of them ended a line on the way. */
  links: OpenLink[] = [];
  decided = 0;
  /** The line's characters that are not spaces; whether it ends in a space, or has nothing in it. */
  marks = 0;
  space = false;
  empty = true;
  prefix = "";
  pre = 0;
  /** hasText's answers, each element asked once. */
  read = new WeakMap<Node, boolean>();
  done = false;

  constructor(public base: URL, readonly self: string) {}

  walk(n: Node): void {
    if (this.done) return;
    if (n.nodeName === "#document") return this.children(n);
    if (n.nodeName === "#text") return this.write(n.value ?? "");
    if (!isElement(n)) return;
    // An SVG or MathML element is a picture or a formula.
    const name = n.nodeName;
    if (n.namespaceURI !== HTML_NS || SKIPPED.has(name)) return;
    switch (name) {
      case "br":
        return this.flush();
      case "hr":
        this.flush();
        this.writeOut("---\n");
        return;
      case "img":
        return;
    }
    // An element with no text, as a link around a picture or an empty
    // box, breaks no line and says nothing.
    if (!this.hasText(n)) return;
    if ((name === "td" || name === "th") && this.marks > 0) this.write(" | ");
    const block = BLOCKS.has(name);
    if (block) {
      this.flush();
      if (/^h[1-6]$/.test(name)) this.prefix = `${"#".repeat(Number(name[1]))} `;
      else if (name === "li") this.prefix = "- ";
      else if (name === "pre") this.pre++;
    }
    const link = this.link(n);
    if (link) this.anchor(n, link);
    else this.children(n);
    if (block) {
      this.flush();
      this.prefix = "";
      if (name === "pre") this.pre--;
    }
  }

  /** Whether n has text to read: a text node of more than spaces, or an element read with one in it (Go's hasText). */
  hasText(n: Node): boolean {
    if (n.nodeName === "#text") return hasMark(n.value ?? "");
    if (isElement(n) && (n.namespaceURI !== HTML_NS || SKIPPED.has(n.nodeName))) return false;
    const children = n.childNodes;
    if (!children?.length) return false;
    let has = this.read.get(n);
    if (has === undefined) {
      has = children.some((c) => this.hasText(c));
      this.read.set(n, has);
    }
    return has;
  }

  children(n: Node): void {
    for (const c of n.childNodes ?? []) {
      if (this.done) return;
      this.walk(c);
    }
  }

  /** A link's text as [text](link) (Go's anchor). */
  anchor(n: Node, link: string): void {
    const lines = this.outRunes;
    this.links.push({ at: this.parts.length, marks: this.marks });
    this.parts.push("");
    this.children(n);
    const l = this.links.pop()!;
    this.decided = Math.min(this.decided, this.links.length);
    if (this.outRunes !== lines) {
      this.flush();
      this.writeOut(`<${link}>\n`);
      return;
    }
    // The text goes in the brackets without the spaces it starts and ends
    // with, a space of which stays outside them.
    const parts = this.parts;
    let lead = "", trail = "";
    for (let i = l.at + 1; i < parts.length; i++) {
      if (parts[i] !== "") {
        if (parts[i][0] === " ") lead = " ";
        break;
      }
    }
    for (let i = parts.length - 1; i > l.at; i--) {
      if (parts[i] !== "") {
        if (parts[i].endsWith(" ")) trail = " ";
        break;
      }
    }
    for (let i = l.at + 1; i < parts.length; i++) {
      if ((parts[i] = trimLeft(parts[i])) !== "") break;
    }
    for (let i = parts.length - 1; i > l.at; i--) {
      if ((parts[i] = trimRight(parts[i])) !== "") break;
    }
    parts[l.at] = `${lead}[`;
    parts.push(`](${link})${trail}`);
    this.marks += 4 + runes(link);
    this.empty = false;
    this.space = trail !== "";
    this.check();
  }

  /** Where an <a> goes, or "" when nowhere to show (Go's link). */
  link(n: Node): string {
    if (n.nodeName !== "a") return "";
    const href = attr(n, "href");
    if (href === undefined) return "";
    let u: URL;
    try {
      u = new URL(trimSpace(href), this.base);
    } catch {
      return "";
    }
    if ((u.protocol !== "http:" && u.protocol !== "https:") || !u.host) return "";
    const s = linkString(u);
    return s !== this.self ? s : "";
  }

  /** Adds text to the line, its spaces run together, but in a <pre> (Go's write). */
  write(s: string): void {
    if (this.pre > 0) {
      for (let from = 0; !this.done;) {
        const nl = s.indexOf("\n", from);
        this.add(nl < 0 ? s.slice(from) : s.slice(from, nl));
        if (nl < 0) return;
        const line = `${this.prefix}${trimRightOf(this.parts.join(""), " \t")}\n`;
        this.prefix = "";
        this.newLine(true);
        this.writeOut(line);
        from = nl + 1;
      }
      return;
    }
    s = s.replace(SPACES, " ");
    if (this.empty || this.space) s = s.replace(/^ +/, "");
    this.add(s);
  }

  /** Puts s at the end of the line (Go's add). */
  add(s: string): void {
    if (s === "") return;
    this.parts.push(s);
    this.empty = false;
    this.space = s.endsWith(" ");
    this.marks += marksIn(s);
    this.check();
  }

  /** Ends the line, when it has text, after its prefix (Go's flush). */
  flush(): void {
    if (this.marks === 0) return this.newLine(false);
    const joined = this.parts.join("");
    const line = `${this.prefix}${this.pre > 0 ? trimRightOf(joined, " \t") : trimSpace(joined)}\n`;
    this.prefix = "";
    this.newLine(true);
    this.writeOut(line);
  }

  /** Starts the next line, after one written or one with no text (Go's newLine). */
  newLine(wrote: boolean): void {
    this.parts = [];
    if (wrote) this.decided = this.links.length;
    for (let i = this.decided; i < this.links.length; i++) {
      this.links[i] = { at: this.parts.length, marks: 0 };
      this.parts.push("");
    }
    this.marks = 0;
    this.space = false;
    this.empty = true;
  }

  /** Adds s to the finished lines, the line it ends begun anew (Go's writeOut). */
  writeOut(s: string): void {
    this.out.push(s);
    const t = trimRightOf(s, "\n");
    if (t !== "") this.outText = this.outRunes + runes(t);
    this.outRunes += runes(s);
    this.check();
  }

  /** Stops the reading once the text has gone on past MAX_PAGE_TEXT whatever comes next (Go's check). */
  check(): void {
    const kept = this.decided < this.links.length ? this.links[this.decided].marks : this.marks;
    if (this.outText > MAX_PAGE_TEXT || kept > 0 && this.outRunes + runes(this.prefix) + kept > MAX_PAGE_TEXT) {
      this.done = true;
    }
  }
}

/** A page nested deeper than MAX_OPEN_ELEMENTS. */
class TooDeep extends Error {}

/**
 * How long a page may take to parse, in ms. Each tag of a page whose
 * elements are nested deep takes a while, as the parser looks through
 * them: a long page takes a fraction of this, and one made to take longer
 * is not read, as the function has its seconds of CPU for all its run.
 */
const MAX_PARSE_MS = 1000;

/** How much of a page is parsed at a time, between looks at the time. */
const PARSE_CHUNK = 1 << 16;

/**
 * Parses a page, or undefined for one nested deeper than
 * MAX_OPEN_ELEMENTS, which Go's parser refuses at the same element.
 */
function parseHtml(src: string): Node | undefined {
  const parser = new Parser<DefaultTreeAdapterMap>();
  const stack = parser.openElements;
  const push = stack.push.bind(stack);
  stack.push = (element, tagID) => {
    push(element, tagID);
    if (stack.stackTop >= MAX_OPEN_ELEMENTS) throw new TooDeep();
  };
  const start = performance.now();
  try {
    let at = 0;
    do {
      let end = Math.min(at + PARSE_CHUNK, src.length);
      // A piece ends between characters, not between a character's halves.
      const c = src.charCodeAt(end - 1);
      if (end < src.length && c >= 0xd800 && c < 0xdc00) end++;
      parser.tokenizer.write(src.slice(at, end), end === src.length);
      if (end < src.length && performance.now() - start > MAX_PARSE_MS) {
        throw new Error("The page takes too long to read");
      }
      at = end;
    } while (at < src.length);
  } catch (e) {
    if (e instanceof TooDeep) return undefined;
    throw e;
  }
  return parser.document as unknown as Node;
}

/** Reads a page's HTML, page being where it was read (Go's ReadHTML). */
export function readHtml(page: string, src: string): WebPage {
  const result: WebPage = { url: page, title: "", text: "", links: [], truncated: false };
  let base: URL;
  try {
    base = new URL(page);
  } catch {
    return result;
  }
  const root = parseHtml(src);
  if (!root) return result;
  const reader = new PageReader(base, linkString(base));
  const head = find(root, "head");
  if (head) {
    const title = find(head, "title");
    if (title) {
      const text = (title.childNodes ?? []).filter((c) => c.nodeName === "#text").map((c) => c.value).join("");
      result.title = trimSpace(text.replace(SPACES, " "));
    }
    const b = find(head, "base");
    const href = b && attr(b, "href");
    if (href !== undefined) {
      try {
        reader.base = new URL(trimSpace(href), base);
      } catch {
        // A base that is no URL is none.
      }
    }
  }
  reader.walk(root);
  reader.flush();
  [result.text, result.truncated] = truncate(trimRightOf(reader.out.join(""), "\n"));
  result.links = urlsOf(result.text);
  return result;
}

/** Cuts text to MAX_PAGE_TEXT characters, at a line's end in the last quarter (Go's truncate). */
export function truncate(text: string): [string, boolean] {
  // Where the characters past MAX_PAGE_TEXT start, if there are any.
  let end = 0;
  for (let n = 0; n < MAX_PAGE_TEXT && end < text.length; n++) {
    const c = text.charCodeAt(end++);
    if (c >= 0xd800 && c < 0xdc00 && end < text.length) {
      const d = text.charCodeAt(end);
      if (d >= 0xdc00 && d < 0xe000) end++;
    }
  }
  if (end >= text.length) return [text, false];
  let cut = text.slice(0, end);
  const at = cut.lastIndexOf("\n");
  if (at >= 0 && runes(cut.slice(0, at)) >= MAX_PAGE_TEXT * 3 / 4) cut = cut.slice(0, at);
  return [trimRightOf(cut, " \t\n"), true];
}

const MARKDOWN_URL = /\]\((https?:\/\/[^()\t\n\f\r ]+)\)|<(https?:\/\/[^<>\t\n\f\r ]+)>/g;
const BARE_URL = /https?:\/\/[^\t\n\f\r <>"'()\[\]]+/g;

/** The web links in text, once each: those it links, in order, then those written out (Go's urlsOf). */
export function urlsOf(text: string): string[] {
  const out = new Set<string>();
  for (const m of text.matchAll(MARKDOWN_URL)) out.add(m[1] ?? m[2]);
  for (const m of text.matchAll(BARE_URL)) out.add(m[0].replace(/[.,;:!?]+$/, ""));
  return [...out];
}

/** The encoding a page's bytes are in: by BOM, Content-Type, its own <meta>, or UTF-8 if valid (Go's charset.DetermineEncoding). */
function encodingOf(bytes: Uint8Array, contentType: string): string {
  if (bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) return "utf-8";
  if (bytes[0] === 0xfe && bytes[1] === 0xff) return "utf-16be";
  if (bytes[0] === 0xff && bytes[1] === 0xfe) return "utf-16le";
  const known = (label: string | undefined) => {
    if (!label) return undefined;
    try {
      return new TextDecoder(label.trim()).encoding;
    } catch {
      return undefined;
    }
  };
  const declared = known(/charset\s*=\s*"?([^";\s]+)/i.exec(contentType)?.[1]);
  if (declared) return declared;
  const head = new TextDecoder("windows-1252").decode(bytes.subarray(0, 1024));
  const meta = known(/<meta[^>]+charset\s*=\s*["']?([^"'>\s/;]+)/i.exec(head)?.[1]);
  if (meta) return meta;
  try {
    new TextDecoder("utf-8", { fatal: true }).decode(bytes.subarray(0, 1024));
    return "utf-8";
  } catch {
    return "windows-1252";
  }
}

/** Whether a media type is text an agent reads. */
function isText(type: string): boolean {
  return type.startsWith("text/") || type === "application/json" || type.endsWith("+json") ||
    type === "application/xml" || type.endsWith("+xml");
}

/**
 * Reads a page for an agent, from the public web only (safeFetch), its
 * redirects followed as far as MAX_REDIRECTS, each checked again. An
 * http:// page is asked for over https, which the server's own network has
 * no certificate for (net.ts safeFetch).
 */
export async function readPage(raw: string, signal?: AbortSignal, resolve?: Resolver): Promise<WebPage> {
  let url = raw.trim().replace(/^http:\/\//i, "https://");
  let res: Response | undefined;
  for (let hop = 0; hop <= MAX_REDIRECTS && !res; hop++) {
    const answer = await publicFetch(url, {
      headers: {
        "User-Agent":
          "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Leaflet",
        Accept: "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5",
      },
      signal,
    }, resolve);
    const next = answer.status >= 300 && answer.status < 400 ? answer.headers.get("location") : null;
    if (!next) {
      res = answer;
      break;
    }
    await answer.body?.cancel().catch(() => {});
    url = new URL(next, url).href.replace(/^http:\/\//i, "https://");
  }
  if (!res) throw new UnsafeUrlError("The page redirected too many times");
  if (res.status >= 400 || res.type === "opaqueredirect") {
    await res.body?.cancel().catch(() => {});
    throw new Error(`The page answered ${res.status}`);
  }
  const contentType = res.headers.get("content-type") ?? "";
  const type = contentType.split(";")[0].trim().toLowerCase() || "text/html";
  if (!isText(type) && type !== "application/xhtml+xml") {
    await res.body?.cancel().catch(() => {});
    throw new Error("That is not a page of text");
  }
  const bytes = await readBytes(res, MAX_PAGE_BYTES, true);
  const src = new TextDecoder(encodingOf(bytes, contentType)).decode(bytes);
  if (type === "text/html" || type === "application/xhtml+xml") return readHtml(url, src);
  const [text, truncated] = truncate(trimSpace(src));
  return { url, title: "", text, links: urlsOf(text), truncated };
}
