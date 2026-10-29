-- The server kept a page's newest 50 unnamed snapshots, whoever wrote them,
-- and any editor may add snapshots: 50 of an editor's pushed out every
-- other snapshot of the page, the owner's included. Each writer now keeps
-- their own newest 50 unnamed snapshots of a page, so a writer's snapshots
-- only ever prune their own. created_by is the caller (stamp_writers), and
-- is null only for writes with no caller or a writer whose account is gone.

create or replace function public.prune_page_history()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  delete from public.page_history h
  where h.page_id = new.page_id
    and h.reason <> 'manual'
    and h.created_by is not distinct from new.created_by
    and h.id not in (
      select k.id from public.page_history k
      where k.page_id = new.page_id and k.reason <> 'manual'
        and k.created_by is not distinct from new.created_by
      order by k.created_at desc, k.id desc
      limit 50
    );
  return null;
end;
$$;
