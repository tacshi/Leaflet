// Leaflet's sign-in with Supabase, on Cloudflare Workers. Two routes:
//
//   GET  /auth/supabase   the OAuth app's redirect URL. Supabase takes only
//                         https ones, which a desktop app can't answer: the
//                         page sends the browser on to the app, waiting on
//                         this computer, with the code and state.
//   POST /token           Supabase's token endpoint, with the OAuth app's
//                         client secret added, so the secret never ships in
//                         the app.
//
// The secret is CLIENT_SECRET (wrangler secret put CLIENT_SECRET); the app's
// client ID is CLIENT_ID in wrangler.toml.

export interface Env {
  CLIENT_ID: string;
  CLIENT_SECRET: string;
  // The token endpoint; tests point it elsewhere.
  SUPABASE_TOKEN_URL?: string;
}

// The port Leaflet waits on for the browser (internal/supabaseapi.Port).
const LOOPBACK = "http://127.0.0.1:51741/auth/callback";

const PAGE = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Leaflet</title>
<script>location.replace(${JSON.stringify(LOOPBACK)} + location.search);</script>
<style>
  body { font: 16px -apple-system, system-ui, sans-serif; display: grid; place-items: center; height: 90vh; margin: 0; color: #16181d; background: #fff; }
  p { color: #565c67; margin: 0; }
  @media (prefers-color-scheme: dark) { body { color: #e8e9ec; background: #16181d; } p { color: #9aa0aa; } }
</style>
</head>
<body>
<div style="text-align:center">
  <h2 style="margin:0 0 8px">Returning to Leaflet…</h2>
  <p>If nothing happens, open Leaflet and sign in with Supabase again.</p>
</div>
</body>
</html>`;

function refuse(status: number, error: string, description: string): Response {
  return Response.json({ error, error_description: description }, { status });
}

// token passes an exchange on to Supabase with the client secret: a code,
// only with its PKCE verifier and this Worker's redirect URL, or a refresh
// token, only for Leaflet's client ID.
async function token(request: Request, env: Env): Promise<Response> {
  if (!request.headers.get("content-type")?.startsWith("application/x-www-form-urlencoded")) {
    return refuse(415, "invalid_request", "Send the form url-encoded.");
  }
  const form = new URLSearchParams(await request.text());
  if (form.get("client_id") !== env.CLIENT_ID) {
    return refuse(401, "invalid_client", "This proxy serves Leaflet only.");
  }
  const out = new URLSearchParams({ grant_type: form.get("grant_type") ?? "" });
  switch (out.get("grant_type")) {
    case "authorization_code": {
      const code = form.get("code"), verifier = form.get("code_verifier");
      const redirect = new URL("/auth/supabase", request.url).href;
      if (!code || !verifier) return refuse(400, "invalid_request", "A code goes with its PKCE verifier.");
      if (form.get("redirect_uri") !== redirect) return refuse(400, "invalid_grant", "Unknown redirect URL.");
      out.set("code", code);
      out.set("code_verifier", verifier);
      out.set("redirect_uri", redirect);
      break;
    }
    case "refresh_token": {
      const refresh = form.get("refresh_token");
      if (!refresh) return refuse(400, "invalid_request", "No refresh token.");
      out.set("refresh_token", refresh);
      break;
    }
    default:
      return refuse(400, "unsupported_grant_type", "Only codes and refresh tokens.");
  }
  const res = await fetch(env.SUPABASE_TOKEN_URL ?? "https://api.supabase.com/v1/oauth/token", {
    method: "POST",
    headers: {
      "Content-Type": "application/x-www-form-urlencoded",
      "Accept": "application/json",
      "Authorization": "Basic " + btoa(`${env.CLIENT_ID}:${env.CLIENT_SECRET}`),
    },
    body: out,
  });
  // Supabase's answer as it is, but for headers that are not the proxy's
  // to pass on.
  return new Response(res.body, {
    status: res.status,
    headers: { "Content-Type": res.headers.get("Content-Type") ?? "application/json", "Cache-Control": "no-store" },
  });
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const { pathname } = new URL(request.url);
    if ((pathname === "/auth/supabase" || pathname === "/auth/supabase/") && request.method === "GET") {
      return new Response(PAGE, {
        headers: {
          "Content-Type": "text/html; charset=utf-8",
          "Cache-Control": "no-store",
          "Referrer-Policy": "no-referrer",
        },
      });
    }
    if (pathname === "/token" && request.method === "POST") {
      if (!env.CLIENT_SECRET) return refuse(500, "server_error", "The proxy has no client secret.");
      return await token(request, env);
    }
    return new Response("Not found", { status: 404 });
  },
};
