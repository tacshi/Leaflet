-- Checks what the migrations made of the rows seed/*.sql put in before
-- them, each right after the migration it is named for: shares kept,
-- invites moved, deleted comments emptied, history pruned and dated back,
-- pages placed, deleted pages read by who had them, folders out of their
-- loops, and all of it gone with its owner's account.
-- scripts/check-migrations.sh runs it, as postgres, after every
-- migration. It changes nothing: it rolls back each of its two
-- transactions. A check that fails raises, and psql stops there.

\set ON_ERROR_STOP on
\set VERBOSITY terse

begin;

do $$
declare
  v_page constant uuid := '5eed0000-0000-0000-0000-0000000000a1';
  v_subpage constant uuid := '5eed0000-0000-0000-0000-0000000000a2';
  v_owner constant uuid := '5eed0000-0000-0000-0000-000000000001';
  v_editor constant uuid := '5eed0000-0000-0000-0000-000000000002';
  v_seeded timestamptz := (select at from seed.marks where name = 'seeded');
  r record;
begin
  -- A share from before invites stands as accepted when it was made, and
  -- the devices were not sent it again.
  select * into r from public.page_permissions where page_id = v_page and user_id = v_editor;
  if r.accepted_at is distinct from '2026-01-02'::timestamptz then
    raise exception 'the share from before invites was accepted at %', r.accepted_at;
  end if;
  if r.updated_at <> (select at from seed.marks where name = 'share_updated_at') then
    raise exception 'accepting the shares from before invites moved their updated_at';
  end if;

  -- An invite to an account that confirmed its address waits for it,
  -- showing the address.
  select * into r from public.page_permissions
  where page_id = v_page and user_id = '5eed0000-0000-0000-0000-000000000004';
  if r.accepted_at is not null or r.invited_email is distinct from 'seed-viewer@example.com' then
    raise exception 'the invite to a confirmed address is %, to %', r.accepted_at, r.invited_email;
  end if;
  if r.updated_at <> (select at from seed.marks where name = 'invite_updated_at') then
    raise exception 'noting the invites'' addresses moved their updated_at';
  end if;

  -- One to an account that had not confirmed its address waits for the
  -- address instead.
  if exists (
    select 1 from public.page_permissions
    where page_id = v_page and user_id = '5eed0000-0000-0000-0000-000000000003'
  ) then
    raise exception 'an unconfirmed account still holds its invite';
  end if;
  select * into r from public.page_email_invites where page_id = v_page;
  if r.email is distinct from 'seed-unconfirmed@example.com' or r.role is distinct from 'editor'
     or r.invited_by is distinct from v_owner or r.created_at is distinct from '2026-03-02'::timestamptz then
    raise exception 'the invite waiting for the address is %', to_jsonb(r);
  end if;

  -- Deleted comments keep nothing of what they said, nor a guest's name.
  if exists (
    select 1 from public.comments
    where page_id = v_page and (body <> '' or quote is not null or mentions <> '[]'::jsonb)
  ) then
    raise exception 'a deleted comment kept its text';
  end if;
  if (select agent_name from public.comments where id = '5eed0000-0000-0000-0000-0000000000c2') is distinct from 'Guest' then
    raise exception 'a deleted guest comment kept the guest''s name';
  end if;

  -- The owner's newest 50 unnamed snapshots stay, and the editor's
  -- newest 50 named ones.
  select count(*) as kept, count(*) filter (where created_at > '2026-01-01'::timestamptz + interval '5 minutes') as newest into r
  from public.page_history where page_id = v_page and reason = 'autosave';
  if r.kept <> 50 or r.newest <> 50 then
    raise exception 'the page kept % unnamed snapshots, % of its newest 50', r.kept, r.newest;
  end if;
  select count(*) as kept, count(*) filter (where created_at > '2026-02-01'::timestamptz + interval '2 minutes') as newest into r
  from public.page_history where page_id = v_page and reason = 'manual';
  if r.kept <> 50 or r.newest <> 50 then
    raise exception 'the editor kept % named snapshots, % of their newest 50', r.kept, r.newest;
  end if;

  -- A snapshot dated ahead came back to when it was dated back.
  if (select created_at from public.page_history where page_id = v_subpage) not between v_seeded and now() then
    raise exception 'the snapshot dated ahead is still ahead';
  end if;

  -- Pages from before placed_at are placed when it came, not when made.
  if exists (
    select 1 from public.pages where id in (v_page, v_subpage) and placed_at <= v_seeded
  ) then
    raise exception 'a page from before placed_at was placed before it came';
  end if;

  -- A revision out of reach starts again.
  if (select revision from public.page_content where page_id = v_page) <> 0 then
    raise exception 'the content kept its revision out of reach';
  end if;

  if to_regclass('public.page_updates') is not null then
    raise exception 'page_updates is still there';
  end if;
end;
$$;

-- The editor reads the page's history from when they accepted the share,
-- and none of the subpage's from before it was placed under the page.
-- What is on a subpage deleted after they accepted it, before
-- 20261031000000 filled placed_at in, is theirs still, and they may
-- restore the page; what is on one deleted before, is not, nor may they.
-- Both pages they see. A tree deeper than a tree may now go stays read to
-- its deepest page, by those it is shared with, and takes no new page
-- under it.
create function pg_temp.act_as(p_user uuid) returns void
language sql as $$ select set_config('request.jwt.claim.sub', p_user::text, true) $$;
set local role authenticated;
select pg_temp.act_as('5eed0000-0000-0000-0000-000000000002');
do $$
declare
  n bigint;
begin
  select count(*) into n from public.page_history where page_id = '5eed0000-0000-0000-0000-0000000000a1';
  if n <> 50 then
    raise exception 'the editor reads % of the page''s history, not their own 50', n;
  end if;
  if exists (select 1 from public.page_history where page_id = '5eed0000-0000-0000-0000-0000000000a2') then
    raise exception 'the editor reads the subpage''s history from before it was placed';
  end if;

  if (select count(*) from public.pages
      where id in ('5eed0000-0000-0000-0000-0000000000a4', '5eed0000-0000-0000-0000-0000000000a5')) <> 2 then
    raise exception 'the editor does not see the deleted subpages';
  end if;
  select (select count(*) from public.page_content where page_id = '5eed0000-0000-0000-0000-0000000000a4')
       + (select count(*) from public.comments where page_id = '5eed0000-0000-0000-0000-0000000000a4')
  into n;
  if n <> 2 then
    raise exception 'the editor reads % of what is on the subpage deleted after they had it', n;
  end if;
  if exists (select 1 from public.page_content where page_id = '5eed0000-0000-0000-0000-0000000000a5')
     or exists (select 1 from public.comments where page_id = '5eed0000-0000-0000-0000-0000000000a5') then
    raise exception 'the editor reads what is on the subpage deleted before they had it';
  end if;
  update public.pages set deleted_at = null where id = '5eed0000-0000-0000-0000-0000000000a4';
  if not found then
    raise exception 'the editor could not restore the subpage deleted after they had it';
  end if;
  begin
    update public.pages set deleted_at = null where id = '5eed0000-0000-0000-0000-0000000000a5';
    raise exception 'the editor restored the subpage deleted before they had it';
  exception when insufficient_privilege then
    if sqlerrm <> 'forbidden_restore' then
      raise;
    end if;
  end;

  if not exists (select 1 from public.pages where title = 'Deep 300') then
    raise exception 'the editor does not reach the deepest page of a tree deeper than a tree may go';
  end if;
  begin
    insert into public.pages (space_id, parent_id, title)
    select space_id, id, 'Deeper' from public.pages where title = 'Deep 300';
    raise exception 'a page was made under a tree deeper than a tree may go';
  exception when check_violation then
    if sqlerrm <> 'page_too_deep' then
      raise;
    end if;
  end;
end;
$$;

-- What is in a tree deeper than a tree may now go stays its people's to
-- change, sent whole or not, and it moves as long as it goes no deeper.
do $$
declare
  v_deep uuid := (select id from public.pages where title = 'Deep 300');
  v_parent uuid := (select parent_id from public.pages where title = 'Deep 300');
  v_higher uuid := (select id from public.pages where title = 'Deep 280');
  v_space uuid := (select space_id from public.pages where title = 'Deep 300');
begin
  perform pg_temp.act_as('5eed0000-0000-0000-0000-000000000001');
  insert into public.pages (id, space_id, parent_id, title)
  values (v_deep, v_space, v_parent, 'Deep 300, renamed')
  on conflict (id) do update set title = excluded.title;
  insert into public.pages (id, space_id, parent_id, title, deleted_at)
  values (v_deep, v_space, v_parent, 'Deep 300, renamed', now())
  on conflict (id) do update set deleted_at = excluded.deleted_at;
  if (select deleted_at from public.pages where id = v_deep) is null then
    raise exception 'a page past the depth did not take its deletion sent whole';
  end if;
  update public.pages set deleted_at = null, parent_id = v_higher where id = v_deep;
  if (select parent_id from public.pages where id = v_deep) is distinct from v_higher then
    raise exception 'a page past the depth did not move up';
  end if;
  begin
    update public.pages set parent_id = v_deep where id = '5eed0000-0000-0000-0000-0000000000a2';
    raise exception 'a page went under a tree deeper than a tree may go';
  exception when check_violation then
    if sqlerrm <> 'page_too_deep' then
      raise;
    end if;
  end;
end;
$$;

-- Who had a page hears of its deletion, whatever the clock of the device
-- that deleted it said, and whatever moved since. The owner's device, a
-- minute behind, deletes a page and moves the page above it, inside the
-- share; the editor still reads the deleted row. The editor's device,
-- offline meanwhile, deletes a page and the one under it: the first
-- deletion hides neither from them. A device's clock far behind, past when
-- they accepted the share, hides nothing either.
do $$
declare
  v_owner constant uuid := '5eed0000-0000-0000-0000-000000000001';
  v_editor constant uuid := '5eed0000-0000-0000-0000-000000000002';
  v_moved constant uuid := '5eed0000-0000-0000-0000-0000000000d1';
  v_there constant uuid := '5eed0000-0000-0000-0000-0000000000d2';
  v_gone constant uuid := '5eed0000-0000-0000-0000-0000000000d3';
  v_top constant uuid := '5eed0000-0000-0000-0000-0000000000d4';
  v_under constant uuid := '5eed0000-0000-0000-0000-0000000000d5';
  v_behind constant uuid := '5eed0000-0000-0000-0000-0000000000d6';
begin
  perform pg_temp.act_as(v_owner);
  update public.pages set deleted_at = now() - interval '1 minute' where id = v_gone;
  update public.pages set parent_id = v_there where id = v_moved;
  update public.pages set deleted_at = '2025-01-01' where id = v_behind;

  perform pg_temp.act_as(v_editor);
  if not exists (select 1 from public.pages where id = v_gone and deleted_at is not null) then
    raise exception 'the editor does not hear of a page deleted before the page above it moved';
  end if;
  if not exists (select 1 from public.pages where id = v_behind and deleted_at is not null) then
    raise exception 'the editor does not hear of a page deleted by a clock behind their share';
  end if;
  update public.pages set deleted_at = now() - interval '1 minute' where id = v_top;
  if not found then
    raise exception 'the editor could not delete a page that moved under them';
  end if;
  update public.pages set deleted_at = now() - interval '1 minute' where id = v_under;
  if not found then
    raise exception 'deleting a page hid the rest of its tree from the editor deleting it';
  end if;
end;
$$;
reset role;

-- The folders left in loops are out of them: each loop is opened at its
-- folder of the smallest id, which is at the top level and goes to the
-- devices again; the others, the folders in no loop, and the ring that
-- runs through a deleted folder, are where they were, and are not sent
-- again.
do $$
declare
  v_looped timestamptz := (select at from seed.marks where name = 'folders_looped');
  v_are text;
begin
  select string_agg(right(f.id::text, 2) || '>' || coalesce(right(f.parent_folder_id::text, 2), '') ||
           case when f.updated_at > v_looped then '!' else '' end, ' ' order by f.id)
  into v_are
  from public.folders f where f.id::text like '5eed0000-0000-0000-0000-0000000000f_';
  if v_are is distinct from 'f1>! f2>f1 f3>f2 f4>! f5>f6 f6>f4 f7> f8>f7 fa>fb fb>fc fc>fa' then
    raise exception 'the folders that were in loops are %', v_are;
  end if;
  if exists (
    select 1 from public.folders f
    where f.id in ('5eed0000-0000-0000-0000-0000000000f1', '5eed0000-0000-0000-0000-0000000000f4')
      and f.changed_at <= v_looped
  ) then
    raise exception 'a folder taken out of a loop is not changed since, for the devices to take';
  end if;
end;
$$;

-- The address confirmed, the invite waiting for it goes to the account,
-- under the same id.
do $$
declare
  v_invite uuid := (select id from public.page_email_invites where email = 'seed-unconfirmed@example.com');
begin
  update auth.users set email_confirmed_at = now() where id = '5eed0000-0000-0000-0000-000000000003';
  if exists (select 1 from public.page_email_invites where id = v_invite) or not exists (
    select 1 from public.page_permissions
    where user_id = '5eed0000-0000-0000-0000-000000000003' and invite_id = v_invite
      and role = 'editor' and accepted_at is null
  ) then
    raise exception 'confirming the address did not take the invite waiting for it';
  end if;
end;
$$;

rollback;

-- An account deleted goes through every foreign key and trigger, on rows
-- from before this transaction, as a project's are. The editor's: the
-- recording they made on the owner's page stays, theirs no more, with its
-- audio. The owner's: their space goes whole, its folders, some inside
-- others, its pages, however deep, and what is on them.
begin;

delete from auth.users where id = '5eed0000-0000-0000-0000-000000000002';
do $$
begin
  if (select audio_path from public.recordings
      where id = '5eed0000-0000-0000-0000-0000000000b1' and created_by is null)
     is distinct from '5eed0000-0000-0000-0000-000000000002/5eed0000-0000-0000-0000-0000000000b1.wav' then
    raise exception 'an editor''s account did not leave the recording they made on the owner''s page';
  end if;
  if exists (select 1 from public.spaces where owner_id = '5eed0000-0000-0000-0000-000000000002') then
    raise exception 'a deleted account left its space';
  end if;
end;
$$;

delete from auth.users where id = '5eed0000-0000-0000-0000-000000000001';
do $$
begin
  if exists (select 1 from public.spaces where owner_id = '5eed0000-0000-0000-0000-000000000001')
     or exists (select 1 from public.folders where id::text like '5eed0000-%')
     or exists (select 1 from public.pages where id::text like '5eed0000-%' or title like 'Deep %')
     or exists (select 1 from public.page_content where page_id::text like '5eed0000-%')
     or exists (select 1 from public.comments where page_id::text like '5eed0000-%')
     or exists (select 1 from public.recordings where id::text like '5eed0000-%') then
    raise exception 'a deleted account left some of its space';
  end if;
end;
$$;

rollback;
