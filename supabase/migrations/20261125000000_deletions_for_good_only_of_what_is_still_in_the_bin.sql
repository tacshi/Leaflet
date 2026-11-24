-- * A page or a file is deleted for good only as it is still in the bin,
--   deleted as the device that deletes it saw it: a device sends the
--   deletion it saw with it. One restored since, on another device, or
--   deleted again since, which gave it its days in the bin anew, is
--   refused (page_restored, file_restored), as a device that had not
--   heard of it, once asleep or offline, would delete for good what is
--   back in use. The device then takes the row as the server has it, and
--   what was on the page with it, which it kept until the server took the
--   deletion.
-- * purged_objects lists, by page, every object of a page deleted for good
--   where it is stored, not only those of the people it was shared with:
--   the device that deleted it removes them all, also those of the files
--   and recordings it had not heard of yet, which another device of the
--   owner's put there.

-- Whether a deletion for good is of the deletion the row has: p_seen is
-- the deletion the device saw, p_deleted and p_changed the row's deletion,
-- or, live, when it last changed, as when it was restored. A device keeps
-- times to the millisecond.
create or replace function public.purges_its_deletion(p_deleted timestamptz, p_changed timestamptz, p_seen timestamptz)
returns boolean
language sql
immutable
set search_path = ''
as $$
  select p_seen is not null and case
    when p_deleted is not null then date_trunc('milliseconds', p_deleted) <= p_seen
    else date_trunc('milliseconds', p_changed) <= p_seen
  end;
$$;

revoke execute on function public.purges_its_deletion(timestamptz, timestamptz, timestamptz) from public, anon, authenticated, service_role;

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
  elsif tg_op = 'UPDATE' and not public.purges_its_deletion(old.deleted_at, old.changed_at, new.deleted_at) then
    raise exception 'page_restored' using errcode = '42501';
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
    if tg_op = 'UPDATE' and not public.purges_its_deletion(old.deleted_at, old.changed_at, new.deleted_at) then
      raise exception 'file_restored' using errcode = '42501';
    end if;
    new.purged_at := least(new.purged_at, now());
    new.deleted_at := least(coalesce(new.deleted_at, new.purged_at), new.purged_at);
  end if;
  return new;
end;
$$;

revoke execute on function public.files_purge() from public, anon, authenticated, service_role;

alter table public.purged_objects add column if not exists page_id uuid references public.pages (id) on delete cascade;

create index if not exists purged_objects_page_idx on public.purged_objects (page_id);

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
  insert into public.purged_objects (bucket_id, name, owner_id, page_id)
  select 'files', f.storage_path, s.owner_id, new.id
  from public.files f join public.spaces s on s.id = new.space_id
  where f.page_id = new.id and f.storage_path is not null
  union
  select 'recordings', r.audio_path, s.owner_id, new.id
  from public.recordings r join public.spaces s on s.id = new.space_id
  where r.page_id = new.id and r.audio_path is not null
  on conflict (bucket_id, name) do update set owner_id = excluded.owner_id, page_id = excluded.page_id;
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
