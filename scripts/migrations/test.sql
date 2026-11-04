-- Checks what the migrations promise, on a database they were all applied
-- to: scripts/check-migrations.sh runs it, as postgres, after them. It
-- changes nothing: it rolls back at the end. A check that fails raises, and
-- psql stops there.

\set ON_ERROR_STOP on
\set VERBOSITY terse

begin;

-- Every foreign key has an index that starts with it, so deleting the row
-- it points at never scans the whole table.
do $$
declare
  v_missing text;
begin
  select string_agg(c.conrelid::regclass || '.' || a.attname, ', ' order by c.conrelid::regclass::text, a.attname) into v_missing
  from pg_constraint c
  join pg_attribute a on a.attrelid = c.conrelid and a.attnum = c.conkey[1]
  where c.contype = 'f'
    and c.connamespace = 'public'::regnamespace
    and not exists (
      select 1 from pg_index i where i.indrelid = c.conrelid and i.indkey[0] = c.conkey[1]
    );
  if v_missing is not null then
    raise exception 'foreign keys with no index: %', v_missing;
  end if;
end;
$$;

-- An owner, and an editor of one of the owner's pages.
insert into auth.users (id, email, email_confirmed_at) values
  ('a0000000-0000-0000-0000-000000000001', 'owner@example.com', now()),
  ('b0000000-0000-0000-0000-000000000002', 'editor@example.com', now());

create function pg_temp.act_as(p_user uuid) returns void
language sql as $$ select set_config('request.jwt.claim.sub', p_user::text, true) $$;

set local role authenticated;

select pg_temp.act_as('a0000000-0000-0000-0000-000000000001');
insert into public.folders (id, space_id, name)
select 'f0000000-0000-0000-0000-0000000000f0', id, 'Folder' from public.spaces where owner_id = auth.uid();
insert into public.pages (id, space_id, title)
select 'f0000000-0000-0000-0000-00000000000a', id, 'Shared' from public.spaces where owner_id = auth.uid();
select public.share_page('f0000000-0000-0000-0000-00000000000a', 'editor@example.com', 'editor');

select pg_temp.act_as('b0000000-0000-0000-0000-000000000002');
select public.accept_page_invite('f0000000-0000-0000-0000-00000000000a');

-- The editor makes a subpage of the shared page, in no folder.
insert into public.pages (id, space_id, parent_id, title)
select 'f0000000-0000-0000-0000-0000000000b1', space_id, id, 'Subpage'
from public.pages where id = 'f0000000-0000-0000-0000-00000000000a';

-- But not in a folder of the owner's.
do $$
begin
  insert into public.pages (id, space_id, parent_id, folder_id, title)
  select 'f0000000-0000-0000-0000-0000000000b2', space_id, id, 'f0000000-0000-0000-0000-0000000000f0', 'Filed'
  from public.pages where id = 'f0000000-0000-0000-0000-00000000000a';
  raise exception 'an editor made a page in the owner''s folder';
exception when insufficient_privilege then
  if sqlerrm <> 'forbidden_folder' then
    raise;
  end if;
end;
$$;

-- Nor move one there.
do $$
begin
  update public.pages set folder_id = 'f0000000-0000-0000-0000-0000000000f0'
  where id = 'f0000000-0000-0000-0000-0000000000b1';
  raise exception 'an editor moved a page into the owner''s folder';
exception when insufficient_privilege then
  if sqlerrm <> 'forbidden_folder' then
    raise;
  end if;
end;
$$;

-- The owner files the editor's subpage in the folder.
select pg_temp.act_as('a0000000-0000-0000-0000-000000000001');
update public.pages set folder_id = 'f0000000-0000-0000-0000-0000000000f0'
where id = 'f0000000-0000-0000-0000-0000000000b1';

-- The editor still sends it whole, as the app does: an upsert, whose
-- insert comes first and sees the folder.
select pg_temp.act_as('b0000000-0000-0000-0000-000000000002');
insert into public.pages (id, space_id, parent_id, folder_id, title)
select id, space_id, parent_id, folder_id, 'Subpage, renamed'
from public.pages where id = 'f0000000-0000-0000-0000-0000000000b1'
on conflict (id) do update set title = excluded.title;

do $$
begin
  if (select title from public.pages where id = 'f0000000-0000-0000-0000-0000000000b1') <> 'Subpage, renamed' then
    raise exception 'the editor''s upsert of a filed page did not go through';
  end if;
end;
$$;

-- The editor records on the owner's page: the audio is theirs.
insert into public.recordings (id, page_id, audio_path) values (
  'c0000000-0000-0000-0000-0000000000c1', 'f0000000-0000-0000-0000-00000000000a',
  'b0000000-0000-0000-0000-000000000002/c0000000-0000-0000-0000-0000000000c1.wav'
);
insert into storage.objects (bucket_id, name, owner) values (
  'recordings', 'b0000000-0000-0000-0000-000000000002/c0000000-0000-0000-0000-0000000000c1.wav',
  'b0000000-0000-0000-0000-000000000002'
);

-- Their account going, the service role gives up its ownership of the
-- audio that stays with the owner's page.
do $$
begin
  perform public.release_kept_recordings('b0000000-0000-0000-0000-000000000002');
  raise exception 'a user released audio';
exception when insufficient_privilege then
end;
$$;
reset role;
set local role service_role;
do $$
begin
  if array(select public.release_kept_recordings('b0000000-0000-0000-0000-000000000002'))
    is distinct from array['b0000000-0000-0000-0000-000000000002/c0000000-0000-0000-0000-0000000000c1.wav'] then
    raise exception 'the kept recording''s audio was not released';
  end if;
  if (select owner from storage.objects where name like 'b0000000-0000-0000-0000-000000000002/%') is not null then
    raise exception 'the audio is still the account''s';
  end if;
end;
$$;

-- The account's deletion clears the recording's creator, as the cascade
-- does, with no caller.
reset role;
select set_config('request.jwt.claim.sub', '', true);
update public.recordings set created_by = null where id = 'c0000000-0000-0000-0000-0000000000c1';

-- The page's owner still hears it, and can't point it at other audio.
set local role authenticated;
select pg_temp.act_as('a0000000-0000-0000-0000-000000000001');
do $$
begin
  if not exists (
    select 1 from storage.objects
    where name = 'b0000000-0000-0000-0000-000000000002/c0000000-0000-0000-0000-0000000000c1.wav'
  ) then
    raise exception 'the page''s owner can''t read the kept recording''s audio';
  end if;
  update public.recordings set audio_path = 'a0000000-0000-0000-0000-000000000001/c0000000-0000-0000-0000-0000000000c1.wav'
  where id = 'c0000000-0000-0000-0000-0000000000c1';
  raise exception 'the kept recording''s audio path changed';
exception when insufficient_privilege then
  if sqlerrm <> 'recording_audio_kept' then
    raise;
  end if;
end;
$$;

-- A public link's lease, renewed, leaves the page's time as it was, so no
-- device pulls the page again for it; any other change moves it on.
do $$
declare
  v_before timestamptz;
  v_after timestamptz;
begin
  insert into public.pages (id, space_id, title)
  select 'f0000000-0000-0000-0000-0000000000e1', id, 'Leased' from public.spaces where owner_id = auth.uid();
  select updated_at into v_before from public.pages where id = 'f0000000-0000-0000-0000-0000000000e1';
  update public.pages set public_link_until = now() + interval '1 hour' where id = 'f0000000-0000-0000-0000-0000000000e1';
  select updated_at into v_after from public.pages where id = 'f0000000-0000-0000-0000-0000000000e1';
  if v_after is distinct from v_before then
    raise exception 'renewing a public link''s lease moved the page''s time on';
  end if;
  update public.pages set title = 'Leased, renamed' where id = 'f0000000-0000-0000-0000-0000000000e1';
  select updated_at into v_after from public.pages where id = 'f0000000-0000-0000-0000-0000000000e1';
  if v_after <= v_before then
    raise exception 'renaming the page left its time as it was';
  end if;
end;
$$;

-- The lease is set through lease_public_link, an hour on by the server's
-- clock, by someone who may edit the page alone, and leaves the page's
-- time as it was too. Ended with the lease its holder got, it goes; with
-- an older one, which a newer lease has replaced, it stays.
do $$
declare
  v_page constant uuid := 'f0000000-0000-0000-0000-0000000000e1';
  v_before timestamptz;
  v_until timestamptz;
begin
  -- Without the lease above, of this transaction's now() too.
  update public.pages set public_link_until = null where id = v_page;
  select updated_at into v_before from public.pages where id = v_page;
  v_until := public.lease_public_link(v_page);
  if v_until is distinct from now() + interval '1 hour'
     or (select public_link_until from public.pages where id = v_page) is distinct from v_until
     or (select updated_at from public.pages where id = v_page) is distinct from v_before then
    raise exception 'the lease is not an hour on by the server''s clock, or moved the page''s time on';
  end if;
  perform public.lease_public_link(v_page, v_until - interval '1 minute');
  if (select public_link_until from public.pages where id = v_page) is distinct from v_until then
    raise exception 'an older lease''s end ended the newer one';
  end if;
  perform public.lease_public_link(v_page, v_until);
  if (select public_link_until from public.pages where id = v_page) is not null then
    raise exception 'the lease did not end';
  end if;

  perform pg_temp.act_as('b0000000-0000-0000-0000-000000000002');
  begin
    perform public.lease_public_link(v_page);
    raise exception 'someone who may not edit the page leased it';
  exception when insufficient_privilege then
    if sqlerrm <> 'forbidden_public_link' then
      raise;
    end if;
  end;
  if public.lease_public_link('f0000000-0000-0000-0000-00000000000a') is null then
    raise exception 'an editor could not lease a page shared with them';
  end if;
  perform pg_temp.act_as('a0000000-0000-0000-0000-000000000001');
end;
$$;

-- The owner asks the workspace agent, a job of no page; a job of no page
-- in another surface, or of a page in the workspace's, is refused.
insert into public.ai_jobs (id, user_id, surface, request)
values ('e0000000-0000-0000-0000-0000000000e1', auth.uid(), 'workspace', '{"instruction": "File my pages"}');
do $$
begin
  insert into public.ai_jobs (user_id, surface) values (auth.uid(), 'sidebar');
  raise exception 'a sidebar job of no page went in';
exception when check_violation then
end;
$$;
do $$
begin
  insert into public.ai_jobs (user_id, page_id, surface)
  values (auth.uid(), 'f0000000-0000-0000-0000-00000000000a', 'workspace');
  raise exception 'a workspace job of a page went in';
exception when check_violation then
end;
$$;

-- The workspace agent finds the owner's pages by title, tag or text, and
-- none of anyone else's.
update public.pages set tags = '{Plans}' where id = 'f0000000-0000-0000-0000-00000000000a';
insert into public.page_content (page_id, content, plain_text)
values ('f0000000-0000-0000-0000-00000000000a', '{}', 'the launch is in May')
on conflict (page_id) do update set plain_text = excluded.plain_text;
do $$
begin
  if (select count(*) from public.agent_search_pages('shar', '{}', 20) where page_id = 'f0000000-0000-0000-0000-00000000000a') <> 1 then
    raise exception 'the owner''s page is not found by its title';
  end if;
  if (select snippet from public.agent_search_pages('LAUNCH', '{}', 20) where page_id = 'f0000000-0000-0000-0000-00000000000a')
     is distinct from 'the launch is in May' then
    raise exception 'the owner''s page is not found by its text with the words around it';
  end if;
  if exists (select 1 from public.agent_search_pages('', '{nope}', 20))
     or not exists (select 1 from public.agent_search_pages('', '{plans}', 20)) then
    raise exception 'tags do not keep to the pages with them';
  end if;
end;
$$;
-- Titles and tags come before text, the words around a match in text are
-- cut where it is, however lowering the text changes its length, and no
-- more pages are found than asked for.
update public.page_content set plain_text = repeat('İ', 50) || ' the launch was leased in May'
where page_id = 'f0000000-0000-0000-0000-00000000000a';
do $$
begin
  if (select snippet from public.agent_search_pages('LAUNCH', '{}', 20) where page_id = 'f0000000-0000-0000-0000-00000000000a')
     not like '%the launch was leased in May' then
    raise exception 'the words around a match are not where it is in the text';
  end if;
  if array(select page_id from public.agent_search_pages('leased', '{}', 20)) is distinct from
     array['f0000000-0000-0000-0000-0000000000e1', 'f0000000-0000-0000-0000-00000000000a']::uuid[] then
    raise exception 'a title''s match does not come before a text''s';
  end if;
  if (select count(*) from public.agent_search_pages('leased', '{}', 1)) <> 1 then
    raise exception 'more pages were found than asked for';
  end if;
end;
$$;
select pg_temp.act_as('b0000000-0000-0000-0000-000000000002');
do $$
begin
  if exists (select 1 from public.agent_search_pages('', '{}', 20)) then
    raise exception 'the editor found the owner''s pages';
  end if;
end;
$$;

rollback;
