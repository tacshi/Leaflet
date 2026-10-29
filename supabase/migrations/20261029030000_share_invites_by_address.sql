-- Sharing still told the owner whether an email has an account: share_page
-- answered alike, but page_members (and page_permissions itself) showed the
-- owner a pending invite only for an address with one, as that account. And
-- share_page matched an account whether or not it had confirmed the
-- address, so whoever signed up with someone else's address got the
-- invites meant for them.
--
-- Now an invite goes to an account only once it has confirmed the address;
-- until one has, it waits for the address in page_email_invites, and an
-- account that confirms the address takes it. To the owner the two look
-- alike, from the moment they share until the person accepts: an id that is
-- not the account's, the address as they typed it, and the role, with no
-- name.

create table public.page_email_invites (
  id uuid primary key default gen_random_uuid(),
  page_id uuid not null references public.pages (id) on delete cascade,
  -- Lowercased and trimmed, as share_page keeps it.
  email text not null,
  role text not null check (role in ('viewer', 'editor')),
  invited_by uuid references auth.users (id) on delete set null,
  created_at timestamptz not null default now(),
  constraint page_email_invites_page_email_key unique (page_id, email)
);

create index page_email_invites_email_idx on public.page_email_invites (email);

-- Read and written only through the functions here.
alter table public.page_email_invites enable row level security;
revoke all on public.page_email_invites from anon, authenticated;

-- What the owner sees of an invite to an account until it is accepted.
-- A volatile default gives each row its own id.
alter table public.page_permissions
  add column invite_id uuid not null default gen_random_uuid(),
  add column invited_email text;

-- updated_at stays as it was, so the devices do not pull every share again.
alter table public.page_permissions disable trigger page_permissions_set_updated_at;
update public.page_permissions pp
set invited_email = lower(u.email)
from auth.users u
where u.id = pp.user_id and pp.accepted_at is null;
alter table public.page_permissions enable trigger page_permissions_set_updated_at;

-- A pending invite to an account that has not confirmed its address waits
-- for whoever confirms it, under the same id.
insert into public.page_email_invites (id, page_id, email, role, invited_by, created_at)
select pp.invite_id, pp.page_id, pp.invited_email, pp.role, pp.invited_by, pp.created_at
from public.page_permissions pp
join auth.users u on u.id = pp.user_id
where pp.accepted_at is null and u.email_confirmed_at is null and pp.invited_email is not null
on conflict on constraint page_email_invites_page_email_key do nothing;

delete from public.page_permissions pp
using auth.users u
where u.id = pp.user_id and pp.accepted_at is null and u.email_confirmed_at is null;

-- The owner reads the shares of their pages once accepted; the person
-- invited still reads their own, pending or not.
drop policy page_permissions_select on public.page_permissions;
create policy page_permissions_select on public.page_permissions for select to authenticated
  using (
    user_id = (select auth.uid())
    or (accepted_at is not null and public.page_role(page_id) = 'owner')
  );

-- An address confirmed, on sign-up or a change of address, takes the
-- invites waiting for it, keeping their ids, so the owner sees nothing
-- change until the person accepts. One on a page the account owns, or
-- that has a share for it already, keeps waiting.
create or replace function public.claim_email_invites()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_op = 'UPDATE' and new.email is not distinct from old.email
     and old.email_confirmed_at is not null then
    return null;
  end if;
  with claimed as (
    insert into public.page_permissions (page_id, user_id, role, invited_by, accepted_at, invite_id, invited_email)
    select i.page_id, new.id, i.role, i.invited_by, null, i.id, i.email
    from public.page_email_invites i
    join public.pages p on p.id = i.page_id
    join public.spaces s on s.id = p.space_id
    where i.email = lower(new.email) and s.owner_id <> new.id
    on conflict on constraint page_permissions_pkey do nothing
    returning invite_id
  )
  delete from public.page_email_invites i
  using claimed c
  where i.id = c.invite_id;
  return null;
end;
$$;

drop trigger if exists on_auth_user_email_confirmed on auth.users;
create trigger on_auth_user_email_confirmed
  after insert or update of email, email_confirmed_at on auth.users
  for each row when (new.email_confirmed_at is not null and new.email is not null)
  execute function public.claim_email_invites();

-- ---------------------------------------------------------------------------
-- RPCs
-- ---------------------------------------------------------------------------

-- Share a page (and its subpages) by email: an invite to the account that
-- confirmed the address, or one waiting for it. It answers no rows either
-- way. Sharing again changes only the role: an accepted share stays
-- accepted, a pending one pending.
create or replace function public.share_page(p_page_id uuid, p_email text, p_role text)
returns table (user_id uuid, display_name text, email text, role text)
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_email text := lower(btrim(coalesce(p_email, '')));
  v_user uuid;
begin
  if public.page_role(p_page_id) is distinct from 'owner' then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  if p_role not in ('viewer', 'editor') then
    raise exception 'invalid_role' using errcode = '22023';
  end if;
  if v_email !~ '^[^@[:space:]]+@[^@[:space:]]+$' or char_length(v_email) > 320 then
    raise exception 'invalid_email' using errcode = '22023';
  end if;
  -- The caller's own address, confirmed or not: it tells them nothing.
  if exists (select 1 from auth.users u where u.id = auth.uid() and lower(u.email) = v_email) then
    raise exception 'cannot_share_with_self' using errcode = '22023';
  end if;

  select u.id into v_user
  from auth.users u
  where lower(u.email) = v_email and u.email_confirmed_at is not null
  limit 1;

  if v_user is null then
    insert into public.page_email_invites (page_id, email, role, invited_by)
    values (p_page_id, v_email, p_role, auth.uid())
    on conflict on constraint page_email_invites_page_email_key
      do update set role = excluded.role;
    return;
  end if;

  insert into public.page_permissions (page_id, user_id, role, invited_by, accepted_at, invited_email)
  values (p_page_id, v_user, p_role, auth.uid(), null, v_email)
  on conflict on constraint page_permissions_pkey
    do update set role = excluded.role;
end;
$$;

-- Withdraw a share, or an invite by the id page_members gives it.
create or replace function public.unshare_page(p_page_id uuid, p_user_id uuid)
returns void
language plpgsql
security definer
set search_path = ''
as $$
begin
  if public.page_role(p_page_id) is distinct from 'owner' then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  delete from public.page_permissions pp
  where pp.page_id = p_page_id
    and (pp.user_id = p_user_id or (pp.invite_id = p_user_id and pp.accepted_at is null));
  delete from public.page_email_invites i
  where i.page_id = p_page_id and i.id = p_user_id;
end;
$$;

-- Everyone with access to a page: the owner, plus accepted grants on the
-- page or any ancestor (the highest role wins). Only the owner sees the
-- invites still waiting, each address once (the nearest page's), whether
-- it waits for an account or for an address with none, and not one for
-- someone already a member. Callers must be able to view the page.
create or replace function public.page_members(p_page_id uuid)
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
           max(case pp.role when 'editor' then 2 else 1 end) as rank
    from chain c
    join public.page_permissions pp on pp.page_id = c.id
    where pp.accepted_at is not null
    group by pp.user_id
  ),
  members as (
    select s.owner_id as uid, 'owner'::text as member_role, 0 as ord
    from chain c join public.spaces s on s.id = c.space_id
    where c.depth = 0
    union all
    select g.uid, case g.rank when 2 then 'editor' else 'viewer' end, 1
    from grants g
  ),
  waiting as (
    select distinct on (w.mail) w.invite, w.mail, w.invite_role
    from (
      select pp.invite_id as invite, coalesce(pp.invited_email, '') as mail, pp.role as invite_role, c.depth
      from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.accepted_at is null
        and not exists (select 1 from members m where m.uid = pp.user_id)
      union all
      select i.id, i.email, i.role, c.depth
      from chain c
      join public.page_email_invites i on i.page_id = c.id
    ) w
    where v_owner
    order by w.mail, w.depth, w.invite
  )
  select r.uid, r.name, r.mail, r.member_role, r.is_accepted
  from (
    select m.uid, coalesce(pr.display_name, '') as name, u.email::text as mail, m.member_role,
           true as is_accepted, m.ord, coalesce(pr.display_name, u.email::text) as sort_name
    from members m
    join auth.users u on u.id = m.uid
    left join public.profiles pr on pr.id = m.uid
    union all
    select w.invite, '', w.mail, w.invite_role, false, 1, w.mail
    from waiting w
  ) r
  order by r.ord, r.sort_name;
end;
$$;
