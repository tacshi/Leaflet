import { createClient, type SupabaseClient, type User } from "jsr:@supabase/supabase-js@2.118.0";
import { HttpError } from "./cors.ts";
import { expiresBefore } from "./deadline.ts";

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

/**
 * How a function reaches the project: as the caller, and as itself. Tests
 * give their own, on a project of their own.
 */
export interface Clients {
  user(req: Request): SupabaseClient;
  service(): SupabaseClient;
}

/** The project the function runs in, from the environment the platform gives it. */
export const projectClients: Clients = { user: userClient, service: serviceClient };

export async function requireUser(
  req: Request,
  client: SupabaseClient,
): Promise<User> {
  const { data, error } = await client.auth.getUser(bearer(req));
  if (error || !data.user) throw new HttpError(401, "Sign in first");
  return data.user;
}

/**
 * Refuses a token that runs out before `until` (ms since the epoch), the
 * end of work that writes as the caller: PostgREST checks the token on
 * each request, so the work's last writes would fail, its status among
 * them. The 401 names the JWT, as the gateway's does for an expired one,
 * so the app renews it and asks again (internal/remote). Call it before
 * the work claims anything, so asking again finds it as it was.
 */
export function requireTokenUntil(req: Request, until: number): void {
  if (expiresBefore(bearer(req), until)) {
    throw new HttpError(401, "JWT expires before this would finish: renew it and try again");
  }
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

/** What a caller hears when a provider changed between its reads, or under its save. */
export const PROVIDER_MOVED = "This provider was changed just now, while this was asked. Try again.";

/** Whether a database error is a provider's row having moved (provider_moved). */
export function providerMoved(error: { message: string }): boolean {
  return error.message.includes("provider_moved");
}

/**
 * A provider's key, for the row the caller read: given only while the
 * provider still has that row's base URL and preset, read with them in
 * one statement, so a save that moves the provider and gives it a new key
 * after the caller's read never has that key sent to the old address.
 */
export async function providerSecret(
  sc: SupabaseClient,
  row: Pick<ProviderRow, "id" | "base_url" | "preset">,
): Promise<string | null> {
  const { data, error } = await sc.rpc("get_provider_secret", {
    p_provider_id: row.id,
    p_base_url: row.base_url,
    p_preset: row.preset,
  });
  if (error) throw providerMoved(error) ? new HttpError(409, PROVIDER_MOVED) : new Error(error.message);
  return (data as string | null) ?? null;
}
