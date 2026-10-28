// Model providers (protocol §5): save a connection (its key goes to Vault),
// list a provider's models, test it (including tool calling), delete it.
// Clients cannot write model_providers themselves (20261028000000): rows are
// written here, with the service role, once their base URL is checked.

import type { SupabaseClient, User } from "jsr:@supabase/supabase-js@2";
import { errorResponse, HttpError, json, preflight, readJson } from "../_shared/cors.ts";
import {
  loadProvider,
  type ProviderRow,
  providerSecret,
  requireUser,
  serviceClient,
  userClient,
} from "../_shared/supabase.ts";
import {
  chatCompletion,
  type Connection,
  listModels,
  LOCAL_PRESETS,
  normalizeBaseUrl,
  PRESET_BASE_URLS,
  PRESET_NAMES,
  ProviderError,
  type ToolDef,
} from "../_shared/openai.ts";
import { checkPublicUrl, UnsafeUrlError } from "../_shared/net.ts";

const PRESETS = new Set(Object.keys(PRESET_NAMES));
const TIMEOUT_MS = 20_000;

interface ProviderInput {
  id?: string;
  preset: string;
  name?: string;
  base_url?: string;
  auth_kind?: "account" | "api_key" | "none";
  api_key?: string;
  default_model?: string | null;
}

interface Body {
  action: "save" | "models" | "test" | "delete";
  provider?: ProviderInput;
  provider_id?: string;
  base_url?: string;
  api_key?: string;
  preset?: string;
  model?: string;
  id?: string;
}

Deno.serve(async (req) => {
  const pre = preflight(req);
  if (pre) return pre;
  try {
    const body = await readJson<Body>(req);
    const uc = userClient(req);
    const user = await requireUser(req, uc);
    const sc = serviceClient();
    switch (body.action) {
      case "save":
        return json({ provider: await save(uc, sc, user, body.provider) });
      case "models":
        return json(await models(uc, sc, user, body));
      case "test":
        return json(await test(uc, sc, user, body));
      case "delete": {
        const id = body.id ?? body.provider_id;
        if (!id) throw new HttpError(400, "id is required");
        const { error } = await uc.from("model_providers").delete().eq("id", id);
        if (error) throw new Error(error.message);
        return json({ ok: true });
      }
      default:
        throw new HttpError(400, "Unknown action");
    }
  } catch (e) {
    return errorResponse(e);
  }
});

async function save(
  uc: SupabaseClient,
  sc: SupabaseClient,
  user: User,
  input?: ProviderInput,
): Promise<ProviderRow> {
  if (!input || !PRESETS.has(input.preset)) throw new HttpError(400, "Unknown provider preset");
  const local = LOCAL_PRESETS.has(input.preset);
  if (local && input.api_key) {
    throw new HttpError(400, "Keys for local providers stay on your device");
  }
  const authKind = input.auth_kind ?? (local ? "none" : "api_key");
  if (authKind === "account") {
    throw new HttpError(400, "Account sign-in is not available for this provider yet. Use an API key.");
  }
  let baseUrl: string;
  try {
    baseUrl = normalizeBaseUrl(input.base_url ?? "", input.preset);
  } catch {
    throw new HttpError(400, "Enter the provider's base URL");
  }
  // Local providers are only listed; the server never calls them.
  if (!local) {
    try {
      await checkPublicUrl(baseUrl);
    } catch (e) {
      throw new HttpError(400, e instanceof UnsafeUrlError ? e.message : "Enter the provider's base URL");
    }
  }

  const row = {
    preset: input.preset,
    name: input.name?.trim() || PRESET_NAMES[input.preset],
    base_url: baseUrl,
    auth_kind: authKind,
    default_model: input.default_model ?? null,
    is_local: local,
  };

  let id = input.id;
  if (id) {
    const { data, error } = await sc.from("model_providers").update(row).eq("id", id).eq("user_id", user.id)
      .select("id");
    if (error) throw new Error(error.message);
    if (!data?.length) throw new HttpError(404, "Model provider not found");
  } else {
    const { data, error } = await sc.from("model_providers").insert({ ...row, user_id: user.id }).select("id")
      .single();
    if (error) throw new Error(error.message);
    id = (data as { id: string }).id;
  }

  if (input.api_key !== undefined && !local) {
    const { error } = await uc.rpc("set_provider_secret", {
      p_provider_id: id,
      p_secret: input.api_key.trim(),
    });
    if (error) throw new Error(error.message);
  }
  return await loadProvider(uc, id!);
}

async function connection(
  uc: SupabaseClient,
  sc: SupabaseClient,
  body: Body,
): Promise<{ conn: Connection; row?: ProviderRow }> {
  if (body.provider_id) {
    const row = await loadProvider(uc, body.provider_id);
    if (row.is_local || LOCAL_PRESETS.has(row.preset)) {
      throw new HttpError(400, "Local providers are reached from your device, not from the server");
    }
    const apiKey = body.api_key || await providerSecret(sc, row.id);
    return { conn: { baseUrl: normalizeBaseUrl(row.base_url, row.preset), apiKey, preset: row.preset }, row };
  }
  const preset = body.preset;
  if (preset && LOCAL_PRESETS.has(preset)) {
    throw new HttpError(400, "Local providers are reached from your device, not from the server");
  }
  const raw = body.base_url || (preset ? PRESET_BASE_URLS[preset] : "");
  if (!raw) throw new HttpError(400, "provider_id or base_url is required");
  return { conn: { baseUrl: normalizeBaseUrl(raw, preset), apiKey: body.api_key ?? null, preset } };
}

async function models(uc: SupabaseClient, sc: SupabaseClient, user: User, body: Body) {
  const { conn, row } = await connection(uc, sc, body);
  let ids: string[];
  try {
    ids = await listModels(conn, AbortSignal.timeout(TIMEOUT_MS));
  } catch (e) {
    throw new HttpError(502, `Could not list models: ${e instanceof Error ? e.message : e}`);
  }
  if (row) {
    await sc.from("model_providers").update({ models: ids }).eq("id", row.id).eq("user_id", user.id);
  }
  return { models: ids, supports_tools: row?.supports_tools ?? undefined };
}

const PING: ToolDef = {
  type: "function",
  function: {
    name: "ping",
    description: "Confirms the connection works.",
    parameters: {
      type: "object",
      properties: { ok: { type: "boolean" } },
      required: ["ok"],
    },
  },
};

async function test(uc: SupabaseClient, sc: SupabaseClient, user: User, body: Body) {
  const { conn, row } = await connection(uc, sc, body);

  let ids: string[] = [];
  let listError = "";
  try {
    ids = await listModels(conn, AbortSignal.timeout(TIMEOUT_MS));
  } catch (e) {
    listError = e instanceof Error ? e.message : String(e);
  }

  const model = body.model || row?.default_model || ids[0];
  if (!model) {
    return {
      ok: false,
      models: ids,
      supports_tools: false,
      message: listError ? `Could not reach the provider: ${listError}` : "Choose a model to test",
    };
  }

  let ok = false;
  let supportsTools = false;
  let message = "";
  try {
    const res = await chatCompletion(conn, {
      model,
      messages: [{ role: "user", content: 'Call the ping tool with {"ok": true}.' }],
      tools: [PING],
    }, AbortSignal.timeout(TIMEOUT_MS * 2));
    ok = true;
    supportsTools = !!res.message.tool_calls?.some((c) => c.function.name === "ping");
  } catch (e) {
    if (e instanceof ProviderError && e.rejectedTools) {
      try {
        await chatCompletion(conn, {
          model,
          messages: [{ role: "user", content: "Reply with the word ok." }],
        }, AbortSignal.timeout(TIMEOUT_MS * 2));
        ok = true;
      } catch (inner) {
        message = inner instanceof Error ? inner.message : String(inner);
      }
    } else {
      message = e instanceof Error ? e.message : String(e);
    }
  }

  if (ok) {
    const count = ids.length ? ` · ${ids.length} ${ids.length === 1 ? "model" : "models"} available` : "";
    message = supportsTools ? `Connected${count}` : `Connected${count} · chat only (no tool calling)`;
    if (row) {
      const patch: Record<string, unknown> = { supports_tools: supportsTools };
      if (ids.length) patch.models = ids;
      await sc.from("model_providers").update(patch).eq("id", row.id).eq("user_id", user.id);
    }
  }
  return { ok, models: ids, supports_tools: supportsTools, message };
}
