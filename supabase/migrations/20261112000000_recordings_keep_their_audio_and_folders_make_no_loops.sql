-- * A device sends a recording whole, an upsert, whose insert comes first
--   and is its sender's own: keep_recording_audio_path took the path of
--   an editor's recording, sent by the page's owner as they pulled it, for
--   one that is not the sender's, and emptied it; the update that followed
--   wrote the empty path, which it took from anyone. The page's members
--   then read the audio no more (recordings_objects_shared_read goes by
--   the path), and nothing named the object again. Once the recorder's
--   account was gone, the same upsert was refused (recording_audio_kept),
--   and the device dropped what its owner had changed. And a device that
--   pulled a recording before its audio was up sent no path over the one
--   the recording device had set since. An upsert of a recording that is
--   there now keeps its path through the insert, and no path over one is
--   dropped, as any other that is not the row's own: only the recorder
--   clears it, once the audio is gone from the bucket.
-- * Any editor of a page could delete a recording on it outright, someone
--   else's too. A recording has no deleted_at, so the other devices kept
--   theirs and never heard of it, and its audio stayed in the bucket under
--   no row. No app deletes a recording's row: no caller does now, as with
--   comments (20261031010000). A recording goes with its page.
-- * A folder could be moved inside one of its own: folders_guard looked
--   only at the folder's space, and for the folder itself. Two devices that
--   each moved one of two folders into the other left both in a loop, in
--   no folder list. A folder now goes under none that is inside it
--   (folder_cycle), each move in a space after the one before, as pages
--   move. Inside it as the apps show folders: one in a deleted folder is
--   at the top level, so what is above a deleted folder is not above the
--   folders in it, and a folder restored into what it came to hold comes
--   back at the top level. The loops there are opened here, at the folder
--   of the smallest id in each, which goes to the top level, as the apps
--   open one they find; a ring that runs through a deleted folder is no
--   loop, and stays.
-- * Deleting a folder's row deleted every folder inside it with it (its
--   foreign key's cascade), though a folder that goes leaves what was in
--   it: its pages and files leave it (on delete set null), and now its
--   folders do, to the top level.

-- ---------------------------------------------------------------------------
-- recordings: the audio stays where it is
-- ---------------------------------------------------------------------------

-- A caller's write keeps a recording's audio path the row's own,
-- {created_by}/{id}.{ext}, or leaves it as it was: any other path is
-- dropped, not refused, and so is no path over one, unless the caller is
-- the recorder and the audio is gone from the bucket (only they remove
-- it: recordings_objects_own_delete). An upsert's insert, of a recording
-- that is there, goes by that row's recorder and path, not the sender's,
-- so the update that follows is sent what the row keeps. Runs after
-- keep_recording_creator (triggers of a kind run in name order), which
-- has refused a change of the path of a recording whose recorder is gone.
-- Writes with no caller (the service role) keep what they set. It runs as
-- its owner to see the row and the audio whoever sends.
create or replace function public.keep_recording_audio_path()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_creator uuid := new.created_by;
  v_stored text;
begin
  if v_uid is null then
    return new;
  end if;
  if tg_op = 'UPDATE' then
    v_stored := old.audio_path;
  else
    select r.created_by, r.audio_path into v_creator, v_stored
    from public.recordings r where r.id = new.id;
    if not found then
      v_creator := new.created_by;
    end if;
  end if;
  if new.audio_path is not distinct from v_stored then
    return new;
  end if;
  if new.audio_path is null then
    if v_creator is distinct from v_uid or exists (
      select 1 from storage.objects o where o.bucket_id = 'recordings' and o.name = v_stored
    ) then
      new.audio_path := v_stored;
    end if;
  elsif v_creator is null
     or new.audio_path !~ ('^' || v_creator::text || '/' || new.id::text || '\.[A-Za-z0-9]+$') then
    new.audio_path := v_stored;
  end if;
  return new;
end;
$$;

revoke execute on function public.keep_recording_audio_path() from public, anon, authenticated, service_role;

-- ---------------------------------------------------------------------------
-- recordings: no caller deletes one
-- ---------------------------------------------------------------------------

drop policy if exists recordings_delete on public.recordings;
revoke delete on public.recordings from anon, authenticated;

-- ---------------------------------------------------------------------------
-- folders: no loops, and one that goes leaves its folders
-- ---------------------------------------------------------------------------

alter table public.folders drop constraint if exists folders_parent_folder_id_fkey;
alter table public.folders add constraint folders_parent_folder_id_fkey
  foreign key (parent_folder_id) references public.folders (id) on delete set null;

-- Folders stay in their space and nest only within it, none inside itself,
-- as the apps show them: a folder in a deleted one is at the top level,
-- so the walk up from the new parent ends at a deleted folder. A folder
-- moved under one inside it is refused. One restored where it is, under a
-- folder that came to be inside it while it was deleted, comes back at
-- the top level, as the apps restore it.
create or replace function public.folders_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_moved boolean;
  v_cycle boolean;
begin
  if tg_op = 'UPDATE' and new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;
  if new.parent_folder_id is not null and not exists (
    select 1 from public.folders f
    where f.id = new.parent_folder_id and f.space_id = new.space_id and f.id <> new.id
  ) then
    raise exception 'parent_folder_in_other_space' using errcode = '23514';
  end if;
  -- A new folder has none inside it; an upsert of one that is there goes
  -- on to the update.
  if tg_op = 'UPDATE' and new.parent_folder_id is not null then
    v_moved := new.parent_folder_id is distinct from old.parent_folder_id;
    if v_moved or (old.deleted_at is not null and new.deleted_at is null) then
      -- Held to the end of the transaction, so the walk below sees any
      -- move or restore made in the space before this one.
      perform pg_advisory_xact_lock(hashtextextended('leaflet.folder_moves:' || new.space_id::text, 0));
      -- Up from the new parent, each folder once, so it ends on a loop
      -- too, and no further up than a deleted folder.
      with recursive above (id) as (
        select new.parent_folder_id
        union
        select f.parent_folder_id
        from public.folders f join above a on f.id = a.id
        where f.parent_folder_id is not null and f.deleted_at is null
      )
      select exists (select 1 from above a where a.id = new.id) into v_cycle;
      if v_cycle and v_moved then
        raise exception 'folder_cycle' using errcode = '23514';
      elsif v_cycle then
        new.parent_folder_id := null;
      end if;
    end if;
  end if;
  return new;
end;
$$;

-- The loops there: from each folder that is not deleted up, each folder
-- above it once, no further than a deleted one. A folder that comes to
-- itself is in a loop, with all those above it, and the one of the
-- smallest id among them goes to the top level. updated_at moves on, and
-- changed_at, so the devices pull it and take it over their own.
with recursive above (start, id) as (
  select f.id, f.parent_folder_id from public.folders f
  where f.parent_folder_id is not null and f.deleted_at is null
  union
  select a.start, f.parent_folder_id
  from above a join public.folders f on f.id = a.id
  where f.parent_folder_id is not null and f.deleted_at is null
)
update public.folders f
set parent_folder_id = null
where f.id in (
  select a.start from above a
  group by a.start
  having bool_or(a.id = a.start) and bool_and(a.start <= a.id)
);
