-- keep_recording_creator kept any update that cleared created_by, so that
-- the ON DELETE SET NULL cascade, when the recorder's account is deleted,
-- could clear it. But any editor of the page could clear it too: its
-- viewers then lost the audio (recordings_objects_shared_read matches the
-- object's path against created_by), the recorder could no longer upload
-- or replace it (owns_recording_path needs created_by to be them), and
-- deleting the recorder's account skipped the row and left its object
-- behind. As pages_guard keeps pages.created_by and files_guard
-- files.created_by, a write with a caller now keeps it whatever it sets;
-- only writes with no caller (the cascade, and the service role) change it.

create or replace function public.keep_recording_creator()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if auth.uid() is not null then
    new.created_by := old.created_by;
  end if;
  return new;
end;
$$;
