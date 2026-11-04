-- The workspace agent leaves as it is a page someone else has, or is asked
-- to have: of the caller's own space, a page shared with anyone but the
-- caller, by person or by address, the invite taken up or still waiting,
-- as the app counts its members (page_members, which lists the invites to
-- the owner). A waiting invite is no row the owner reads
-- (page_permissions_select), so this reads them for the caller, of their
-- own space alone.
create function public.agent_shared_pages()
returns table (page_id uuid)
language sql stable security definer set search_path = ''
as $$
  select pp.page_id
  from public.page_permissions pp
  join public.pages p on p.id = pp.page_id
  join public.spaces s on s.id = p.space_id and s.owner_id = (select auth.uid())
  where pp.user_id is distinct from (select auth.uid())
  union
  select i.page_id
  from public.page_email_invites i
  join public.pages p on p.id = i.page_id
  join public.spaces s on s.id = p.space_id and s.owner_id = (select auth.uid())
$$;

revoke execute on function public.agent_shared_pages() from public, anon, service_role;
grant execute on function public.agent_shared_pages() to authenticated;

-- The workspace agent leaves as it is a page whose title someone else
-- reads, as its card is on a page they read: of p_pages, the pages of the
-- caller's own space others read, the pages their cards name, found
-- without the pages' content, which can be long, leaving the database.
-- Under the caller's row-level security, as their own reads are.
create function public.agent_card_pages(p_pages uuid[])
returns table (page_id text)
language sql stable security invoker set search_path = ''
as $$
  select distinct card #>> '{}'
  from public.page_content c
  join public.pages p on p.id = c.page_id
  join public.spaces s on s.id = p.space_id and s.owner_id = (select auth.uid())
  cross join lateral jsonb_path_query(c.content, '$.** ? (@.type == "subpage").data.page_id') card
  where c.page_id = any(p_pages) and jsonb_typeof(card) = 'string'
$$;

revoke execute on function public.agent_card_pages(uuid[]) from public, anon, service_role;
grant execute on function public.agent_card_pages(uuid[]) to authenticated;
