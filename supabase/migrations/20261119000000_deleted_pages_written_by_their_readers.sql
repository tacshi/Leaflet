-- * A member reads nothing on a page deleted before their view of it
--   began, nor on the pages under it (can_read_page), and cannot restore
--   it (forbidden_restore), but could still write to it: can_view_page
--   and can_edit_page asked only for a role on the page's chain, so a
--   viewer added comments to such a page, and an editor its content,
--   history snapshots, recordings, files, subpages, a move of a page they
--   have under it, or its public link's lease (lease_public_link). They
--   now ask can_read_page as well: whoever may not read what is on a page
--   writes nothing to it either, as every policy and function that asks
--   them has it. The owner reads, and so writes, all of it.
-- * The page's own row stays as it was: pages_select and pages_update ask
--   for the role alone, as can_view_page and can_edit_page did. Its
--   deletion syncs to them, and an editor's device still deletes a page
--   under one it deleted, whatever its clock dated that deletion;
--   pages_guard refuses, by name, a restore of a page they cannot read
--   (forbidden_restore) and a move out from under a deleted page
--   (forbidden_move).

create or replace function public.can_view_page(p_page_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select public.page_role(p_page_id) is not null and public.can_read_page(p_page_id);
$$;

create or replace function public.can_edit_page(p_page_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select coalesce(public.page_role(p_page_id) in ('owner', 'editor'), false)
    and public.can_read_page(p_page_id);
$$;

-- A page in the table is seen by its own chain, deleted or not. Only a new
-- one, not in the table yet as its upsert is checked, needs its parent's:
-- it was placed in this transaction (pages_guard sets placed_at to now()
-- as a page is made or moved), which a row the caller merely reads was
-- not.
drop policy if exists pages_select on public.pages;
create policy pages_select on public.pages for select to authenticated
  using (
    public.is_space_owner(space_id)
    or public.page_role(id) is not null
    or (parent_id is not null and placed_at = now() and public.page_role(parent_id) is not null)
  );

drop policy if exists pages_update on public.pages;
create policy pages_update on public.pages for update to authenticated
  using (coalesce(public.page_role(id) in ('owner', 'editor'), false))
  with check (coalesce(public.page_role(id) in ('owner', 'editor'), false));
