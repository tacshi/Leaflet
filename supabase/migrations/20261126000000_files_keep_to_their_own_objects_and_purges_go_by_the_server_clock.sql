-- * A file's storage_path was whatever its writer sent, and a page deleted
--   for good listed the path of every file and recording on it in
--   purged_objects, for the page's owner, who may then read and remove
--   that object: anyone who had read the path of someone else's file, as
--   on a page once shared with them, could give it to a file on a page of
--   their own, delete that page for good, and read and remove the other's
--   object; an editor could do the same on a shared page, and have its
--   owner's own devices remove another of the owner's objects. A listing
--   of an object listed already took it over too, for the latest page's
--   owner. A file's path now names its own object, {space}/{file}/{name}
--   (files_storage_path_own), as the apps have always made it, and a row
--   that names any other has it emptied here. The list takes only such a
--   path of a file, and of a recording only one that names its own audio,
--   {recorder}/{recording}.{ext}, as keep_recording_audio_path keeps it;
--   and an object stays listed for whoever it was listed for first.
-- * A live page under one deleted for good stayed under it, where the
--   shares of the page above still reached it, and where its owner could
--   not take them back (inherited_share): one restored on its own while
--   the page above was in the bin, one made or moved there by a device
--   that had not heard, or one under it all along. The apps show such a
--   page at the top level, and it now goes there: as the page above is
--   deleted for good (pages_purged), as it is made, moved or restored
--   under one (pages_purge), and here, for those under one already. A
--   deleted page under it stays, in the bin, to be deleted for good next
--   or restored, which places it at the top level as well.
-- * Each page deleted for good read the whole list of objects to remove,
--   every account's, to forget those gone from storage: it now reads only
--   its owner's.
-- * The deletion for good of a page or a file restored, or deleted again,
--   since the deletion its device saw was refused by comparing when the
--   row last changed, or was deleted, with that deletion
--   (purges_its_deletion): two devices' clocks, the one that restored and
--   the one that deletes for good. A restore made on a device whose clock
--   is behind looked older than the deletion, and a deletion for good
--   that crossed it was taken. A device now sends seen_updated_at with
--   the deletion: the row's updated_at, by the server's clock, as the
--   device last had the row from the server. The deletion is refused
--   (page_restored, file_restored) when the row has changed on the server
--   since, whatever the clocks; one sent without it, by an app from
--   before, goes by purges_its_deletion as before. The column is never
--   kept: it is null on every row. A server without this migration knows
--   no such column and refuses the write, which waits on the device, as a
--   file put in the bin waits for a server with `kept`, until the server
--   is updated.

-- ---------------------------------------------------------------------------
-- files: a path names the file's own object
-- ---------------------------------------------------------------------------

-- What named another space's or another file's object, or had no name in
-- it, names nothing now. Their triggers stay out of this one update:
-- updated_at stays.
alter table public.files disable trigger user;
update public.files set storage_path = null
where storage_path is not null
  and not (split_part(storage_path, '/', 1) = space_id::text
    and split_part(storage_path, '/', 2) = id::text
    and split_part(storage_path, '/', 3) <> ''
    and split_part(storage_path, '/', 4) = '');
alter table public.files enable trigger user;

alter table public.files drop constraint if exists files_storage_path_own;
alter table public.files add constraint files_storage_path_own check (
  storage_path is null
  or (split_part(storage_path, '/', 1) = space_id::text
    and split_part(storage_path, '/', 2) = id::text
    and split_part(storage_path, '/', 3) <> ''
    and split_part(storage_path, '/', 4) = '')
);

-- ---------------------------------------------------------------------------
-- pages and files: what a deletion for good saw
-- ---------------------------------------------------------------------------

alter table public.pages add column if not exists seen_updated_at timestamptz;
alter table public.files add column if not exists seen_updated_at timestamptz;

-- Runs after pages_guard (triggers of a kind run in name order), which
-- checks the row as the writer sent it, and before the triggers that move
-- updated_at and changed_at on. A live page goes to the top level rather
-- than under one deleted for good; the row policies then judge it where
-- it goes, so no one but the space's owner makes one there.
create or replace function public.pages_purge()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_seen timestamptz := new.seen_updated_at;
begin
  new.seen_updated_at := null;
  if tg_op = 'UPDATE' and old.purged_at is not null then
    if new.purged_at is null or new.deleted_at is null then
      raise exception 'page_purged' using errcode = '42501';
    end if;
    return old;
  elsif new.purged_at is null then
    if new.deleted_at is null and new.parent_id is not null and exists (
      select 1 from public.pages p where p.id = new.parent_id and p.purged_at is not null
    ) then
      new.parent_id := null;
    end if;
    return new;
  elsif (select auth.uid()) is not null and not public.is_space_owner(new.space_id) then
    raise exception 'forbidden_purge' using errcode = '42501';
  elsif tg_op = 'UPDATE' and v_seen is not null and date_trunc('milliseconds', old.updated_at) > v_seen then
    raise exception 'page_restored' using errcode = '42501';
  elsif tg_op = 'UPDATE' and v_seen is null and not public.purges_its_deletion(old.deleted_at, old.changed_at, new.deleted_at) then
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
declare
  v_seen timestamptz := new.seen_updated_at;
begin
  new.seen_updated_at := null;
  if tg_op = 'UPDATE' and old.purged_at is not null then
    if new.purged_at is null or new.deleted_at is null then
      raise exception 'file_purged' using errcode = '42501';
    end if;
    return old;
  elsif new.purged_at is not null then
    if (select auth.uid()) is not null and not public.is_space_owner(new.space_id) then
      raise exception 'forbidden_purge' using errcode = '42501';
    end if;
    if tg_op = 'UPDATE' and v_seen is not null and date_trunc('milliseconds', old.updated_at) > v_seen
       or tg_op = 'UPDATE' and v_seen is null and not public.purges_its_deletion(old.deleted_at, old.changed_at, new.deleted_at) then
      raise exception 'file_restored' using errcode = '42501';
    end if;
    new.purged_at := least(new.purged_at, now());
    new.deleted_at := least(coalesce(new.deleted_at, new.purged_at), new.purged_at);
  end if;
  return new;
end;
$$;

revoke execute on function public.files_purge() from public, anon, authenticated, service_role;

-- ---------------------------------------------------------------------------
-- pages_purged: only objects of their own, for whoever had them listed
-- first; no live page left under it; only its owner's list cleared
-- ---------------------------------------------------------------------------

create or replace function public.pages_purged()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_owner uuid;
begin
  if new.purged_at is null or (tg_op = 'UPDATE' and old.purged_at is not null) then
    return null;
  end if;
  select s.owner_id into v_owner from public.spaces s where s.id = new.space_id;
  insert into public.purged_objects (bucket_id, name, owner_id, page_id)
  select 'files', f.storage_path, v_owner, new.id
  from public.files f
  where f.page_id = new.id and f.storage_path is not null
    and split_part(f.storage_path, '/', 1) = f.space_id::text
    and split_part(f.storage_path, '/', 2) = f.id::text
    and split_part(f.storage_path, '/', 3) <> ''
    and split_part(f.storage_path, '/', 4) = ''
  union
  select 'recordings', r.audio_path, v_owner, new.id
  from public.recordings r
  where r.page_id = new.id and r.audio_path is not null
    and (r.created_by is null or split_part(r.audio_path, '/', 1) = r.created_by::text)
    and split_part(split_part(r.audio_path, '/', 2), '.', 1) = r.id::text
    and split_part(r.audio_path, '/', 3) = ''
  on conflict do nothing;
  delete from public.purged_objects p
  where p.owner_id = v_owner
    and not exists (select 1 from storage.objects o where o.bucket_id = p.bucket_id and o.name = p.name);
  -- A live page under it goes to the top level, out of the reach of its
  -- shares. A deleted one is in the bin, deleted for good with it or after.
  update public.pages set parent_id = null
  where parent_id = new.id and purged_at is null and deleted_at is null;
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

-- The live pages under one deleted for good already go to the top level,
-- as the server's move: changed_at stays, and updated_at moves on, so the
-- devices that have them pull them, and pages_touch_moved has the rows
-- under them reach whoever reads them now.
alter table public.pages disable trigger pages_set_changed_at_on_update;
update public.pages c set parent_id = null
where c.purged_at is null and c.deleted_at is null
  and exists (select 1 from public.pages p where p.id = c.parent_id and p.purged_at is not null);
alter table public.pages enable trigger pages_set_changed_at_on_update;
