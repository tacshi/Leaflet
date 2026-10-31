-- A page could not be uploaded: the app sends a page as an upsert (insert,
-- or update when the server has it), and Postgres checks the new row
-- against pages_select for an upsert, and for the row a write returns.
-- pages_select was can_view_page(id), which looks the page up in pages,
-- where the new row is not yet: every new page was refused with "new row
-- violates row-level security policy", and its content, history and
-- subpages after it.
--
-- It now asks of the row itself what page_role asks of the page found:
-- the caller owns its space, or may see its parent. Who may see a page
-- that is there is the same as before. The cheap check goes first, then
-- the page itself, which a page that is there answers in one walk; the
-- parent only for a new subpage of someone else's.

drop policy if exists pages_select on public.pages;
create policy pages_select on public.pages for select to authenticated
  using (
    public.is_space_owner(space_id)
    or public.can_view_page(id)
    or (parent_id is not null and public.can_view_page(parent_id))
  );
