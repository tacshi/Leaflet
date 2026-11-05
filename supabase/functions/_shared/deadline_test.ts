import { assert, assertEquals, assertFalse } from "jsr:@std/assert@1";
import { expiresBefore, SAVE_MS, timeoutSignal, workDeadline } from "./deadline.ts";

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

// The wall clock is the worker's: a run on a worker 60 s old has the rest
// of it, not the two minutes it would have on a new one.
Deno.test("work stops as far as its worker's wall clock lets it", () => {
  assertEquals(workDeadline(now, 120_000), now + 120_000);
  assertEquals(workDeadline(now, 120_000, now - 60_000), now + 60_000);
  assertEquals(workDeadline(now, 120_000, now - 60_000) + SAVE_MS, now - 60_000 + 150_000);
});

// A worker older than half of the free plan's wall clock, which that plan
// no longer serves from, or a deadline past it, is a paid plan's.
Deno.test("a paid plan's longer wall clock is taken from the worker's age or the deadline", () => {
  assertEquals(workDeadline(now, 120_000, now - 100_000), now + 120_000);
  assertEquals(workDeadline(now, 360_000, now - 60_000), now + 310_000);
  assertEquals(workDeadline(now, 380_000), now + 370_000);
});

// Either end stops a call: its time, as a timeout, or the worker shutting
// down, at once, and one already shutting down from the start.
Deno.test("a call stops at its time or as the worker shuts down", async () => {
  const unloading = new AbortController();
  const signal = timeoutSignal(60_000, unloading.signal);
  assertFalse(signal.aborted);
  unloading.abort();
  assert(signal.aborted);
  assertEquals((signal.reason as DOMException).name, "AbortError");
  assert(timeoutSignal(60_000, unloading.signal).aborted);

  const late = timeoutSignal(1, new AbortController().signal);
  await new Promise((r) => late.addEventListener("abort", r));
  assertEquals((late.reason as DOMException).name, "TimeoutError");
  assertEquals(timeoutSignal(60_000).aborted, false);
});
