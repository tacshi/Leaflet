import type { SupabaseClient } from "jsr:@supabase/supabase-js@2.118.0";
import { errorResponse, HttpError, json, preflight, readJson } from "../_shared/cors.ts";
import { type Clients, requireUser } from "../_shared/supabase.ts";

export async function handle(req: Request, clients: Clients): Promise<Response> {
  const pre = preflight(req);
  if (pre) return pre;
  try {
    const body = await readJson<{ action?: string }>(req);
    if (body.action !== "delete") throw new HttpError(400, "Unknown action");
    const user = await requireUser(req, clients.user(req));
    const sc = clients.service();

    const { data: spaces, error } = await sc.from("spaces").select("id").eq("owner_id", user.id);
    if (error) throw new Error(error.message);

    const { data: kept, error: keptError } = await sc.rpc("release_kept_recordings", { p_user_id: user.id });
    if (keptError) throw new Error(`recordings: ${keptError.message}`);
    if (!Array.isArray(kept) || kept.some((path) => typeof path !== "string")) {
      throw new Error("recordings: the audio to keep came back as no list of paths");
    }
    await removePrefix(sc, "recordings", user.id, new Set(kept as string[]));
    for (const space of spaces ?? []) {
      await removePaths(sc, "recordings", await othersRecordings(sc, space.id, user.id));
      await removePrefix(sc, "files", space.id);
      await removePrefix(sc, "images", space.id);
    }

    const { error: deleteError } = await sc.auth.admin.deleteUser(user.id);
    if (deleteError) throw new Error(deleteError.message);
    return json({ ok: true });
  } catch (e) {
    return errorResponse(e);
  }
}

async function removePrefix(
  sc: SupabaseClient,
  bucket: string,
  prefix: string,
  keep: Set<string> = new Set(),
): Promise<void> {
  const paths: string[] = [];
  const visit = async (folder: string) => {
    for (let offset = 0;; offset += 1000) {
      const { data, error } = await sc.storage.from(bucket).list(folder, { limit: 1000, offset });
      if (error) throw new Error(`${bucket}: ${error.message}`);
      for (const item of data ?? []) {
        const path = `${folder}/${item.name}`;
        if (item.id === null) await visit(path);
        else if (!keep.has(path)) paths.push(path);
      }
      if (!data || data.length < 1000) break;
    }
  };
  await visit(prefix);
  await removePaths(sc, bucket, paths);
}

async function othersRecordings(sc: SupabaseClient, spaceId: string, userId: string): Promise<string[]> {
  const paths: string[] = [];
  for (let from = 0;; from += 1000) {
    const { data, error } = await sc
      .from("recordings")
      .select("id, audio_path, created_by, pages!inner(space_id)")
      .eq("pages.space_id", spaceId)
      .not("audio_path", "is", null)
      .order("id")
      .range(from, from + 999);
    if (error) throw new Error(`recordings: ${error.message}`);
    for (const r of data ?? []) {
      if (r.audio_path && r.created_by !== userId && ownAudioPath(r.audio_path, r.created_by, r.id)) {
        paths.push(r.audio_path);
      }
    }
    if (!data || data.length < 1000) break;
  }
  return paths;
}

export function ownAudioPath(path: string, createdBy: string | null, id: string): boolean {
  const [folder, name, ...rest] = path.split("/");
  if (rest.length > 0 || !folder || (createdBy !== null && folder !== createdBy) || !name) return false;
  const dot = name.indexOf(".");
  return (dot < 0 ? name : name.slice(0, dot)) === id;
}

async function removePaths(sc: SupabaseClient, bucket: string, paths: string[]): Promise<void> {
  for (let i = 0; i < paths.length; i += 100) {
    const { error } = await sc.storage.from(bucket).remove(paths.slice(i, i + 100));
    if (error) throw new Error(`${bucket}: ${error.message}`);
  }
}
