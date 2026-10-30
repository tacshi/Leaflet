-- An editor could move a page they can edit under any other page they can
-- edit, including one shared with more people, or with others: the page
-- and everything under it then reached that page's people, which the
-- owner never chose. Now only the space's owner moves a page to where
-- other shares reach it. Anyone else who may edit both parents still moves
-- a page between them when the same shares reach both: within the page
-- shared with them, and between its subpages that are not shared on their
-- own. A page's own shares, and those of the pages under it, go with it.

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
  with recursive chain as (
    select p.id, p.parent_id, 0 as depth
    from public.pages p
    where p.id = p_page_id
    union all
    select p.id, p.parent_id, c.depth + 1
    from public.pages p
    join chain c on p.id = c.parent_id
    where c.depth < 64
  )
  select coalesce(array_agg(c.id order by c.id), '{}'::uuid[])
  from chain c
  where exists (select 1 from public.page_permissions pp where pp.page_id = c.id)
     or exists (select 1 from public.page_email_invites i where i.page_id = c.id);
$$;

revoke execute on function public.shared_chain(uuid) from public, anon, authenticated;

-- Pages stay in their space; a subpage lives in its parent's space; no cycles;
-- moving a page under a new parent needs edit access to that parent (the
-- update policy already requires edit access to the page itself), and,
-- for anyone but the space's owner, the same shares on both parents.
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
    if v_uid is not null then
      new.created_by := v_uid;
    end if;
    return new;
  end if;

  if new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;

  if new.parent_id is distinct from old.parent_id then
    if new.parent_id is not null and exists (
      with recursive anc as (
        select p.id, p.parent_id, 0 as depth from public.pages p where p.id = new.parent_id
        union all
        select p.id, p.parent_id, a.depth + 1
        from public.pages p join anc a on p.id = a.parent_id
        where a.depth < 64
      )
      select 1 from anc where anc.id = new.id
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
  end if;

  if new.folder_id is distinct from old.folder_id
     and v_uid is not null
     and not public.is_space_owner(new.space_id) then
    raise exception 'forbidden_folder' using errcode = '42501';
  end if;

  return new;
end;
$$;
