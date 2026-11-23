-- * The apps keep what was deleted in a bin for 30 days, to restore, and
--   then delete it for good, or sooner, as the person asks. A page was
--   only ever soft-deleted, and kept everything on it; a file went with
--   its contents and kept nothing of what it was (scrub_deleted_file).
--   Now a file put in the bin says so by `kept`, and keeps its row and
--   object until it is deleted for good; one deleted without it, as by an
--   app from before the bin, which removes its object, keeps nothing, as
--   before. A server without this migration knows no `kept`, and refuses
--   the change: it waits on the device until the server is updated. A
--   page or a file deleted for good says so by `purged_at`, which only the
--   space's owner sets, and which stays: the row stays readable, so the
--   deletion reaches every device, and keeps nothing of what it was.
-- * A page deleted for good keeps no title, icon, tags, template or link,
--   and what was on it goes (pages_purged): its content, history,
--   comments, recordings, files and pending invitations. Its shares stay,
--   so the deletion reaches those it was shared with too. The device that
--   deleted it removes the objects of its files and recordings, by their
--   paths. No one reads or writes anything on it again (can_read_page),
--   nor brings it back, or changes its row otherwise (page_purged),
--   whatever a device that had not heard sends.
-- * What someone a page is shared with put on it, a file in their own
--   space or a recording under their own account, its owner may not
--   remove from storage. As the page is deleted for good, purged_objects
--   lists those objects for its owner to remove (purged_objects_remove),
--   whether those people come back or not; their devices remove them too
--   as they hear of it. The list forgets what is gone from storage as the
--   next page is deleted for good.
-- * A file deleted for good keeps none of what it was, as a deleted one
--   did before, and comes back no more (file_purged).

alter table public.pages add column if not exists purged_at timestamptz;
alter table public.files add column if not exists purged_at timestamptz;
alter table public.files add column if not exists kept boolean;

create table if not exists public.purged_objects (
  bucket_id text not null,
  name text not null,
  owner_id uuid not null references auth.users (id) on delete cascade,
  primary key (bucket_id, name)
);

create index if not exists purged_objects_owner_idx on public.purged_objects (owner_id);

alter table public.purged_objects enable row level security;
revoke all on public.purged_objects from public, anon, authenticated, service_role;
grant select on public.purged_objects to authenticated;
grant select, insert, update, delete on public.purged_objects to service_role;

drop policy if exists purged_objects_select on public.purged_objects;
create policy purged_objects_select on public.purged_objects for select to authenticated
  using (owner_id = (select auth.uid()));

-- Whether the caller may remove the object, listed for them as a page of
-- theirs was deleted for good. They read it too, as a removal reads what
-- it removes, as they could while the page was there.
create or replace function public.may_remove_purged(p_bucket text, p_name text)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select exists (
    select 1 from public.purged_objects p
    where p.bucket_id = p_bucket and p.name = p_name and p.owner_id = (select auth.uid())
  );
$$;

revoke execute on function public.may_remove_purged(text, text) from public, anon, authenticated, service_role;
grant execute on function public.may_remove_purged(text, text) to authenticated;

drop policy if exists purged_objects_read on storage.objects;
create policy purged_objects_read on storage.objects for select to authenticated
  using (bucket_id in ('files', 'recordings') and public.may_remove_purged(bucket_id, name));

drop policy if exists purged_objects_remove on storage.objects;
create policy purged_objects_remove on storage.objects for delete to authenticated
  using (bucket_id in ('files', 'recordings') and public.may_remove_purged(bucket_id, name));

create or replace function public.can_read_page(p_page_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  with chain as (
    select a.id,
           coalesce(
             max(nullif(a.placed_at, (select public.placed_at_backfilled())))
               over (order by a.depth rows between unbounded preceding and 1 preceding),
             '-infinity'::timestamptz
           ) as under_since,
           min(a.deleted_at) over (order by a.depth rows between unbounded preceding and current row) as gone_since
    from public.page_ancestors(p_page_id) a
  )
  select case
    when (select auth.uid()) is null then false
    when exists (select 1 from public.pages p where p.id = p_page_id and p.purged_at is not null) then false
    when exists (
      select 1 from public.pages p
      join public.spaces s on s.id = p.space_id
      where p.id = p_page_id and s.owner_id = (select auth.uid())
    ) then true
    else exists (
      select 1 from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.user_id = (select auth.uid()) and pp.accepted_at is not null
        and greatest(pp.accepted_at, c.under_since) <= coalesce(c.gone_since, 'infinity'::timestamptz)
    )
  end;
$$;

revoke execute on function public.can_read_page(uuid) from public, anon, authenticated, service_role;
grant execute on function public.can_read_page(uuid) to authenticated;

-- Runs after pages_guard (triggers of a kind run in name order), which
-- checks the row as the writer sent it.
create or replace function public.pages_purge()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_op = 'UPDATE' and old.purged_at is not null then
    if new.purged_at is null or new.deleted_at is null then
      raise exception 'page_purged' using errcode = '42501';
    end if;
    return old;
  elsif new.purged_at is null then
    return new;
  elsif (select auth.uid()) is not null and not public.is_space_owner(new.space_id) then
    raise exception 'forbidden_purge' using errcode = '42501';
  else
    new.purged_at := least(new.purged_at, now());
    new.deleted_at := least(coalesce(new.deleted_at, new.purged_at), new.purged_at);
  end if;
  new.title := '';
  new.icon := null;
  new.tags := '{}';
  new.default_template_id := null;
  new.public_link_until := null;
  return new;
end;
$$;

revoke execute on function public.pages_purge() from public, anon, authenticated, service_role;

drop trigger if exists pages_purge on public.pages;
create trigger pages_purge before insert or update on public.pages
  for each row execute function public.pages_purge();

create or replace function public.pages_purged()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if new.purged_at is null or (tg_op = 'UPDATE' and old.purged_at is not null) then
    return null;
  end if;
  insert into public.purged_objects (bucket_id, name, owner_id)
  select 'files', f.storage_path, s.owner_id
  from public.files f join public.spaces s on s.id = new.space_id
  where f.page_id = new.id and f.storage_path is not null and f.space_id <> new.space_id
  union
  select 'recordings', r.audio_path, s.owner_id
  from public.recordings r join public.spaces s on s.id = new.space_id
  where r.page_id = new.id and r.audio_path is not null and r.created_by is distinct from s.owner_id
  on conflict do nothing;
  delete from public.purged_objects p
  where not exists (select 1 from storage.objects o where o.bucket_id = p.bucket_id and o.name = p.name);
  delete from public.page_content where page_id = new.id;
  delete from public.page_history where page_id = new.id;
  delete from public.comments where page_id = new.id;
  delete from public.recordings where page_id = new.id;
  delete from public.files where page_id = new.id;
  delete from public.page_email_invites where page_id = new.id;
  return null;
end;
$$;

revoke execute on function public.pages_purged() from public, anon, authenticated, service_role;

drop trigger if exists pages_purged on public.pages;
create trigger pages_purged after insert or update of purged_at on public.pages
  for each row execute function public.pages_purged();

-- Runs after files_guard and before files_scrub_deleted.
create or replace function public.files_purge()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_op = 'UPDATE' and old.purged_at is not null then
    if new.purged_at is null or new.deleted_at is null then
      raise exception 'file_purged' using errcode = '42501';
    end if;
    return old;
  elsif new.purged_at is not null then
    if (select auth.uid()) is not null and not public.is_space_owner(new.space_id) then
      raise exception 'forbidden_purge' using errcode = '42501';
    end if;
    new.purged_at := least(new.purged_at, now());
    new.deleted_at := least(coalesce(new.deleted_at, new.purged_at), new.purged_at);
  end if;
  return new;
end;
$$;

revoke execute on function public.files_purge() from public, anon, authenticated, service_role;

drop trigger if exists files_purge on public.files;
create trigger files_purge before insert or update on public.files
  for each row execute function public.files_purge();

-- A deleted file keeps what it was while it is in the bin, and nothing
-- once it is deleted for good, or when it was deleted without the bin.
-- A file restored is in no bin.
create or replace function public.scrub_deleted_file()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if new.deleted_at is null then
    new.kept := null;
  elsif new.purged_at is not null or not coalesce(new.kept, false) then
    new.name := '';
    new.mime_type := null;
    new.size_bytes := null;
    new.storage_path := null;
  end if;
  return new;
end;
$$;

revoke execute on function public.scrub_deleted_file() from public, anon, authenticated, service_role;
