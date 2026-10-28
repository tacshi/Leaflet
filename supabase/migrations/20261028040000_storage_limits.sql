-- Bucket limits, and recordings written only for a recording row.
--
-- Sizes: recordings are 16 kHz mono WAV from the app (about 1.9 MB a
-- minute), so 500 MiB is over four hours; files are any attachment; images
-- are images. A bucket's limit cannot raise the project's global upload
-- limit (config.toml [storage] file_size_limit locally, Storage settings on
-- a hosted project), which caps every bucket.

update storage.buckets
set file_size_limit = 500 * 1024 * 1024,
    allowed_mime_types = array['audio/*']
where id = 'recordings';

update storage.buckets
set file_size_limit = 200 * 1024 * 1024,
    allowed_mime_types = null
where id = 'files';

update storage.buckets
set file_size_limit = 50 * 1024 * 1024,
    allowed_mime_types = array['image/*']
where id = 'images';

-- recordings/{user_id}/{recording_id}.{ext}: the caller writes only an
-- object for a recording row they made, so the bucket holds no audio
-- without a row. They still read and delete anything in their folder.
create or replace function public.owns_recording_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_id text := split_part(split_part(p_name, '/', 2), '.', 1);
begin
  if v_uid is null
     or split_part(p_name, '/', 1) <> v_uid::text
     or split_part(p_name, '/', 3) <> ''
     or v_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then
    return false;
  end if;
  return exists (
    select 1 from public.recordings r
    where r.id = v_id::uuid and r.created_by = v_uid
  );
end;
$$;

drop policy if exists recordings_objects_own on storage.objects;
drop policy if exists recordings_objects_own_delete on storage.objects;
drop policy if exists recordings_objects_own_insert on storage.objects;
drop policy if exists recordings_objects_own_update on storage.objects;

create policy recordings_objects_own on storage.objects for select to authenticated
  using (bucket_id = 'recordings' and split_part(name, '/', 1) = (select auth.uid())::text);
create policy recordings_objects_own_delete on storage.objects for delete to authenticated
  using (bucket_id = 'recordings' and split_part(name, '/', 1) = (select auth.uid())::text);
create policy recordings_objects_own_insert on storage.objects for insert to authenticated
  with check (bucket_id = 'recordings' and public.owns_recording_path(name));
create policy recordings_objects_own_update on storage.objects for update to authenticated
  using (bucket_id = 'recordings' and split_part(name, '/', 1) = (select auth.uid())::text)
  with check (bucket_id = 'recordings' and public.owns_recording_path(name));
