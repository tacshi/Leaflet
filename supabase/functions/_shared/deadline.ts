// How long a function's work may take. An Edge Function is stopped at its
// wall-clock limit (150 s on the free plan, 400 s on paid plans), so work
// that can run long stops at a deadline, keeping time to save what it did;
// and since it saves as the caller, the caller's access token must last
// until then.

/** A duration in ms from the environment, within [min, max], or fallback. */
export function envMs(name: string, fallback: number, min: number, max: number): number {
  const n = Number(Deno.env.get(name));
  return Number.isFinite(n) && n > 0 ? Math.min(max, Math.max(min, n)) : fallback;
}

/** What work may take past its deadline to save, before the platform stops it. */
export const SAVE_MS = 30_000;

/** How long a worker lives on the free plan and on paid ones. */
const FREE_WALL_CLOCK_MS = 150_000;
const PAID_WALL_CLOCK_MS = 400_000;

/**
 * When this worker started, as index.ts first loads this module. The wall
 * clock is the worker's, not a request's: a worker serves requests until
 * half of it has gone, and the runtime stops them all when it ends.
 */
export const workerStarted = Date.now();

/**
 * When work that began at started must stop calling out to keep time to
 * save what it did: deadlineMs on, or as far as the worker's own wall clock
 * lets it, whichever comes first. The plan is not told. Its wall clock is
 * taken from a deadline set past the free plan's, or from a worker older
 * than half of the free plan's, which that plan would have stopped
 * serving; otherwise the shorter one, which on a paid plan only ends work
 * on a warm worker sooner than it had to.
 */
export function workDeadline(started: number, deadlineMs: number, workerStartedAt = started): number {
  const paid = deadlineMs + SAVE_MS > FREE_WALL_CLOCK_MS || started - workerStartedAt >= FREE_WALL_CLOCK_MS / 2;
  return Math.min(started + deadlineMs, workerStartedAt + (paid ? PAID_WALL_CLOCK_MS : FREE_WALL_CLOCK_MS) - SAVE_MS);
}

/**
 * A signal that aborts as the platform says it will shut this worker down:
 * near its wall clock, at its CPU's soft limit, or dropped early with
 * nothing left to answer. What is left is little, and how little is the
 * platform's, so work that hears it ends and saves at once.
 */
export function unloadSignal(): AbortSignal {
  const unloading = new AbortController();
  addEventListener("beforeunload", () => unloading.abort(), { once: true });
  return unloading.signal;
}

/**
 * A signal that aborts after ms, or as unloading does when there is one.
 * Not AbortSignal.any, whose signal, with only its listeners holding it,
 * did not abort in the Edge Runtime as its worker was shut down.
 */
export function timeoutSignal(ms: number, unloading?: AbortSignal): AbortSignal {
  const timeout = AbortSignal.timeout(ms);
  if (!unloading) return timeout;
  const either = new AbortController();
  if (unloading.aborted) either.abort(unloading.reason);
  for (const source of [timeout, unloading]) {
    source.addEventListener("abort", () => either.abort(source.reason), { once: true, signal: either.signal });
  }
  return either.signal;
}

/**
 * Whether an access token runs out before `until` (ms since the epoch)
 * when a fresh one would not: one lasting as long as this one does from
 * when it was issued. A token that cannot last that long even fresh (the
 * project's tokens are that short) passes, since renewing it would not
 * help. The claims are read without checking the signature: the Auth
 * server checks the token (requireUser), and this only reads when it
 * expires.
 */
export function expiresBefore(token: string, until: number, now = Date.now()): boolean {
  const claims = tokenClaims(token);
  if (!claims || typeof claims.exp !== "number") return false;
  const exp = claims.exp * 1000;
  if (exp >= until) return false;
  if (typeof claims.iat !== "number") return true;
  return exp - claims.iat * 1000 >= until - now;
}

function tokenClaims(token: string): { exp?: unknown; iat?: unknown } | null {
  const payload = token.split(".")[1];
  if (!payload) return null;
  try {
    const b64 = payload.replace(/-/g, "+").replace(/_/g, "/");
    const claims = JSON.parse(atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4)));
    return claims && typeof claims === "object" ? claims : null;
  } catch {
    return null;
  }
}
