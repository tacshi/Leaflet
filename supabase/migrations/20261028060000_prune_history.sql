-- page_history only ever grew. The app shows a page's newest 50 snapshots
-- and its named ('manual') versions, and keeps no others on the device; the
-- server now keeps a page's newest 50 unnamed snapshots and all its named
-- ones. Clients cannot delete history (it is append-only for them), so the
-- trigger that prunes runs as its owner.

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
    and h.id not in (
      select k.id from public.page_history k
      where k.page_id = new.page_id and k.reason <> 'manual'
      order by k.created_at desc, k.id desc
      limit 50
    );
  return null;
end;
$$;

drop trigger if exists page_history_prune on public.page_history;
create trigger page_history_prune after insert on public.page_history
  for each row execute function public.prune_page_history();

-- What every page has already gone past 50.
delete from public.page_history h
using (
  select id, row_number() over (partition by page_id order by created_at desc, id desc) as n
  from public.page_history
  where reason <> 'manual'
) r
where h.id = r.id and r.n > 50;
