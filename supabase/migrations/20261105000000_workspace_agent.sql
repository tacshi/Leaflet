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

-- The workspace agent finds the caller's pages as the app's search does
-- (store.Search): those of their own space, neither deleted nor templates,
-- with every one of p_tags, in any case, whose title, a tag or text holds
-- p_text, in any case; with no p_text, the most recently changed. Titles
-- and tags that match come before text that does, and a match in text
-- comes with the words around it.
create function public.agent_search_pages(p_text text, p_tags text[], p_limit integer)
returns table (page_id uuid, title text, snippet text)
language sql stable security invoker set search_path = ''
as $$
  with found as (
    select p.id, p.title, p.updated_at,
      position(lower(p_text) in lower(p.title)) > 0
        or exists (select 1 from unnest(p.tags) t where position(lower(p_text) in lower(t)) > 0) as named,
      position(lower(p_text) in lower(coalesce(c.plain_text, ''))) as at,
      c.plain_text
    from public.pages p
    join public.spaces s on s.id = p.space_id and s.owner_id = (select auth.uid())
    left join public.page_content c on c.page_id = p.id
    where p.deleted_at is null and not p.is_template
      and not exists (
        select 1 from unnest(coalesce(p_tags, '{}')) want
        where not exists (select 1 from unnest(p.tags) t where lower(t) = lower(want))
      )
  )
  select f.id, f.title,
    case when p_text <> '' and not f.named and f.at > 0
      then substring(f.plain_text from greatest(f.at - 40, 1) for length(p_text) + 80)
    end
  from found f
  where p_text = '' or f.named or f.at > 0
  order by p_text <> '' and f.named desc, f.updated_at desc, f.id
  limit least(greatest(coalesce(p_limit, 20), 1), 50)
$$;

revoke execute on function public.agent_search_pages(text, text[], integer) from public, anon, service_role;
grant execute on function public.agent_search_pages(text, text[], integer) to authenticated;
