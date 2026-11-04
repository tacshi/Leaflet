-- The workspace agent finds the caller's pages as the app's agents search
-- them (store.SearchPages): those of their own space, neither deleted nor
-- templates, with every one of p_tags, in any case, whose title, a tag or
-- text holds p_text, in any case; with no p_text, the most recently
-- changed. Titles and tags first, then the pages' text the most recently
-- changed page first, and only until it has found enough, rather than
-- every page's whole text for every search. Text is matched under the
-- caller's row-level security, whose rules no index of it could be read
-- past, so reading less is what makes a search quick. A match's words
-- around it are cut from where it is in the text itself: lowering text can
-- change its length, as 'İ' becomes two letters, so where it is in the
-- text lowered is not where it is in the text.
create or replace function public.agent_search_pages(p_text text, p_tags text[], p_limit integer)
returns table (page_id uuid, title text, snippet text)
language sql stable security invoker set search_path = ''
as $$
  with mine as materialized (
    select p.id, p.title, p.tags, p.updated_at
    from public.pages p
    join public.spaces s on s.id = p.space_id and s.owner_id = (select auth.uid())
    where p.deleted_at is null and not p.is_template
      and not exists (
        select 1 from unnest(coalesce(p_tags, '{}')) want
        where not exists (select 1 from unnest(p.tags) t where lower(t) = lower(want))
      )
  ),
  wanted as (select least(greatest(coalesce(p_limit, 20), 1), 50) as n),
  named as (
    select m.id, m.title, m.updated_at
    from mine m
    where p_text = ''
      or position(lower(p_text) in lower(m.title)) > 0
      or exists (select 1 from unnest(m.tags) t where position(lower(p_text) in lower(t)) > 0)
    order by m.updated_at desc, m.id
    limit (select n from wanted)
  ),
  -- Read a page at a time, the most recently changed first: the limit
  -- ends the reading once it is reached. Only while titles and tags have
  -- not filled it, and of pages they did not find.
  texts as (
    select m.id, m.title, m.updated_at, c.plain_text
    from (
      select * from mine
      where p_text <> ''
        and (select count(*) from named) < (select n from wanted)
        and id not in (select id from named)
      order by updated_at desc, id
      offset 0
    ) m
    cross join lateral (
      select c.plain_text from public.page_content c
      where c.page_id = m.id and position(lower(p_text) in lower(c.plain_text)) > 0
      limit 1
    ) c
    limit (select n from wanted) - (select count(*) from named)
  )
  select h.id, h.title, h.snippet from (
    select n.id, n.title, null::text as snippet, 0 as rank, n.updated_at from named n
    union all
    select t.id, t.title,
      substring(t.plain_text from greatest(coalesce(length(substring(t.plain_text from
        '(?i)^(.*?)' || regexp_replace(p_text, '([^[:alnum:][:space:]])', '\\\1', 'g'))), 0) + 1 - 40, 1)
        for length(p_text) + 80),
      1, t.updated_at
    from texts t
  ) h
  order by h.rank, h.updated_at desc, h.id
$$;

revoke execute on function public.agent_search_pages(text, text[], integer) from public, anon, service_role;
grant execute on function public.agent_search_pages(text, text[], integer) to authenticated;
