-- * page_content's revision moved on with every update, but an insert
--   kept the one its writer sent: an editor could make a page's content
--   at the largest bigint, and every save of it after failed for
--   everyone, out of range. A row now starts at revision 0. Rows already
--   past 2^53, where the Functions' numbers no longer tell revisions
--   apart, start again at 0 here.
-- * recordings.audio_path names the object a recording's viewers may read
--   (recordings_objects_shared_read), and any editor of its page could
--   point it at other audio; once the recorder's account was gone, the
--   read rule matched it by the recording's id alone. A caller's write now
--   keeps it the row's own, {created_by}/{id}.{ext}, or empty: any other
--   path is dropped, not refused, as stamp_page_history_writer drops an
--   ai_job_id, so a device that synced through iCloud Drive, whose
--   recordings name their audio by its path there until it is uploaded,
--   still sends them.
-- * files_objects_shared_read let the viewers of a page read a file's
--   object after the file was deleted (deleted_at), until the device that
--   deleted it removed the object, which it may never do. A deleted file's
--   object is now read only by those who may delete it: its space's owner
--   (files_objects_owner_read) and its page's owner, who removes it
--   (files_objects_page_owner_delete, which needs it seen).
-- * A deleted comment was emptied, but a guest's kept the name the guest
--   gave (agent_name). It is now 'Guest', as a guest who gave none is,
--   and the ones deleted already are renamed here.
-- * can_access_space_path has had no caller since 20261028030000, yet any
--   signed-in caller could ask it who shares what; page_updates, kept for
--   collaboration that never came, took any number of rows of any size
--   from any editor. Both go.

-- ---------------------------------------------------------------------------
-- page_content: revisions start at 0
-- ---------------------------------------------------------------------------

-- page_content's revision starts at 0 and moves on with every update,
-- whatever the writer sent, so a writer that does not check it still
-- makes the others retry.
create or replace function public.bump_revision()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if tg_op = 'INSERT' then
    new.revision := 0;
  else
    new.revision := old.revision + 1;
  end if;
  return new;
end;
$$;

drop trigger if exists page_content_bump_revision on public.page_content;
create trigger page_content_bump_revision before insert or update on public.page_content
  for each row execute function public.bump_revision();

-- The trigger would take such a row past the largest bigint: it stays out
-- of this one update.
alter table public.page_content disable trigger page_content_bump_revision;
update public.page_content set revision = 0 where revision > 9007199254740992;
alter table public.page_content enable trigger page_content_bump_revision;

-- ---------------------------------------------------------------------------
-- recordings: audio at the row's own path
-- ---------------------------------------------------------------------------

-- Runs after keep_recording_creator (triggers of a kind run in name
-- order), so created_by is the row's own: a recording whose recorder is
-- gone has refused a change of its path there already. A path the row
-- has already stays as it is.
create or replace function public.keep_recording_audio_path()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if auth.uid() is null or new.audio_path is null
     or (tg_op = 'UPDATE' and new.audio_path is not distinct from old.audio_path) then
    return new;
  end if;
  if new.created_by is null
     or new.audio_path !~ ('^' || new.created_by::text || '/' || new.id::text || '\.[A-Za-z0-9]+$') then
    new.audio_path := case when tg_op = 'INSERT' then null else old.audio_path end;
  end if;
  return new;
end;
$$;

drop trigger if exists recordings_own_audio on public.recordings;
create trigger recordings_own_audio before insert or update on public.recordings
  for each row execute function public.keep_recording_audio_path();

-- ---------------------------------------------------------------------------
-- files: a deleted file's object is for those who may delete it
-- ---------------------------------------------------------------------------

drop policy if exists files_objects_shared_read on storage.objects;
create policy files_objects_shared_read on storage.objects for select to authenticated
  using (
    bucket_id = 'files'
    and exists (
      select 1 from public.files f
      where f.storage_path = storage.objects.name
        and split_part(storage.objects.name, '/', 1) = f.space_id::text
        and split_part(storage.objects.name, '/', 2) = f.id::text
        and f.page_id is not null
        and ((f.deleted_at is null and public.can_view_page(f.page_id))
          or public.page_role(f.page_id) = 'owner')
    )
  );

-- ---------------------------------------------------------------------------
-- comments: a deleted guest comment keeps no name
-- ---------------------------------------------------------------------------

create or replace function public.scrub_deleted_comment()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if new.deleted_at is not null then
    new.body := '';
    new.quote := null;
    new.mentions := '[]'::jsonb;
    if new.author_kind = 'guest' then
      new.agent_name := 'Guest';
    end if;
  end if;
  return new;
end;
$$;

-- updated_at moves on, so the devices pull the renamed rows.
update public.comments
set agent_name = 'Guest'
where deleted_at is not null and author_kind = 'guest' and agent_name is distinct from 'Guest';

-- ---------------------------------------------------------------------------
-- Leftovers
-- ---------------------------------------------------------------------------

drop function if exists public.can_access_space_path(text);
drop table if exists public.page_updates;
