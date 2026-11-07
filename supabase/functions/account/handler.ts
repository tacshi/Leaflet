// Account (protocol §5): delete the caller's account, their storage objects,
// and (by cascade) their space, pages, recordings and model connections.
// Pages shared with them stay with their owners, with the recordings the
// caller made on them, audio and all. index.ts serves it; tests give it a
// project of their own.

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

    // Storage objects are removed through the Storage API before the rows
    // that name them go with the account: the account is not deleted while
    // it owns any. The audio of recordings that stay, on others' pages, is
    // no longer the account's, in place, and stays. Their paths come as one
    // array, whole: as rows, the Data API would answer the first thousand
    // (max_rows), and the audio of the rest would be removed.
    const { data: kept, error: keptError } = await sc.rpc("release_kept_recordings", { p_user_id: user.id });
    if (keptError) throw new Error(`recordings: ${keptError.message}`);
    if (!Array.isArray(kept) || kept.some((path) => typeof path !== "string")) {
      throw new Error("recordings: the audio to keep came back as no list of paths");
    }
    await removePrefix(sc, "recordings", user.id, new Set(kept as string[]));
    for (const space of spaces ?? []) {
      // Others' recordings on this user's pages go with the pages.
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

/** Removes every object under `prefix/` in a bucket, folders included, but those in keep. */
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
        // Folders come back without an id.
        if (item.id === null) await visit(path);
        else if (!keep.has(path)) paths.push(path);
      }
      if (!data || data.length < 1000) break;
    }
  };
  await visit(prefix);
  await removePaths(sc, bucket, paths);
}

/**
 * Audio paths of recordings made by others on pages in a space. A row's
 * audio_path is the client's to set, so only a path that is the row's own,
 * recordings/{created_by}/{id}.{ext}, is removed: any other would let a
 * row name someone else's audio for the service role to delete.
 */
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

/**
 * Whether path is recording id's own: {createdBy}/{id}.{ext}. A recording
 * whose recorder's account went (createdBy null) keeps the path it had,
 * which no one can change since, in its recorder's folder.
 */
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
