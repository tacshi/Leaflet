import { assert, assertFalse } from "jsr:@std/assert@1";
import { expiresBefore } from "./deadline.ts";

function token(claims: Record<string, unknown>): string {
  const b64url = (s: string) => btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${b64url(JSON.stringify({ alg: "HS256", typ: "JWT" }))}.${b64url(JSON.stringify(claims))}.sig`;
}

const now = 1_800_000_000_000;
const s = (ms: number) => Math.floor(ms / 1000);

Deno.test("a token that runs out before the deadline is refused when a fresh one would last", () => {
  // An hour's token with 90 s left, for a run of 150 s.
  const t = token({ exp: s(now + 90_000), iat: s(now - 3_510_000) });
  assert(expiresBefore(t, now + 150_000, now));
});

Deno.test("a token that lasts past the deadline passes", () => {
  const t = token({ exp: s(now + 600_000), iat: s(now - 3_000_000) });
  assertFalse(expiresBefore(t, now + 150_000, now));
});

Deno.test("a token too short even when fresh passes", () => {
  // The project's tokens last 2 minutes: renewing would not help.
  const t = token({ exp: s(now + 60_000), iat: s(now - 60_000) });
  assertFalse(expiresBefore(t, now + 150_000, now));
});

Deno.test("a token without iat that runs out first is refused", () => {
  assert(expiresBefore(token({ exp: s(now + 10_000) }), now + 150_000, now));
});

Deno.test("a token that cannot be read is left to the Auth server", () => {
  assertFalse(expiresBefore("not-a-jwt", now + 150_000, now));
  assertFalse(expiresBefore("a.!!!.c", now + 150_000, now));
  assertFalse(expiresBefore(token({ sub: "u1" }), now + 150_000, now));
});
