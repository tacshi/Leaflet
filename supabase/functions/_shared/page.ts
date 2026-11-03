// Reading a web page for an agent: its text, with Markdown's headings, list
// items and links, the links made whole. Mirrors internal/web/read.go; both
// read the pages of testdata/pages.json alike.

import { parse } from "npm:parse5@8.0.1";
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

/** The most of a page read from the web. */
const MAX_PAGE_BYTES = 5 << 20;

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
const SPACES = /[\t\n\f\r  ]+/g;

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

/** Trims Go's unicode.IsSpace, as strings.TrimSpace does. */
function trimSpace(s: string): string {
  return s.replace(/^[\s\u0085]+|[\s\u0085]+$/gu, "");
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

/** Whether n has text to read: a text node of more than spaces, or an element read with one in it (Go's hasText). */
function hasText(n: Node): boolean {
  if (n.nodeName === "#text") return trimSpace((n.value ?? "").replace(SPACES, " ")) !== "";
  if (isElement(n) && (n.namespaceURI !== HTML_NS || SKIPPED.has(n.nodeName))) return false;
  return (n.childNodes ?? []).some(hasText);
}

class PageReader {
  out = "";
  line = "";
  prefix = "";
  pre = 0;

  constructor(public base: URL, readonly self: string) {}

  walk(n: Node): void {
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
        this.out += "---\n";
        return;
      case "img":
        return;
    }
    // An element with no text, as a link around a picture or an empty
    // box, breaks no line and says nothing.
    if (!hasText(n)) return;
    if ((name === "td" || name === "th") && trimSpace(this.line) !== "") this.write(" | ");
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

  children(n: Node): void {
    for (const c of n.childNodes ?? []) this.walk(c);
  }

  /** A link's text as [text](link) (Go's anchor). */
  anchor(n: Node, link: string): void {
    const lines = this.out.length;
    const at = this.line.length;
    this.children(n);
    if (this.out.length !== lines) {
      this.flush();
      this.out += `<${link}>\n`;
      return;
    }
    const text = this.line.slice(at);
    const inner = trimSpace(text);
    const lead = text.startsWith(" ") ? " " : "";
    const trail = text.endsWith(" ") ? " " : "";
    this.line = `${this.line.slice(0, at)}${lead}[${inner}](${link})${trail}`;
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

  write(s: string): void {
    if (this.pre > 0) {
      s.split("\n").forEach((l, i) => {
        if (i > 0) {
          this.out += `${this.prefix}${this.line.replace(/[ \t]+$/, "")}\n`;
          this.line = "";
          this.prefix = "";
        }
        this.line += l;
      });
      return;
    }
    s = s.replace(SPACES, " ");
    if (this.line === "" || this.line.endsWith(" ")) s = s.replace(/^ +/, "");
    this.line += s;
  }

  flush(): void {
    const line = this.pre > 0 ? this.line.replace(/[ \t]+$/, "") : trimSpace(this.line);
    this.line = "";
    if (trimSpace(line) !== "") {
      this.out += `${this.prefix}${line}\n`;
      this.prefix = "";
    }
  }
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
  const root = parse(src) as unknown as Node;
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
  [result.text, result.truncated] = truncate(reader.out.replace(/\n+$/, ""));
  result.links = urlsOf(result.text);
  return result;
}

/** Cuts text to MAX_PAGE_TEXT characters, at a line's end in the last quarter (Go's truncate). */
export function truncate(text: string): [string, boolean] {
  const chars = [...text];
  if (chars.length <= MAX_PAGE_TEXT) return [text, false];
  let cut = chars.slice(0, MAX_PAGE_TEXT).join("");
  const at = cut.lastIndexOf("\n");
  if (at >= 0 && [...cut.slice(0, at)].length >= MAX_PAGE_TEXT * 3 / 4) cut = cut.slice(0, at);
  return [cut.replace(/[ \t\n]+$/, ""), true];
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
