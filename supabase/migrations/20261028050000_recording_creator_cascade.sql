-- keep_recording_creator put created_by back on every update, including the
-- ON DELETE SET NULL cascade when the recorder's account is deleted, which
-- left the row naming an account that no longer exists. An update that
-- clears created_by now keeps it cleared (the row's audio is then readable
-- by no one but the owner); any other change to it is still undone.

create or replace function public.keep_recording_creator()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if new.created_by is not null then
    new.created_by := old.created_by;
  end if;
  return new;
end;
$$;
