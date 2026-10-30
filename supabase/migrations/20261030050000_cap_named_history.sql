-- Named ('manual') snapshots were never pruned, and anyone who can edit a
-- page may add them, so an editor could add any number to someone else's
-- page; and a snapshot kept the created_at its writer sent, so one dated
-- ahead stayed the newest for good. Now a snapshot is never dated after it
-- reached the server (one made offline keeps its earlier time), and each
-- writer other than the page's owner keeps their own newest 50 named
-- snapshots of a page, as every writer does their unnamed ones. The
-- owner's named snapshots are all kept.

create or replace function public.stamp_page_history_writer()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
begin
  if v_uid is null then
    return new;
  end if;
  new.created_by := v_uid;
  new.created_at := least(coalesce(new.created_at, now()), now());
  if new.ai_job_id is not null and not exists (
    select 1 from public.ai_jobs j
    where j.id = new.ai_job_id and j.user_id = v_uid and j.page_id = new.page_id
  ) then
    new.ai_job_id := null;
  end if;
  return new;
end;
$$;

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
  if new.reason = 'manual' and not exists (
    select 1 from public.pages p
    join public.spaces s on s.id = p.space_id
    where p.id = new.page_id and s.owner_id is not distinct from new.created_by
  ) then
    delete from public.page_history h
    where h.page_id = new.page_id
      and h.reason = 'manual'
      and h.created_by is not distinct from new.created_by
      and h.id not in (
        select k.id from public.page_history k
        where k.page_id = new.page_id and k.reason = 'manual'
          and k.created_by is not distinct from new.created_by
        order by k.created_at desc, k.id desc
        limit 50
      );
  end if;
  return null;
end;
$$;

-- Named snapshots already past 50 for a writer who is not the owner.
delete from public.page_history h
using (
  select k.id,
         row_number() over (partition by k.page_id, k.created_by order by k.created_at desc, k.id desc) as n
  from public.page_history k
  join public.pages p on p.id = k.page_id
  join public.spaces s on s.id = p.space_id
  where k.reason = 'manual' and k.created_by is distinct from s.owner_id
) r
where h.id = r.id and r.n > 50;

-- Snapshots dated ahead of now come back to when this runs.
update public.page_history set created_at = now() where created_at > now();
