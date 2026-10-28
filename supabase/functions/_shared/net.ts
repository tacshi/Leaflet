// Fetching addresses that people type (model provider and speech-to-text
// base URLs): https only, to public hosts only, never following redirects,
// so the server cannot be pointed at itself or its private network.

export class UnsafeUrlError extends Error {}

/** Hosts the app itself points at; they skip the DNS check. */
const TRUSTED_HOSTS = new Set([
  "api.openai.com",
  "api.anthropic.com",
  "api.x.ai",
  "openrouter.ai",
  "api.deepseek.com",
  "open.bigmodel.cn",
  "api.soniox.com",
]);

const BLOCKED_SUFFIXES = [".localhost", ".local", ".internal", ".home.arpa"];

function parseIPv4(text: string): number[] | null {
  const parts = text.split(".");
  if (parts.length !== 4) return null;
  const bytes = parts.map((p) => (/^\d{1,3}$/.test(p) ? Number(p) : Number.NaN));
  return bytes.every((b) => Number.isInteger(b) && b >= 0 && b <= 255) ? bytes : null;
}

/** The eight 16-bit groups of an IPv6 address, or null. */
function parseIPv6(text: string): number[] | null {
  let s = text.replace(/^\[|\]$/g, "").toLowerCase();
  const zone = s.indexOf("%");
  if (zone >= 0) s = s.slice(0, zone);
  if (!s.includes(":")) return null;
  const lastColon = s.lastIndexOf(":");
  const last = s.slice(lastColon + 1);
  if (last.includes(".")) {
    // A dotted IPv4 tail is two groups.
    const v4 = parseIPv4(last);
    if (!v4) return null;
    const hex = (hi: number, lo: number) => ((hi << 8) | lo).toString(16);
    s = `${s.slice(0, lastColon + 1)}${hex(v4[0], v4[1])}:${hex(v4[2], v4[3])}`;
  }
  const halves = s.split("::");
  if (halves.length > 2) return null;
  const words = (h: string) =>
    h === "" ? [] : h.split(":").map((w) => (/^[0-9a-f]{1,4}$/.test(w) ? parseInt(w, 16) : Number.NaN));
  const head = words(halves[0]);
  const rest = halves.length === 2 ? words(halves[1]) : [];
  if ([...head, ...rest].some((w) => Number.isNaN(w))) return null;
  if (halves.length === 1) return head.length === 8 ? head : null;
  if (head.length + rest.length > 7) return null;
  return [...head, ...new Array(8 - head.length - rest.length).fill(0), ...rest];
}

function privateIPv4([a, b, c]: number[]): boolean {
  return a === 0 || // this network
    a === 10 ||
    (a === 100 && b >= 64 && b <= 127) || // CGNAT
    a === 127 ||
    (a === 169 && b === 254) || // link-local, cloud metadata
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 0 && (c === 0 || c === 2)) ||
    (a === 192 && b === 88 && c === 99) ||
    (a === 192 && b === 168) ||
    (a === 198 && (b === 18 || b === 19)) || // benchmarking
    (a === 198 && b === 51 && c === 100) ||
    (a === 203 && b === 0 && c === 113) ||
    a >= 224; // multicast, reserved, broadcast
}

function privateIPv6(w: number[]): boolean {
  const embedded = () => [w[6] >> 8, w[6] & 0xff, w[7] >> 8, w[7] & 0xff];
  if (w.slice(0, 6).every((x) => x === 0)) {
    // ::, ::1 and the deprecated IPv4-compatible ::a.b.c.d
    return true;
  }
  if (w.slice(0, 5).every((x) => x === 0) && w[5] === 0xffff) return privateIPv4(embedded()); // IPv4-mapped
  if (w[0] === 0x64 && w[1] === 0xff9b) {
    // NAT64: the well-known prefix carries an IPv4 address; the local-use one is private.
    return w[2] === 1 || privateIPv4(embedded());
  }
  if (w[0] === 0x100 && w[1] === 0 && w[2] === 0 && w[3] === 0) return true; // discard
  if (w[0] === 0x2001 && (w[1] === 0 || w[1] === 0xdb8)) return true; // Teredo, documentation
  if (w[0] === 0x2002) return true; // 6to4
  if ((w[0] & 0xfe00) === 0xfc00) return true; // unique local
  if ((w[0] & 0xffc0) === 0xfe80 || (w[0] & 0xffc0) === 0xfec0) return true; // link-local, site-local
  if ((w[0] & 0xff00) === 0xff00) return true; // multicast
  return false;
}

/** The address as an IP if it is one, with whether it is private. */
export function ipKind(address: string): "public" | "private" | null {
  const v4 = parseIPv4(address);
  if (v4) return privateIPv4(v4) ? "private" : "public";
  const v6 = parseIPv6(address);
  if (v6) return privateIPv6(v6) ? "private" : "public";
  return null;
}

/** True for loopback, private, link-local, CGNAT, unique-local and other non-public addresses. */
export function isPrivateAddress(address: string): boolean {
  return ipKind(address) !== "public";
}

/**
 * Parses a URL someone typed and rejects anything but https to a public
 * host name or address. The host's DNS is checked by `assertPublicHost`.
 */
export function publicHttpsUrl(raw: string): URL {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new UnsafeUrlError("Enter a valid URL");
  }
  if (url.protocol !== "https:") throw new UnsafeUrlError("The URL must start with https://");
  if (url.username || url.password) throw new UnsafeUrlError("Put the API key in its own field, not in the URL");
  const host = url.hostname.replace(/\.$/, "").toLowerCase();
  if (!host) throw new UnsafeUrlError("Enter a valid URL");
  const kind = ipKind(host);
  if (kind === "private") throw new UnsafeUrlError("The server cannot reach private or local addresses");
  if (kind === null) {
    if (host === "localhost" || !host.includes(".") || BLOCKED_SUFFIXES.some((s) => host.endsWith(s))) {
      throw new UnsafeUrlError("The server cannot reach private or local addresses");
    }
  }
  return url;
}

type Resolver = (host: string, type: "A" | "AAAA") => Promise<string[]>;

const denoResolver: Resolver = (host, type) => Deno.resolveDns(host, type);

/** Rejects a host whose name resolves to a private address, or to nothing. */
export async function assertPublicHost(url: URL, resolve: Resolver = denoResolver): Promise<void> {
  const host = url.hostname.replace(/\.$/, "").toLowerCase();
  if (ipKind(host) !== null || TRUSTED_HOSTS.has(host)) return;
  const results = await Promise.allSettled([resolve(host, "A"), resolve(host, "AAAA")]);
  const addresses = results.flatMap((r) => (r.status === "fulfilled" ? r.value : []));
  if (!addresses.length) throw new UnsafeUrlError(`Could not find ${host}`);
  if (addresses.some(isPrivateAddress)) {
    throw new UnsafeUrlError("The server cannot reach private or local addresses");
  }
}

/** Checks a URL someone typed: https, a public host, a name that resolves publicly. */
export async function checkPublicUrl(raw: string, resolve?: Resolver): Promise<URL> {
  const url = publicHttpsUrl(raw);
  await assertPublicHost(url, resolve);
  return url;
}

/**
 * fetch for addresses someone typed. A redirect is an error rather than
 * followed (it could lead anywhere); network failures say only that the
 * host could not be reached. Aborts and timeouts are thrown as they are.
 */
export async function safeFetch(raw: string, init: RequestInit = {}, resolve?: Resolver): Promise<Response> {
  const url = await checkPublicUrl(raw, resolve);
  let res: Response;
  try {
    res = await fetch(url, { ...init, redirect: "manual" });
  } catch (e) {
    if (e instanceof DOMException && (e.name === "AbortError" || e.name === "TimeoutError")) throw e;
    if (init.signal?.aborted) throw e;
    throw new UnsafeUrlError(`Could not reach ${url.hostname}`);
  }
  if (res.status >= 300 && res.status < 400 || res.type === "opaqueredirect") {
    await res.body?.cancel().catch(() => {});
    throw new UnsafeUrlError(`${url.hostname} answered with a redirect; enter the address it redirects to`);
  }
  return res;
}

/** True when an error is an abort or timeout of a fetch. */
export function isTimeout(e: unknown): boolean {
  return e instanceof DOMException && (e.name === "AbortError" || e.name === "TimeoutError");
}
