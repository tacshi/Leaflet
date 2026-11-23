-- * A member reads nothing on a page deleted before their view of it
--   began (can_read_page), yet read the history snapshots of it taken
--   since, as an editor who had it before the deletion, or the owner's
--   device, took them: page_history_select asked only when their history
--   of the page starts (page_history_since). It now asks can_read_page as
--   well, as the policies on the rest of what is on a page do.
-- * A page's owner may only mark a file someone else put on their page
--   deleted (deleted_at, as files_guard keeps them to), yet could delete
--   its row outright (files_delete): the deletion reached none of its
--   writer's devices, which could send the row back, and its object was
--   no longer theirs to remove, as that goes by the row. Now only the
--   file's space owner deletes its row, while they can edit its page, as
--   before.

drop policy if exists page_history_select on public.page_history;
create policy page_history_select on public.page_history for select to authenticated
  using (created_at >= public.page_history_since(page_id) and public.can_read_page(page_id));

drop policy if exists files_delete on public.files;
create policy files_delete on public.files for delete to authenticated
  using (public.is_space_owner(space_id) and (page_id is null or public.can_edit_page(page_id)));
