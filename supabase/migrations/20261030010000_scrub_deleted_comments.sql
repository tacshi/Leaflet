-- A deleted comment stayed readable: soft-deleting it (deleted_at) kept its
-- body, quote and mentions for anyone who can view the page. A comment is
-- now emptied as it is deleted, and the ones deleted already are emptied
-- here; the row stays, so its replies keep their thread and the deletion
-- reaches the other devices through the pull.
--
-- The trigger runs after comments_guard (triggers of a kind run in name
-- order), which still compares what the caller sent with the row as it
-- was. The app shows a deleted comment as deleted, never its text, and
-- does not undelete one.

create or replace function public.scrub_deleted_comment()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if new.deleted_at is not null then
    new.body := '';
    new.quote := null;
    new.mentions := '[]'::jsonb;
  end if;
  return new;
end;
$$;

drop trigger if exists comments_scrub_deleted on public.comments;
create trigger comments_scrub_deleted before insert or update on public.comments
  for each row execute function public.scrub_deleted_comment();

-- updated_at moves on, so the devices pull the emptied rows.
update public.comments
set body = '', quote = null, mentions = '[]'::jsonb
where deleted_at is not null
  and (body <> '' or quote is not null or mentions <> '[]'::jsonb);
