import { createClient, type SupabaseClient, type User } from "jsr:@supabase/supabase-js@2.118.0";
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

export function userClient(req: Request): SupabaseClient {
  const token = bearer(req);
  return createClient(env("SUPABASE_URL"), env("SUPABASE_ANON_KEY"), {
    global: { headers: { Authorization: `Bearer ${token}` } },
    auth: { persistSession: false, autoRefreshToken: false },
  });
}

export function serviceClient(): SupabaseClient {
  return createClient(
    env("SUPABASE_URL"),
    env("SUPABASE_SERVICE_ROLE_KEY"),
    { auth: { persistSession: false, autoRefreshToken: false } },
  );
}

export interface Clients {
  user(req: Request): SupabaseClient;
  service(): SupabaseClient;
}

export const projectClients: Clients = { user: userClient, service: serviceClient };

export async function requireUser(
  req: Request,
  client: SupabaseClient,
): Promise<User> {
  const { data, error } = await client.auth.getUser(bearer(req));
  if (error || !data.user) throw new HttpError(401, "Sign in first");
  return data.user;
}
