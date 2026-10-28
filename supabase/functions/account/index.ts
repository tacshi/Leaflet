// Account (protocol §5): delete the caller's account, their storage objects,
// and (by cascade) their space, pages, recordings and model connections.
// Pages shared with them stay with their owners.

import type { SupabaseClient } from "jsr:@supabase/supabase-js@2";
import { errorResponse, HttpError, json, preflight, readJson } from "../_shared/cors.ts";
import { requireUser, serviceClient, userClient } from "../_shared/supabase.ts";

Deno.serve(async (req) => {
  const pre = preflight(req);
  if (pre) return pre;
  try {
    const body = await readJson<{ action?: string }>(req);
    if (body.action !== "delete") throw new HttpError(400, "Unknown action");
    const user = await requireUser(req, userClient(req));
    const sc = serviceClient();

    const { data: spaces, error } = await sc.from("spaces").select("id").eq("owner_id", user.id);
    if (error) throw new Error(error.message);

    // Storage objects are removed through the Storage API before the rows
    // that name them go with the account.
    await removePrefix(sc, "recordings", user.id);
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
});

/** Removes every object under `prefix/` in a bucket, folders included. */
async function removePrefix(sc: SupabaseClient, bucket: string, prefix: string): Promise<void> {
  const paths: string[] = [];
  const visit = async (folder: string) => {
    for (let offset = 0;; offset += 1000) {
      const { data, error } = await sc.storage.from(bucket).list(folder, { limit: 1000, offset });
      if (error) throw new Error(`${bucket}: ${error.message}`);
      for (const item of data ?? []) {
        const path = `${folder}/${item.name}`;
        // Folders come back without an id.
        if (item.id === null) await visit(path);
        else paths.push(path);
      }
      if (!data || data.length < 1000) break;
    }
  };
  await visit(prefix);
  await removePaths(sc, bucket, paths);
}

/** Audio paths of recordings made by others on pages in a space. */
async function othersRecordings(sc: SupabaseClient, spaceId: string, userId: string): Promise<string[]> {
  const paths: string[] = [];
  for (let from = 0;; from += 1000) {
    const { data, error } = await sc
      .from("recordings")
      .select("audio_path, created_by, pages!inner(space_id)")
      .eq("pages.space_id", spaceId)
      .not("audio_path", "is", null)
      .order("id")
      .range(from, from + 999);
    if (error) throw new Error(`recordings: ${error.message}`);
    for (const r of data ?? []) {
      if (r.audio_path && r.created_by !== userId) paths.push(r.audio_path);
    }
    if (!data || data.length < 1000) break;
  }
  return paths;
}

async function removePaths(sc: SupabaseClient, bucket: string, paths: string[]): Promise<void> {
  for (let i = 0; i < paths.length; i += 100) {
    const { error } = await sc.storage.from(bucket).remove(paths.slice(i, i + 100));
    if (error) throw new Error(`${bucket}: ${error.message}`);
  }
}
