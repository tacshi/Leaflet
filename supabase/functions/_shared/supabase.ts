import { createClient, type SupabaseClient, type User } from "jsr:@supabase/supabase-js@2";
import { HttpError } from "./cors.ts";

function env(name: string): string {
  const value = Deno.env.get(name);
  if (!value) throw new Error(`Missing environment variable ${name}`);
  return value;
}

function bearer(req: Request): string {
  const header = req.headers.get("Authorization") ?? "";
  const match = header.match(/^Bearer\s+(.+)$/i);
  if (!match) throw new HttpError(401, "Sign in first");
  return match[1];
}

/** A client that acts as the caller, so row-level security applies. */
export function userClient(req: Request): SupabaseClient {
  const token = bearer(req);
  return createClient(env("SUPABASE_URL"), env("SUPABASE_ANON_KEY"), {
    global: { headers: { Authorization: `Bearer ${token}` } },
    auth: { persistSession: false, autoRefreshToken: false },
  });
}

/** A client that bypasses row-level security. Use only for secrets and admin. */
export function serviceClient(): SupabaseClient {
  return createClient(
    env("SUPABASE_URL"),
    env("SUPABASE_SERVICE_ROLE_KEY"),
    { auth: { persistSession: false, autoRefreshToken: false } },
  );
}

export async function requireUser(
  req: Request,
  client: SupabaseClient,
): Promise<User> {
  const { data, error } = await client.auth.getUser(bearer(req));
  if (error || !data.user) throw new HttpError(401, "Sign in first");
  return data.user;
}

/** Columns of model_providers that clients (and user clients) may read. */
export const PROVIDER_COLUMNS =
  "id, user_id, preset, name, base_url, auth_kind, expires_at, has_secret, default_model, is_local, supports_tools, models, created_at, updated_at";

export interface ProviderRow {
  id: string;
  user_id: string;
  preset: string;
  name: string;
  base_url: string;
  auth_kind: "account" | "api_key" | "none";
  expires_at: string | null;
  has_secret: boolean;
  default_model: string | null;
  is_local: boolean;
  supports_tools: boolean | null;
  models: string[];
  created_at: string;
  updated_at: string;
}

export async function loadProvider(
  uc: SupabaseClient,
  id: string,
): Promise<ProviderRow> {
  const { data, error } = await uc
    .from("model_providers")
    .select(PROVIDER_COLUMNS)
    .eq("id", id)
    .maybeSingle();
  if (error) throw new Error(error.message);
  if (!data) throw new HttpError(404, "Model provider not found");
  return data as unknown as ProviderRow;
}

export async function providerSecret(
  sc: SupabaseClient,
  providerId: string,
): Promise<string | null> {
  const { data, error } = await sc.rpc("get_provider_secret", {
    p_provider_id: providerId,
  });
  if (error) throw new Error(error.message);
  return (data as string | null) ?? null;
}
