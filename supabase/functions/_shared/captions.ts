// What is said in a video, as its site's captions give it, for agents to
// read: the captions its creator gave, or the site's own speech
// recognition of it. Mirrors internal/web/captions.go; both read the cases
// of testdata/captions.json alike.

import { fields, type VideoRef } from "./blocks.ts";
import { readBytes } from "./openai.ts";
import { publicFetch, type Resolver } from "./net.ts";

/** A video's captions in one language (web.Captions). */
export interface Captions {
  /** The captions' language, as en or pt-BR. */
  language: string;
  /** The site's speech recognition, not its creator's captions. */
  auto: boolean;
  lines: Caption[];
}

/** A line of captions: when it starts, in milliseconds, and what is said. */
export interface Caption {
  start_ms: number;
  text: string;
}

/** A video's captions as its site lists them (web.Track). */
export interface Track {
  url: string;
  language: string;
  auto: boolean;
}

/** Why a video's captions could not be read: none, the site refusing, or a video it shows only to some. */
export class CaptionsError extends Error {
  constructor(readonly kind: "none" | "refused" | "restricted", message: string, readonly reason = "") {
    super(message);
  }
}

const noCaptions = () => new CaptionsError("none", "the video has no captions");
const refused = () => new CaptionsError("refused", "the site refused to give the captions");
const restricted = (reason: string) =>
  new CaptionsError(
    "restricted",
    reason ? `the video can't be watched here: ${reason}` : "the video can't be watched here",
    reason,
  );

/** Whether a video's site gives the captions of its videos (web.HasCaptions): YouTube's and Vimeo's do. */
export function hasCaptions(v: VideoRef): boolean {
  return v.provider === "youtube" || v.provider === "vimeo";
}

/** The most of a site's answer read. */
const MAX_CAPTION_BYTES = 8 << 20;

const AGENT = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Leaflet";

/** The most redirects a request follows, as Go's http.Client does. */
const MAX_CAPTION_REDIRECTS = 10;

/**
 * What a site answers a request, from the public web only (web.ask). Its
 * redirects are followed as Go's http.Client follows them, each checked
 * again: a 307 or 308 asks again as it asked, any other with GET, and the
 * cookie goes on only to the host it was for or one under it. An http://
 * address is asked for over https (net.ts safeFetch).
 */
export async function ask(
  url: string,
  init: { body?: string; cookie?: string; signal?: AbortSignal; resolve?: Resolver } = {},
): Promise<string> {
  const host = new URL(url).hostname;
  let { body, cookie } = init;
  for (let hop = 0;; hop++) {
    const headers: Record<string, string> = { "User-Agent": AGENT, "Accept-Language": "en-US,en;q=0.8" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (cookie) headers.Cookie = cookie;
    const res = await publicFetch(url, {
      method: body === undefined ? "GET" : "POST",
      headers,
      body,
      signal: init.signal,
    }, init.resolve);
    const next = res.status >= 300 && res.status < 400 ? res.headers.get("location") : null;
    if (next && hop < MAX_CAPTION_REDIRECTS) {
      await res.body?.cancel().catch(() => {});
      url = new URL(next, url).href.replace(/^http:\/\//i, "https://");
      if (res.status !== 307 && res.status !== 308) body = undefined;
      const to = new URL(url).hostname;
      if (to !== host && !to.endsWith("." + host)) cookie = undefined;
      continue;
    }
    if (res.status === 429) {
      await res.body?.cancel().catch(() => {});
      throw refused();
    }
    if (res.status >= 300) {
      await res.body?.cancel().catch(() => {});
      throw Object.assign(new Error(`${new URL(url).hostname} answered ${res.status}`), { status: res.status });
    }
    return new TextDecoder().decode(await readBytes(res, MAX_CAPTION_BYTES, true));
  }
}

/** Reads a video's captions from its site (web.ReadCaptions). */
export async function readCaptions(v: VideoRef, signal?: AbortSignal): Promise<Captions> {
  if (v.provider === "youtube") return await youtubeCaptions(v.id, signal);
  if (v.provider === "vimeo") return await vimeoCaptions(v.id, v.hash, signal);
  throw noCaptions();
}

/**
 * Reads a YouTube video's captions as the youtube-transcript-api project
 * does: its watch page names the key of YouTube's own API, whose player,
 * asked as the Android app, lists the video's captions, each a link to its
 * lines. A watch page from Europe first asks to agree to cookies, which a
 * cookie says yes to.
 */
async function youtubeCaptions(id: string, signal?: AbortSignal): Promise<Captions> {
  const watch = "https://www.youtube.com/watch?v=" + encodeURIComponent(id);
  let page = await ask(watch, { signal });
  if (page.includes('action="https://consent.youtube.com/s"')) {
    const m = page.match(/name="v" value="(.*?)"/);
    if (!m) throw refused();
    page = await ask(watch, { cookie: "CONSENT=YES+" + m[1], signal });
  }
  const key = page.match(/"INNERTUBE_API_KEY":\s*"([a-zA-Z0-9_-]+)"/);
  if (!key) throw refused();
  const body = JSON.stringify({
    context: { client: { clientName: "ANDROID", clientVersion: "20.10.38" } },
    videoId: id,
  });
  const player = await ask("https://www.youtube.com/youtubei/v1/player?key=" + key[1], { body, signal });
  const track = youtubeTrack(player);
  const xml = await ask(track.url, { signal });
  return { language: track.language, auto: track.auto, lines: youtubeLines(xml) };
}

// deno-lint-ignore no-explicit-any
type JSONValue = any;

/**
 * Picks the captions to read of those YouTube's player answered for a
 * video (web.YouTubeTrack): the creator's in the language spoken, which
 * YouTube's own recognition is in, else that recognition, else the
 * creator's YouTube shows first, in English, or any.
 */
export function youtubeTrack(player: string): Track {
  let p: JSONValue;
  try {
    p = JSON.parse(player);
  } catch (e) {
    throw new Error(`youtube: ${e instanceof Error ? e.message : String(e)}`);
  }
  const status = p?.playabilityStatus ?? {};
  const reason = typeof status?.reason === "string" ? status.reason : "";
  if (status?.status !== "OK") {
    if (status.status === "LOGIN_REQUIRED" && reason.toLowerCase().includes("bot")) throw refused();
    throw restricted(reason);
  }
  const list = p?.captions?.playerCaptionsTracklistRenderer ?? {};
  const tracks: Track[] = [];
  for (const t of Array.isArray(list.captionTracks) ? list.captionTracks : []) {
    if (typeof t?.baseUrl === "string" && t.baseUrl !== "") {
      tracks.push({
        url: t.baseUrl,
        language: typeof t.languageCode === "string" ? t.languageCode : "",
        auto: t.kind === "asr",
      });
    }
  }
  const audio = Array.isArray(list.audioTracks) ? list.audioTracks : [];
  const index = audio[0]?.defaultCaptionTrackIndex;
  const first = Number.isInteger(index) ? index : -1;
  const t = pickTrack(tracks, first, -1);
  if (!t) throw noCaptions();
  // A link that wants a token YouTube's own player makes gives nothing.
  if (t.url.includes("&exp=xpe")) throw refused();
  return t;
}

/**
 * Picks captions to read (web.pickTrack): the creator's in the language
 * spoken, which the site's recognition is in, else that recognition, else
 * the track at first, the creator's in English, the track at then, or the
 * first; first and then are -1 for none.
 */
function pickTrack(tracks: Track[], first: number, then: number): Track | null {
  if (tracks.length === 0) return null;
  const base = (lang: string) => lang.toLowerCase().split("-")[0];
  const auto = tracks.find((t) => t.auto);
  if (auto) return tracks.find((t) => !t.auto && base(t.language) === base(auto.language)) ?? auto;
  if (first >= 0 && first < tracks.length) return tracks[first];
  const en = tracks.find((t) => base(t.language) === "en");
  if (en) return en;
  if (then >= 0 && then < tracks.length) return tracks[then];
  return tracks[0];
}

/**
 * Reads the lines of YouTube's captions (web.YouTubeLines): <p t="ms"> in
 * its own format, or <text start="s"> in the older one, words in them in
 * <s>s.
 */
export function youtubeLines(xml: string): Caption[] {
  const lines: Caption[] = [];
  for (const m of xml.matchAll(/<(p|text)\b([^>]*)>([\s\S]*?)<\/(?:p|text)>/g)) {
    let start = 0;
    const t = m[2].match(/(?:^|\s)(t|start)="([0-9.]+)"/);
    if (t && t[1] === "t") start = goAtoi(t[2]);
    else if (t) start = Math.trunc(goParseFloat(t[2]) * 1000 + 0.5);
    const text = captionText(m[3]);
    if (text) lines.push({ start_ms: start, text });
  }
  return lines;
}

/** A whole number as Go's strconv.Atoi reads one, or 0. */
function goAtoi(s: string): number {
  return /^[0-9]+$/.test(s) && s.length <= 18 ? Number(s) : 0;
}

/** A number as Go's strconv.ParseFloat reads digits and points, or 0. */
function goParseFloat(s: string): number {
  return /^(?:[0-9]+\.?[0-9]*|\.[0-9]+)$/.test(s) ? Number(s) : 0;
}

/**
 * A caption's words (web.captionText): its tags taken out, and its
 * entities read, twice, as YouTube's older captions escape theirs again,
 * on one line.
 */
function captionText(s: string): string {
  for (let i = 0; i < 2; i++) s = unescapeCaption(s.replace(/<[^>]*>/g, ""));
  return fields(s).join(" ");
}

const ENTITIES: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " };

/** The entities captions write (web.unescapeCaption): numbers, and the few names of XML and HTML's no-break space. */
function unescapeCaption(s: string): string {
  return s.replace(/&(#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[a-zA-Z]+);/g, (e, name: string) => {
    if (Object.hasOwn(ENTITIES, name)) return ENTITIES[name];
    if (name.startsWith("#")) {
      const hex = name[1] === "x" || name[1] === "X";
      const n = parseInt(name.slice(hex ? 2 : 1), hex ? 16 : 10);
      // A valid rune, as Go's utf8.ValidRune has it: not a surrogate.
      if (n > 0 && n <= 0x10ffff && !(n >= 0xd800 && n <= 0xdfff)) return String.fromCodePoint(n);
    }
    return e;
  });
}

/** Reads a Vimeo video's captions, which its player's configuration lists, each a link to a WebVTT file (web.vimeoCaptions). */
async function vimeoCaptions(id: string, hash: string, signal?: AbortSignal): Promise<Captions> {
  let url = "https://player.vimeo.com/video/" + encodeURIComponent(id) + "/config";
  if (hash) url += "?h=" + encodeURIComponent(hash);
  let config: string;
  try {
    config = await ask(url, { signal });
  } catch (e) {
    const status = (e as { status?: number }).status;
    if (status === 403 || status === 404) throw restricted("");
    throw e;
  }
  const track = vimeoTrack(config);
  return { language: track.language, auto: track.auto, lines: vttLines(await ask(track.url, { signal })) };
}

/**
 * Picks the captions to read of those a Vimeo player's configuration
 * lists (web.VimeoTrack), as pickTrack does: Vimeo's own recognition is in
 * a language as en-x-autogen.
 */
export function vimeoTrack(config: string): Track {
  let c: JSONValue;
  try {
    c = JSON.parse(config);
  } catch (e) {
    throw new Error(`vimeo: ${e instanceof Error ? e.message : String(e)}`);
  }
  const tracks: Track[] = [];
  let captions = -1, chosen = -1;
  const list = c?.request?.text_tracks;
  for (const t of Array.isArray(list) ? list : []) {
    let url = typeof t?.url === "string" ? t.url : "";
    if (url.startsWith("/")) url = "https://player.vimeo.com" + url;
    if (!webLink(url)) continue;
    const lang = typeof t.lang === "string" ? t.lang : "";
    const auto = lang.endsWith("-x-autogen");
    // Captions are in the language spoken, as subtitles need not be; the
    // track the player shows first may be a translation.
    if (t.kind === "captions" && captions < 0) captions = tracks.length;
    if (t.default === true && chosen < 0) chosen = tracks.length;
    tracks.push({ url, language: auto ? lang.slice(0, -"-x-autogen".length) : lang, auto });
  }
  const t = pickTrack(tracks, captions, chosen);
  if (!t) throw noCaptions();
  return t;
}

/** Whether s is an http(s) link with a host (web.WebLink). */
function webLink(s: string): boolean {
  try {
    const u = new URL(s);
    return (u.protocol === "https:" || u.protocol === "http:") && u.host !== "";
  } catch {
    return false;
  }
}

/** The cues of a WebVTT file (web.VTTLines): each its start and its text, its lines on one, its notes, styles and regions left out. */
export function vttLines(vtt: string): Caption[] {
  const lines: Caption[] = [];
  vtt = vtt.replaceAll("\r\n", "\n").replaceAll("\r", "\n");
  for (const block of vtt.split("\n\n")) {
    const rows = block.replace(/^\n+|\n+$/g, "").split("\n");
    const at = rows.findIndex((r) => r.includes("-->"));
    if (at < 0) continue;
    const start = vttTime(rows[at].split("-->")[0].trim());
    if (start === null) continue;
    const text = captionText(rows.slice(at + 1).join(" "));
    if (text) lines.push({ start_ms: start, text });
  }
  return lines;
}

/** A cue's time, as 01:02:03.456 or 02:03.456, in milliseconds (web.vttTime). */
function vttTime(s: string): number | null {
  const dot = s.indexOf(".");
  const whole = dot < 0 ? s : s.slice(0, dot);
  let frac = dot < 0 ? "" : s.slice(dot + 1);
  const parts = whole.split(":");
  if (parts.length < 2 || parts.length > 3) return null;
  let ms = 0;
  for (const p of parts) {
    if (!/^[0-9]+$/.test(p)) return null;
    ms = ms * 60 + Number(p);
  }
  ms *= 1000;
  if (frac !== "") {
    frac = (frac + "000").slice(0, 3);
    if (!/^[0-9]+$/.test(frac)) return null;
    ms += Number(frac);
  }
  return ms;
}

/**
 * Captions joined into passages of what is said, for agents to read
 * (web.Passages): a passage ends at a sentence's end once it is six seconds
 * long, and at twenty seconds whatever is said.
 */
export function passages(lines: Caption[]): Caption[] {
  const out: Caption[] = [];
  for (const l of lines) {
    const cur = out[out.length - 1];
    if (cur) {
      const long = l.start_ms - cur.start_ms;
      if (long < 20000 && (long < 6000 || !endsSentence(cur.text))) {
        cur.text += " " + l.text;
        continue;
      }
    }
    out.push({ ...l });
  }
  return out;
}

/** Whether text ends a sentence, its closing quotes and brackets aside (web.endsSentence). */
function endsSentence(text: string): boolean {
  const trimmed = text.replace(/["'”’)\]）」』]+$/u, "");
  const last = [...trimmed].pop() ?? "";
  return ".!?。！？…".includes(last) && last !== "";
}
