-- Checks what the migrations made of the rows seed/*.sql put in before
-- them, each right after the migration it is named for: shares kept,
-- invites moved, deleted comments emptied, history pruned and dated back,
-- pages placed, deleted pages read by who had them.
-- scripts/check-migrations.sh runs it, as postgres, after every
-- migration. It changes nothing: it rolls back at the end. A check that
-- fails raises, and psql stops there.

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
-- Both pages they see.
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
end;
$$;
reset role;

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
