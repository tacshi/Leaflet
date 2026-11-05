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
      ('devices', 'authenticated', 'INSERT,SELECT,UPDATE'),
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
      'accept_page_invite', 'agent_card_pages', 'agent_search_pages', 'agent_shared_pages', 'can_edit_page', 'can_read_page',
      'can_view_image_path',
      'can_view_page',
      'can_write_file_path', 'claim_starter_pages',
      'decline_page_invite', 'has_space_access', 'has_stt_secret', 'is_space_owner', 'lease_public_link', 'owns_recording_path',
      'owns_space_path', 'page_history_since', 'page_invites', 'page_members', 'page_own_text', 'page_role',
      'set_provider_secret', 'set_stt_secret', 'share_page', 'shares_page_with', 'sign_out_device', 'tags_in_bounds',
      'unshare_page'
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

-- A tree goes 1,000 pages deep and no deeper: the shared page and 999
-- under it. The share reaches the deepest; no page is made under it; and
-- a page with a subpage goes under the 997th, but not the 998th, where
-- its subpage would be the 1,001st.
do $$
declare
  v_deepest uuid := 'e0000000-0000-0000-0000-0000000000e1';
  v_997 uuid;
  v_998 uuid;
  v_moved constant uuid := 'e0000000-0000-0000-0000-0000000000c0';
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  for i in 1 .. 999 loop
    insert into public.pages (space_id, parent_id, title)
    select p.space_id, p.id, 'Down ' || i from public.pages p where p.id = v_deepest
    returning id into v_deepest;
    if i = 997 then
      v_997 := v_deepest;
    elsif i = 998 then
      v_998 := v_deepest;
    end if;
  end loop;
  begin
    insert into public.pages (space_id, parent_id, title)
    select p.space_id, p.id, 'Down 1000' from public.pages p where p.id = v_deepest;
    raise exception 'a page was made 1,001 pages deep';
  exception when check_violation then
    if sqlerrm <> 'page_too_deep' then
      raise;
    end if;
  end;

  insert into public.pages (id, space_id, title)
  select v_moved, id, 'Moved' from public.spaces where owner_id = auth.uid();
  insert into public.pages (space_id, parent_id, title)
  select p.space_id, p.id, 'Under moved' from public.pages p where p.id = v_moved;
  begin
    update public.pages set parent_id = v_998 where id = v_moved;
    raise exception 'a move took a tree 1,001 pages deep';
  exception when check_violation then
    if sqlerrm <> 'page_too_deep' then
      raise;
    end if;
  end;
  update public.pages set parent_id = v_997 where id = v_moved;

  perform pg_temp.act_as('a3000000-0000-0000-0000-000000000003');
  if not exists (select 1 from public.pages where id = v_deepest) then
    raise exception 'the share does not reach 1,000 pages down';
  end if;
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

-- An editor's new subpage of the page is read back as it is made, from
-- its parent, as the app's upsert reads it; a stranger makes none.
do $$
declare
  v_page uuid;
begin
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  insert into public.pages (space_id, parent_id, title)
  select space_id, id, 'Read back' from public.pages where id = 'e0000000-0000-0000-0000-0000000000e1'
  returning id into v_page;
  if v_page is null then
    raise exception 'the editor''s new subpage was not read back';
  end if;
  perform pg_temp.act_as('a5000000-0000-0000-0000-000000000005');
  perform pg_temp.refused(format(
    'insert into public.pages (space_id, parent_id, title) values (%L, %L, ''Mine'') returning id',
    (select p.space_id from public.pages p where p.id = v_page), v_page));
end;
$$;

-- A page moved under the share brings its content to the people it now
-- reaches (updated_at moves on), but leaves its revision, which moves on
-- only with the content: a device with an edit waiting saves it over the
-- revision it read.
do $$
declare
  v_page constant uuid := 'e0000000-0000-0000-0000-0000000000f2';
  v_before timestamptz;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  insert into public.pages (id, space_id, title)
  select v_page, id, 'Private' from public.spaces where owner_id = auth.uid();
  insert into public.page_content (page_id) values (v_page);
  update public.page_content set plain_text = 'Draft' where page_id = v_page;
  v_before := clock_timestamp();
  update public.pages set parent_id = 'e0000000-0000-0000-0000-0000000000e1' where id = v_page;
  if (select revision from public.page_content where page_id = v_page) <> 1
     or (select updated_at from public.page_content where page_id = v_page) <= v_before then
    raise exception 'a move under the share moved the content''s revision, or not its updated_at';
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

-- What a row was made with stays: a page's, comment's and recording's id,
-- and the page a page's content is of.
do $$
begin
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  perform pg_temp.refused(
    'update public.recordings set id = ''e0000000-0000-0000-0000-0000000000a7'' where id = ''e0000000-0000-0000-0000-0000000000a9''',
    'recording_id_immutable');
  perform pg_temp.refused(
    'update public.comments set id = gen_random_uuid() where page_id = ''e0000000-0000-0000-0000-0000000000e1'' and author_id = auth.uid()',
    'comment_immutable');
  perform pg_temp.refused(
    'update public.page_content set page_id = ''e0000000-0000-0000-0000-0000000000a2'' where page_id = ''e0000000-0000-0000-0000-0000000000e1''',
    'content_page_immutable');
  begin
    update public.pages set id = gen_random_uuid() where id = 'e0000000-0000-0000-0000-0000000000a2';
    raise exception 'a page took another id';
  exception when check_violation then
    if sqlerrm <> 'page_id_immutable' then
      raise;
    end if;
  end;
end;
$$;

-- Two subpages of the shared page the owner deleted: one before those it
-- is shared with had it (its deleted_at before their view of it began),
-- one after. Everyone sees both pages, which brings the deletions. What is
-- on the first (content, comments, a recording and its audio, a file and
-- its object, an image) only the owner reads; what is on the second,
-- everyone who had it. Only someone who may read the first restores it.
do $$
declare
  v_owner constant uuid := 'a1000000-0000-0000-0000-000000000001';
  v_gone constant uuid := 'e0000000-0000-0000-0000-0000000000d7';
  v_late constant uuid := 'e0000000-0000-0000-0000-0000000000d8';
  v_rec constant uuid := 'e0000000-0000-0000-0000-0000000000d9';
  v_file constant uuid := 'e0000000-0000-0000-0000-0000000000da';
  v_space uuid;
  v_objects text[];
  v_who uuid;
  v_on_gone bigint;
  v_on_late bigint;
begin
  perform pg_temp.act_as(v_owner);
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into public.pages (id, space_id, parent_id, title) values
    (v_gone, v_space, 'e0000000-0000-0000-0000-0000000000e1', 'Gone before'),
    (v_late, v_space, 'e0000000-0000-0000-0000-0000000000e1', 'Gone after');
  insert into public.page_content (page_id) values (v_gone), (v_late);
  insert into public.comments (page_id, block_id, body) values (v_gone, 'b1', 'Before'), (v_late, 'b1', 'After');
  v_objects := array[
    v_owner || '/' || v_rec || '.wav',
    v_space || '/' || v_file || '/a.txt',
    v_space || '/' || v_gone || '/e0000000-0000-0000-0000-0000000000db.png'
  ];
  insert into public.recordings (id, page_id, audio_path) values (v_rec, v_gone, v_objects[1]);
  insert into public.files (id, space_id, page_id, name, storage_path)
  values (v_file, v_space, v_gone, 'a.txt', v_objects[2]);
  insert into storage.objects (bucket_id, name, owner) values
    ('recordings', v_objects[1], auth.uid()), ('files', v_objects[2], auth.uid()), ('images', v_objects[3], auth.uid());
  update public.pages set deleted_at = now() - interval '1 day' where id = v_gone;
  update public.pages set deleted_at = now() + interval '1 hour' where id = v_late;

  foreach v_who in array array[v_owner, 'a2000000-0000-0000-0000-000000000002', 'a3000000-0000-0000-0000-000000000003']::uuid[] loop
    perform pg_temp.act_as(v_who);
    if pg_temp.seen(format('select 1 from public.pages where id in (%L, %L)', v_gone, v_late)) <> 2 then
      raise exception '% does not see the deleted pages', v_who;
    end if;
    v_on_gone := pg_temp.seen(format('select 1 from public.page_content where page_id = %L', v_gone))
      + pg_temp.seen(format('select 1 from public.comments where page_id = %L', v_gone))
      + pg_temp.seen(format('select 1 from public.recordings where id = %L', v_rec))
      + pg_temp.seen(format('select 1 from public.files where id = %L', v_file))
      + pg_temp.seen(format('select 1 from storage.objects where name = any (%L::text[])', v_objects));
    if v_on_gone <> (case when v_who = v_owner then 7 else 0 end) then
      raise exception '% reads % of what was on the page deleted before they had it', v_who, v_on_gone;
    end if;
    v_on_late := pg_temp.seen(format('select 1 from public.page_content where page_id = %L', v_late))
      + pg_temp.seen(format('select 1 from public.comments where page_id = %L', v_late));
    if v_on_late <> 2 then
      raise exception '% reads % of what is on the page deleted after they had it', v_who, v_on_late;
    end if;
  end loop;

  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  perform pg_temp.refused(format('update public.pages set deleted_at = null where id = %L', v_gone), 'forbidden_restore');
  update public.pages set deleted_at = null where id = v_late;
  if not found then
    raise exception 'the editor could not restore a page they had';
  end if;
  perform pg_temp.act_as(v_owner);
  update public.pages set deleted_at = null where id = v_gone;
  if not found then
    raise exception 'the owner could not restore their page';
  end if;
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

-- A provider's key stays with the address it was saved for, whoever
-- writes the row (the providers function writes it as the service role):
-- a path changed at the same origin keeps it; another origin, another
-- preset, or turning local, and it goes, from Vault too; a key saved after
-- such a move, as the function saves one, stays.
insert into public.model_providers (id, user_id, preset, base_url)
values ('e0000000-0000-0000-0000-0000000000ef', 'a1000000-0000-0000-0000-000000000001', 'openai', 'https://api.openai.com/v1');
create function pg_temp.moved_key(p_change text, p_key text) returns text
language plpgsql as $$
declare
  v_secret uuid;
  v_key text;
begin
  set local role authenticated;
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  perform public.set_provider_secret('e0000000-0000-0000-0000-0000000000ef', p_key);
  reset role;
  select encrypted_secret into v_secret from public.model_providers where id = 'e0000000-0000-0000-0000-0000000000ef';
  set local role service_role;
  execute format('update public.model_providers set %s where id = ''e0000000-0000-0000-0000-0000000000ef''', p_change);
  reset role;
  select s.decrypted_secret into v_key from public.model_providers mp
  join vault.decrypted_secrets s on s.id = mp.encrypted_secret
  where mp.id = 'e0000000-0000-0000-0000-0000000000ef';
  if v_key is null and exists (select 1 from vault.secrets where id = v_secret) then
    raise exception 'a key dropped by "%" is still in Vault', p_change;
  end if;
  return v_key;
end;
$$;
do $$
begin
  if pg_temp.moved_key('base_url = ''https://API.openai.com:443/v2''', 'key-a') is distinct from 'key-a' then
    raise exception 'a new path at the same origin lost the key';
  end if;
  if pg_temp.moved_key('base_url = ''https://elsewhere.example/v1''', 'key-b') is not null then
    raise exception 'the key went to another origin';
  end if;
  -- A URL parser reads its host as evil.example: a backslash is a slash.
  if pg_temp.moved_key('base_url = ''https://evil.example\@api.openai.com/v1''', 'key-g') is not null then
    raise exception 'the key went to a host behind a backslash';
  end if;
  if pg_temp.moved_key('base_url = ''https://api.openai.com/v1''', 'key-h') is not null then
    raise exception 'the key went back from a URL read its own way';
  end if;
  if pg_temp.moved_key('preset = ''custom''', 'key-c') is not null then
    raise exception 'the key went to another preset';
  end if;
  if pg_temp.moved_key('is_local = true, preset = ''ollama''', 'key-d') is not null then
    raise exception 'the key went with a provider turned local';
  end if;
  if pg_temp.moved_key('is_local = false, preset = ''openai'', base_url = ''https://api.openai.com/v1''', 'key-e') is not null then
    raise exception 'the key went with a provider turned cloud again';
  end if;
  -- The origins compared, as a URL parser reads them; one it reads its
  -- own way, or with no host, is the whole URL.
  if (select string_agg(coalesce(public.base_url_origin(u), 'null'), ' ' order by n)
      from unnest(array[
        'https://API.openai.com:443/v2', 'api.openai.com/v1', 'https://user:pw@api.openai.com/v1',
        E'https://api.openai.com\t.evil.example/v1', 'https:///a.example/v1', 'http://example.com:80/v1',
        'https://evil.example\@api.openai.com/v1', E'https://\u0430pi.openai.com/v1', '', 'https:///'
      ]) with ordinality v (u, n))
     is distinct from
     'https://api.openai.com https://api.openai.com https://api.openai.com https://api.openai.com.evil.example '
     || 'https://a.example http://example.com https://evil.example\@api.openai.com/v1 '
     || E'https://\u0430pi.openai.com/v1 https:// https:///' then
    raise exception 'base URLs'' origins are not as a URL parser reads them';
  end if;
  set local role authenticated;
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  perform public.set_provider_secret('e0000000-0000-0000-0000-0000000000ef', 'key-f');
  reset role;
  if (select s.decrypted_secret from public.model_providers mp
      join vault.decrypted_secrets s on s.id = mp.encrypted_secret
      where mp.id = 'e0000000-0000-0000-0000-0000000000ef') is distinct from 'key-f' then
    raise exception 'a key saved after a move did not stay';
  end if;
end;
$$;
delete from public.model_providers where id = 'e0000000-0000-0000-0000-0000000000ef';
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

-- Comments keep to their bounds: none says more than 100,000 characters,
-- or quotes more, nor names a block of more than 256; and on a page of
-- someone else's, one person writes at most 2,000 comments, of 10 MB in
-- all, mentions with the text. The page's owner is not counted.
do $$
declare
  v_page constant uuid := 'e0000000-0000-0000-0000-0000000000e1';
  v_viewer constant uuid := 'a3000000-0000-0000-0000-000000000003';
begin
  perform pg_temp.act_as(v_viewer);
  begin
    insert into public.comments (page_id, block_id, body) values (v_page, 'b1', repeat('x', 100001));
    raise exception 'a comment of 100,001 characters went in';
  exception when check_violation then
    if sqlerrm not like '%comments_body_length%' then
      raise;
    end if;
  end;
  begin
    insert into public.comments (page_id, block_id, body, quote) values (v_page, 'b1', 'Hi', repeat('x', 100001));
    raise exception 'a quote of 100,001 characters went in';
  exception when check_violation then
    if sqlerrm not like '%comments_quote_length%' then
      raise;
    end if;
  end;
  begin
    insert into public.comments (page_id, block_id, body, mentions) values (v_page, 'b1', 'Hi', '{}');
    raise exception 'mentions that are no list went in';
  exception when check_violation then
    if sqlerrm not like '%comments_mentions_size%' then
      raise;
    end if;
  end;
  begin
    insert into public.comments (page_id, block_id, body) values (v_page, repeat('b', 257), 'Hi');
    raise exception 'a block ID of 257 characters went in';
  exception when check_violation then
    if sqlerrm not like '%comments_block_id_length%' then
      raise;
    end if;
  end;
  insert into public.comments (page_id, block_id, body) values (v_page, 'b1', repeat('x', 100000));

  -- 10 MB of mentions, which count as the text does: one more is refused.
  reset role;
  perform set_config('request.jwt.claim.sub', '', true);
  insert into public.comments (page_id, block_id, body, mentions, author_id)
  select v_page, 'b1', 'm', jsonb_build_array(repeat('m', 64990)), v_viewer from generate_series(1, 162);
  set local role authenticated;
  perform pg_temp.act_as(v_viewer);
  begin
    insert into public.comments (page_id, block_id, body) values (v_page, 'b1', 'One more');
    raise exception 'a comment went past 10 MB of one person''s mentions on a page';
  exception when check_violation then
    if sqlerrm <> 'comments_too_long' then
      raise;
    end if;
  end;
  reset role;
  perform set_config('request.jwt.claim.sub', '', true);
  delete from public.comments where page_id = v_page and author_id = v_viewer and body = 'm';
  set local role authenticated;
  perform pg_temp.act_as(v_viewer);

  -- 10 MB of the viewer's comments on the page, put in with no caller,
  -- as the service role would: one more is refused, and so is a comment
  -- made longer.
  reset role;
  perform set_config('request.jwt.claim.sub', '', true);
  insert into public.comments (page_id, block_id, body, author_id)
  select v_page, 'b1', repeat('y', 100000), v_viewer from generate_series(1, 104);
  set local role authenticated;
  perform pg_temp.act_as(v_viewer);
  begin
    insert into public.comments (page_id, block_id, body) values (v_page, 'b1', 'One more');
    raise exception 'a comment went past 10 MB of one person''s on a page';
  exception when check_violation then
    if sqlerrm <> 'comments_too_long' then
      raise;
    end if;
  end;
  begin
    update public.comments set body = 'Hi, and more' where page_id = v_page and author_id = auth.uid() and body = 'Hi';
    raise exception 'a comment grew past 10 MB of one person''s on a page';
  exception when check_violation then
    if sqlerrm <> 'comments_too_long' then
      raise;
    end if;
  end;

  -- 2,000 of them: one more is refused. The owner's own are not counted.
  reset role;
  perform set_config('request.jwt.claim.sub', '', true);
  delete from public.comments where page_id = v_page and author_id = v_viewer and body like 'yyy%';
  insert into public.comments (page_id, block_id, body, author_id)
  select v_page, 'b1', 'y', v_viewer
  from generate_series(1, 2000 - (select count(*) from public.comments where page_id = v_page and author_id = v_viewer));
  insert into public.comments (page_id, block_id, body, author_id)
  select v_page, 'b1', 'y', 'a1000000-0000-0000-0000-000000000001' from generate_series(1, 2000);
  set local role authenticated;
  perform pg_temp.act_as(v_viewer);
  begin
    insert into public.comments (page_id, block_id, body) values (v_page, 'b1', 'One more');
    raise exception 'a comment went past 2,000 of one person''s on a page';
  exception when check_violation then
    if sqlerrm <> 'too_many_comments' then
      raise;
    end if;
  end;
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  insert into public.comments (page_id, block_id, body) values (v_page, 'b1', 'The owner''s 2,001st');
  reset role;
  delete from public.comments where page_id = v_page and body in ('y', 'The owner''s 2,001st');
  set local role authenticated;
end;
$$;

-- Devices. Each write of a device's row names the session it was made
-- with, whatever was sent, and no client sets or clears revoked_at, or
-- deletes a row. Signing one out revokes it and ends its session, with
-- its refresh tokens, and leaves the others; the row keeps naming that
-- session. Its own upsert from that session, as its heartbeat sends it,
-- still goes, and finds it revoked; one from a new session, as after its
-- owner signs in on it again, finds the revocation stale, and clears it.
-- A row revoked with no session named stays revoked. Another person's
-- device is not theirs to sign out.
reset role;
insert into auth.sessions (id, user_id) values
  ('5e550000-0000-0000-0000-000000000001', 'a1000000-0000-0000-0000-000000000001'),
  ('5e550000-0000-0000-0000-000000000002', 'a1000000-0000-0000-0000-000000000001'),
  ('5e550000-0000-0000-0000-000000000003', 'a1000000-0000-0000-0000-000000000001');
insert into public.devices (id, user_id, name, revoked_at)
values ('de000000-0000-0000-0000-000000000004', 'a1000000-0000-0000-0000-000000000001', 'Old', now());
insert into auth.refresh_tokens (token, user_id, session_id)
values ('refresh-2', 'a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000002');
create function pg_temp.act_in(p_user uuid, p_session uuid) returns void
language sql as $$
  select pg_temp.act_as(p_user);
  select set_config('request.jwt.claims', json_build_object('sub', p_user, 'session_id', p_session)::text, true);
$$;
-- A device's heartbeat, as the app upserts it.
create function pg_temp.beat(p_device uuid) returns timestamptz
language sql as $$
  insert into public.devices (id, user_id, name, platform, last_active_at)
  values (p_device, auth.uid(), 'Device', 'darwin', now())
  on conflict (id) do update set
    id = excluded.id, user_id = excluded.user_id, name = excluded.name,
    platform = excluded.platform, last_active_at = excluded.last_active_at
  returning revoked_at;
$$;
set local role authenticated;
do $$
declare
  v_mac constant uuid := 'de000000-0000-0000-0000-000000000001';
  v_ipad constant uuid := 'de000000-0000-0000-0000-000000000002';
begin
  perform pg_temp.act_in('a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000001');
  insert into public.devices (id, user_id, name, session_id)
  values (v_mac, auth.uid(), 'Mac', '5e550000-0000-0000-0000-000000000002');
  perform pg_temp.act_in('a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000002');
  perform pg_temp.beat(v_ipad);
  if (select session_id from public.devices where id = v_mac) is distinct from '5e550000-0000-0000-0000-000000000001'
     or (select session_id from public.devices where id = v_ipad) is distinct from '5e550000-0000-0000-0000-000000000002' then
    raise exception 'a device''s row does not name the session it was written with';
  end if;
  perform pg_temp.refused(
    'insert into public.devices (id, user_id, revoked_at) values (''de000000-0000-0000-0000-000000000003'', auth.uid(), now())',
    'forbidden_revoke');
  perform pg_temp.refused(format('update public.devices set revoked_at = now() where id = %L', v_mac), 'forbidden_revoke');

  perform pg_temp.act_in('a5000000-0000-0000-0000-000000000005', null);
  begin
    perform public.sign_out_device(v_ipad);
    raise exception 'someone signed out another person''s device';
  exception when no_data_found then
    if sqlerrm <> 'device_not_found' then
      raise;
    end if;
  end;

  perform pg_temp.act_in('a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000001');
  perform public.sign_out_device(v_ipad);
  if (select session_id from public.devices where id = v_ipad) is distinct from '5e550000-0000-0000-0000-000000000002' then
    raise exception 'a signed-out device''s row no longer names the session that was ended';
  end if;

  perform pg_temp.act_in('a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000002');
  perform pg_temp.refused(format('update public.devices set revoked_at = null where id = %L', v_ipad), 'forbidden_revoke');
  perform pg_temp.refused(format('delete from public.devices where id = %L', v_ipad));
  if pg_temp.beat(v_ipad) is null then
    raise exception 'a signed-out device''s heartbeat found it signed in';
  end if;
  perform pg_temp.act_in('a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000001');
  if pg_temp.beat(v_mac) is not null then
    raise exception 'signing one device out signed out another';
  end if;
  if pg_temp.beat('de000000-0000-0000-0000-000000000004') is null then
    raise exception 'a device revoked with no session named came back signed in';
  end if;
  perform pg_temp.act_in('a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000003');
  if pg_temp.beat('de000000-0000-0000-0000-000000000004') is null then
    raise exception 'a device revoked with no session named came back signed in from a second session';
  end if;

  -- Signed in again on the iPad: a new session.
  perform pg_temp.act_in('a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000003');
  perform pg_temp.refused(format('update public.devices set revoked_at = null where id = %L', v_ipad), 'forbidden_revoke');
  if pg_temp.beat(v_ipad) is not null then
    raise exception 'a new session''s heartbeat found its device signed out by an old one';
  end if;
  if (select session_id from public.devices where id = v_ipad) is distinct from '5e550000-0000-0000-0000-000000000003' then
    raise exception 'a device''s row does not name the new session it was written with';
  end if;
  perform pg_temp.refused(format('update public.devices set revoked_at = now() where id = %L', v_ipad), 'forbidden_revoke');
  perform pg_temp.act_in('a1000000-0000-0000-0000-000000000001', '5e550000-0000-0000-0000-000000000001');
  perform public.sign_out_device(v_ipad);
  perform set_config('request.jwt.claims', '', true);
end;
$$;
reset role;
do $$
begin
  if exists (select 1 from auth.sessions where id in ('5e550000-0000-0000-0000-000000000002', '5e550000-0000-0000-0000-000000000003'))
     or exists (select 1 from auth.refresh_tokens where token = 'refresh-2') then
    raise exception 'the signed-out device''s session, or its refresh token, is still there';
  end if;
  if not exists (select 1 from auth.sessions where id = '5e550000-0000-0000-0000-000000000001') then
    raise exception 'signing a device out ended another''s session';
  end if;
end;
$$;

-- A template is the space owner's. An editor of a shared page cannot mark
-- it a template, or make a template subpage of it; the owner marks their
-- own.
set local role authenticated;
do $$
declare
  v_page constant uuid := 'fa000000-0000-0000-0000-0000000000e9';
  v_space uuid;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into public.pages (id, space_id, title) values (v_page, v_space, 'Shared');
  perform public.share_page(v_page, 'editor@example.com', 'editor');
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  perform public.accept_page_invite(v_page);
  perform pg_temp.refused(
    format('update public.pages set is_template = true where id = %L', v_page),
    'forbidden_template');
  perform pg_temp.refused(
    format('insert into public.pages (space_id, parent_id, title, is_template) '
           'select space_id, %L, ''T'', true from public.pages where id = %L', v_page, v_page),
    'forbidden_template');
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  update public.pages set is_template = true where id = v_page;
  if not found then
    raise exception 'the owner could not mark their own page a template';
  end if;
  update public.pages set is_template = false where id = v_page;
end;
$$;

-- A page's title is at most 2,000 characters.
do $$
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  begin
    update public.pages set title = repeat('t', 2001) where id = 'e0000000-0000-0000-0000-0000000000e1';
    raise exception 'a title of 2,001 characters went in';
  exception when check_violation then
    if sqlerrm not like '%pages_title_length%' then
      raise;
    end if;
  end;
end;
$$;

-- A deletion is stamped no later than the server's clock: a write cannot
-- date it ahead and so keep deleted content readable to those who came
-- after.
do $$
declare
  v_page constant uuid := 'fa000000-0000-0000-0000-000000000001';
  v_space uuid;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into public.pages (id, space_id, title) values (v_page, v_space, 'Soon gone');
  update public.pages set deleted_at = now() + interval '1 day' where id = v_page;
  if (select deleted_at from public.pages where id = v_page) > now() then
    raise exception 'a deletion was dated ahead of the server''s clock';
  end if;
end;
$$;

-- A page under a deleted one, not deleted itself, is not moved out from
-- under it by anyone but the space's owner who never had what was on it:
-- the move needs the page readable where it was. A page they can read
-- moves as before.
do $$
declare
  v_root constant uuid := 'fa000000-0000-0000-0000-000000000002';
  v_gone constant uuid := 'fa000000-0000-0000-0000-000000000003';
  v_under constant uuid := 'fa000000-0000-0000-0000-000000000004';
  v_one constant uuid := 'fa000000-0000-0000-0000-000000000005';
  v_two constant uuid := 'fa000000-0000-0000-0000-000000000006';
  v_space uuid;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into public.pages (id, space_id, title) values (v_root, v_space, 'Shared root');
  insert into public.page_content (page_id) values (v_root);
  insert into public.pages (id, space_id, parent_id, title) values
    (v_gone, v_space, v_root, 'Deleted parent'),
    (v_under, v_space, v_gone, 'Live under it'),
    (v_one, v_space, v_root, 'One'),
    (v_two, v_space, v_root, 'Two');
  insert into public.page_content (page_id, plain_text) values (v_under, 'what was taken off');
  update public.pages set deleted_at = now() - interval '1 hour' where id = v_gone;
  perform public.share_page(v_root, 'editor@example.com', 'editor');
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  perform public.accept_page_invite(v_root);

  if public.can_read_page(v_under) then
    raise exception 'the editor read a page under one deleted before they had it';
  end if;
  perform pg_temp.refused(format('update public.pages set parent_id = %L where id = %L', v_root, v_under), 'forbidden_move');
  -- A page they can read moves under another they can.
  update public.pages set parent_id = v_two where id = v_one;
  if not found or (select parent_id from public.pages where id = v_one) is distinct from v_two then
    raise exception 'the editor could not move a page they had';
  end if;
end;
$$;

-- A deleted file's row reaches the members of its page, so their devices
-- drop their copies, but says only that it was deleted: no name, type,
-- size or path, which holds its name, for them or anyone the page is
-- shared with later.
do $$
declare
  v_file constant uuid := 'fa000000-0000-0000-0000-0000000000f1';
  v_root constant uuid := 'fa000000-0000-0000-0000-000000000002';
  v_space uuid;
  v_before timestamptz;
  r record;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into public.files (id, space_id, page_id, name, storage_path, mime_type, size_bytes)
  values (v_file, v_space, v_root, 'secret.txt', v_space || '/' || v_file || '/secret.txt', 'text/plain', 42);
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  select updated_at into v_before from public.files where id = v_file and name = 'secret.txt';
  if v_before is null then
    raise exception 'the editor could not read a file still on a page they have';
  end if;
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  update public.files set deleted_at = now() where id = v_file;
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  select * into r from public.files where id = v_file and updated_at > v_before;
  if r.id is null or r.deleted_at is null then
    raise exception 'a file''s deletion did not reach a member of its page';
  end if;
  if r.name <> '' or r.mime_type is not null or r.size_bytes is not null or r.storage_path is not null then
    raise exception 'a deleted file kept "%", %, % bytes at %', r.name, r.mime_type, r.size_bytes, r.storage_path;
  end if;

  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  perform public.share_page(v_root, 'viewer@example.com', 'viewer');
  perform pg_temp.act_as('a3000000-0000-0000-0000-000000000003');
  perform public.accept_page_invite(v_root);
  select * into r from public.files where id = v_file;
  if r.id is null or r.name <> '' or r.storage_path is not null then
    raise exception 'a member who came later read a deleted file''s name: %', r.name;
  end if;
end;
$$;

-- The server posting a comment keeps to a page's bounds by the row's
-- author, clamps an agent's name, and checks the thread is on the page.
-- A thread on another page, and the owner's page the replies go on.
reset role;
insert into public.comments (id, page_id, block_id, author_id, body)
values ('fa000000-0000-0000-0000-0000000000c1', 'e0000000-0000-0000-0000-0000000000e1', 'b1',
        'a1000000-0000-0000-0000-000000000001', 'Root');
-- The service role posts an agent's reply: its name is clamped to 80, and
-- its parent must be on the page. It carries no person's sub, and its
-- claim names the role, as PostgREST sets them from the service key.
set local role service_role;
select set_config('request.jwt.claim.sub', '', true);
select set_config('request.jwt.claims', '{"role":"service_role"}', true);
insert into public.comments (id, page_id, block_id, author_id, author_kind, agent_name, body)
values ('fa000000-0000-0000-0000-0000000000c2', 'fa000000-0000-0000-0000-000000000001', 'b1',
        'a1000000-0000-0000-0000-000000000001', 'agent', repeat('n', 200), 'An answer');
do $$
begin
  if (select char_length(agent_name) from public.comments where id = 'fa000000-0000-0000-0000-0000000000c2') <> 80 then
    raise exception 'an agent''s name was not clamped to 80 characters';
  end if;
  begin
    insert into public.comments (page_id, block_id, author_id, author_kind, parent_comment_id, body)
    values ('fa000000-0000-0000-0000-000000000001', 'b1', 'a1000000-0000-0000-0000-000000000001', 'agent',
            'fa000000-0000-0000-0000-0000000000c1', 'Reply on the wrong page');
    raise exception 'the server threaded a reply onto another page';
  exception when check_violation then
    if sqlerrm not like '%parent_comment_on_other_page%' then
      raise;
    end if;
  end;
end;
$$;
-- 2,000 of the viewer's on a page, seeded with no bound as the migrations
-- do: one more, posted by the server as the viewer, is refused.
reset role;
select set_config('request.jwt.claims', '', true);
insert into public.comments (page_id, block_id, body, author_id)
select 'fa000000-0000-0000-0000-000000000001', 'b1', 'y', 'a3000000-0000-0000-0000-000000000003'
from generate_series(1, 2000);
set local role service_role;
select set_config('request.jwt.claim.sub', '', true);
select set_config('request.jwt.claims', '{"role":"service_role"}', true);
do $$
begin
  begin
    insert into public.comments (page_id, block_id, author_id, body)
    values ('fa000000-0000-0000-0000-000000000001', 'b1', 'a3000000-0000-0000-0000-000000000003', 'One more');
    raise exception 'the server posted past 2,000 of one person''s comments on a page';
  exception when check_violation then
    if sqlerrm <> 'too_many_comments' then
      raise;
    end if;
  end;
end;
$$;
select set_config('request.jwt.claims', '', true);
-- A comment keeps its created_at: its author cannot move it.
reset role;
insert into public.comments (id, page_id, block_id, author_id, body)
values ('fa000000-0000-0000-0000-0000000000c3', 'fa000000-0000-0000-0000-000000000001', 'b1',
        'a1000000-0000-0000-0000-000000000001', 'Mine');
set local role authenticated;
select pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
select pg_temp.refused(
  'update public.comments set created_at = now() - interval ''1 day'' where id = ''fa000000-0000-0000-0000-0000000000c3''',
  'comment_immutable');

-- A device's name and platform are in bounds, and a user has at most 100.
do $$
declare
  v_them constant uuid := 'a5000000-0000-0000-0000-000000000005';
begin
  perform pg_temp.act_as(v_them);
  begin
    insert into public.devices (user_id, name) values (v_them, repeat('n', 257));
    raise exception 'a device name of 257 characters went in';
  exception when check_violation then
    if sqlerrm not like '%devices_name_length%' then
      raise;
    end if;
  end;
  insert into public.devices (user_id, name)
  select v_them, 'd' from generate_series(1, 100);
  begin
    insert into public.devices (user_id, name) values (v_them, 'one too many');
    raise exception 'a 101st device went in';
  exception when check_violation then
    if sqlerrm <> 'too_many_devices' then
      raise;
    end if;
  end;
end;
$$;

-- 100 devices signed out, whose rows stay, keep no new one out.
reset role;
insert into public.devices (user_id, name, revoked_at)
select 'a4000000-0000-0000-0000-000000000004', 'Signed out', now() from generate_series(1, 100);
set local role authenticated;
select pg_temp.act_as('a4000000-0000-0000-0000-000000000004');
insert into public.devices (user_id, name) values ('a4000000-0000-0000-0000-000000000004', 'New one');

-- An AI job's request is in bounds as it is written; the apps and the
-- function send far less.
do $$
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  begin
    insert into public.ai_jobs (surface, request)
    values ('workspace', jsonb_build_object('instruction', repeat('x', 1048577)));
    raise exception 'an AI job request over 1 MB went in';
  exception when check_violation then
    if sqlerrm <> 'ai_job_request_too_long' then
      raise;
    end if;
  end;
end;
$$;
-- A job already past the bounds, from before they were kept, still
-- changes: it is marked failed, and given a result; its request is not
-- made longer.
reset role;
alter table public.ai_jobs disable trigger ai_jobs_in_bounds;
insert into public.ai_jobs (id, user_id, surface, status, request)
values ('fa000000-0000-0000-0000-0000000000a9', 'a1000000-0000-0000-0000-000000000001', 'workspace', 'running',
        jsonb_build_object('instruction', repeat('x', 1048577)));
alter table public.ai_jobs enable trigger ai_jobs_in_bounds;
set local role authenticated;
do $$
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  update public.ai_jobs set status = 'failed', error = 'The run stopped before it finished'
  where id = 'fa000000-0000-0000-0000-0000000000a9';
  update public.ai_jobs set result = '{"reply": ""}' where id = 'fa000000-0000-0000-0000-0000000000a9';
  if (select status from public.ai_jobs where id = 'fa000000-0000-0000-0000-0000000000a9') <> 'failed' then
    raise exception 'a job past the bounds could not be marked failed';
  end if;
  begin
    update public.ai_jobs set request = jsonb_build_object('instruction', repeat('y', 1048577))
    where id = 'fa000000-0000-0000-0000-0000000000a9';
    raise exception 'a job''s request was written past 1 MB';
  exception when check_violation then
    if sqlerrm <> 'ai_job_request_too_long' then
      raise;
    end if;
  end;
end;
$$;

-- A speech-to-text key is saved for Soniox alone.
do $$
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  begin
    perform public.set_stt_secret('made-up', 'k');
    raise exception 'a key was saved for a provider that has none here';
  exception when invalid_parameter_value then
    if sqlerrm <> 'unknown_stt_provider' then
      raise;
    end if;
  end;
  perform public.set_stt_secret('soniox', 'k');
  if not public.has_stt_secret('soniox') then
    raise exception 'Soniox''s key was not saved';
  end if;
  perform public.set_stt_secret('soniox', '');
end;
$$;

-- The workspace agent reads a page's title as one others read when its
-- card is on a page they read, by a subpage block or an inline mention in
-- its text.
do $$
declare
  v_host constant uuid := 'fa000000-0000-0000-0000-00000000000a';
  v_mentioned constant uuid := 'fa000000-0000-0000-0000-00000000000b';
  v_carded constant uuid := 'fa000000-0000-0000-0000-00000000000c';
  v_space uuid;
  v_found uuid[];
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into public.pages (id, space_id, title) values
    (v_host, v_space, 'Host'), (v_mentioned, v_space, 'Mentioned'), (v_carded, v_space, 'Carded');
  insert into public.page_content (page_id, content) values (v_host, jsonb_build_object(
    'document', jsonb_build_object('type', 'page', 'id', 'root', 'children', jsonb_build_array(
      jsonb_build_object('id', 'b1', 'type', 'paragraph', 'data', jsonb_build_object('delta', jsonb_build_array(
        jsonb_build_object('insert', 'see '),
        jsonb_build_object('insert', 'Mentioned', 'attributes', jsonb_build_object(
          'mention', jsonb_build_object('kind', 'page', 'id', v_mentioned::text, 'name', 'Mentioned')))))),
      jsonb_build_object('id', 'b2', 'type', 'subpage', 'data', jsonb_build_object('page_id', v_carded::text)))),
    'meta', jsonb_build_object('v', 1, 'blocks', '{}'::jsonb, 'deleted', '{}'::jsonb)));
  select array_agg(page_id::uuid order by page_id) into v_found
  from public.agent_card_pages(array[v_host]);
  if not (v_found @> array[v_mentioned, v_carded]) then
    raise exception 'the agent did not read a title through a mention and a card: %', v_found;
  end if;
end;
$$;

-- The workspace agent's search cuts a match's words from a page's own
-- text: not from what its sites wrote, a link card's title and description
-- and a video's title, though the page is found by them still, in its
-- place, with no words. A card's link and a video's notes are its own.
do $$
declare
  v_older constant uuid := 'fa000000-0000-0000-0000-0000000000b1';
  v_newer constant uuid := 'fa000000-0000-0000-0000-0000000000b2';
  v_space uuid;
  v_hits text[];
  v_snippets text[];
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into public.pages (id, space_id, title) values (v_older, v_space, 'Snippet host');
  insert into public.page_content (page_id, content, plain_text) values (v_older, jsonb_build_object(
    'document', jsonb_build_object('type', 'page', 'id', 'root', 'children', jsonb_build_array(
      jsonb_build_object('id', 'b1', 'type', 'paragraph', 'data', jsonb_build_object('delta', jsonb_build_array(
        jsonb_build_object('insert', 'my own words about '),
        jsonb_build_object('insert', 'quokkas', 'attributes', jsonb_build_object('bold', true))))),
      jsonb_build_object('id', 'b2', 'type', 'link_preview', 'data', jsonb_build_object(
        'url', 'https://example.com/z', 'title', 'Sites write zebras here', 'description', 'zebras galore')),
      jsonb_build_object('id', 'b3', 'type', 'video', 'data', jsonb_build_object(
        'url', 'https://example.com/v', 'title', 'Elephant video', 'notes', 'Notes I took')))),
    'meta', jsonb_build_object('v', 1, 'blocks', '{}'::jsonb, 'deleted', '{}'::jsonb)),
    E'my own words about quokkas\nSites write zebras here\nElephant video\nNotes I took');
  insert into public.pages (id, space_id, title) values (v_newer, v_space, 'Second host');
  insert into public.page_content (page_id, content, plain_text) values (v_newer, jsonb_build_object(
    'document', jsonb_build_object('type', 'page', 'id', 'root', 'children', jsonb_build_array(
      jsonb_build_object('id', 'b1', 'type', 'paragraph', 'data', jsonb_build_object('delta', jsonb_build_array(
        jsonb_build_object('insert', 'zebras of my own')))))),
    'meta', jsonb_build_object('v', 1, 'blocks', '{}'::jsonb, 'deleted', '{}'::jsonb)),
    'zebras of my own');

  if public.page_own_text((select content from public.page_content where page_id = v_older))
     is distinct from E'my own words about quokkas\nhttps://example.com/z\nNotes I took' then
    raise exception 'a page''s own text kept what its sites wrote, or lost its own';
  end if;

  -- Found by the newer page's own text and the older page's card, the
  -- newer first, as before; the older has no words.
  select array_agg(page_id::text order by ord), array_agg(coalesce(snippet, '-') order by ord)
  into v_hits, v_snippets
  from public.agent_search_pages('zebras', '{}', 20) with ordinality s (page_id, title, snippet, ord);
  if v_hits is distinct from array[v_newer::text, v_older::text]
     or v_snippets is distinct from array['zebras of my own', '-'] then
    raise exception 'a search found %, with %', v_hits, v_snippets;
  end if;
  if (select snippet from public.agent_search_pages('Elephant', '{}', 20) where page_id = v_older) is not null then
    raise exception 'a search''s words were a video''s title';
  end if;
  if (select snippet from public.agent_search_pages('quokkas', '{}', 20) where page_id = v_older)
     is distinct from E'my own words about quokkas\nhttps://example.com/z\nNotes I took' then
    raise exception 'a search''s words were not cut from the page''s own text';
  end if;
  if (select snippet from public.agent_search_pages('took', '{}', 20) where page_id = v_older) is null then
    raise exception 'a search found no words in a video''s notes';
  end if;
end;
$$;

-- changed_at is when a row was changed where it was changed: the time its
-- writer sends, no later than the server's clock, or the server's when it
-- sends none or the same as before. A lease of a page's public link alone
-- is no change. Clients read and write it on pages, folders, comments,
-- files and recordings alike, and a page's owner deletes a member's file
-- on it with the time the device changed it.
do $$
declare
  v_space uuid;
  v_copy constant uuid := 'fa000000-0000-0000-0000-0000000000d1';
  v_ahead constant uuid := 'fa000000-0000-0000-0000-0000000000d2';
  v_none constant uuid := 'fa000000-0000-0000-0000-0000000000d3';
  v_folder constant uuid := 'fa000000-0000-0000-0000-0000000000d4';
  v_comment constant uuid := 'fa000000-0000-0000-0000-0000000000d5';
  v_file constant uuid := 'fa000000-0000-0000-0000-0000000000d6';
  v_rec constant uuid := 'fa000000-0000-0000-0000-0000000000d7';
  v_theirs constant uuid := 'fa000000-0000-0000-0000-0000000000d8';
  v_root constant uuid := 'fa000000-0000-0000-0000-000000000002';
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();

  -- Made: a time sent stays, one ahead of the server is the server's, and
  -- none is the server's.
  insert into public.pages (id, space_id, title, changed_at) values
    (v_copy, v_space, 'Old copy', now() - interval '1 day'),
    (v_ahead, v_space, 'Ahead', now() + interval '1 day');
  insert into public.pages (id, space_id, title) values (v_none, v_space, 'None sent');
  if (select changed_at from public.pages where id = v_copy) is distinct from now() - interval '1 day'
     or (select changed_at from public.pages where id = v_ahead) is distinct from now()
     or (select changed_at from public.pages where id = v_none) is distinct from now() then
    raise exception 'a new row''s changed_at is not the time sent, no later than now, or now';
  end if;

  -- Changed: none sent, or the same, is now; another time sent stays, no
  -- later than now.
  update public.pages set title = 'Renamed' where id = v_copy;
  if (select changed_at from public.pages where id = v_copy) is distinct from now() then
    raise exception 'a change with no changed_at sent did not move it to now';
  end if;
  update public.pages set title = 'Again', changed_at = now() - interval '2 hours' where id = v_copy;
  if (select changed_at from public.pages where id = v_copy) is distinct from now() - interval '2 hours' then
    raise exception 'a change lost the changed_at it sent';
  end if;
  update public.pages set title = 'Same time', changed_at = now() - interval '2 hours' where id = v_copy;
  if (select changed_at from public.pages where id = v_copy) is distinct from now() then
    raise exception 'a change that sent the same changed_at did not move it to now';
  end if;
  update public.pages set changed_at = now() + interval '1 hour' where id = v_copy;
  if (select changed_at from public.pages where id = v_copy) is distinct from now() then
    raise exception 'a change dated its changed_at ahead of the server''s clock';
  end if;

  -- A lease alone moves neither changed_at nor updated_at.
  update public.pages set changed_at = now() - interval '3 hours' where id = v_copy;
  perform public.lease_public_link(v_copy);
  if (select changed_at from public.pages where id = v_copy) is distinct from now() - interval '3 hours' then
    raise exception 'a public link''s lease moved the page''s changed_at';
  end if;

  -- The other tables keep a time sent too.
  insert into public.folders (id, space_id, name, changed_at) values (v_folder, v_space, 'F', now() - interval '1 day');
  insert into public.comments (id, page_id, block_id, body, changed_at)
  values (v_comment, v_copy, 'b1', 'Hi', now() - interval '1 day');
  insert into public.files (id, space_id, page_id, name, changed_at)
  values (v_file, v_space, v_copy, 'c.txt', now() - interval '1 day');
  insert into public.recordings (id, page_id, changed_at) values (v_rec, v_copy, now() - interval '1 day');
  if (select count(*) from public.folders where id = v_folder and changed_at = now() - interval '1 day')
     + (select count(*) from public.comments where id = v_comment and changed_at = now() - interval '1 day')
     + (select count(*) from public.files where id = v_file and changed_at = now() - interval '1 day')
     + (select count(*) from public.recordings where id = v_rec and changed_at = now() - interval '1 day') <> 4 then
    raise exception 'a folder, comment, file or recording lost the changed_at it was made with';
  end if;
  update public.comments set resolved = true where id = v_comment;
  if (select changed_at from public.comments where id = v_comment) is distinct from now() then
    raise exception 'a comment''s change did not move its changed_at to now';
  end if;

  -- A member's file on the owner's page: the owner deletes it, with the
  -- time their device changed it.
  perform pg_temp.act_as('a2000000-0000-0000-0000-000000000002');
  insert into public.files (id, space_id, page_id, name)
  select v_theirs, s.id, v_root, 'theirs.txt' from public.spaces s where s.owner_id = auth.uid();
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  update public.files set deleted_at = now(), changed_at = now() - interval '1 minute' where id = v_theirs;
  if not found or (select changed_at from public.files where id = v_theirs) is distinct from now() - interval '1 minute' then
    raise exception 'the page''s owner could not delete a member''s file with the time it was changed';
  end if;
  -- Sent again as the device had it, the row stays deleted and emptied.
  update public.files set name = 'theirs.txt', storage_path = 'x/y/theirs.txt'
  where id = v_theirs;
  if (select name from public.files where id = v_theirs) <> '' then
    raise exception 'a deleted file took back its name';
  end if;
end;
$$;

-- A write another trigger makes keeps changed_at: the touch of a subtree a
-- move brings under a share, so its readers pull it, and a foreign key's
-- set null. The page moved is changed now.
do $$
declare
  v_space uuid;
  v_root constant uuid := 'fa000000-0000-0000-0000-000000000002';
  v_top constant uuid := 'fa000000-0000-0000-0000-0000000000e1';
  v_sub constant uuid := 'fa000000-0000-0000-0000-0000000000e2';
  v_comment constant uuid := 'fa000000-0000-0000-0000-0000000000e3';
  v_file constant uuid := 'fa000000-0000-0000-0000-0000000000e4';
  v_rec constant uuid := 'fa000000-0000-0000-0000-0000000000e5';
  v_folder constant uuid := 'fa000000-0000-0000-0000-0000000000e6';
  v_filed constant uuid := 'fa000000-0000-0000-0000-0000000000e7';
  v_then constant timestamptz := now() - interval '1 day';
  v_before timestamptz;
begin
  perform pg_temp.act_as('a1000000-0000-0000-0000-000000000001');
  select id into v_space from public.spaces where owner_id = auth.uid();
  insert into public.pages (id, space_id, title, changed_at) values (v_top, v_space, 'Private', v_then);
  insert into public.pages (id, space_id, parent_id, title, changed_at) values (v_sub, v_space, v_top, 'Under it', v_then);
  insert into public.comments (id, page_id, block_id, body, changed_at) values (v_comment, v_sub, 'b1', 'Hi', v_then);
  insert into public.files (id, space_id, page_id, name, changed_at) values (v_file, v_space, v_sub, 'f.txt', v_then);
  insert into public.recordings (id, page_id, changed_at) values (v_rec, v_sub, v_then);
  v_before := clock_timestamp();

  update public.pages set parent_id = v_root where id = v_top;
  if (select updated_at from public.pages where id = v_sub) <= v_before
     or (select updated_at from public.comments where id = v_comment) <= v_before then
    raise exception 'a move under a share did not touch the subtree';
  end if;
  if (select changed_at from public.pages where id = v_sub) is distinct from v_then
     or (select changed_at from public.comments where id = v_comment) is distinct from v_then
     or (select changed_at from public.files where id = v_file) is distinct from v_then
     or (select changed_at from public.recordings where id = v_rec) is distinct from v_then then
    raise exception 'the touch of a moved subtree moved its changed_at';
  end if;
  if (select changed_at from public.pages where id = v_top) is distinct from now() then
    raise exception 'the page moved was not changed now';
  end if;

  -- A folder that goes takes its pages out of it, a foreign key's set null.
  insert into public.folders (id, space_id, name) values (v_folder, v_space, 'Going');
  insert into public.pages (id, space_id, folder_id, title, changed_at) values (v_filed, v_space, v_folder, 'Filed', v_then);
  delete from public.folders where id = v_folder;
  if (select folder_id from public.pages where id = v_filed) is not null
     or (select changed_at from public.pages where id = v_filed) is distinct from v_then then
    raise exception 'a foreign key''s set null moved changed_at';
  end if;
end;
$$;

-- A walk of the tree ends on a cycle, should one ever get past
-- pages_guard: the page moved under Ideas made its parent. It stops 1,000
-- pages up, as deep as a tree may go; one that went round for good would
-- time out.
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
