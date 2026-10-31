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
