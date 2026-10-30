-- recordings_update checks edit access to the row's page before and after
-- an update, each on its own, so an editor of two pages could move someone
-- else's recording from one to the other, and with it who may play its
-- audio. A recording stays on the page it was made on: the app never moves
-- one (a copied page's recordings are new rows), so a caller's update that
-- changes page_id is refused. Writes with no caller (the service role)
-- keep what they set.

create or replace function public.keep_recording_page()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if auth.uid() is not null and new.page_id is distinct from old.page_id then
    raise exception 'recording_page_immutable' using errcode = '42501';
  end if;
  return new;
end;
$$;

drop trigger if exists recordings_keep_page on public.recordings;
create trigger recordings_keep_page before update on public.recordings
  for each row execute function public.keep_recording_page();
