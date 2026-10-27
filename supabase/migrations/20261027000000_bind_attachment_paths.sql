-- A recording or file row lets those who can view its page read the object
-- at its path, so the path must be one of the row's own: anyone may write a
-- row on their own page, naming any path.

-- recordings/{created_by}/{id}.{ext}: a recording is its recorder's, who
-- cannot be named for someone else.
drop policy recordings_insert on public.recordings;
create policy recordings_insert on public.recordings for insert to authenticated
  with check (public.can_edit_page(page_id) and created_by = (select auth.uid()));

create or replace function public.keep_recording_creator()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  new.created_by := old.created_by;
  return new;
end;
$$;

create trigger recordings_keep_creator before update on public.recordings
  for each row execute function public.keep_recording_creator();

drop policy recordings_objects_shared_read on storage.objects;
create policy recordings_objects_shared_read on storage.objects for select to authenticated
  using (
    bucket_id = 'recordings'
    and exists (
      select 1 from public.recordings r
      where r.audio_path = storage.objects.name
        and split_part(storage.objects.name, '/', 1) = r.created_by::text
        and split_part(split_part(storage.objects.name, '/', 2), '.', 1) = r.id::text
        and public.can_view_page(r.page_id)
    )
  );

-- files/{space_id}/{id}/{name}: only the space's owner writes its rows.
drop policy files_objects_shared_read on storage.objects;
create policy files_objects_shared_read on storage.objects for select to authenticated
  using (
    bucket_id = 'files'
    and exists (
      select 1 from public.files f
      where f.storage_path = storage.objects.name
        and split_part(storage.objects.name, '/', 1) = f.space_id::text
        and split_part(storage.objects.name, '/', 2) = f.id::text
        and f.page_id is not null and public.can_view_page(f.page_id)
    )
  );
