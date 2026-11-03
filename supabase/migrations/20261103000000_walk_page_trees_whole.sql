-- Every walk of the page tree stopped 64 levels up or down, and was
-- written out anew in each function that made one:
-- * page_role and page_members found no share above 64 levels, so a page
--   shared with someone stopped reaching them that far down;
-- * pages_guard's cycle check looked only 64 levels up, so a page could be
--   moved under one of its own subpages deeper than that, and the two
--   then hung off each other, out of everyone's tree;
-- * pages_touch_moved left the pages more than 64 levels under a moved
--   one unmarked, so the pulls of those the move reached passed them by.
-- page_ancestors is now the one walk up, with Postgres' CYCLE clause in
-- place of the depth cap: it goes as high as the tree does, and still ends
-- where it would come back to a page it passed. page_role, page_members,
-- page_history_since, shared_chain, shares_page_with, unshare_page and
-- pages_guard read it; pages_touch_moved walks down the same way. And two
-- moves at once, each checked against the tree as it was before the
-- other, could still make a cycle together: the moves in a space now go
-- one at a time.
--
-- The storage path checks each matched a path's segments against a UUID
-- pattern of their own: uuid_or_null is that check, once.
--
-- And a new page's placed_at was its created_at, which the client sends:
-- a page made offline, and moved under a shared page before it went up,
-- arrived with its old time, and its history from before the move,
-- private until then, passed page_history_since for the share's people.
-- placed_at is now when the page reached the server, as it is when one is
-- moved: its snapshots from before then stay the writer's and the owner's.

-- ---------------------------------------------------------------------------
-- The walk, and the UUID check
-- ---------------------------------------------------------------------------

-- p_page_id and the pages above it, nearest first: depth 0 is the page
-- itself, and its parent 1. The walk ends at the top of the tree, or where
-- it would come back to a page it passed (pages_guard keeps cycles out,
-- but a walk must end even so). It reads past row-level security only as
-- the definer functions here, which call it as their owner: a caller of
-- its own would see only the pages they may.
create or replace function public.page_ancestors(p_page_id uuid)
returns table (id uuid, parent_id uuid, space_id uuid, placed_at timestamptz, depth integer)
language sql
stable
set search_path = ''
as $$
  with recursive chain (id, parent_id, space_id, placed_at, depth) as (
    select p.id, p.parent_id, p.space_id, p.placed_at, 0
    from public.pages p
    where p.id = p_page_id
    union all
    select p.id, p.parent_id, p.space_id, p.placed_at, c.depth + 1
    from public.pages p
    join chain c on p.id = c.parent_id
  ) cycle id set looped using path
  select c.id, c.parent_id, c.space_id, c.placed_at, c.depth
  from chain c
  where not c.looped;
$$;

-- p_text as a UUID when it is one, in the form Postgres writes them (any
-- case), else null: a storage path's segments are anyone's to name.
create or replace function public.uuid_or_null(p_text text)
returns uuid
language sql
immutable
set search_path = ''
as $$
  select case
    when p_text ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then p_text::uuid
  end;
$$;

-- Only the functions here call them.
revoke execute on function public.page_ancestors(uuid) from public, anon, authenticated, service_role;
revoke execute on function public.uuid_or_null(text) from public, anon, authenticated, service_role;

-- ---------------------------------------------------------------------------
-- Access helpers
-- ---------------------------------------------------------------------------

-- 'owner' | 'editor' | 'viewer' | null for the current user on a page.
-- An accepted permission on any ancestor applies to its subpages. The
-- space's owner, the one person in most projects, is told without a walk.
create or replace function public.page_role(p_page_id uuid)
returns text
language sql
stable
security definer
set search_path = ''
as $$
  with chain as (
    select a.id from public.page_ancestors(p_page_id) a
  )
  select case
    when (select auth.uid()) is null then null
    when exists (
      select 1 from public.pages p
      join public.spaces s on s.id = p.space_id
      where p.id = p_page_id and s.owner_id = (select auth.uid())
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

-- When the current user's history of a page starts: -infinity for the
-- owner, and for anyone else the earliest of their accepted shares on the
-- page's chain, each from when it was accepted or, if later, from when the
-- page came under the shared page (the latest placed_at of the pages below
-- it on the chain); null for someone it is not shared with.
create or replace function public.page_history_since(p_page_id uuid)
returns timestamptz
language sql
stable
security definer
set search_path = ''
as $$
  with chain as (
    select a.id,
           coalesce(
             max(a.placed_at) over (order by a.depth rows between unbounded preceding and 1 preceding),
             '-infinity'::timestamptz
           ) as under_since
    from public.page_ancestors(p_page_id) a
  )
  select case
    when (select auth.uid()) is null then null
    when public.page_role(p_page_id) = 'owner' then '-infinity'::timestamptz
    else (
      select min(greatest(pp.accepted_at, c.under_since))
      from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.user_id = (select auth.uid()) and pp.accepted_at is not null
    )
  end;
$$;

-- The pages on p_page_id's chain, itself and its ancestors, that are shared
-- (with someone, or waiting for an address), in order; empty for null.
-- Only the guard calls it: it tells what is shared in any space.
create or replace function public.shared_chain(p_page_id uuid)
returns uuid[]
language sql
stable
security definer
set search_path = ''
as $$
  select coalesce(array_agg(a.id order by a.id), '{}'::uuid[])
  from public.page_ancestors(p_page_id) a
  where exists (select 1 from public.page_permissions pp where pp.page_id = a.id)
     or exists (select 1 from public.page_email_invites i where i.page_id = a.id);
$$;

-- True when the current user and p_other can both view at least one page,
-- by shares that were accepted: the space's owner and someone shared a
-- page of it, or two people whose shares are of the same page or of a
-- page and one it is inside.
create or replace function public.shares_page_with(p_other uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  with mine as (
    select a.id
    from public.page_permissions pp
    cross join lateral public.page_ancestors(pp.page_id) a
    where pp.user_id = (select auth.uid()) and pp.accepted_at is not null
  ),
  theirs as (
    select a.id
    from public.page_permissions pp
    cross join lateral public.page_ancestors(pp.page_id) a
    where pp.user_id = p_other and pp.accepted_at is not null
  )
  select exists (
    select 1
    from public.page_permissions pp
    join public.pages p on p.id = pp.page_id
    join public.spaces s on s.id = p.space_id
    where pp.accepted_at is not null
      and ((pp.user_id = (select auth.uid()) and s.owner_id = p_other)
        or (pp.user_id = p_other and s.owner_id = (select auth.uid())))
  ) or exists (
    -- Their share is of one of my shared pages or a page above one.
    select 1 from public.page_permissions b
    where b.user_id = p_other and b.accepted_at is not null
      and b.page_id in (select m.id from mine m)
  ) or exists (
    -- Mine is of one of theirs or a page above one.
    select 1 from public.page_permissions a
    where a.user_id = (select auth.uid()) and a.accepted_at is not null
      and a.page_id in (select t.id from theirs t)
  );
$$;

-- First path segment of a storage object, as a space ID the user owns.
create or replace function public.owns_space_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_space uuid := public.uuid_or_null(split_part(p_name, '/', 1));
begin
  if v_space is null then
    return false;
  end if;
  return public.is_space_owner(v_space);
end;
$$;

-- images/{space_id}/{page_id}/{image_id}.{ext}: readable by those who can
-- view the page.
create or replace function public.can_view_image_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_space uuid := public.uuid_or_null(split_part(p_name, '/', 1));
  v_page uuid := public.uuid_or_null(split_part(p_name, '/', 2));
begin
  if v_space is null or v_page is null
     or split_part(p_name, '/', 3) = '' or split_part(p_name, '/', 4) <> '' then
    return false;
  end if;
  return exists (
    select 1 from public.pages p
    where p.id = v_page and p.space_id = v_space
  ) and public.can_view_page(v_page);
end;
$$;

-- recordings/{user_id}/{recording_id}.{ext}: the caller writes only an
-- object for a recording row they made, on a page they can still edit, so
-- the bucket holds no audio without a row. They still read and delete
-- anything in their folder.
create or replace function public.owns_recording_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_id uuid := public.uuid_or_null(split_part(split_part(p_name, '/', 2), '.', 1));
begin
  if v_uid is null or v_id is null
     or split_part(p_name, '/', 1) <> v_uid::text
     or split_part(p_name, '/', 3) <> '' then
    return false;
  end if;
  return exists (
    select 1 from public.recordings r
    where r.id = v_id and r.created_by = v_uid and public.can_edit_page(r.page_id)
  );
end;
$$;

-- files/{space_id}/{file_id}/{name}: the space's owner writes an object
-- whose row is on no page, or on one they can edit, or that has no row
-- yet (no one else reads it then). They still read and delete anything in
-- their space.
create or replace function public.can_write_file_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_id uuid := public.uuid_or_null(split_part(p_name, '/', 2));
begin
  if not public.owns_space_path(p_name) then
    return false;
  end if;
  if v_id is null then
    return true;
  end if;
  return not exists (
    select 1 from public.files f
    where f.id = v_id
      and f.space_id = split_part(p_name, '/', 1)::uuid
      and f.page_id is not null
      and not public.can_edit_page(f.page_id)
  );
end;
$$;

-- ---------------------------------------------------------------------------
-- RPCs
-- ---------------------------------------------------------------------------

-- Withdraw a share, or an invite by the id page_members gives it. A share
-- or invite page_members lists for the page but that is on a page above it
-- is withdrawn on that page, not here: when nothing here matched and one
-- there does, it raises 'inherited_share' rather than doing nothing.
create or replace function public.unshare_page(p_page_id uuid, p_user_id uuid)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_grants integer;
  v_invites integer;
begin
  if public.page_role(p_page_id) is distinct from 'owner' then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  delete from public.page_permissions pp
  where pp.page_id = p_page_id
    and (pp.user_id = p_user_id or (pp.invite_id = p_user_id and pp.accepted_at is null));
  get diagnostics v_grants = row_count;
  delete from public.page_email_invites i
  where i.page_id = p_page_id and i.id = p_user_id;
  get diagnostics v_invites = row_count;

  if v_grants + v_invites = 0 and exists (
    select 1 from public.page_ancestors(p_page_id) a
    where a.depth > 0
      and (exists (
        select 1 from public.page_permissions pp
        where pp.page_id = a.id
          and (pp.user_id = p_user_id or (pp.invite_id = p_user_id and pp.accepted_at is null))
      ) or exists (
        select 1 from public.page_email_invites i
        where i.page_id = a.id and i.id = p_user_id
      ))
  ) then
    raise exception 'inherited_share' using errcode = '22023';
  end if;
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
  with chain as (
    select a.id, a.space_id, a.depth from public.page_ancestors(p_page_id) a
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

-- ---------------------------------------------------------------------------
-- pages: no cycles at any depth, and placed when they reach the server
-- ---------------------------------------------------------------------------

-- Pages stay in their space; a subpage lives in its parent's space; no cycles;
-- moving a page under a new parent needs edit access to that parent (the
-- update policy already requires edit access to the page itself), and,
-- for anyone but the space's owner, the same shares on both parents.
-- created_by is who made the page, and placed_at when it reached the
-- server or was last put under a parent: the server sets both. A new
-- page goes in a folder only for the space's owner, as a moved one does.
create or replace function public.pages_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_parent_space uuid;
begin
  if new.parent_id is not null then
    select p.space_id into v_parent_space from public.pages p where p.id = new.parent_id;
    if v_parent_space is null then
      raise exception 'parent_not_found' using errcode = 'P0002';
    end if;
    if v_parent_space <> new.space_id then
      raise exception 'parent_in_other_space' using errcode = '23514';
    end if;
  end if;

  if new.folder_id is not null and not exists (
    select 1 from public.folders f where f.id = new.folder_id and f.space_id = new.space_id
  ) then
    raise exception 'folder_in_other_space' using errcode = '23514';
  end if;

  if tg_op = 'INSERT' then
    if new.folder_id is not null
       and v_uid is not null
       and not public.is_space_owner(new.space_id)
       and not exists (select 1 from public.pages p where p.id = new.id) then
      raise exception 'forbidden_folder' using errcode = '42501';
    end if;
    if v_uid is not null then
      new.created_by := v_uid;
    end if;
    -- Not created_at, which is the client's: a page that went under a
    -- shared one before it came up would bring its private history.
    new.placed_at := now();
    return new;
  end if;

  if new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;

  if v_uid is not null then
    new.created_by := old.created_by;
  end if;
  new.placed_at := old.placed_at;

  if new.parent_id is distinct from old.parent_id then
    -- Held to the end of the transaction, so the check below sees any
    -- move made in the space before this one.
    perform pg_advisory_xact_lock(hashtextextended('leaflet.page_moves:' || new.space_id::text, 0));
    if new.parent_id is not null and exists (
      select 1 from public.page_ancestors(new.parent_id) a where a.id = new.id
    ) then
      raise exception 'page_cycle' using errcode = '23514';
    end if;

    if v_uid is not null then
      if new.parent_id is null then
        if not public.is_space_owner(new.space_id) then
          raise exception 'forbidden_move' using errcode = '42501';
        end if;
      elsif not public.can_edit_page(new.parent_id) then
        raise exception 'forbidden_move' using errcode = '42501';
      elsif not public.is_space_owner(new.space_id)
            and public.shared_chain(old.parent_id) <> public.shared_chain(new.parent_id) then
        raise exception 'forbidden_move' using errcode = '42501';
      end if;
    end if;

    new.placed_at := now();
  end if;

  if new.folder_id is distinct from old.folder_id
     and v_uid is not null
     and not public.is_space_owner(new.space_id) then
    raise exception 'forbidden_folder' using errcode = '42501';
  end if;

  return new;
end;
$$;

-- When a page comes under shares its old parent was not under, the rows
-- under it and on it, which did not change, move their updated_at on, so
-- the pulls of those the move reached bring them. A move within the same
-- shares, or out of some, reaches no one new and leaves them.
create or replace function public.pages_touch_moved()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_pages uuid[];
begin
  if public.shared_chain(new.parent_id) <@ public.shared_chain(old.parent_id) then
    return null;
  end if;
  with recursive tree (id) as (
    select p.id from public.pages p where p.parent_id = new.id
    union all
    select p.id from public.pages p join tree t on p.parent_id = t.id
  ) cycle id set looped using path
  select coalesce(array_agg(t.id), '{}'::uuid[]) into v_pages from tree t where not t.looped;

  -- updated_at is the server's (set_updated_at): any update moves it on.
  update public.pages p set updated_at = now() where p.id = any(v_pages);
  v_pages := v_pages || new.id;
  update public.page_content c set updated_at = now() where c.page_id = any(v_pages);
  update public.comments c set updated_at = now() where c.page_id = any(v_pages);
  update public.files f set updated_at = now() where f.page_id = any(v_pages);
  update public.recordings r set updated_at = now() where r.page_id = any(v_pages);
  return null;
end;
$$;
