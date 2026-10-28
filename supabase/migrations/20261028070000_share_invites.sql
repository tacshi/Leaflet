-- Sharing a page invites: the page reaches the person it is shared with
-- only once they accept, and they can decline. A page_permissions row is
-- pending until accepted_at is set, and every access check counts only the
-- accepted ones. The person invited still reads their own rows, pending or
-- not (page_permissions_select), and the page's owner reads them all.

alter table public.page_permissions add column accepted_at timestamptz;

-- The shares made before invites stand as accepted. updated_at stays as it
-- was, so the devices do not pull every shared page again over it.
alter table public.page_permissions disable trigger page_permissions_set_updated_at;
update public.page_permissions set accepted_at = created_at where accepted_at is null;
alter table public.page_permissions enable trigger page_permissions_set_updated_at;

create index page_permissions_pending_idx on public.page_permissions (user_id)
  where accepted_at is null;

-- Only the person invited accepts, through accept_page_invite: the owner,
-- who may update the row (page_permissions_update), cannot set accepted_at,
-- and a row moved to another page or person waits for them again. A share
-- made by a caller always starts pending. Writes with no caller (the
-- service role) keep what they set.
create or replace function public.guard_page_permission_acceptance()
returns trigger
language plpgsql
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
begin
  if v_uid is null then
    return new;
  end if;
  if tg_op = 'INSERT' then
    new.accepted_at := null;
  elsif (new.page_id, new.user_id) is distinct from (old.page_id, old.user_id) then
    new.accepted_at := null;
  elsif new.accepted_at is distinct from old.accepted_at and new.user_id is distinct from v_uid then
    new.accepted_at := old.accepted_at;
  end if;
  return new;
end;
$$;

drop trigger if exists page_permissions_guard_acceptance on public.page_permissions;
create trigger page_permissions_guard_acceptance before insert or update on public.page_permissions
  for each row execute function public.guard_page_permission_acceptance();

-- ---------------------------------------------------------------------------
-- Access helpers: accepted shares only
-- ---------------------------------------------------------------------------

-- 'owner' | 'editor' | 'viewer' | null for the current user on a page.
-- An accepted permission on any ancestor applies to its subpages.
create or replace function public.page_role(p_page_id uuid)
returns text
language sql
stable
security definer
set search_path = ''
as $$
  with recursive chain as (
    select p.id, p.parent_id, p.space_id, 0 as depth
    from public.pages p
    where p.id = p_page_id
    union all
    select p.id, p.parent_id, p.space_id, c.depth + 1
    from public.pages p
    join chain c on p.id = c.parent_id
    where c.depth < 64
  )
  select case
    when (select auth.uid()) is null then null
    when exists (
      select 1 from chain c
      join public.spaces s on s.id = c.space_id
      where c.depth = 0 and s.owner_id = (select auth.uid())
    ) then 'owner'
    when exists (
      select 1 from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.user_id = (select auth.uid()) and pp.role = 'editor'
        and pp.accepted_at is not null
    ) then 'editor'
    when exists (
      select 1 from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.user_id = (select auth.uid()) and pp.role = 'viewer'
        and pp.accepted_at is not null
    ) then 'viewer'
    else null
  end;
$$;

-- True when the current user owns the space or has accepted a page shared
-- in it.
create or replace function public.has_space_access(p_space_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select public.is_space_owner(p_space_id) or exists (
    select 1
    from public.page_permissions pp
    join public.pages p on p.id = pp.page_id
    where p.space_id = p_space_id and pp.user_id = (select auth.uid())
      and pp.accepted_at is not null
  );
$$;

-- True when the current user and p_other share at least one page, either
-- way, by a share that was accepted.
create or replace function public.shares_page_with(p_other uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select exists (
    select 1
    from public.page_permissions pp
    join public.pages p on p.id = pp.page_id
    join public.spaces s on s.id = p.space_id
    where pp.accepted_at is not null
      and ((pp.user_id = (select auth.uid()) and s.owner_id = p_other)
        or (pp.user_id = p_other and s.owner_id = (select auth.uid())))
  ) or exists (
    select 1
    from public.page_permissions a
    join public.pages pa on pa.id = a.page_id
    join public.pages pb on pb.space_id = pa.space_id
    join public.page_permissions b on b.page_id = pb.id
    where a.user_id = (select auth.uid()) and b.user_id = p_other
      and a.accepted_at is not null and b.accepted_at is not null
  );
$$;

-- can_view_page, can_edit_page, can_access_space_path, can_view_image_path
-- and the policies on them read these, so they count accepted shares too.

-- ---------------------------------------------------------------------------
-- RPCs
-- ---------------------------------------------------------------------------

-- Share a page (and its subpages) by email: an invite, which the person
-- accepts or declines. It answers no rows whether or not the email has an
-- account. Sharing again changes only the role: an accepted share stays
-- accepted, a pending one pending.
create or replace function public.share_page(p_page_id uuid, p_email text, p_role text)
returns table (user_id uuid, display_name text, email text, role text)
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_user uuid;
begin
  if public.page_role(p_page_id) is distinct from 'owner' then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  if p_role not in ('viewer', 'editor') then
    raise exception 'invalid_role' using errcode = '22023';
  end if;

  select u.id into v_user
  from auth.users u
  where lower(u.email) = lower(trim(p_email))
  limit 1;

  if v_user is null then
    return;
  end if;
  if v_user = auth.uid() then
    raise exception 'cannot_share_with_self' using errcode = '22023';
  end if;

  insert into public.page_permissions (page_id, user_id, role, invited_by, accepted_at)
  values (p_page_id, v_user, p_role, auth.uid(), null)
  on conflict on constraint page_permissions_pkey
    do update set role = excluded.role;
end;
$$;

-- The caller's pending invites, newest first: pages that are still there.
create or replace function public.page_invites()
returns table (page_id uuid, title text, role text, inviter_name text, created_at timestamptz)
language sql
stable
security definer
set search_path = ''
as $$
  select pp.page_id, p.title, pp.role, coalesce(pr.display_name, ''), pp.created_at
  from public.page_permissions pp
  join public.pages p on p.id = pp.page_id
  left join public.profiles pr on pr.id = pp.invited_by
  where pp.user_id = (select auth.uid())
    and pp.accepted_at is null
    and p.deleted_at is null
  order by pp.created_at desc, pp.page_id;
$$;

-- Accept the caller's invite to a page. Its updated_at moves on, so the
-- caller's devices pull the page with the next pull. Accepting again does
-- nothing.
create or replace function public.accept_page_invite(p_page_id uuid)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
begin
  if v_uid is null then
    raise exception 'not_signed_in' using errcode = '42501';
  end if;
  update public.page_permissions pp
  set accepted_at = now()
  where pp.page_id = p_page_id and pp.user_id = v_uid and pp.accepted_at is null;
  if not found and not exists (
    select 1 from public.page_permissions pp
    where pp.page_id = p_page_id and pp.user_id = v_uid
  ) then
    raise exception 'no_invite' using errcode = 'P0002';
  end if;
end;
$$;

-- Decline the caller's pending invite to a page; an accepted share stays.
create or replace function public.decline_page_invite(p_page_id uuid)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
begin
  if v_uid is null then
    raise exception 'not_signed_in' using errcode = '42501';
  end if;
  delete from public.page_permissions pp
  where pp.page_id = p_page_id and pp.user_id = v_uid and pp.accepted_at is null;
end;
$$;

-- Everyone with access to a page: the owner, plus grants on the page or any
-- ancestor (the highest accepted role wins), and whether each has accepted.
-- Only the owner sees those still invited. Callers must be able to view the
-- page. Its columns changed, so it is made again.
drop function public.page_members(uuid);

create function public.page_members(p_page_id uuid)
returns table (user_id uuid, display_name text, email text, role text, accepted boolean)
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_owner boolean;
begin
  if not public.can_view_page(p_page_id) then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  v_owner := public.page_role(p_page_id) = 'owner';

  return query
  with recursive chain as (
    select p.id, p.parent_id, p.space_id, 0 as depth
    from public.pages p where p.id = p_page_id
    union all
    select p.id, p.parent_id, p.space_id, c.depth + 1
    from public.pages p join chain c on p.id = c.parent_id
    where c.depth < 64
  ),
  grants as (
    select pp.user_id as uid,
           bool_or(pp.accepted_at is not null) as is_accepted,
           coalesce(
             max(case when pp.accepted_at is not null then case pp.role when 'editor' then 2 else 1 end end),
             max(case pp.role when 'editor' then 2 else 1 end)
           ) as rank
    from chain c
    join public.page_permissions pp on pp.page_id = c.id
    group by pp.user_id
  ),
  members as (
    select s.owner_id as uid, 'owner'::text as member_role, true as is_accepted, 0 as ord
    from chain c join public.spaces s on s.id = c.space_id
    where c.depth = 0
    union all
    select g.uid, case g.rank when 2 then 'editor' else 'viewer' end, g.is_accepted, 1
    from grants g
    where g.is_accepted or v_owner
  )
  select m.uid, coalesce(pr.display_name, ''), u.email::text, m.member_role, m.is_accepted
  from members m
  join auth.users u on u.id = m.uid
  left join public.profiles pr on pr.id = m.uid
  order by m.ord, coalesce(pr.display_name, u.email::text);
end;
$$;

revoke execute on function public.page_members(uuid) from public, anon;
revoke execute on function public.page_invites() from public, anon;
revoke execute on function public.accept_page_invite(uuid) from public, anon;
revoke execute on function public.decline_page_invite(uuid) from public, anon;
grant execute on function public.page_members(uuid) to authenticated;
grant execute on function public.page_invites() to authenticated;
grant execute on function public.accept_page_invite(uuid) to authenticated;
grant execute on function public.decline_page_invite(uuid) to authenticated;
