import { assertEquals } from "jsr:@std/assert@1";
import { handle, type ProviderDeps } from "./handler.ts";
import type { ChatResult } from "../_shared/openai.ts";
import { Project, type Row } from "../_testing/project.ts";

/**
 * Ann's OpenAI provider, its key saved (key, which no client reads), its
 * default model gpt; and the keys saved, in turn.
 */
function project(): { p: Project; secrets: string[] } {
  const p = new Project();
  p.user("ann", "ann-token");
  p.tables.model_providers = [{
    id: "openai",
    user_id: "ann",
    preset: "openai",
    name: "OpenAI",
    base_url: "https://api.openai.com/v1",
    auth_kind: "api_key",
    has_secret: true,
    default_model: "gpt",
    is_local: false,
    supports_tools: true,
    models: [],
    key: "sk-saved",
  }];
  const secrets: string[] = [];
  // The row the key is for, as the database finds it: none moved from the
  // base URL and preset the caller gives. A key is read of no local one.
  const at = (args: Row, read = false): Row | undefined => {
    const saved = p.tables.model_providers.find((r) => r.id === args.p_provider_id);
    if (
      saved && args.p_base_url != null &&
      (saved.base_url !== args.p_base_url || saved.preset !== args.p_preset || (read && saved.is_local))
    ) {
      throw new Error("provider_moved");
    }
    return saved;
  };
  p.rpcs.set_provider_secret = (args, caller) => {
    const saved = at(args);
    if (saved?.user_id !== caller) throw new Error("provider_not_found");
    secrets.push(String(args.p_secret));
    saved.key = args.p_secret || null;
    saved.has_secret = args.p_secret !== "";
    return null;
  };
  p.rpcs.get_provider_secret = (args) => at(args, true)?.key ?? null;
  return { p, secrets };
}

/** A provider whose models call tools when callsTools says so. */
function deps(p: Project, callsTools: (model: string) => boolean = () => true): ProviderDeps {
  return {
    clients: p.clients(),
    checkUrl: (raw: string) => Promise.resolve(new URL(raw)),
    listModels: () => Promise.resolve(["gpt", "gpt-mini"]),
    chat: (_conn, request): Promise<ChatResult> =>
      Promise.resolve({
        message: {
          role: "assistant",
          content: null,
          tool_calls: callsTools(request.model)
            ? [{ id: "c", type: "function", function: { name: "ping", arguments: '{"ok":true}' } }]
            : undefined,
        },
        finishReason: "stop",
      }),
  };
}

async function call(
  p: Project,
  d: ProviderDeps,
  body: unknown,
): Promise<{ status: number; body: Record<string, unknown> }> {
  const res = await handle(p.request("ann-token", body), d);
  return { status: res.status, body: await res.json() };
}

const row = (p: Project) => p.tables.model_providers[0];

// A saved key goes only to the address it was saved for: moving the
// provider elsewhere without a new key is refused and changes nothing,
// and with a new key it goes.
Deno.test("a saved key stays with its address", async () => {
  const { p, secrets } = project();
  const moved = { id: "openai", preset: "openai", base_url: "https://attacker.example/v1" };
  const refused = await call(p, deps(p), { action: "save", provider: moved });
  assertEquals(refused.status, 400);
  assertEquals(row(p).base_url, "https://api.openai.com/v1");
  assertEquals(secrets, []);

  const kept = await call(p, deps(p), {
    action: "save",
    provider: { id: "openai", preset: "openai", base_url: "api.openai.com/v1/", default_model: "gpt" },
  });
  assertEquals(kept.status, 200);
  const rekeyed = await call(p, deps(p), { action: "save", provider: { ...moved, api_key: " sk-new " } });
  assertEquals(rekeyed.status, 200);
  assertEquals(row(p).base_url, "https://attacker.example/v1");
  assertEquals(secrets, ["sk-new"]);
});

// A key that is no string is no key: the save is refused before the row
// is read or written, so the saved key never sits at the new address.
Deno.test("a key that is no string is refused before anything is written", async () => {
  const { p, secrets } = project();
  for (const api_key of [null, 42, { key: "sk-new" }]) {
    const refused = await call(p, deps(p), {
      action: "save",
      provider: { id: "openai", preset: "openai", base_url: "https://attacker.example/v1", api_key },
    });
    assertEquals(refused.status, 400);
  }
  assertEquals(row(p).base_url, "https://api.openai.com/v1");
  assertEquals(secrets, []);
  assertEquals(p.requests.filter((r) => !r.startsWith("GET /auth/")), []);
});

// A base URL a URL parser reads otherwise than it looks (a backslash,
// read as a slash, so the host is evil.example; a tab, dropped; a letter
// past ASCII, mapped) is refused, with a new key or without, before
// anything is written.
Deno.test("a base URL is what a parser reads it as", async () => {
  const { p, secrets } = project();
  for (
    const base_url of [
      "https://evil.example\\@api.openai.com/v1",
      "https://api.openai.com\t.evil.example/v1",
      "https://аpi.openai.com/v1",
    ]
  ) {
    for (const provider of [{ id: "openai", api_key: "sk-new" }, {}]) {
      const refused = await call(p, deps(p), { action: "save", provider: { preset: "openai", base_url, ...provider } });
      assertEquals(refused.status, 400);
    }
  }
  assertEquals([p.tables.model_providers.length, row(p).base_url, secrets], [1, "https://api.openai.com/v1", []]);
  assertEquals(p.requests.filter((r) => !r.startsWith("GET")), []);
});

// A provider made local, at any address, takes no key, and loses the one
// it had rather than keeping it there; the server never calls it.
Deno.test("a provider made local loses its key", async () => {
  const { p, secrets } = project();
  const listed: string[] = [];
  const d = { ...deps(p), listModels: (conn: { baseUrl: string }) => (listed.push(conn.baseUrl), Promise.resolve([])) };
  const keyed = await call(p, d, {
    action: "save",
    provider: { id: "openai", preset: "ollama", base_url: "https://attacker.example/v1", api_key: "sk-new" },
  });
  assertEquals(keyed.status, 400);
  assertEquals(row(p).preset, "openai");

  const saved = await call(p, d, {
    action: "save",
    provider: { id: "openai", preset: "ollama", base_url: "https://attacker.example/v1" },
  });
  assertEquals(saved.status, 200);
  assertEquals(secrets, [""]);
  assertEquals([row(p).base_url, row(p).is_local, row(p).has_secret], ["https://attacker.example/v1", true, false]);
  assertEquals((await call(p, d, { action: "models", provider_id: "openai" })).status, 400);
  assertEquals(listed, []);
});

// A test of a model other than the provider's default says nothing of the
// default: only the default's result is kept, and a new default is not
// tested yet.
Deno.test("whether tools work is kept for the default model only", async () => {
  const { p } = project();
  const d = deps(p, (model) => model !== "gpt-mini");
  const mini = await call(p, d, { action: "test", provider_id: "openai", model: "gpt-mini" });
  assertEquals([mini.body.ok, mini.body.supports_tools], [true, false]);
  assertEquals(row(p).supports_tools, true);
  assertEquals(row(p).models, ["gpt", "gpt-mini"]);

  await call(p, d, { action: "save", provider: { id: "openai", preset: "openai", default_model: "gpt-mini" } });
  assertEquals(row(p).supports_tools, null);
  await call(p, d, { action: "test", provider_id: "openai" });
  assertEquals(row(p).supports_tools, false);
});

// A save that moves the provider and gives it a new key lands between a
// call's read of the provider and its read of the key: the new key goes
// nowhere, least of all to the old address, and the call is told to ask
// again.
Deno.test("a key saved as its provider is called goes not to its old address", async () => {
  const { p } = project();
  const read = p.rpcs.get_provider_secret;
  p.rpcs.get_provider_secret = (args, caller) => {
    Object.assign(row(p), { base_url: "https://elsewhere.example/v1", key: "sk-new" });
    return read(args, caller);
  };
  const sent: unknown[] = [];
  const d: ProviderDeps = {
    ...deps(p),
    listModels: (conn) => (sent.push(conn), Promise.resolve(["gpt"])),
    chat: (conn) => (sent.push(conn), Promise.reject(new Error("unused"))),
  };
  for (const action of ["models", "test"]) {
    Object.assign(row(p), { base_url: "https://api.openai.com/v1", key: "sk-saved" });
    const res = await call(p, d, { action, provider_id: "openai" });
    assertEquals(res.status, 409);
  }
  assertEquals(sent, []);
});

// Another save moves the provider between this save's write of its row
// and of its key: the key, which was for the address this save wrote,
// stays off the one the other did, and this save is told to go again.
Deno.test("a key saved as another save moves its provider stays off that address", async () => {
  const { p, secrets } = project();
  const write = p.rpcs.set_provider_secret;
  p.rpcs.set_provider_secret = (args, caller) => {
    Object.assign(row(p), { base_url: "https://other.example/v1", key: null, has_secret: false });
    return write(args, caller);
  };
  const res = await call(p, deps(p), {
    action: "save",
    provider: { id: "openai", preset: "openai", base_url: "https://new.example/v1", api_key: "sk-new" },
  });
  assertEquals(res.status, 409);
  assertEquals([row(p).base_url, row(p).key, row(p).has_secret, secrets], [
    "https://other.example/v1",
    null,
    false,
    [],
  ]);

  // Made local as another save moves it with a key: that key stays.
  p.rpcs.set_provider_secret = (args, caller) => {
    Object.assign(row(p), { base_url: "https://other.example/v1", is_local: false, preset: "openai", key: "sk-b" });
    return write(args, caller);
  };
  Object.assign(row(p), { key: "sk-a", has_secret: true });
  const local = await call(p, deps(p), {
    action: "save",
    provider: { id: "openai", preset: "ollama", base_url: "http://localhost:11434/v1" },
  });
  assertEquals(local.status, 409);
  assertEquals([row(p).base_url, row(p).key, secrets], ["https://other.example/v1", "sk-b", []]);
});
