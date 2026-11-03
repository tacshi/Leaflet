import { assertEquals } from "jsr:@std/assert@1";
import { handle, type ProviderDeps } from "./handler.ts";
import type { ChatResult } from "../_shared/openai.ts";
import { Project } from "../_testing/project.ts";

/** Ann's OpenAI provider, its key saved, its default model gpt. */
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
  }];
  const secrets: string[] = [];
  p.rpcs.set_provider_secret = (args) => {
    secrets.push(String(args.p_secret));
    return null;
  };
  p.rpcs.get_provider_secret = () => "sk-saved";
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
