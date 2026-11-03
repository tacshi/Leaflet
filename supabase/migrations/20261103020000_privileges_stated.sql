-- Who may read and write each table, and call each function, came from
-- the defaults a Supabase project gives the roles of its Data API: every
-- privilege on every new table and function, to anon, authenticated and
-- service_role. The migrations only took some away. A project whose
-- defaults grant less would have refused the app everything, and one whose
-- defaults grant more would have had it pass unseen. The privileges on
-- Leaflet's tables and functions are now stated here in full, as the
-- policies need them, and scripts/migrations/access.sql checks them: a
-- table or function added later states its own, or the check fails.
--
-- * anon, the Data API signed out, has nothing.
-- * authenticated, the app, has the writes the policies allow and no more:
--   no TRUNCATE, REFERENCES or TRIGGER, which no policy covers, and none
--   of the writes taken away before (model_providers but reading some of
--   its columns and deleting, page_permissions but reading and deleting,
--   comments but deleting, stt_secrets and page_email_invites not at all).
--   It calls the RPCs and the helpers the policies call.
-- * service_role, the Functions, reads and writes every table, and alone
--   calls the functions that read secrets or release audio.
-- Trigger functions need no grant: a trigger runs its function whoever
-- fires it. Only Leaflet's own are named: a project may hold others'.

grant usage on schema public to authenticated, service_role;

do $$
declare
  v_tables constant text[] := array[
    'ai_jobs', 'comments', 'devices', 'files', 'folders', 'model_providers', 'page_content',
    'page_email_invites', 'page_history', 'page_permissions', 'pages', 'profiles', 'recordings',
    'spaces', 'stt_secrets', 'stt_settings'
  ];
  v_functions constant text[] := array[
    'accept_page_invite', 'bump_revision', 'can_edit_page', 'can_view_image_path', 'can_view_page',
    'can_write_file_path', 'claim_email_invites', 'comments_guard', 'decline_page_invite', 'files_guard',
    'folders_guard', 'get_provider_secret', 'get_stt_secret', 'guard_page_permission_acceptance',
    'handle_new_user', 'has_space_access', 'has_stt_secret', 'is_space_owner', 'keep_recording_audio_path',
    'keep_recording_creator', 'keep_recording_page', 'model_providers_cleanup', 'owns_recording_path',
    'owns_space_path', 'page_ancestors', 'page_history_since', 'page_invites', 'page_members', 'page_role',
    'pages_guard', 'pages_touch_moved', 'prune_page_history', 'release_kept_recordings',
    'scrub_deleted_comment', 'set_provider_secret', 'set_stt_secret', 'set_updated_at', 'share_page',
    'shared_chain', 'shares_page_with', 'stamp_page_content_writer', 'stamp_page_history_writer',
    'stamp_page_permission_inviter', 'stt_secrets_cleanup', 'tags_in_bounds', 'unshare_page', 'uuid_or_null'
  ];
  t text;
  f regprocedure;
begin
  foreach t in array v_tables loop
    execute format('revoke all on public.%I from public, anon, authenticated, service_role', t);
    execute format('grant select, insert, update, delete on public.%I to service_role', t);
  end loop;
  for f in
    select p.oid::regprocedure from pg_proc p
    where p.pronamespace = 'public'::regnamespace and p.proname = any (v_functions)
  loop
    execute format('revoke execute on function %s from public, anon, authenticated, service_role', f);
  end loop;
end;
$$;

grant select, insert, update, delete on
  public.spaces, public.folders, public.pages, public.page_content, public.recordings,
  public.files, public.ai_jobs, public.stt_settings, public.devices
  to authenticated;
grant select, insert, update on public.profiles, public.comments to authenticated;
grant select, insert on public.page_history to authenticated;
grant select, delete on public.page_permissions to authenticated;
-- Clients never read Vault secret IDs: column by column (no `*`).
grant select (
  id, user_id, preset, name, base_url, auth_kind, expires_at, has_secret,
  default_model, is_local, supports_tools, models, created_at, updated_at
) on public.model_providers to authenticated;
grant delete on public.model_providers to authenticated;

-- The helpers the policies call, as the caller.
grant execute on function
  public.is_space_owner(uuid), public.page_role(uuid), public.can_view_page(uuid),
  public.can_edit_page(uuid), public.has_space_access(uuid), public.shares_page_with(uuid),
  public.page_history_since(uuid), public.owns_space_path(text), public.can_view_image_path(text),
  public.owns_recording_path(text), public.can_write_file_path(text)
  to authenticated;
-- pages' tags_in_bounds check.
grant execute on function public.tags_in_bounds(text[]) to authenticated, service_role;
-- The RPCs.
grant execute on function
  public.share_page(uuid, text, text), public.unshare_page(uuid, uuid), public.page_members(uuid),
  public.page_invites(), public.accept_page_invite(uuid), public.decline_page_invite(uuid),
  public.set_provider_secret(uuid, text), public.set_stt_secret(text, text), public.has_stt_secret(text)
  to authenticated;
-- The Functions' own.
grant execute on function
  public.get_provider_secret(uuid), public.get_stt_secret(uuid, text), public.release_kept_recordings(uuid)
  to service_role;
