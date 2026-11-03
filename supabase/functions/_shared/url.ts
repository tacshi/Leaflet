// Links as the app reads them: a port of what Go's net/url.Parse takes and
// refuses, and of doc.WebURL and doc.agentURL on it, so a link an agent
// writes is an image, a video or a link card here exactly when it is one
// in the app. The browser's URL parser, which this side has, takes links
// Go refuses (a port past 65535, an IPv4 address out of range, a host
// with a backslash, %zz) and refuses some it takes.

import { isSpace, trimSpace } from "./blocks.ts";

/** A link's parts, as net/url.URL has them: path and fragment unescaped. */
export interface GoURL {
  scheme: string;
  opaque: string;
  /** host[:port], unescaped, with an IPv6 address in brackets. */
  host: string;
  path: string;
  rawQuery: string;
  fragment: string;
}

// The bytes each part may hold without escaping (net/url's shouldEscape):
// in a host or a zone, letters, digits and these.
const HOST_CHARS = new Set("!\"$&'()*+,-.:;<=>[]_~");

function isHex(c: string | undefined): boolean {
  return !!c && /^[0-9A-Fa-f]$/.test(c);
}

type Mode = "path" | "host" | "zone" | "fragment" | "userinfo" | "query";

/** Whether an ASCII character needs escaping in a host or a zone. */
function escapesInHost(c: string): boolean {
  return !/^[A-Za-z0-9]$/.test(c) && !HOST_CHARS.has(c);
}

/**
 * s with its %XX escapes undone (net/url's unescape), or null where Go
 * refuses it: a % not followed by two hex digits, an escaped ASCII byte in
 * a host but %25, or a character a host may not hold.
 */
function unescape(s: string, mode: Mode): string | null {
  let escapes = false;
  for (let i = 0; i < s.length;) {
    const c = s[i];
    if (c === "%") {
      if (i + 2 >= s.length || !isHex(s[i + 1]) || !isHex(s[i + 2])) return null;
      const v = parseInt(s.slice(i + 1, i + 3), 16);
      if (mode === "host" && v < 0x80 && s.slice(i, i + 3) !== "%25") return null;
      // A zone may escape only what a host may hold as it is, and spaces;
      // a byte past ASCII is one a host escapes.
      if (
        mode === "zone" && s.slice(i, i + 3) !== "%25" && v !== 0x20 &&
        (v >= 0x80 || escapesInHost(String.fromCharCode(v)))
      ) return null;
      escapes = true;
      i += 3;
    } else {
      if ((mode === "host" || mode === "zone") && c.charCodeAt(0) < 0x80 && escapesInHost(c)) return null;
      i++;
    }
  }
  if (!escapes && !(mode === "query" && s.includes("+"))) return s;
  // The bytes, as Go has the string, decoded as UTF-8 at the end.
  const bytes: number[] = [];
  const enc = new TextEncoder();
  for (let i = 0; i < s.length;) {
    if (s[i] === "%") {
      bytes.push(parseInt(s.slice(i + 1, i + 3), 16));
      i += 3;
    } else if (s[i] === "+" && mode === "query") {
      bytes.push(0x20);
      i++;
    } else {
      const cp = s.codePointAt(i)!;
      const ch = String.fromCodePoint(cp);
      bytes.push(...enc.encode(ch));
      i += ch.length;
    }
  }
  return new TextDecoder().decode(new Uint8Array(bytes));
}

/** The scheme before a colon, and the rest (net/url's getScheme); null for ":…". */
function scheme(raw: string): { scheme: string; rest: string } | null {
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i];
    if (/[A-Za-z]/.test(c)) continue;
    if (/[0-9+.-]/.test(c)) {
      if (i === 0) return { scheme: "", rest: raw };
      continue;
    }
    if (c === ":") {
      if (i === 0) return null;
      return { scheme: raw.slice(0, i), rest: raw.slice(i + 1) };
    }
    return { scheme: "", rest: raw };
  }
  return { scheme: "", rest: raw };
}

/** Whether port is "" or ":" and digits (validOptionalPort). */
function validPort(port: string): boolean {
  return port === "" || /^:[0-9]*$/.test(port);
}

/** Whether s is an IPv4 address (netip's parseIPv4Fields). */
function isIPv4(s: string): boolean {
  const parts = s.split(".");
  return parts.length === 4 && parts.every((p) => /^(0|[1-9][0-9]{0,2})$/.test(p) && Number(p) <= 255);
}

/** Whether s is an IPv6 address, with a zone or not (netip's parseIPv6). */
function isIPv6(s: string): boolean {
  const zone = s.indexOf("%");
  if (zone >= 0) {
    if (zone === s.length - 1) return false;
    s = s.slice(0, zone);
  }
  let ellipsis = -1;
  if (s.startsWith("::")) {
    ellipsis = 0;
    s = s.slice(2);
    if (s === "") return true;
  }
  let i = 0;
  while (i < 16) {
    const m = s.match(/^[0-9A-Fa-f]*/)![0];
    if (m.length > 4) return false;
    if (m.length === 0) return false;
    if (s[m.length] === ".") {
      if ((ellipsis < 0 && i !== 12) || i + 4 > 16) return false;
      if (!isIPv4(s)) return false;
      s = "";
      i += 4;
      break;
    }
    i += 2;
    s = s.slice(m.length);
    if (s === "") break;
    if (s[0] !== ":" || s.length === 1) return false;
    s = s.slice(1);
    if (s[0] === ":") {
      if (ellipsis >= 0) return false;
      ellipsis = i;
      s = s.slice(1);
      if (s === "") break;
    }
  }
  if (s !== "") return false;
  return i < 16 ? ellipsis >= 0 : ellipsis < 0;
}

/** host[:port] unescaped, or null where Go refuses it (net/url's parseHost). */
function parseHost(scheme: string, host: string): string | null {
  const open = host.lastIndexOf("[");
  if (open > 0) return null;
  if (open === 0) {
    const close = host.lastIndexOf("]");
    if (close < 0) return null;
    const colonPort = host.slice(close + 1);
    if (!validPort(colonPort)) return null;
    const name = host.slice(1, close);
    const zone = name.indexOf("%25");
    const unescaped = zone >= 0
      ? [unescape(name.slice(0, zone), "host"), unescape(name.slice(zone), "zone")]
      : [unescape(name, "host")];
    if (unescaped.some((u) => u === null)) return null;
    const address = unescaped.join("");
    // Only an IPv6 address goes in brackets: netip.ParseAddr reads the
    // first of '.', ':' or '%' as what the address is.
    const kind = address.match(/[.:%]/)?.[0];
    if (kind !== ":" || !isIPv6(address)) return null;
    return `[${address}]${colonPort}`;
  }
  const colon = host.indexOf(":");
  if (colon >= 0) {
    // A second colon is no port's, for http and https.
    const at = scheme === "http" || scheme === "https" ? colon : host.lastIndexOf(":");
    if (!validPort(host.slice(at))) return null;
  }
  return unescape(host, "host");
}

/** Whether userinfo holds only what Go takes in it (validUserinfo). */
function validUserinfo(s: string): boolean {
  return /^[A-Za-z0-9\-._:~!$&'()*+,;=%@]*$/.test(s);
}

/**
 * A link as Go's url.Parse reads it, or null where Go refuses it: a
 * control character, a bad escape, a host it cannot hold or a port that
 * is not digits.
 */
export function parseURL(raw: string): GoURL | null {
  const hash = raw.indexOf("#");
  const rest0 = hash < 0 ? raw : raw.slice(0, hash);
  // Go refuses any ASCII control character (stringContainsCTLByte).
  for (const c of rest0) {
    const code = c.charCodeAt(0);
    if (code < 0x20 || code === 0x7f) return null;
  }
  const u: GoURL = { scheme: "", opaque: "", host: "", path: "", rawQuery: "", fragment: "" };
  if (rest0 !== "*") {
    const s = scheme(rest0);
    if (!s) return null;
    u.scheme = s.scheme.toLowerCase();
    let rest = s.rest;
    if (rest.endsWith("?") && rest.indexOf("?") === rest.length - 1) {
      rest = rest.slice(0, -1);
    } else {
      const q = rest.indexOf("?");
      if (q >= 0) {
        u.rawQuery = rest.slice(q + 1);
        rest = rest.slice(0, q);
      }
    }
    let opaque = false;
    if (!rest.startsWith("/")) {
      if (u.scheme !== "") {
        u.opaque = rest;
        opaque = true;
      } else if (rest.split("/")[0].includes(":")) {
        return null;
      }
    }
    if (!opaque) {
      if ((u.scheme !== "" || !rest.startsWith("///")) && rest.startsWith("//")) {
        let authority = rest.slice(2);
        rest = "";
        const slash = authority.indexOf("/");
        if (slash >= 0) {
          rest = authority.slice(slash);
          authority = authority.slice(0, slash);
        }
        const at = authority.lastIndexOf("@");
        const host = parseHost(u.scheme, authority.slice(at + 1));
        if (host === null) return null;
        if (at >= 0) {
          const info = authority.slice(0, at);
          if (!validUserinfo(info)) return null;
          const colon = info.indexOf(":");
          const parts = colon < 0 ? [info] : [info.slice(0, colon), info.slice(colon + 1)];
          if (parts.some((part) => unescape(part, "userinfo") === null)) return null;
        }
        u.host = host;
      }
      const path = unescape(rest, "path");
      if (path === null) return null;
      u.path = path;
    }
  } else {
    u.path = "*";
  }
  if (hash >= 0) {
    const fragment = unescape(raw.slice(hash + 1), "fragment");
    if (fragment === null) return null;
    u.fragment = fragment;
  }
  return u;
}

/** The host without its port and brackets (URL.Hostname). */
export function hostname(host: string): string {
  const colon = host.lastIndexOf(":");
  if (colon >= 0 && validPort(host.slice(colon))) host = host.slice(0, colon);
  return host.startsWith("[") && host.endsWith("]") ? host.slice(1, -1) : host;
}

/** Whether s is a web link, http or https to a host (doc.WebURL). */
export function webURL(s: string): boolean {
  const u = parseURL(trimSpace(s));
  return !!u && (u.scheme === "http" || u.scheme === "https") && u.host !== "" && !/[ \t\n]/.test(s);
}

/**
 * Whether an agent's link for a block the app loads by itself is a web
 * link with a host's name and no space of any kind (doc.agentURL).
 */
export function agentURL(s: string): boolean {
  if (!webURL(s) || isSpace(s)) return false;
  const u = parseURL(s);
  return !!u && hostname(u.host) !== "";
}

/** The first value of key in a query, as url.Values.Get has it after ParseQuery. */
export function queryValue(rawQuery: string, key: string): string {
  if (rawQuery.split("&").length > 10_000) return "";
  for (const pair of rawQuery.split("&")) {
    if (pair === "" || pair.includes(";")) continue;
    const eq = pair.indexOf("=");
    const k = unescape(eq < 0 ? pair : pair.slice(0, eq), "query");
    const v = unescape(eq < 0 ? "" : pair.slice(eq + 1), "query");
    if (k === null || v === null) continue;
    if (k === key) return v;
  }
  return "";
}

/** A path's extension, from its last dot in its last element (path.Ext). */
export function pathExt(path: string): string {
  for (let i = path.length - 1; i >= 0 && path[i] !== "/"; i--) {
    if (path[i] === ".") return path.slice(i);
  }
  return "";
}

/**
 * s in lower case as Go's strings.ToLower has it: a character at a time,
 * so İ is i, as JavaScript, which writes it i and a dot, does not.
 */
export function goLower(s: string): string {
  let out = "";
  for (const c of s) out += c === "\u0130" ? "i" : c.toLowerCase();
  return out;
}
