import { assertEquals } from "jsr:@std/assert@1";
import worker, { type Env } from "./index.ts";

const ORIGIN = "https://leaflet-auth.example.workers.dev";

// withSupabase runs fn against a token endpoint that records what it was
// sent and answers with a token.
async function withSupabase(fn: (env: Env, sent: { auth?: string; form?: URLSearchParams }) => Promise<void>) {
  const sent: { auth?: string; form?: URLSearchParams } = {};
  const server = Deno.serve({ port: 0, onListen() {} }, async (req) => {
    sent.auth = req.headers.get("authorization") ?? undefined;
    sent.form = new URLSearchParams(await req.text());
    return Response.json({ access_token: "a", refresh_token: "r", expires_in: 3600, token_type: "Bearer" });
  });
  const env: Env = {
    CLIENT_ID: "leaflet-app",
    CLIENT_SECRET: "s3cret",
    SUPABASE_TOKEN_URL: `http://127.0.0.1:${server.addr.port}/v1/oauth/token`,
  };
  try {
    await fn(env, sent);
  } finally {
    await server.shutdown();
  }
}

function post(form: Record<string, string>): Request {
  return new Request(`${ORIGIN}/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams(form),
  });
}

Deno.test("the redirect page sends the browser on to the app", async () => {
  const res = await worker.fetch(new Request(`${ORIGIN}/auth/supabase?code=c&state=s`), {} as Env);
  assertEquals(res.status, 200);
  const page = await res.text();
  assertEquals(page.includes(`location.replace("http://127.0.0.1:51741/auth/callback" + location.search)`), true);
});

Deno.test("a code goes on with the secret, its verifier and the redirect URL", async () => {
  await withSupabase(async (env, sent) => {
    const res = await worker.fetch(
      post({
        grant_type: "authorization_code",
        client_id: "leaflet-app",
        code: "c",
        code_verifier: "v",
        redirect_uri: `${ORIGIN}/auth/supabase`,
      }),
      env,
    );
    assertEquals(res.status, 200);
    assertEquals((await res.json()).access_token, "a");
    assertEquals(sent.auth, "Basic " + btoa("leaflet-app:s3cret"));
    assertEquals(sent.form?.get("code_verifier"), "v");
    assertEquals(sent.form?.get("redirect_uri"), `${ORIGIN}/auth/supabase`);
    assertEquals(sent.form?.has("client_secret"), false);
  });
});

Deno.test("a refresh token goes on", async () => {
  await withSupabase(async (env, sent) => {
    const res = await worker.fetch(
      post({ grant_type: "refresh_token", client_id: "leaflet-app", refresh_token: "r" }),
      env,
    );
    assertEquals(res.status, 200);
    assertEquals(sent.form?.get("refresh_token"), "r");
  });
});

Deno.test("anything else is refused before Supabase", async () => {
  await withSupabase(async (env, sent) => {
    const cases: [Record<string, string>, number][] = [
      [{ grant_type: "authorization_code", client_id: "other", code: "c", code_verifier: "v" }, 401],
      [{ grant_type: "authorization_code", client_id: "leaflet-app", code: "c" }, 400],
      [
        {
          grant_type: "authorization_code",
          client_id: "leaflet-app",
          code: "c",
          code_verifier: "v",
          redirect_uri: "https://evil.example/cb",
        },
        400,
      ],
      [{ grant_type: "client_credentials", client_id: "leaflet-app" }, 400],
    ];
    for (const [form, status] of cases) {
      const res = await worker.fetch(post(form), env);
      assertEquals(res.status, status, JSON.stringify(form));
      await res.body?.cancel();
    }
    assertEquals(sent.form, undefined);
    const res = await worker.fetch(new Request(`${ORIGIN}/token`), env);
    assertEquals(res.status, 404);
  });
});
