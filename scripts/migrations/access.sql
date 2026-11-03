-- Checks who may read and change what, on a database all the migrations
-- were applied to: scripts/check-migrations.sh runs it, as postgres, after
-- test.sql. It changes nothing: it rolls back at the end. A check that
-- fails raises, and psql stops there.

\set ON_ERROR_STOP on
\set VERBOSITY terse

begin;

-- The catalog: every table keeps rows to those its policies let see them,
-- and every function that runs as its owner pins its search_path, which
-- a caller could otherwise point at tables of their own.
do $$
declare
  v_open text;
begin
  select string_agg(c.relname, ', ' order by c.relname) into v_open
  from pg_class c
  where c.relnamespace = 'public'::regnamespace and c.relkind in ('r', 'p') and not c.relrowsecurity;
  if v_open is not null then
    raise exception 'tables without row level security: %', v_open;
  end if;

  select string_agg(p.proname, ', ' order by p.proname) into v_open
  from pg_proc p
  where p.pronamespace = 'public'::regnamespace and p.prosecdef
    and not exists (select 1 from unnest(coalesce(p.proconfig, '{}')) s where s like 'search_path=%');
  if v_open is not null then
    raise exception 'definer functions with no search_path: %', v_open;
  end if;

  if to_regclass('public.page_updates') is not null or to_regproc('public.can_access_space_path') is not null then
    raise exception 'page_updates or can_access_space_path is still there';
  end if;
end;
$$;

-- The privileges, as 20261103020000 states them: anon, signed out, has
-- none; authenticated, the app, the writes the policies allow, the RPCs
-- and the policies' helpers; service_role, the Functions, every table and
-- the functions that read secrets. A table or function added later is
-- added here, with what it should have, or this fails.
do $$
declare
  v_drift text;
begin
  with expected (tbl, who, privs) as (
    values
      ('ai_jobs', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE'),
      ('comments', 'authenticated', 'INSERT,SELECT,UPDATE'),
      ('devices', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE'),
      ('files', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE'),
      ('folders', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE'),
      ('model_providers', 'authenticated', 'DELETE'),
      ('page_content', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE'),
      ('page_history', 'authenticated', 'INSERT,SELECT'),
      ('page_permissions', 'authenticated', 'DELETE,SELECT'),
      ('pages', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE'),
      ('profiles', 'authenticated', 'INSERT,SELECT,UPDATE'),
      ('recordings', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE'),
      ('spaces', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE'),
      ('stt_settings', 'authenticated', 'DELETE,INSERT,SELECT,UPDATE')
    union all
    select c.relname::text, 'service_role', 'DELETE,INSERT,SELECT,UPDATE'
    from pg_class c
    where c.relnamespace = 'public'::regnamespace and c.relkind in ('r', 'p')
  ),
  actual as (
    select c.relname::text as tbl, r.who, string_agg(p.priv, ',' order by p.priv) as privs
    from pg_class c
    cross join unnest(array['anon', 'authenticated', 'service_role']) r (who)
    cross join unnest(array['SELECT', 'INSERT', 'UPDATE', 'DELETE', 'TRUNCATE', 'REFERENCES', 'TRIGGER']) p (priv)
    where c.relnamespace = 'public'::regnamespace and c.relkind in ('r', 'p')
      and has_table_privilege(r.who, c.oid, p.priv)
    group by 1, 2
  )
  select string_agg(
    format('%s to %s: %s, not %s', coalesce(a.tbl, e.tbl), coalesce(a.who, e.who),
           coalesce(a.privs, 'nothing'), coalesce(e.privs, 'nothing')),
    '; ' order by coalesce(a.tbl, e.tbl), coalesce(a.who, e.who))
  into v_drift
  from actual a
  full join expected e on e.tbl = a.tbl and e.who = a.who
  where a.privs is distinct from e.privs;
  if v_drift is not null then
    raise exception 'table privileges: %', v_drift;
  end if;

  -- model_providers' columns: all but the Vault secrets' IDs, to read.
  select string_agg(a.attname, ', ' order by a.attname) into v_drift
  from pg_attribute a
  where a.attrelid = 'public.model_providers'::regclass and a.attnum > 0 and not a.attisdropped
    and has_column_privilege('authenticated', a.attrelid, a.attnum, 'SELECT')
      = (a.attname in ('encrypted_secret', 'refresh_token'));
  if v_drift is not null
     or has_any_column_privilege('authenticated', 'public.model_providers', 'INSERT')
     or has_any_column_privilege('authenticated', 'public.model_providers', 'UPDATE') then
    raise exception 'model_providers columns readable as they should not be, or writable: %', v_drift;
  end if;

  with expected (fn, who) as (
    select f, 'authenticated'
    from unnest(array[
      'accept_page_invite', 'can_edit_page', 'can_view_image_path', 'can_view_page', 'can_write_file_path',
      'claim_starter_pages',
      'decline_page_invite', 'has_space_access', 'has_stt_secret', 'is_space_owner', 'owns_recording_path',
      'owns_space_path', 'page_history_since', 'page_invites', 'page_members', 'page_role',
      'set_provider_secret', 'set_stt_secret', 'share_page', 'shares_page_with', 'tags_in_bounds', 'unshare_page'
    ]) f
    union all
    select f, 'service_role'
    from unnest(array['get_provider_secret', 'get_stt_secret', 'release_kept_recordings', 'tags_in_bounds']) f
  ),
  actual as (
    select p.proname::text as fn, r.who
    from pg_proc p
    cross join unnest(array['anon', 'authenticated', 'service_role']) r (who)
    where p.pronamespace = 'public'::regnamespace and has_function_privilege(r.who, p.oid, 'execute')
  )
  select string_agg(
    format('%s %s %s', coalesce(a.who, e.who), case when a.fn is null then 'may not call' else 'may call' end,
           coalesce(a.fn, e.fn)),
    '; ' order by coalesce(a.fn, e.fn), coalesce(a.who, e.who))
  into v_drift
  from actual a
  full join expected e on e.fn = a.fn and e.who = a.who
  where a.fn is null or e.fn is null;
  if v_drift is not null then
    raise exception 'function privileges: %', v_drift;
  end if;
end;
$$;

-- The owner of a page, its editor and its viewer, who accepted their
-- invites, one invited who has not, and a stranger.
insert into auth.users (id, email, email_confirmed_at) values
  ('a1000000-0000-0000-0000-000000000001', 'owner@example.com', now()),
  ('a2000000-0000-0000-0000-000000000002', 'editor@example.com', now()),
  ('a3000000-0000-0000-0000-000000000003', 'viewer@example.com', now()),
  ('a4000000-0000-0000-0000-000000000004', 'invitee@example.com', now()),
  ('a5000000-0000-0000-0000-000000000005', 'stranger@example.com', now());

create function pg_temp.act_as(p_user uuid) returns void
language sql as $$ select set_config('request.jwt.claim.sub', p_user::text, true) $$;

-- Runs p_sql, which must be refused (insufficient_privilege), with
-- p_message when one is given.
create function pg_temp.refused(p_sql text, p_message text default null) returns void
language plpgsql as $$
begin
  execute p_sql;
  raise exception 'not refused: %', p_sql;
exception when insufficient_privilege then
  if p_message is not null and sqlerrm <> p_message then
    raise exception 'refused with %, not %: %', sqlerrm, p_message, p_sql;
  end if;
end;
$$;

-- How many of the rows p_sql selects the caller sees.
create function pg_temp.seen(p_sql text) returns bigint
language plpgsql as $$
declare
  n bigint;
begin
  execute format('select count(*) from (%s) q', p_sql) into n;
  return n;
end;
$$;

set local role authenticated;

select pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
insert into public.pages (id, space_id, title)
select 'e0000000-0000-0000-0000-0000000000e1', id, 'Plan' from public.spaces where owner_id = auth.uid();
insert into public.page_content (page_id) values ('e0000000-0000-0000-0000-0000000000e1');
select public.share_page('e0000000-0000-0000-0000-0000000000e1', 'editor@example.com', 'editor');
select public.share_page('e0000000-0000-0000-0000-0000000000e1', 'viewer@example.com', 'viewer');
select public.share_page('e0000000-0000-0000-0000-0000000000e1', 'invitee@example.com', 'viewer');
-- And an address no account has.
select public.share_page('e0000000-0000-0000-0000-0000000000e1', 'nobody@example.com', 'viewer');
select pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
select public.accept_page_invite('e0000000-0000-0000-0000-0000000000e1');
select pg_temp.act_as('a3000000-0000-0000-0000-000000000003');
select public.accept_page_invite('e0000000-0000-0000-0000-0000000000e1');

-- The page's history, from before it was shared and after.
reset role;
insert into public.page_history (id, page_id, content, reason, created_by, created_at) values
  ('d0000000-0000-0000-0000-0000000000d1', 'e0000000-0000-0000-0000-0000000000e1', '{}', 'manual',
   'a1000000-0000-0000-0000-000000000001', now() - interval '1 day'),
  ('d0000000-0000-0000-0000-0000000000d2', 'e0000000-0000-0000-0000-0000000000e1', '{}', 'manual',
   'a1000000-0000-0000-0000-000000000001', now() + interval '1 minute');
set local role authenticated;

-- Each of them, against the page: who sees it and its content, who
-- changes them, who comments on it, and what of its history each reads.
-- A viewer comments; a pending invite gives nothing.
do $$
declare
  v_page constant uuid := 'e0000000-0000-0000-0000-0000000000e1';
  v_who constant text[] := array['owner', 'editor', 'viewer', 'invitee', 'stranger'];
  v_ids constant uuid[] := array[
    'a1000000-0000-0000-0000-000000000001', 'a2000000-0000-0000-0000-000000000002',
    'a3000000-0000-0000-0000-000000000003', 'a4000000-0000-0000-0000-000000000004',
    'a5000000-0000-0000-0000-000000000005'
  ]::uuid[];
  v_sees constant boolean[] := array[true, true, true, false, false];
  v_edits constant boolean[] := array[true, true, false, false, false];
  -- The history before the share is the owner's alone.
  v_history constant int[] := array[2, 1, 1, 0, 0];
  -- Each reads the profiles of those a page is shared with them by or
  -- with, and their own.
  v_profiles constant int[] := array[3, 3, 3, 1, 1];
  n bigint;
begin
  for i in 1 .. array_length(v_who, 1) loop
    perform pg_temp.act_as(v_ids[i]);
    select count(*) into n from public.pages where id = v_page;
    if (n = 1) <> v_sees[i] then
      raise exception 'the % sees the page: %', v_who[i], n = 1;
    end if;
    select count(*) into n from public.page_content where page_id = v_page;
    if (n = 1) <> v_sees[i] then
      raise exception 'the % reads its content: %', v_who[i], n = 1;
    end if;
    update public.pages set title = 'Plan' where id = v_page;
    get diagnostics n = row_count;
    if (n = 1) <> v_edits[i] then
      raise exception 'the % renames the page: %', v_who[i], n = 1;
    end if;
    update public.page_content set plain_text = v_who[i] where page_id = v_page;
    get diagnostics n = row_count;
    if (n = 1) <> v_edits[i] then
      raise exception 'the % writes its content: %', v_who[i], n = 1;
    end if;
    select count(*) into n from public.page_history where page_id = v_page;
    if n <> v_history[i] then
      raise exception 'the % reads % of its history, not %', v_who[i], n, v_history[i];
    end if;
    select count(*) into n from public.profiles where id = any(v_ids);
    if n <> v_profiles[i] then
      raise exception 'the % reads % profiles, not %', v_who[i], n, v_profiles[i];
    end if;
    begin
      insert into public.comments (page_id, block_id, body) values (v_page, 'b1', 'Hi');
      if not v_sees[i] then
        raise exception 'the % commented on a page they can''t see', v_who[i];
      end if;
    exception when insufficient_privilege then
      if v_sees[i] then
        raise exception 'the % could not comment', v_who[i];
      end if;
    end;
  end loop;
end;
$$;

-- Who has the page, as page_members tells it: the owner sees everyone,
-- and the invites still waiting alike, whether for an account (under an
-- id that is not the account's) or an address with none; the editor sees
-- those with access; the invitee, who can't see the page yet, is refused,
-- and finds the invite with page_invites.
do $$
declare
  v_page constant uuid := 'e0000000-0000-0000-0000-0000000000e1';
  r record;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select count(*) filter (where accepted) as members,
         string_agg(email, ', ' order by email) filter (where not accepted) as waiting,
         count(*) filter (where user_id = 'a4000000-0000-0000-0000-000000000004') as named
  into r from public.page_members(v_page);
  if r.members <> 3 or r.waiting is distinct from 'invitee@example.com, nobody@example.com' or r.named <> 0 then
    raise exception 'the owner sees % members and waiting %, % by account', r.members, r.waiting, r.named;
  end if;

  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  select count(*) filter (where accepted) as members, count(*) filter (where not accepted) as waiting
  into r from public.page_members(v_page);
  if r.members <> 3 or r.waiting <> 0 then
    raise exception 'the editor sees % members and % invites', r.members, r.waiting;
  end if;
  if exists (select 1 from public.page_invites()) then
    raise exception 'the editor has invites';
  end if;

  perform pg_temp.act_as('a4000000-0000-0000-0000-000000000004');
  perform pg_temp.refused(format('select * from public.page_members(%L)', v_page), 'forbidden');
  select * into r from public.page_invites();
  if r.page_id is distinct from v_page or r.title <> 'Plan' or r.role <> 'viewer' or r.inviter_name <> 'owner' then
    raise exception 'the invitee''s invite is %', to_jsonb(r);
  end if;
end;
$$;

-- Moves. Subpages of the shared page, and another page shared with the
-- editor alone: the editor moves a page between parents the same shares
-- reach, but not to where other shares reach it, nor to the top; the
-- owner moves it anywhere in their space.
select pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
insert into public.pages (id, space_id, parent_id, title)
select v.id, p.space_id, p.id, v.title
from public.pages p, (values
  ('e0000000-0000-0000-0000-0000000000a1'::uuid, 'First'),
  ('e0000000-0000-0000-0000-0000000000a2'::uuid, 'Second')
) v (id, title)
where p.id = 'e0000000-0000-0000-0000-0000000000e1';
insert into public.pages (id, space_id, title)
select 'e0000000-0000-0000-0000-0000000000e3', id, 'Ideas' from public.spaces where owner_id = auth.uid();
select public.share_page('e0000000-0000-0000-0000-0000000000e3', 'editor@example.com', 'editor');
select pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
select public.accept_page_invite('e0000000-0000-0000-0000-0000000000e3');

do $$
begin
  update public.pages set parent_id = 'e0000000-0000-0000-0000-0000000000a1'
  where id = 'e0000000-0000-0000-0000-0000000000a2';
  if not found then
    raise exception 'the editor could not move a page within the page shared with them';
  end if;
  perform pg_temp.refused(
    'update public.pages set parent_id = ''e0000000-0000-0000-0000-0000000000e3'' where id = ''e0000000-0000-0000-0000-0000000000a1''',
    'forbidden_move');
  perform pg_temp.refused(
    'update public.pages set parent_id = null where id = ''e0000000-0000-0000-0000-0000000000a1''',
    'forbidden_move');

  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  update public.pages set parent_id = 'e0000000-0000-0000-0000-0000000000e3'
  where id = 'e0000000-0000-0000-0000-0000000000a1';
  if not found then
    raise exception 'the owner could not move a page to another share';
  end if;
end;
$$;

-- A tree 70 pages deep, past where walks of the tree used to stop: moved
-- under the shared page, every page of it moves its updated_at on; the
-- share reaches its deepest page; and it can't go under its own deepest.
do $$
declare
  v_top constant uuid := 'e0000000-0000-0000-0000-0000000000d0';
  v_deepest uuid := v_top;
  v_before timestamptz;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  insert into public.pages (id, space_id, title)
  select v_top, id, 'Deep' from public.spaces where owner_id = auth.uid();
  for i in 1 .. 70 loop
    insert into public.pages (space_id, parent_id, title)
    select p.space_id, p.id, 'Level ' || i from public.pages p where p.id = v_deepest
    returning id into v_deepest;
  end loop;

  v_before := clock_timestamp();
  update public.pages set parent_id = 'e0000000-0000-0000-0000-0000000000e1' where id = v_top;
  if exists (select 1 from public.pages where title like 'Level %' and updated_at <= v_before) then
    raise exception 'a move under a share left the pages deep under it as they were';
  end if;

  perform pg_temp.act_as('a3000000-0000-0000-0000-000000000003');
  if not exists (select 1 from public.pages where id = v_deepest) then
    raise exception 'the share does not reach 71 pages down';
  end if;

  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  begin
    update public.pages set parent_id = v_deepest where id = v_top;
    raise exception 'a page went under its own subpage 70 levels down';
  exception when check_violation then
    if sqlerrm <> 'page_cycle' then
      raise;
    end if;
  end;
end;
$$;

-- A new page is placed when it reaches the server, whatever created_at
-- its client sends, and its content starts at revision 0, whatever
-- revision it is sent with.
do $$
declare
  v_page constant uuid := 'e0000000-0000-0000-0000-0000000000f1';
begin
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  insert into public.pages (id, space_id, parent_id, title, created_at)
  select v_page, space_id, id, 'Made offline', '2020-01-01'
  from public.pages where id = 'e0000000-0000-0000-0000-0000000000e1';
  if (select placed_at from public.pages where id = v_page) <> now() then
    raise exception 'a new page was placed when its client said it was made';
  end if;
  insert into public.page_content (page_id, revision) values (v_page, 9223372036854775807);
  update public.page_content set plain_text = 'Saved' where page_id = v_page;
  if (select revision from public.page_content where page_id = v_page) <> 1 then
    raise exception 'new content kept the revision its writer sent';
  end if;
end;
$$;

-- A guest's comment is posted by the page's owner only, whose public link
-- its visitors use; deleted, it keeps neither its text nor the guest's
-- name.
do $$
declare
  v_comment constant uuid := 'e0000000-0000-0000-0000-0000000000c1';
  r record;
begin
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  perform pg_temp.refused(
    'insert into public.comments (page_id, block_id, author_kind, agent_name, body) values (''e0000000-0000-0000-0000-0000000000e1'', ''b1'', ''guest'', ''Bob'', ''Hi'')',
    'guest_comment_not_owner');

  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  insert into public.comments (id, page_id, block_id, author_kind, agent_name, body)
  values (v_comment, 'e0000000-0000-0000-0000-0000000000e1', 'b1', 'guest', 'Ada', 'Hello');
  update public.comments set deleted_at = now() where id = v_comment;
  select * into r from public.comments where id = v_comment;
  if r.body <> '' or r.agent_name is distinct from 'Guest' then
    raise exception 'a deleted guest comment kept "%", by %', r.body, r.agent_name;
  end if;
end;
$$;

-- An editor's file on a page shared with them: its row in their own
-- space, on the page, and its object theirs to write. The page's viewers
-- read it and a stranger does not; a viewer can't put a file of theirs on
-- the page. The page's owner only deletes it, and then it is read only by
-- the editor and the owner, who removes it.
do $$
declare
  v_file constant uuid := 'e0000000-0000-0000-0000-0000000000b1';
  v_path text;
  v_object text;
begin
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  insert into public.files (id, space_id, page_id, name, storage_path)
  select v_file, s.id, 'e0000000-0000-0000-0000-0000000000e1', 'a.txt', s.id || '/' || v_file || '/a.txt'
  from public.spaces s where s.owner_id = auth.uid()
  returning storage_path into v_path;
  insert into storage.objects (bucket_id, name, owner) values ('files', v_path, auth.uid());
  v_object := format('select 1 from storage.objects where bucket_id = ''files'' and name = %L', v_path);

  perform pg_temp.act_as('a3000000-0000-0000-0000-000000000003');
  if pg_temp.seen(v_object) <> 1 then
    raise exception 'the viewer can''t read a file on the page';
  end if;
  perform pg_temp.refused(
    'insert into public.files (space_id, page_id, name) select id, ''e0000000-0000-0000-0000-0000000000e1'', ''b.txt'' from public.spaces where owner_id = auth.uid()',
    'forbidden_file_page');
  perform pg_temp.act_as('a5000000-0000-0000-0000-000000000005');
  if pg_temp.seen(v_object) <> 0 then
    raise exception 'a stranger reads a file on the page';
  end if;

  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  perform pg_temp.refused(format('update public.files set name = ''b.txt'' where id = %L', v_file), 'forbidden_file_change');
  update public.files set deleted_at = now() where id = v_file;
  if not found then
    raise exception 'the page''s owner could not delete a file on it';
  end if;
  perform pg_temp.refused(format('update public.files set deleted_at = null where id = %L', v_file), 'forbidden_file_change');

  perform pg_temp.act_as('a3000000-0000-0000-0000-000000000003');
  if pg_temp.seen(v_object) <> 0 then
    raise exception 'the viewer still reads a deleted file';
  end if;
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  if pg_temp.seen(v_object) <> 1 then
    raise exception 'the editor can''t read their own deleted file';
  end if;
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  delete from storage.objects where bucket_id = 'files' and name = v_path;
  if not found then
    raise exception 'the page''s owner could not remove a deleted file''s object';
  end if;
end;
$$;

-- An editor's recording on the page: its audio is at its own path, which
-- no one can point elsewhere; its recorder uploads it there and nowhere
-- else; the page's viewers hear it, and a stranger does not. A viewer
-- makes no recording.
do $$
declare
  v_rec constant uuid := 'e0000000-0000-0000-0000-0000000000a9';
  v_path constant text := 'a2000000-0000-0000-0000-000000000002/e0000000-0000-0000-0000-0000000000a9.wav';
  v_audio constant text := format('select 1 from storage.objects where bucket_id = ''recordings'' and name = %L', v_path);
begin
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  insert into public.recordings (id, page_id, audio_path) values (
    v_rec, 'e0000000-0000-0000-0000-0000000000e1',
    'a5000000-0000-0000-0000-000000000005/e0000000-0000-0000-0000-0000000000a9.wav');
  if (select audio_path from public.recordings where id = v_rec) is not null then
    raise exception 'a recording was made naming someone else''s audio';
  end if;
  update public.recordings set audio_path = v_path where id = v_rec;
  update public.recordings set audio_path = 'a2000000-0000-0000-0000-000000000002/other.wav' where id = v_rec;
  if (select audio_path from public.recordings where id = v_rec) is distinct from v_path then
    raise exception 'the recording''s audio is not at its own path';
  end if;
  insert into storage.objects (bucket_id, name, owner) values ('recordings', v_path, auth.uid());
  perform pg_temp.refused(
    'insert into storage.objects (bucket_id, name) values (''recordings'', ''a2000000-0000-0000-0000-000000000002/e0000000-0000-0000-0000-0000000000a8.wav'')');
  perform pg_temp.refused(
    'insert into storage.objects (bucket_id, name) values (''recordings'', ''a3000000-0000-0000-0000-000000000003/e0000000-0000-0000-0000-0000000000a9.wav'')');

  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  update public.recordings
  set audio_path = 'a1000000-0000-0000-0000-000000000001/e0000000-0000-0000-0000-0000000000a9.wav'
  where id = v_rec;
  if (select audio_path from public.recordings where id = v_rec) is distinct from v_path then
    raise exception 'the page''s owner pointed the recording at other audio';
  end if;

  perform pg_temp.act_as('a3000000-0000-0000-0000-000000000003');
  if pg_temp.seen(v_audio) <> 1 then
    raise exception 'the viewer can''t hear a recording on the page';
  end if;
  perform pg_temp.refused(
    'insert into public.recordings (page_id) values (''e0000000-0000-0000-0000-0000000000e1'')');
  perform pg_temp.act_as('a5000000-0000-0000-0000-000000000005');
  if pg_temp.seen(v_audio) <> 0 then
    raise exception 'a stranger hears a recording on the page';
  end if;
end;
$$;

-- Images: one under the page is read by who can view the page, one at
-- the top of the space by its owner alone, and only the space's owner
-- writes either.
do $$
declare
  v_space uuid;
  v_images text;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into storage.objects (bucket_id, name, owner) values
    ('images', v_space || '/e0000000-0000-0000-0000-0000000000e1/e0000000-0000-0000-0000-0000000000e7.png', auth.uid()),
    ('images', v_space || '/e0000000-0000-0000-0000-0000000000e8.png', auth.uid());
  v_images := format('select 1 from storage.objects where bucket_id = ''images'' and name like %L', v_space || '/%');

  perform pg_temp.act_as('a3000000-0000-0000-0000-000000000003');
  if pg_temp.seen(v_images) <> 1 then
    raise exception 'the viewer reads % of the owner''s images, not the page''s one', pg_temp.seen(v_images);
  end if;
  perform pg_temp.act_as('a5000000-0000-0000-0000-000000000005');
  if pg_temp.seen(v_images) <> 0 then
    raise exception 'a stranger reads the owner''s images';
  end if;
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  perform pg_temp.refused(format(
    'insert into storage.objects (bucket_id, name) values (''images'', %L)',
    v_space || '/e0000000-0000-0000-0000-0000000000e1/e0000000-0000-0000-0000-0000000000e9.png'));
end;
$$;

-- Keys in Vault: a provider's key saved again changes it there, and
-- cleared, or its provider deleted, it goes from there; a speech-to-text
-- key the same. postgres does it, who owns the functions that do.
reset role;
insert into public.model_providers (id, user_id, preset)
values ('e0000000-0000-0000-0000-0000000000ee', 'a1000000-0000-0000-0000-000000000001', 'openai');
create function pg_temp.provider_key() returns text
language sql as $$
  select s.decrypted_secret from public.model_providers mp
  join vault.decrypted_secrets s on s.id = mp.encrypted_secret
  where mp.id = 'e0000000-0000-0000-0000-0000000000ee'
$$;
create temp table vault_secrets_before as select id from vault.secrets;
set local role authenticated;
select pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
select public.set_provider_secret('e0000000-0000-0000-0000-0000000000ee', 'key-1');
select public.set_provider_secret('e0000000-0000-0000-0000-0000000000ee', 'key-2');
reset role;
do $$
begin
  if pg_temp.provider_key() is distinct from 'key-2'
     or (select count(*) from vault.secrets where id not in (select id from vault_secrets_before)) <> 1 then
    raise exception 'saving a key again did not change it in place';
  end if;
end;
$$;
set local role service_role;
do $$
begin
  if public.get_provider_secret('e0000000-0000-0000-0000-0000000000ee') is distinct from 'key-2' then
    raise exception 'the Functions read another key';
  end if;
end;
$$;
set local role authenticated;
select pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
select public.set_provider_secret('e0000000-0000-0000-0000-0000000000ee', '');
reset role;
do $$
begin
  if pg_temp.provider_key() is not null or exists (select 1 from vault.secrets where id not in (select id from vault_secrets_before)) then
    raise exception 'a cleared key is still in Vault';
  end if;
end;
$$;
set local role authenticated;
select pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
select public.set_provider_secret('e0000000-0000-0000-0000-0000000000ee', 'key-3');
delete from public.model_providers where id = 'e0000000-0000-0000-0000-0000000000ee';
select public.set_stt_secret('soniox', 'stt-1');
reset role;
do $$
begin
  if (select count(*) from vault.secrets where id not in (select id from vault_secrets_before)) <> 1 then
    raise exception 'a deleted provider''s key is still in Vault, or the speech-to-text key is not';
  end if;
end;
$$;
set local role authenticated;
select pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
select public.set_stt_secret('soniox', '');
reset role;
do $$
begin
  if exists (select 1 from vault.secrets where id not in (select id from vault_secrets_before)) then
    raise exception 'a cleared speech-to-text key is still in Vault';
  end if;
end;
$$;
set local role authenticated;

-- Unshared, the editor sees the page no more, nor its content.
select pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
select public.unshare_page('e0000000-0000-0000-0000-0000000000e1', 'a2000000-0000-0000-0000-000000000002');
select pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
do $$
begin
  if exists (select 1 from public.pages where id = 'e0000000-0000-0000-0000-0000000000e1')
    or exists (select 1 from public.page_content where page_id = 'e0000000-0000-0000-0000-0000000000e1') then
    raise exception 'the editor still sees the page once it was unshared';
  end if;
  update public.page_content set plain_text = 'after' where page_id = 'e0000000-0000-0000-0000-0000000000e1';
  if found then
    raise exception 'the editor still writes the page once it was unshared';
  end if;
end;
$$;

-- Starter pages: the first device to ask has the owner's space for its
-- own, and still has it asking again; another device of theirs is told
-- the library is another's, and no one else claims the space.
do $$
declare
  v_space uuid;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  if not public.claim_starter_pages(v_space, 'c1a10000-0000-0000-0000-000000000001')
     or not public.claim_starter_pages(v_space, 'c1a10000-0000-0000-0000-000000000001') then
    raise exception 'the first device to ask does not have the starter pages';
  end if;
  if public.claim_starter_pages(v_space, 'c1a10000-0000-0000-0000-000000000002') then
    raise exception 'a second device has the starter pages too';
  end if;
  perform pg_temp.act_as('a5000000-0000-0000-0000-000000000005');
  if public.claim_starter_pages(v_space, 'c1a10000-0000-0000-0000-000000000005') then
    raise exception 'someone claimed another''s space';
  end if;
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  if (select starters_device from public.spaces where id = v_space) <> 'c1a10000-0000-0000-0000-000000000001' then
    raise exception 'the claim moved to another device';
  end if;
end;
$$;

-- A walk of the tree ends on a cycle, should one ever get past
-- pages_guard: the page moved under Ideas made its parent. One that went
-- round for good would time out.
reset role;
set local statement_timeout = '10s';
alter table public.pages disable trigger pages_guard;
update public.pages set parent_id = 'e0000000-0000-0000-0000-0000000000a1'
where id = 'e0000000-0000-0000-0000-0000000000e3';
set local role authenticated;
select pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
do $$
begin
  if public.page_role('e0000000-0000-0000-0000-0000000000a1') is distinct from 'editor' then
    raise exception 'a walk round a cycle lost the share on it';
  end if;
end;
$$;

rollback;
