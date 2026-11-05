-- A device shows the edits another makes to an open page as they are
-- saved, rather than with its next pull: each save of a page's content is
-- told on the page's private Realtime channel, page:<page id>, as the
-- page's id and revision, and a device with the page open hears it there
-- and asks for the content (protocol §6). The content never goes through
-- Realtime, so what someone may read of it stays with page_content's
-- policies.
-- * page_content_told tells of the save once it is made. Realtime missing
--   or failing never fails the save: what it could not tell, the devices'
--   own checks and pulls still bring.
-- * Who may hear a page's channel is who may read the page
--   (can_read_page), asked as they join it and as their access token is
--   renewed. No one may send on it: a broadcast comes only from here.
-- * A project whose Realtime has not set up its messages yet gets no
--   policy, and its devices check their open pages instead.

create or replace function public.page_content_told()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  begin
    perform realtime.send(
      jsonb_build_object('page_id', new.page_id, 'revision', new.revision),
      'saved',
      'page:' || new.page_id::text,
      true
    );
  exception when others then
    raise warning 'page % saved, but not told: %', new.page_id, sqlerrm;
  end;
  return null;
end;
$$;

revoke execute on function public.page_content_told() from public, anon, authenticated, service_role;

drop trigger if exists page_content_told on public.page_content;
create trigger page_content_told after insert or update on public.page_content
  for each row execute function public.page_content_told();

do $$
begin
  if to_regclass('realtime.messages') is null or to_regprocedure('realtime.topic()') is null then
    raise notice 'Realtime has no messages here yet: devices check their open pages instead';
    return;
  end if;
  execute 'drop policy if exists page_saves_heard on realtime.messages';
  -- The topic is checked before it is read as a page's id, which a CASE
  -- keeps in order: anything else is no page's.
  execute $policy$
    create policy page_saves_heard on realtime.messages for select to authenticated
      using (
        realtime.messages.extension = 'broadcast'
        and case
          when (select realtime.topic()) ~ '^page:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
            then public.can_read_page(substr((select realtime.topic()), 6)::uuid)
          else false
        end
      )
  $policy$;
end;
$$;
