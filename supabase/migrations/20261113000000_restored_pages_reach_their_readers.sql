-- * A member reads nothing on a deleted page under a shared one, nor under
--   it, unless it was theirs before it was deleted (can_read_page). Once
--   the page was restored they read it again, but nothing on it had
--   changed: its comments, files, recordings and content kept the
--   updated_at they had, behind the member's pulls, which never brought
--   them. The same held for a page moved out from under a deleted one to
--   another place under the same shares, which pages_touch_moved left as
--   reaching no one new. A page restored, or moved out from under a
--   deleted one, now moves the rows on it and under it on, as a move
--   under a new share does.

-- The rows on a page and on all under it, and the pages under it, move
-- their updated_at on. The tree under a page is at most 1,000 pages deep
-- (pages_guard).
create or replace function public.touch_page_tree(p_page_id uuid)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_pages uuid[];
begin
  with recursive tree (id, depth) as (
    select p.id, 1 from public.pages p where p.parent_id = p_page_id
    union all
    select p.id, t.depth + 1
    from public.pages p join tree t on p.parent_id = t.id
    where t.depth < 1000
  )
  select coalesce(array_agg(t.id), '{}'::uuid[]) into v_pages from tree t;

  -- updated_at is the server's (set_updated_at): any update moves it on.
  update public.pages p set updated_at = now() where p.id = any(v_pages);
  v_pages := v_pages || p_page_id;
  update public.page_content c set updated_at = now() where c.page_id = any(v_pages);
  update public.comments c set updated_at = now() where c.page_id = any(v_pages);
  update public.files f set updated_at = now() where f.page_id = any(v_pages);
  update public.recordings r set updated_at = now() where r.page_id = any(v_pages);
end;
$$;

-- Only the triggers here call it.
revoke execute on function public.touch_page_tree(uuid) from public, anon, authenticated, service_role;

-- A move within the same shares, or out of some, reaches no one new and
-- leaves the rows as they are, unless the page was under a deleted one:
-- its readers then read it for the first time since.
create or replace function public.pages_touch_moved()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if public.shared_chain(new.parent_id) <@ public.shared_chain(old.parent_id)
     and not exists (
       select 1 from public.page_ancestors(old.parent_id) a where a.deleted_at is not null
     ) then
    return null;
  end if;
  perform public.touch_page_tree(new.id);
  return null;
end;
$$;

revoke execute on function public.pages_touch_moved() from public, anon, authenticated, service_role;

-- A restored page under a share is read again by those it was hidden
-- from. One under no share has no other readers, and is left.
create or replace function public.pages_touch_restored()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if cardinality(public.shared_chain(new.id)) = 0 then
    return null;
  end if;
  perform public.touch_page_tree(new.id);
  return null;
end;
$$;

revoke execute on function public.pages_touch_restored() from public, anon, authenticated, service_role;

drop trigger if exists pages_touch_restored on public.pages;
create trigger pages_touch_restored
  after update of deleted_at on public.pages
  for each row when (old.deleted_at is not null and new.deleted_at is null)
  execute function public.pages_touch_restored();
