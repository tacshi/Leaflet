-- A recording made on someone else's page stays with the page when its
-- recorder's account is deleted: the ON DELETE SET NULL cascade clears its
-- created_by. Its audio stays too, but recordings_objects_shared_read
-- matched the object's folder against created_by, so no one could read it
-- any more. Here those who can view the page read it still: its path names
-- the row's ID, and no caller can change the path of such a row. Nor can a
-- caller clear created_by (keep_recording_creator), so only the cascade
-- makes such a row.
--
-- And the account function kept that audio by downloading each object and
-- uploading it again as the service role's, so that the account no longer
-- owned it, all in the function's memory. release_kept_recordings, for the
-- service role only, gives up the account's ownership of the objects in
-- place instead, and says which they are, for the function to leave.

drop policy recordings_objects_shared_read on storage.objects;
create policy recordings_objects_shared_read on storage.objects for select to authenticated
  using (
    bucket_id = 'recordings'
    and exists (
      select 1 from public.recordings r
      where r.audio_path = storage.objects.name
        and (r.created_by is null or split_part(storage.objects.name, '/', 1) = r.created_by::text)
        and split_part(split_part(storage.objects.name, '/', 2), '.', 1) = r.id::text
        and public.can_view_page(r.page_id)
    )
  );

create or replace function public.keep_recording_creator()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if auth.uid() is not null then
    new.created_by := old.created_by;
    -- A recording whose recorder is gone keeps its audio where it is.
    if old.created_by is null and new.audio_path is distinct from old.audio_path then
      raise exception 'recording_audio_kept' using errcode = 'insufficient_privilege';
    end if;
  end if;
  return new;
end;
$$;

-- The audio of the recordings p_user_id made on pages in others' spaces,
-- at its own path ({user}/{recording}.{ext}), which stays when the account
-- goes: the account no longer owns the objects after this, and their
-- paths are returned.
create or replace function public.release_kept_recordings(p_user_id uuid)
returns setof text
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_paths text[];
begin
  select coalesce(array_agg(r.audio_path), '{}') into v_paths
  from public.recordings r
  join public.pages p on p.id = r.page_id
  join public.spaces s on s.id = p.space_id
  where r.created_by = p_user_id
    and s.owner_id <> p_user_id
    and split_part(r.audio_path, '/', 1) = p_user_id::text
    and split_part(split_part(r.audio_path, '/', 2), '.', 1) = r.id::text
    and split_part(r.audio_path, '/', 3) = '';
  update storage.objects set owner = null
  where bucket_id = 'recordings' and name = any (v_paths);
  -- Newer Storage keeps the owner in owner_id as well.
  if exists (
    select 1 from information_schema.columns
    where table_schema = 'storage' and table_name = 'objects' and column_name = 'owner_id'
  ) then
    execute 'update storage.objects set owner_id = null where bucket_id = ''recordings'' and name = any ($1)'
      using v_paths;
  end if;
  return query select unnest(v_paths);
end;
$$;

revoke execute on function public.release_kept_recordings(uuid) from public, anon, authenticated;
grant execute on function public.release_kept_recordings(uuid) to service_role;
