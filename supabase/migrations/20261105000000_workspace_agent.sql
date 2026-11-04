-- The workspace agent: a chat of the person's own, not of a page, whose
-- agent finds, reads, makes and files their pages. Its runs are jobs of
-- no page, in a surface of their own; a job of any other surface is of a
-- page, as before.
alter table public.ai_jobs alter column page_id drop not null;
alter table public.ai_jobs drop constraint ai_jobs_surface_check;
alter table public.ai_jobs add constraint ai_jobs_surface_check
  check (surface in ('inline', 'selection', 'sidebar', 'comment', 'workspace'));
alter table public.ai_jobs add constraint ai_jobs_workspace_of_no_page
  check ((surface = 'workspace') = (page_id is null));

-- A job of a page asks of one the caller may see; a workspace job asks of
-- none, its agent reading only the pages of the caller's own space.
drop policy ai_jobs_insert on public.ai_jobs;
create policy ai_jobs_insert on public.ai_jobs for insert to authenticated
  with check (user_id = (select auth.uid()) and (page_id is null or public.can_view_page(page_id)));
