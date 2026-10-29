-- Anyone who could view a page read all of its history, so a person shared
-- a page read what was on it, and taken off it, before it was shared with
-- them. The owner still reads a page's whole history; anyone else reads
-- the snapshots taken from when they accepted a share of the page, or of a
-- page it is inside, on.

-- When the current user's history of a page starts: -infinity for the
-- owner, their earliest accepted share on the page's chain for anyone
-- else, and null for someone it is not shared with.
create or replace function public.page_history_since(p_page_id uuid)
returns timestamptz
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
  select case
    when (select auth.uid()) is null then null
    when public.page_role(p_page_id) = 'owner' then '-infinity'::timestamptz
    else (
      select min(pp.accepted_at)
      from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.user_id = (select auth.uid()) and pp.accepted_at is not null
    )
  end;
$$;

drop policy page_history_select on public.page_history;
create policy page_history_select on public.page_history for select to authenticated
  using (created_at >= public.page_history_since(page_id));
