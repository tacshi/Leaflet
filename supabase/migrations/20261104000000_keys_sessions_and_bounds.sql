-- * A provider's saved key went wherever its provider was moved, as long
--   as some write let the row change: the providers function kept it when
--   a provider was saved again as a local preset, at any address, and when
--   a save failed after the row had changed. A provider's keys now go, and
--   their Vault secrets with them, whenever its base URL's origin, its
--   preset or whether it is local changes, whoever writes the row
--   (model_providers_key_stays): no write can move a key to an address.
--   The origin is read as a URL parser reads it (base_url_origin); a URL
--   it reads its own way, as one with a backslash, keeps a key only if it
--   stays the very same.
-- * Pages are only soft-deleted, and a page's content, comments,
--   recordings, files and images were read by anyone who could view the
--   page, deleted or not: someone shared a page read what had been taken
--   off it before it was shared with them, as its deleted subpages. Anyone
--   but the owner now reads them only of a page still there, or deleted
--   after their view of it began, which is when its history starts for
--   them (can_read_page); the page's own row stays readable, so its
--   deletion syncs. Nor can they undo such a deletion (forbidden_restore).
--   The placed_at that 20261031000000 filled in for the pages there were
--   counts for none of this, since it tells nothing of when they were
--   placed (their history keeps to it as before).
-- * Signing a device out (Account › Devices) only set its devices row's
--   revoked_at, which the device could clear again, and its session went
--   on. sign_out_device now sets it and ends the device's session, which
--   devices.session_id names: every write a client makes of a devices row
--   stamps it from the caller's token, and no client sets or clears
--   revoked_at, or deletes a row. The revocation is for that session
--   only: the device's first write from a new one, after its owner signs
--   in on it again, clears it.
-- * A page's, comment's or recording's id, and a page_content row's
--   page_id, could be changed on update: an editor could give a recording
--   another id and have its audio path checked against that one, so the
--   row named audio of the owner's it was never made with. They stay as
--   they were made.
-- * Comments had no bound: anyone who can view a page could add any
--   number of any size. body and quote are now at most 100,000
--   characters (the apps' own limit is 4,000, for a public link's
--   visitors), mentions at most 64 KB, and block_id 256 characters (the
--   apps' block IDs are 6 to 8, and a reader's renaming adds a few); rows
--   past that are cut to it first. Anyone but the page's owner adds at
--   most 2,000 comments to a page, of 10 MB in all, body, quote and
--   mentions (too_many_comments, comments_too_long).
-- * page_ancestors walked as high as a tree went, with CYCLE paths that
--   made each walk take time in the square of its depth, and every
--   member's read of a page walks once a row: an editor could make a tree
--   deep enough that the people it is shared with could no longer pull.
--   A tree is now at most 1,000 pages deep (page_too_deep, as a page is
--   made or moved), and the walks stop there, by depth: pages_guard, with
--   its lock, keeps cycles out, and a walk still ends should one get in.
--   page_ancestors goes up a page at a time, by primary key, so a step
--   costs the same whatever the planner makes of the table, and it also
--   tells each page's deleted_at, for can_read_page.
-- * pages_select walked from a page's parent, a second walk, for every row
--   the caller could not see by the page itself, though only a page not
--   yet in the table needs it: now only for one placed in the caller's own
--   transaction, as a new one is (pages_guard sets placed_at to now()).
-- * Moving a page under a share touched its content rows' updated_at,
--   which moved their revision on: every device with an edit waiting on a
--   page under it failed its compare-and-set and merged again for
--   nothing. The revision now moves on when the content or its text
--   changes.

-- ---------------------------------------------------------------------------
-- The walk up: at most 1,000 pages, each with its deleted_at
-- ---------------------------------------------------------------------------

-- p_page_id and the pages above it, nearest first: depth 0 is the page
-- itself, and its parent 1. A tree is at most 1,000 pages deep
-- (pages_guard), so the walk ends at the top of the tree, or 1,000 pages
-- up, where it would go round a cycle that got in (pages_guard keeps them
-- out). Each step is one lookup by primary key: a recursive query's join
-- was planned from the table's statistics, and where they were stale, as
-- in a new project, it scanned the whole table at every step. It reads
-- past row-level security only as the definer functions here, which call
-- it as their owner. Its columns change, so it is made again: the
-- functions that call it name it in their bodies, which keep no tie to
-- it.
drop function public.page_ancestors(uuid);
create function public.page_ancestors(p_page_id uuid)
returns table (
  id uuid, parent_id uuid, space_id uuid, placed_at timestamptz, deleted_at timestamptz, depth integer
)
language plpgsql
stable
set search_path = ''
as $$
declare
  v_next uuid := p_page_id;
begin
  for v_depth in 0 .. 999 loop
    select p.id, p.parent_id, p.space_id, p.placed_at, p.deleted_at, v_depth
    into id, parent_id, space_id, placed_at, deleted_at, depth
    from public.pages p
    where p.id = v_next;
    exit when not found;
    return next;
    exit when parent_id is null;
    v_next := parent_id;
  end loop;
end;
$$;

-- Only the functions here call it.
revoke execute on function public.page_ancestors(uuid) from public, anon, authenticated, service_role;

-- ---------------------------------------------------------------------------
-- What is on a deleted page is read by those who saw it before
-- ---------------------------------------------------------------------------

-- 20261031000000 gave every page there was the time it ran as its
-- placed_at, not knowing when each was put where it was: a placed_at of
-- that time says only that the page was placed no later. This is that
-- time, which the table keeps as the column's value for the rows that
-- were there (its missing value), or null where it no longer tells, as
-- after the table was rewritten.
do $$
declare
  v_at timestamptz;
begin
  select (a.attmissingval::text::timestamptz[])[1] into v_at
  from pg_attribute a
  where a.attrelid = 'public.pages'::regclass and a.attname = 'placed_at' and a.atthasmissing;
  execute format(
    'create or replace function public.placed_at_backfilled() returns timestamptz language sql immutable '
    'set search_path = '''' as $f$ select %s::timestamptz $f$',
    coalesce(quote_literal(v_at), 'null'));
end;
$$;

revoke execute on function public.placed_at_backfilled() from public, anon, authenticated, service_role;

-- Whether the current user may read what is on a page: its content,
-- comments, recordings, files and images. The owner reads all of it.
-- Anyone else reads it through an accepted share on the page's chain
-- whose view of the page began (when it was accepted or, if later, when
-- the page came under the shared page, as page_history_since has it) no
-- later than the page, or a page between it and the shared one, was
-- deleted: what was taken off before then was never theirs. A placed_at
-- that 20261031000000 filled in tells nothing of when a page came under
-- the shared one, so here it does not count, and such a page counts as
-- there from before it was deleted, as it was read before placed_at was
-- kept: a page deleted after the share was accepted, but before that
-- migration ran, stays its member's. Its history keeps to placed_at as it
-- is (page_history_since).
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

drop policy if exists page_content_select on public.page_content;
create policy page_content_select on public.page_content for select to authenticated
  using (public.can_read_page(page_id));

drop policy if exists comments_select on public.comments;
create policy comments_select on public.comments for select to authenticated
  using (public.can_read_page(page_id));

drop policy if exists recordings_select on public.recordings;
create policy recordings_select on public.recordings for select to authenticated
  using (public.can_read_page(page_id));

drop policy if exists files_select_shared on public.files;
create policy files_select_shared on public.files for select to authenticated
  using (page_id is not null and public.can_read_page(page_id));

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
        and ((f.deleted_at is null and public.can_read_page(f.page_id))
          or public.page_role(f.page_id) = 'owner')
    )
  );

drop policy if exists recordings_objects_shared_read on storage.objects;
create policy recordings_objects_shared_read on storage.objects for select to authenticated
  using (
    bucket_id = 'recordings'
    and exists (
      select 1 from public.recordings r
      where r.audio_path = storage.objects.name
        and (r.created_by is null or split_part(storage.objects.name, '/', 1) = r.created_by::text)
        and split_part(split_part(storage.objects.name, '/', 2), '.', 1) = r.id::text
        and public.can_read_page(r.page_id)
    )
  );

-- images/{space_id}/{page_id}/{image_id}.{ext}: readable by those who may
-- read what is on the page.
create or replace function public.can_view_image_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_space uuid := public.uuid_or_null(split_part(p_name, '/', 1));
  v_page uuid := public.uuid_or_null(split_part(p_name, '/', 2));
begin
  if v_space is null or v_page is null
     or split_part(p_name, '/', 3) = '' or split_part(p_name, '/', 4) <> '' then
    return false;
  end if;
  return exists (
    select 1 from public.pages p
    where p.id = v_page and p.space_id = v_space
  ) and public.can_read_page(v_page);
end;
$$;

-- ---------------------------------------------------------------------------
-- pages: no deeper than 1,000, ids kept, deletions kept from who never saw
-- ---------------------------------------------------------------------------

-- Pages stay in their space, with their id; a subpage lives in its
-- parent's space; no cycles, and no tree deeper than 1,000 pages (a page
-- and those above it), as a page is made under a parent or moved; moving
-- a page under a new parent needs edit access to that parent (the update
-- policy already requires edit access to the page itself), and, for
-- anyone but the space's owner, the same shares on both parents.
-- created_by is who made the page, and placed_at when it reached the
-- server or was last put under a parent: the server sets both. A new page
-- goes in a folder only for the space's owner, as a moved one does. A
-- deleted page is restored, or its deletion moved, only by someone who
-- may read what is on it.
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
    if new.folder_id is not null
       and v_uid is not null
       and not public.is_space_owner(new.space_id)
       and not exists (select 1 from public.pages p where p.id = new.id) then
      raise exception 'forbidden_folder' using errcode = '42501';
    end if;
    if new.parent_id is not null then
      -- Held to the end of the transaction, as a move holds it, so a page
      -- made under one that a move takes deeper meanwhile is counted
      -- where the move puts it.
      perform pg_advisory_xact_lock(hashtextextended('leaflet.page_moves:' || new.space_id::text, 0));
      -- depth counts the pages above the parent: with the parent and the
      -- page itself, at most 1,000.
      select max(a.depth) into v_above from public.page_ancestors(new.parent_id) a;
      if v_above >= 999 then
        raise exception 'page_too_deep' using errcode = '23514';
      end if;
    end if;
    if v_uid is not null then
      new.created_by := v_uid;
    end if;
    -- Not created_at, which is the client's: a page that went under a
    -- shared one before it came up would bring its private history.
    new.placed_at := now();
    return new;
  end if;

  if new.id <> old.id then
    raise exception 'page_id_immutable' using errcode = '23514';
  end if;
  if new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;

  if v_uid is not null then
    new.created_by := old.created_by;
  end if;
  new.placed_at := old.placed_at;

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
      if v_above + v_below >= 999 then
        raise exception 'page_too_deep' using errcode = '23514';
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

-- When a page comes under shares its old parent was not under, the rows
-- under it and on it, which did not change, move their updated_at on, so
-- the pulls of those the move reached bring them. A move within the same
-- shares, or out of some, reaches no one new and leaves them. The tree
-- under a page is at most 1,000 pages deep (pages_guard).
create or replace function public.pages_touch_moved()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_pages uuid[];
begin
  if public.shared_chain(new.parent_id) <@ public.shared_chain(old.parent_id) then
    return null;
  end if;
  with recursive tree (id, depth) as (
    select p.id, 1 from public.pages p where p.parent_id = new.id
    union all
    select p.id, t.depth + 1
    from public.pages p join tree t on p.parent_id = t.id
    where t.depth < 1000
  )
  select coalesce(array_agg(t.id), '{}'::uuid[]) into v_pages from tree t;

  -- updated_at is the server's (set_updated_at): any update moves it on.
  update public.pages p set updated_at = now() where p.id = any(v_pages);
  v_pages := v_pages || new.id;
  update public.page_content c set updated_at = now() where c.page_id = any(v_pages);
  update public.comments c set updated_at = now() where c.page_id = any(v_pages);
  update public.files f set updated_at = now() where f.page_id = any(v_pages);
  update public.recordings r set updated_at = now() where r.page_id = any(v_pages);
  return null;
end;
$$;

-- A page in the table is seen by its own chain. Only a new one, not in
-- the table yet as its upsert is checked, needs its parent's: it was
-- placed in this transaction (pages_guard sets placed_at to now() as a
-- page is made or moved), which a row the caller merely reads was not.
drop policy if exists pages_select on public.pages;
create policy pages_select on public.pages for select to authenticated
  using (
    public.is_space_owner(space_id)
    or public.can_view_page(id)
    or (parent_id is not null and placed_at = now() and public.can_view_page(parent_id))
  );

-- ---------------------------------------------------------------------------
-- page_content: the revision moves on with the content, on its own page
-- ---------------------------------------------------------------------------

-- page_content's revision starts at 0 and moves on with every update that
-- changes the content or its text, whatever the writer sent, so a writer
-- that does not check it still makes the others retry. An update that
-- changes neither, as a move's touch of updated_at, keeps it: no one has
-- anything to merge.
create or replace function public.bump_revision()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if tg_op = 'INSERT' then
    new.revision := 0;
  elsif new.content is distinct from old.content or new.plain_text is distinct from old.plain_text then
    new.revision := old.revision + 1;
  else
    new.revision := old.revision;
  end if;
  return new;
end;
$$;

-- A page's content stays its page's. Writes with no caller (the service
-- role) keep what they set.
create or replace function public.keep_page_content_page()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if auth.uid() is not null and new.page_id is distinct from old.page_id then
    raise exception 'content_page_immutable' using errcode = '42501';
  end if;
  return new;
end;
$$;

revoke execute on function public.keep_page_content_page() from public, anon, authenticated, service_role;

drop trigger if exists page_content_keep_page on public.page_content;
create trigger page_content_keep_page before update on public.page_content
  for each row execute function public.keep_page_content_page();

-- ---------------------------------------------------------------------------
-- recordings: on the page, and under the id, they were made with
-- ---------------------------------------------------------------------------

-- A recording stays on the page it was made on, under the id it was made
-- with, which its audio path names (keep_recording_audio_path, which runs
-- after this, checks the path against it). Writes with no caller (the
-- service role) keep what they set.
create or replace function public.keep_recording_page()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if auth.uid() is not null then
    if new.id is distinct from old.id then
      raise exception 'recording_id_immutable' using errcode = '42501';
    end if;
    if new.page_id is distinct from old.page_id then
      raise exception 'recording_page_immutable' using errcode = '42501';
    end if;
  end if;
  return new;
end;
$$;

-- ---------------------------------------------------------------------------
-- comments: kept to a size, and their number, by anyone but the owner
-- ---------------------------------------------------------------------------

-- Rows past the bounds are brought within them, so the checks hold of
-- every row, and the updates that touch a row (as a move's) never fail on
-- one written before them. The apps write none such.
update public.comments set body = left(body, 100000) where char_length(body) > 100000;
update public.comments set quote = left(quote, 100000) where char_length(quote) > 100000;
update public.comments set block_id = left(block_id, 256) where char_length(block_id) > 256;
update public.comments set mentions = '[]'::jsonb
where jsonb_typeof(mentions) <> 'array' or octet_length(mentions::text) > 65536;

alter table public.comments
  drop constraint if exists comments_block_id_length,
  drop constraint if exists comments_body_length,
  drop constraint if exists comments_quote_length,
  drop constraint if exists comments_mentions_size,
  add constraint comments_block_id_length check (char_length(block_id) <= 256),
  add constraint comments_body_length check (char_length(body) <= 100000),
  add constraint comments_quote_length check (char_length(quote) <= 100000),
  add constraint comments_mentions_size check (jsonb_typeof(mentions) = 'array' and octet_length(mentions::text) <= 65536);

-- comments_guard counts a writer's comments on a page; the index on
-- author_id alone, for the foreign key, gives way to one that starts with
-- it.
create index if not exists comments_author_page_idx on public.comments (author_id, page_id);
drop index if exists public.comments_author_idx;

-- Editors may resolve or reopen anyone's thread, but only the author changes
-- what a comment says, and only the author or the space owner deletes it.
-- A comment keeps its id, page, block, thread and author. Guests, a public
-- link's visitors, comment through the page's owner only. Anyone but the
-- page's owner keeps to 2,000 comments on a page, of 10 MB in all, body,
-- quote and mentions (deleted ones count, emptied): a viewer cannot fill
-- the owner's project.
-- The owner's own, their guests' and their agents', are theirs to keep.
create or replace function public.comments_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_count bigint;
  v_bytes bigint;
begin
  if v_uid is null then
    return new;
  end if;
  if tg_op = 'INSERT' then
    new.author_id := v_uid;
    if new.author_kind = 'agent' then
      new.agent_name := coalesce(nullif(left(btrim(coalesce(new.agent_name, '')), 80), ''), 'Agent');
    elsif new.author_kind = 'guest' then
      if public.page_role(new.page_id) is distinct from 'owner' then
        raise exception 'guest_comment_not_owner' using errcode = '42501';
      end if;
      new.agent_name := coalesce(nullif(left(btrim(coalesce(new.agent_name, '')), 80), ''), 'Guest');
    else
      new.agent_name := null;
    end if;
    if new.parent_comment_id is not null and not exists (
      select 1 from public.comments c
      where c.id = new.parent_comment_id and c.page_id = new.page_id
    ) then
      raise exception 'parent_comment_on_other_page' using errcode = '23514';
    end if;
  else
    new.agent_name := old.agent_name;
    if (new.id, new.page_id, new.block_id, new.parent_comment_id, new.author_id, new.author_kind)
       is distinct from
       (old.id, old.page_id, old.block_id, old.parent_comment_id, old.author_id, old.author_kind) then
      raise exception 'comment_immutable' using errcode = '42501';
    end if;
    if old.author_id is distinct from v_uid then
      if (new.body, new.quote, new.mentions)
         is distinct from (old.body, old.quote, old.mentions) then
        raise exception 'not_comment_author' using errcode = '42501';
      end if;
      if new.deleted_at is distinct from old.deleted_at
         and public.page_role(new.page_id) is distinct from 'owner' then
        raise exception 'not_comment_author' using errcode = '42501';
      end if;
    end if;
  end if;

  if (tg_op = 'INSERT' or (new.body, new.quote, new.mentions) is distinct from (old.body, old.quote, old.mentions))
     and not exists (
       select 1 from public.pages p
       join public.spaces s on s.id = p.space_id
       where p.id = new.page_id and s.owner_id = v_uid
     ) then
    select count(*),
           coalesce(sum(octet_length(c.body) + coalesce(octet_length(c.quote), 0) + octet_length(c.mentions::text)), 0)
    into v_count, v_bytes
    from public.comments c
    where c.author_id = v_uid and c.page_id = new.page_id and c.id <> new.id;
    if tg_op = 'INSERT' and v_count >= 2000 then
      raise exception 'too_many_comments' using errcode = '23514';
    end if;
    if v_bytes + octet_length(new.body) + coalesce(octet_length(new.quote), 0) + octet_length(new.mentions::text)
       > 10485760 then
      raise exception 'comments_too_long' using errcode = '23514';
    end if;
  end if;
  return new;
end;
$$;

-- ---------------------------------------------------------------------------
-- model_providers: a key stays with the address it was saved for
-- ---------------------------------------------------------------------------

-- The origin of a base URL, scheme and host (and port, if not the
-- scheme's own), in lower case, as the Functions' normalizeBaseUrl and a
-- URL parser make it: https when it names no scheme. The host is read as
-- the parser reads it for http and https: tabs and line breaks dropped,
-- any slashes after the scheme passed, and the host up to the first
-- slash, question mark or hash, after the last @. Anything the parser
-- reads its own way (a backslash, which it takes for a slash, a space,
-- a character past ASCII, which its host mapping changes), or no host at
-- all, stands for itself: the whole URL is its origin, so only the very
-- same URL keeps a key. The providers function saves none such.
create or replace function public.base_url_origin(p_url text)
returns text
language plpgsql
immutable
set search_path = ''
as $$
declare
  v_url text := btrim(regexp_replace(coalesce(p_url, ''), '[\t\n\r]', '', 'g'));
  v_scheme text;
  v_host text;
begin
  if v_url !~* '^https?://' then
    v_url := 'https://' || v_url;
  end if;
  if v_url ~ '[^!-~]' or strpos(v_url, E'\\') > 0 then
    return v_url;
  end if;
  v_scheme := lower(substring(v_url from '^([A-Za-z]+):'));
  -- No user and password before the host, which origin leaves out.
  v_host := lower(regexp_replace(substring(v_url from '^[A-Za-z]+:/*([^/?#]*)'), '^.*@', ''));
  if v_host = '' then
    return v_url;
  end if;
  return v_scheme || '://' || regexp_replace(v_host, case v_scheme when 'https' then ':443$' else ':80$' end, '');
end;
$$;

revoke execute on function public.base_url_origin(text) from public, anon, authenticated, service_role;

-- A provider's keys are for the address and the preset's headers they were
-- saved with: when its base URL's origin, its preset or whether it is
-- local changes, they go, from the row and from Vault, unless the same
-- write gives it another. Whoever writes the row, the providers function
-- included: a key it should have asked for again, or a write it made
-- before failing, never reaches the new address. The providers function
-- saves a new key after the row, so a provider moved with a new key keeps
-- that one.
create or replace function public.model_providers_key_stays()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if (old.encrypted_secret is null and old.refresh_token is null)
     or (public.base_url_origin(new.base_url) is not distinct from public.base_url_origin(old.base_url)
       and new.preset is not distinct from old.preset
       and new.is_local is not distinct from old.is_local) then
    return new;
  end if;
  if new.encrypted_secret is not distinct from old.encrypted_secret then
    new.encrypted_secret := null;
  end if;
  if new.refresh_token is not distinct from old.refresh_token then
    new.refresh_token := null;
  end if;
  delete from vault.secrets s
  where s.id in (old.encrypted_secret, old.refresh_token)
    and s.id is distinct from new.encrypted_secret
    and s.id is distinct from new.refresh_token;
  return new;
end;
$$;

revoke execute on function public.model_providers_key_stays() from public, anon, authenticated, service_role;

drop trigger if exists model_providers_key_stays on public.model_providers;
create trigger model_providers_key_stays
  before update on public.model_providers
  for each row
  when (old.base_url is distinct from new.base_url
        or old.preset is distinct from new.preset
        or old.is_local is distinct from new.is_local)
  execute function public.model_providers_key_stays();

-- ---------------------------------------------------------------------------
-- devices: signed out for good
-- ---------------------------------------------------------------------------

-- The auth session the device writes its row with, which signing it out
-- ends.
alter table public.devices add column if not exists session_id uuid;

-- A client's write of a devices row, as a device's heartbeat upsert,
-- names the session it was made with, from the caller's token, whatever
-- it sent (a token that names none leaves the one the row has); it
-- neither sets nor clears revoked_at, which only sign_out_device sets.
-- A revocation is for the session it ended, which the row still names: a
-- device signed out by its token running out, before its heartbeat could
-- tell it, keeps its ID, and when its owner signs in on it again, a new
-- session, which only a real sign-in makes, writes the row; the
-- revocation is stale then, and goes. The revoked session's own writes,
-- with a token it had before, still find it revoked, and a row revoked
-- with no session named stays so. sign_out_device runs as its owner, as do
-- the service role's writes, which keep what they set: the role writing
-- tells them apart, where auth.uid() does not.
create or replace function public.devices_guard()
returns trigger
language plpgsql
set search_path = ''
as $$
declare
  v_claim text;
  v_session uuid;
begin
  if current_user is distinct from 'authenticated' then
    return new;
  end if;
  if (tg_op = 'INSERT' and new.revoked_at is not null)
     or (tg_op = 'UPDATE' and new.revoked_at is distinct from old.revoked_at) then
    raise exception 'forbidden_revoke' using errcode = '42501';
  end if;
  v_claim := auth.jwt() ->> 'session_id';
  if v_claim ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then
    v_session := v_claim::uuid;
  end if;
  if tg_op = 'INSERT' then
    new.session_id := v_session;
    return new;
  end if;
  if old.revoked_at is null then
    new.session_id := coalesce(v_session, old.session_id);
  elsif old.session_id is not null and v_session is not null and v_session is distinct from old.session_id then
    new.revoked_at := null;
    new.session_id := v_session;
  else
    -- Still revoked: the row keeps naming the session it was revoked for,
    -- or none.
    new.session_id := old.session_id;
  end if;
  return new;
end;
$$;

revoke execute on function public.devices_guard() from public, anon, authenticated, service_role;

drop trigger if exists devices_guard on public.devices;
create trigger devices_guard before insert or update on public.devices
  for each row execute function public.devices_guard();

-- Signs one of the caller's devices out: its row is revoked (it signs
-- itself out the next time it syncs with that session), and the auth
-- session it last wrote the row with ends, its refresh tokens with it,
-- so it can no longer renew its token. The row keeps naming that session,
-- which the revocation is for (devices_guard). 'device_not_found' when the
-- device is not the caller's.
create or replace function public.sign_out_device(p_device_id uuid)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_session uuid;
begin
  update public.devices d
  set revoked_at = coalesce(d.revoked_at, now())
  where d.id = p_device_id and d.user_id = v_uid
  returning d.session_id into v_session;
  if not found then
    raise exception 'device_not_found' using errcode = 'P0002';
  end if;
  if v_session is not null then
    delete from auth.sessions s where s.id = v_session and s.user_id = v_uid;
  end if;
end;
$$;

revoke execute on function public.sign_out_device(uuid) from public, anon, authenticated, service_role;
grant execute on function public.sign_out_device(uuid) to authenticated;

-- A device's row is never deleted by a client: the app deletes none, and
-- one deleted and made again would come back unrevoked.
revoke delete on public.devices from authenticated;
