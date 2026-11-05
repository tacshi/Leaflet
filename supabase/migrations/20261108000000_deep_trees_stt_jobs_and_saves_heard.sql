-- * Each member's read of a page walks up its tree, once a row, so a
--   thousand rows of a tree a thousand pages deep took seconds of the eight
--   a statement has, and more on a smaller machine: an editor could still
--   make one that the people it is shared with could no longer pull. A page
--   now goes no deeper than 256 (page_too_deep, as a page is made, or moved
--   any deeper). Trees that are deeper already stay as they are, and their
--   walks still reach their top: nothing reads less for this.
-- * Soniox goes on transcribing a long recording past the deadline a call
--   has, and a call that gave up removed what it could of the transcription
--   and left the next to send the audio and pay for it again, every time.
--   A transcription started is now kept, per recording and caller, for the
--   next call to pick up (stt_jobs).
-- * page_saves_heard was made only if Realtime had made realtime.messages
--   as the migration ran, and a project whose Realtime makes it later was
--   left without the policy for good: its devices were refused the channel
--   and kept checking their open pages. ensure_page_saves_heard makes it,
--   here and as the app sets a project up, until it is there.

-- ---------------------------------------------------------------------------
-- pages: no deeper than 256
-- ---------------------------------------------------------------------------

-- Pages stay in their space, with their id; a subpage lives in its
-- parent's space; no cycles, and no tree deeper than 256 pages (a page and
-- those above it), as a page is made under a parent or moved deeper; moving
-- a page under a new parent needs edit access to that parent (the update policy
-- already requires edit access to the page itself), and, for anyone but
-- the space's owner, the same shares on both parents and the page readable
-- where it was. Only the space's owner marks a page a template or puts a
-- page in a folder. created_by is who made the page, and placed_at when it
-- reached the server or was last put under a parent: the server sets both,
-- and a deletion is stamped no later than its clock. A deleted page is
-- restored, or its deletion moved, only by someone who may read what is
-- on it.
create or replace function public.pages_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_parent_space uuid;
  v_above integer;
  v_cycle boolean;
  v_below integer;
  v_was integer;
begin
  if new.parent_id is not null then
    select p.space_id into v_parent_space from public.pages p where p.id = new.parent_id;
    if v_parent_space is null then
      raise exception 'parent_not_found' using errcode = 'P0002';
    end if;
    if v_parent_space <> new.space_id then
      raise exception 'parent_in_other_space' using errcode = '23514';
    end if;
  end if;

  if new.folder_id is not null and not exists (
    select 1 from public.folders f where f.id = new.folder_id and f.space_id = new.space_id
  ) then
    raise exception 'folder_in_other_space' using errcode = '23514';
  end if;

  if tg_op = 'INSERT' then
    if new.is_template
       and v_uid is not null
       and not public.is_space_owner(new.space_id)
       and not exists (select 1 from public.pages p where p.id = new.id) then
      raise exception 'forbidden_template' using errcode = '42501';
    end if;
    if new.folder_id is not null
       and v_uid is not null
       and not public.is_space_owner(new.space_id)
       and not exists (select 1 from public.pages p where p.id = new.id) then
      raise exception 'forbidden_folder' using errcode = '42501';
    end if;
    -- An upsert of a page already there goes on to the update: it is no new
    -- page, in a tree that may be deeper from before.
    if new.parent_id is not null and not exists (select 1 from public.pages p where p.id = new.id) then
      -- Held to the end of the transaction, as a move holds it, so a page
      -- made under one that a move takes deeper meanwhile is counted
      -- where the move puts it.
      perform pg_advisory_xact_lock(hashtextextended('leaflet.page_moves:' || new.space_id::text, 0));
      -- depth counts the pages above the parent: with the parent and the
      -- page itself, at most 256.
      select max(a.depth) into v_above from public.page_ancestors(new.parent_id) a;
      if v_above >= 255 then
        raise exception 'page_too_deep' using errcode = '23514';
      end if;
    end if;
    if v_uid is not null then
      new.created_by := v_uid;
    end if;
    -- Not created_at, which is the client's: a page that went under a
    -- shared one before it came up would bring its private history.
    new.placed_at := now();
    if new.deleted_at is not null then
      new.deleted_at := least(new.deleted_at, now());
    end if;
    return new;
  end if;

  if new.id <> old.id then
    raise exception 'page_id_immutable' using errcode = '23514';
  end if;
  if new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;

  if new.is_template is distinct from old.is_template
     and v_uid is not null
     and not public.is_space_owner(new.space_id) then
    raise exception 'forbidden_template' using errcode = '42501';
  end if;

  if v_uid is not null then
    new.created_by := old.created_by;
  end if;
  new.placed_at := old.placed_at;

  -- A deletion is no later than the server's clock, which everyone
  -- compares it with; a restore clears it, checked below.
  if new.deleted_at is not null and new.deleted_at is distinct from old.deleted_at then
    new.deleted_at := least(new.deleted_at, now());
  end if;

  if old.deleted_at is not null
     and new.deleted_at is distinct from old.deleted_at
     and v_uid is not null
     and not public.can_read_page(old.id) then
    raise exception 'forbidden_restore' using errcode = '42501';
  end if;

  if new.parent_id is distinct from old.parent_id then
    -- Held to the end of the transaction, so the checks below see any
    -- move made in the space before this one, and any page made.
    perform pg_advisory_xact_lock(hashtextextended('leaflet.page_moves:' || new.space_id::text, 0));
    if new.parent_id is not null then
      select max(a.depth), coalesce(bool_or(a.id = new.id), false) into v_above, v_cycle
      from public.page_ancestors(new.parent_id) a;
      if v_cycle then
        raise exception 'page_cycle' using errcode = '23514';
      end if;
      -- How many pages deep the tree under the page goes.
      with recursive tree (id, depth) as (
        select p.id, 1 from public.pages p where p.parent_id = new.id
        union all
        select p.id, t.depth + 1
        from public.pages p join tree t on p.parent_id = t.id
        where t.depth < 1000
      )
      select coalesce(max(t.depth), 0) into v_below from tree t;
      -- A tree deeper from before may move, as long as it goes no deeper.
      if v_above + v_below >= 255 then
        select max(a.depth) into v_was from public.page_ancestors(old.parent_id) a;
        if v_above > coalesce(v_was, -1) then
          raise exception 'page_too_deep' using errcode = '23514';
        end if;
      end if;
    end if;

    if v_uid is not null then
      if new.parent_id is null then
        if not public.is_space_owner(new.space_id) then
          raise exception 'forbidden_move' using errcode = '42501';
        end if;
      elsif not public.can_edit_page(new.parent_id) then
        raise exception 'forbidden_move' using errcode = '42501';
      elsif not public.is_space_owner(new.space_id)
            and public.shared_chain(old.parent_id) <> public.shared_chain(new.parent_id) then
        raise exception 'forbidden_move' using errcode = '42501';
      elsif not public.is_space_owner(new.space_id)
            and not public.can_read_page(old.id) then
        raise exception 'forbidden_move' using errcode = '42501';
      end if;
    end if;

    new.placed_at := now();
  end if;

  if new.folder_id is distinct from old.folder_id
     and v_uid is not null
     and not public.is_space_owner(new.space_id) then
    raise exception 'forbidden_folder' using errcode = '42501';
  end if;

  return new;
end;
$$;

-- ---------------------------------------------------------------------------
-- stt_jobs: a transcription Soniox has, for the next call to pick up
-- ---------------------------------------------------------------------------

-- A caller's transcription of a recording at Soniox, from when it is
-- started until Soniox has finished it and its transcript is saved. The
-- ids are only of use with its caller's key, which picks them up. Only the
-- stt function reads and writes it, with the service role: no client's
-- write can point a call at anything. It goes with its recording, or its
-- caller's account; what is at Soniox then goes by Soniox's own retention,
-- in 30 days.
create table if not exists public.stt_jobs (
  recording_id uuid not null references public.recordings (id) on delete cascade,
  user_id uuid not null references auth.users (id) on delete cascade,
  file_id text not null,
  transcription_id text not null,
  model text not null,
  created_at timestamptz not null default now(),
  primary key (recording_id, user_id)
);

create index if not exists stt_jobs_user_idx on public.stt_jobs (user_id);

alter table public.stt_jobs enable row level security;
revoke all on public.stt_jobs from public, anon, authenticated, service_role;
grant select, insert, update, delete on public.stt_jobs to service_role;

-- ---------------------------------------------------------------------------
-- page_saves_heard: made once Realtime has its messages
-- ---------------------------------------------------------------------------

-- Makes the policy that lets those who may read a page hear its channel,
-- when Realtime has made realtime.messages and the policy is not there,
-- and says whether it is. Who may hear a page's channel is who may read
-- the page, asked as they join it and as their access token is renewed;
-- no one may send on it. The topic is checked before it is read as a
-- page's id, which a CASE keeps in order: anything else is no page's. No
-- role of the API calls this: the app does, through the Management API, as
-- postgres, whenever it sets a project up.
create or replace function public.ensure_page_saves_heard()
returns boolean
language plpgsql
set search_path = ''
as $$
begin
  if to_regclass('realtime.messages') is null or to_regprocedure('realtime.topic()') is null then
    return false;
  end if;
  if not exists (
    select 1 from pg_catalog.pg_policy p
    where p.polrelid = 'realtime.messages'::regclass and p.polname = 'page_saves_heard'
  ) then
    execute $policy$
      create policy page_saves_heard on realtime.messages for select to authenticated
        using (
          realtime.messages.extension = 'broadcast'
          and case
            when (select realtime.topic()) ~ '^page:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
              then public.can_read_page(substr((select realtime.topic()), 6)::uuid)
            else false
          end
        )
    $policy$;
  end if;
  return true;
end;
$$;

revoke execute on function public.ensure_page_saves_heard() from public, anon, authenticated, service_role;

select public.ensure_page_saves_heard();
