-- * A template is the space owner's own: it lives in their space, out of
--   the page list, to start pages from. Anyone else with edit access to a
--   shared page could mark it a template, or a subpage of one, which took
--   it out of the owner's own page list and out of what the workspace
--   agent reads as shared. Only the space's owner now marks a page a
--   template or unmarks one (forbidden_template); the apps never do it on
--   a page they do not own.
-- * A page deleted before someone had it stays hidden from them, by its
--   deletion time against when their view began (can_read_page). A write
--   could date the deletion ahead of the server's clock, which no one
--   compares it with, and so keep deleted content readable to those who
--   came after: a deletion is now stamped no later than the server's
--   clock (pages_guard clamps deleted_at to now()). And a page that was
--   under a deleted one, not deleted itself, could be moved out from under
--   it by an editor who never had it, which brought its content into
--   their view: a move by anyone but the space's owner now needs the page
--   readable where it was (forbidden_move).
-- * A deleted file's row kept its name, type, size and path, which names
--   it too, for anyone the page was shared with later to read; its object
--   was read only by those who may delete it (files_objects_shared_read).
--   The row stays readable, so the deletion reaches the devices that have
--   the file, but now keeps none of what it was, as a deleted comment
--   keeps none of what it said (scrub_deleted_file), and the ones deleted
--   already are emptied here. Its object is found by the file's folder in
--   the bucket ({space}/{file}/), for those who may still read or remove
--   it, rather than by the path it no longer names.
-- * The server posts an agent's reply as the person who asked, with the
--   service role, past comments_guard's bounds: a page's viewer could have
--   the agent write any number of replies of any size on the owner's page.
--   The guard now holds the server's writes to the same bounds, by the
--   row's author, clamps an agent's name to 80 characters and checks a
--   reply's thread is on the page. A comment keeps its created_at, which
--   ordered a thread, on update.
-- * Rows a member writes of their own had no bound: a page's title, a
--   device's name and how many devices, an AI job's request and result,
--   and a speech-to-text key for any provider name. A title is at most
--   2,000 characters, a device's name 256 and its platform 64, a user has
--   at most 100 devices signed in (revoked ones, which stay, do not count),
--   an AI job's request is at most 1 MB and its result 16 MB as either is
--   written (ai_jobs_in_bounds: rows already past them still change
--   otherwise), and a speech-to-text key is saved only for Soniox, the one
--   speech-to-text key that has no model provider.
-- * The workspace agent leaves as it is a page whose title someone else
--   reads, as its card is on a page they read; a page they read also by an
--   inline mention of it in such a page's text (agent_card_pages).
-- * agent_search_pages cut a match's words from a page's plain_text, which
--   holds what its sites wrote too: a link card's title and description
--   and a video's title. They are the web's writing, which the agent reads
--   as the person's. The words are now cut from the page's own text
--   (page_own_text), as the device cuts them; a page found only by what its
--   sites wrote is still found, in its place, with no words.
-- * A device signing in adopts what the server has of a page, folder,
--   comment, file or recording over its own copy by which changed later,
--   and could tell only by updated_at, which is when the server got the
--   write: a copy uploaded late looked newer than a change made after it.
--   Each now keeps changed_at, when it was changed where it was changed:
--   the time a client sends, no later than the server's clock, or the
--   server's when it sends none or the same as before. A write another
--   trigger makes, as the touch of a subtree a move brings under a share,
--   or a foreign key's set null as an account goes, is the server's and
--   keeps it. It orders adoption and nothing else; clients read and write
--   it as they do the row.

-- ---------------------------------------------------------------------------
-- pages: titles in bounds
-- ---------------------------------------------------------------------------

update public.pages set title = left(title, 2000) where char_length(title) > 2000;
alter table public.pages drop constraint if exists pages_title_length;
alter table public.pages add constraint pages_title_length check (char_length(title) <= 2000);

-- ---------------------------------------------------------------------------
-- pages: templates are the owner's, deletions no later than the server's
-- clock, and a page kept from a move that would bring it into view
-- ---------------------------------------------------------------------------

-- Pages stay in their space, with their id; a subpage lives in its
-- parent's space; no cycles, and no tree deeper than 1,000 pages (a page
-- and those above it), as a page is made under a parent or moved; moving
-- a page under a new parent needs edit access to that parent (the update
-- policy already requires edit access to the page itself), and, for
-- anyone but the space's owner, the same shares on both parents and the
-- page readable where it was. Only the space's owner marks a page a
-- template or puts a page in a folder. created_by is who made the page,
-- and placed_at when it reached the server or was last put under a parent:
-- the server sets both, and a deletion is stamped no later than its clock.
-- A deleted page is restored, or its deletion moved, only by someone who
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
-- files: a deleted file keeps none of what it was
-- ---------------------------------------------------------------------------

-- A deleted file's row says only that it was deleted: its name, type and
-- size go, and its path, which holds its name. The devices that had it
-- remove their copies by its id, and the device that deleted it removes
-- its object by the path it queued; nothing brings a deleted file back.
-- Runs after files_guard (triggers of a kind run in name order), which
-- compares a writer's row with the row as it was.
create or replace function public.scrub_deleted_file()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if new.deleted_at is not null then
    new.name := '';
    new.mime_type := null;
    new.size_bytes := null;
    new.storage_path := null;
  end if;
  return new;
end;
$$;

revoke execute on function public.scrub_deleted_file() from public, anon, authenticated, service_role;

drop trigger if exists files_scrub_deleted on public.files;
create trigger files_scrub_deleted before insert or update on public.files
  for each row execute function public.scrub_deleted_file();

-- The files deleted already. Their triggers stay out of this one update:
-- updated_at stays, and the devices that had them have their deletion.
alter table public.files disable trigger user;
update public.files set name = '', mime_type = null, size_bytes = null, storage_path = null
where deleted_at is not null
  and (name <> '' or mime_type is not null or size_bytes is not null or storage_path is not null);
alter table public.files enable trigger user;

-- A live file names its object; a deleted one, which no longer does, has
-- what is left in its folder, {space}/{file}/, for its page's owner to
-- read and remove, as the device that deleted it does.
drop policy if exists files_objects_shared_read on storage.objects;
create policy files_objects_shared_read on storage.objects for select to authenticated
  using (
    bucket_id = 'files'
    and exists (
      select 1 from public.files f
      where split_part(storage.objects.name, '/', 1) = f.space_id::text
        and split_part(storage.objects.name, '/', 2) = f.id::text
        and (f.storage_path = storage.objects.name or f.deleted_at is not null)
        and f.page_id is not null
        and ((f.deleted_at is null and public.can_read_page(f.page_id))
          or public.page_role(f.page_id) = 'owner')
    )
  );

drop policy if exists files_objects_page_owner_delete on storage.objects;
create policy files_objects_page_owner_delete on storage.objects for delete to authenticated
  using (
    bucket_id = 'files'
    and exists (
      select 1 from public.files f
      where split_part(storage.objects.name, '/', 1) = f.space_id::text
        and split_part(storage.objects.name, '/', 2) = f.id::text
        and (f.storage_path = storage.objects.name or f.deleted_at is not null)
        and f.page_id is not null and public.page_role(f.page_id) = 'owner'
    )
  );

-- ---------------------------------------------------------------------------
-- comments: the server's writes in bounds too, a name clamped, created_at
-- kept
-- ---------------------------------------------------------------------------

-- Editors may resolve or reopen anyone's thread, but only the author
-- changes what a comment says, and only the author or the space owner
-- deletes it. A comment keeps its id, page, block, thread, author and
-- created_at. Guests, a public link's visitors, comment through the
-- page's owner only; an agent's name is at most 80 characters. Anyone but
-- the page's owner keeps to 2,000 comments on a page, of 10 MB in all,
-- body, quote and mentions (deleted ones count, emptied): neither a viewer
-- writing them, nor the server posting an agent's reply as one, fills the
-- owner's project. The owner's own, their guests' and their agents', are
-- theirs to keep.
create or replace function public.comments_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_author uuid;
  v_count bigint;
  v_bytes bigint;
begin
  if tg_op = 'INSERT' then
    if v_uid is not null then
      new.author_id := v_uid;
    end if;
    if new.author_kind = 'agent' then
      new.agent_name := coalesce(nullif(left(btrim(coalesce(new.agent_name, '')), 80), ''), 'Agent');
    elsif new.author_kind = 'guest' then
      if v_uid is not null and public.page_role(new.page_id) is distinct from 'owner' then
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
  elsif v_uid is not null then
    new.agent_name := old.agent_name;
    if (new.id, new.page_id, new.block_id, new.parent_comment_id, new.author_id, new.author_kind, new.created_at)
       is distinct from
       (old.id, old.page_id, old.block_id, old.parent_comment_id, old.author_id, old.author_kind, old.created_at) then
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
  else
    new.agent_name := old.agent_name;
  end if;

  -- Whose comments count toward a page's bounds: a member, by their token,
  -- and the server posting a reply as them, by the row's author and the
  -- service role's claim. The page's owner fills no bound on their own
  -- page. Writes with no such caller, as the migrations' own, are left out:
  -- they bring rows within bounds themselves.
  v_author := case
    when v_uid is not null then v_uid
    when (auth.jwt() ->> 'role') = 'service_role' then new.author_id
  end;
  if v_author is not null
     and (tg_op = 'INSERT' or (new.body, new.quote, new.mentions) is distinct from (old.body, old.quote, old.mentions))
     and not exists (
       select 1 from public.pages p
       join public.spaces s on s.id = p.space_id
       where p.id = new.page_id and s.owner_id = v_author
     ) then
    select count(*),
           coalesce(sum(octet_length(c.body) + coalesce(octet_length(c.quote), 0) + octet_length(c.mentions::text)), 0)
    into v_count, v_bytes
    from public.comments c
    where c.author_id = v_author and c.page_id = new.page_id and c.id <> new.id;
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
-- devices: a name and platform in bounds, and no more than 100 a user
-- ---------------------------------------------------------------------------

update public.devices set name = left(name, 256) where char_length(name) > 256;
update public.devices set platform = left(platform, 64) where char_length(platform) > 64;
alter table public.devices drop constraint if exists devices_name_length;
alter table public.devices drop constraint if exists devices_platform_length;
alter table public.devices add constraint devices_name_length check (char_length(name) <= 256);
alter table public.devices add constraint devices_platform_length check (char_length(coalesce(platform, '')) <= 64);

-- Every write of a device's row names the session it was made with,
-- whatever the client sent; no client sets or clears revoked_at, or
-- deletes a row. A user has at most 100 devices signed in: a revoked one,
-- whose row stays, does not count, and a device's own upsert, from an id
-- it already has, is not one more.
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
    if (select count(*) from public.devices d
        where d.user_id = new.user_id and d.id <> new.id and d.revoked_at is null) >= 100 then
      raise exception 'too_many_devices' using errcode = '23514';
    end if;
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

-- ---------------------------------------------------------------------------
-- ai_jobs: a request and result in bounds
-- ---------------------------------------------------------------------------

-- A job's request is at most 1 MB and its result 16 MB, as each is written:
-- jsonb the client and the server write, which the rows there are not
-- brought within, so a row already past them still changes otherwise, as
-- the function marks a run that stopped failed. The apps and the function
-- write far less.
alter table public.ai_jobs drop constraint if exists ai_jobs_request_size;
alter table public.ai_jobs drop constraint if exists ai_jobs_result_size;

create or replace function public.ai_jobs_in_bounds()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if (tg_op = 'INSERT' or new.request is distinct from old.request)
     and octet_length(new.request::text) > 1048576 then
    raise exception 'ai_job_request_too_long' using errcode = '23514';
  end if;
  if new.result is not null
     and (tg_op = 'INSERT' or new.result is distinct from old.result)
     and octet_length(new.result::text) > 16777216 then
    raise exception 'ai_job_result_too_long' using errcode = '23514';
  end if;
  return new;
end;
$$;

revoke execute on function public.ai_jobs_in_bounds() from public, anon, authenticated, service_role;

-- Only a write that names request or result is checked: one of the status
-- alone reads neither.
drop trigger if exists ai_jobs_in_bounds on public.ai_jobs;
create trigger ai_jobs_in_bounds before insert or update of request, result on public.ai_jobs
  for each row execute function public.ai_jobs_in_bounds();

-- ---------------------------------------------------------------------------
-- set_stt_secret: only Soniox's key
-- ---------------------------------------------------------------------------

-- The only speech-to-text key that has no model provider is Soniox's: a
-- key is saved for it alone, so no caller fills Vault with a key for any
-- name.
create or replace function public.set_stt_secret(p_provider text, p_secret text)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_secret_id uuid;
begin
  if v_uid is null then
    raise exception 'not_signed_in' using errcode = '42501';
  end if;
  if p_provider is distinct from 'soniox' then
    raise exception 'unknown_stt_provider' using errcode = '22023';
  end if;
  select s.secret_id into v_secret_id
  from public.stt_secrets s
  where s.user_id = v_uid and s.provider = p_provider;

  if p_secret is null or p_secret = '' then
    delete from public.stt_secrets s where s.user_id = v_uid and s.provider = p_provider;
    return;
  end if;

  if v_secret_id is null then
    v_secret_id := vault.create_secret(
      p_secret,
      'stt:' || p_provider || ':' || v_uid::text,
      'Speech-to-text key for a Leaflet user'
    );
    insert into public.stt_secrets (user_id, provider, secret_id)
    values (v_uid, p_provider, v_secret_id);
  else
    perform vault.update_secret(v_secret_id, p_secret);
  end if;
end;
$$;

revoke execute on function public.set_stt_secret(text, text) from public, anon, service_role;
grant execute on function public.set_stt_secret(text, text) to authenticated;

-- ---------------------------------------------------------------------------
-- agent_card_pages: a title read through an inline mention too
-- ---------------------------------------------------------------------------

-- The workspace agent leaves as it is a page whose title someone else
-- reads, as its card is on a page they read: of p_pages, the pages of the
-- caller's own space others read, the pages their cards name, and the
-- pages an inline mention names in their text, the kind the apps keep with
-- the page's title. Found without the pages' content, which can be long,
-- leaving the database. Under the caller's row-level security, as their
-- own reads are.
create or replace function public.agent_card_pages(p_pages uuid[])
returns table (page_id text)
language sql
stable
set search_path = ''
as $$
  with mine as (
    select c.content
    from public.page_content c
    join public.pages p on p.id = c.page_id
    join public.spaces s on s.id = p.space_id and s.owner_id = (select auth.uid())
    where c.page_id = any(p_pages)
  )
  select distinct card #>> '{}'
  from mine
  cross join lateral jsonb_path_query(mine.content, '$.** ? (@.type == "subpage").data.page_id') card
  where jsonb_typeof(card) = 'string'
  union
  select distinct ref #>> '{}'
  from mine
  cross join lateral jsonb_path_query(mine.content, '$.** ? (@.mention.kind == "page").mention.id') ref
  where jsonb_typeof(ref) = 'string'
$$;

revoke execute on function public.agent_card_pages(uuid[]) from public, anon, service_role;
grant execute on function public.agent_card_pages(uuid[]) to authenticated;

-- ---------------------------------------------------------------------------
-- agent_search_pages: words cut from the page's own text
-- ---------------------------------------------------------------------------

-- A page's own text: plain_text as the apps make it (a line per block that
-- has any, in document order, a block's text without formats, or for a
-- block without text the title or source it shows), without what its
-- sites wrote of its blocks: a link card's title and description, and a
-- video's title. A card's link stays, as its text, and a video's notes,
-- which are the person's.
create or replace function public.page_own_text(p_content jsonb)
returns text
language sql
immutable
set search_path = ''
as $$
  with recursive walk (node, path) as (
    select e.node, array[e.n]
    from jsonb_array_elements(
      case when jsonb_typeof(p_content -> 'document' -> 'children') = 'array'
        then p_content -> 'document' -> 'children' else '[]'::jsonb end
    ) with ordinality e (node, n)
    union all
    select e.node, w.path || e.n
    from walk w
    cross join lateral jsonb_array_elements(
      case when jsonb_typeof(w.node -> 'children') = 'array' then w.node -> 'children' else '[]'::jsonb end
    ) with ordinality e (node, n)
  ),
  blocks as (
    select w.path,
      case w.node ->> 'type'
        when 'chart' then coalesce(d.title, '')
        when 'embed' then coalesce(d.title, '')
        when 'video' then regexp_replace(coalesce(d.notes, ''), '^[[:space:]]+|[[:space:]]+$', '', 'g')
        when 'link_preview' then coalesce(
          nullif(regexp_replace(coalesce(d.name, ''), '^[[:space:]]+|[[:space:]]+$', '', 'g'), ''),
          nullif(regexp_replace(coalesce(d.url, ''), '^[[:space:]]+|[[:space:]]+$', '', 'g'), ''), '')
        when 'file' then coalesce(
          nullif(regexp_replace(coalesce(d.title, ''), '^[[:space:]]+|[[:space:]]+$', '', 'g'), ''),
          nullif(regexp_replace(coalesce(d.name, ''), '^[[:space:]]+|[[:space:]]+$', '', 'g'), ''),
          nullif(regexp_replace(coalesce(d.url, ''), '^[[:space:]]+|[[:space:]]+$', '', 'g'), ''), '')
        when 'math_equation' then coalesce(d.formula, '')
        when 'diagram' then coalesce(d.source, '')
        else (
          select string_agg(r.op ->> 'insert', '' order by r.n)
          from jsonb_array_elements(
            case when jsonb_typeof(w.node -> 'data' -> 'delta') = 'array'
              then w.node -> 'data' -> 'delta' else '[]'::jsonb end
          ) with ordinality r (op, n)
          where jsonb_typeof(r.op -> 'insert') = 'string'
        )
      end as line
    from walk w
    cross join lateral (
      select
        case when jsonb_typeof(w.node -> 'data' -> 'title') = 'string' then w.node -> 'data' ->> 'title' end as title,
        case when jsonb_typeof(w.node -> 'data' -> 'notes') = 'string' then w.node -> 'data' ->> 'notes' end as notes,
        case when jsonb_typeof(w.node -> 'data' -> 'name') = 'string' then w.node -> 'data' ->> 'name' end as name,
        case when jsonb_typeof(w.node -> 'data' -> 'url') = 'string' then w.node -> 'data' ->> 'url' end as url,
        case when jsonb_typeof(w.node -> 'data' -> 'formula') = 'string' then w.node -> 'data' ->> 'formula' end as formula,
        case when jsonb_typeof(w.node -> 'data' -> 'source') = 'string' then w.node -> 'data' ->> 'source' end as source
    ) d
  )
  select coalesce(string_agg(b.line, E'\n' order by b.path), '')
  from blocks b
  where b.line ~ '[^[:space:]]'
$$;

revoke execute on function public.page_own_text(jsonb) from public, anon, authenticated, service_role;
grant execute on function public.page_own_text(jsonb) to authenticated;

-- The workspace agent finds the caller's pages as the app's agents search
-- them (store.SearchPages): those of their own space, neither deleted nor
-- templates, with every one of p_tags, in any case, whose title, a tag or
-- text holds p_text, in any case; with no p_text, the most recently
-- changed. Titles and tags first, then the pages' text the most recently
-- changed page first, and only until it has found enough, rather than
-- every page's whole text for every search. Text is matched under the
-- caller's row-level security, whose rules no index of it could be read
-- past, so reading less is what makes a search quick. A match's words
-- around it are cut from the page's own text (page_own_text), from 40
-- characters before the match, as many as p_text has and 80 more, and
-- only where its own text holds p_text: a page found by what its sites
-- wrote alone is found, with no words. They are cut from where the match
-- is in the text itself: lowering text can change its length, as 'İ'
-- becomes two letters, so where it is in the text lowered is not where it
-- is in the text.
create or replace function public.agent_search_pages(p_text text, p_tags text[], p_limit integer)
returns table (page_id uuid, title text, snippet text)
language sql stable security invoker set search_path = ''
as $$
  with mine as materialized (
    select p.id, p.title, p.tags, p.updated_at
    from public.pages p
    join public.spaces s on s.id = p.space_id and s.owner_id = (select auth.uid())
    where p.deleted_at is null and not p.is_template
      and not exists (
        select 1 from unnest(coalesce(p_tags, '{}')) want
        where not exists (select 1 from unnest(p.tags) t where lower(t) = lower(want))
      )
  ),
  wanted as (select least(greatest(coalesce(p_limit, 20), 1), 50) as n),
  named as (
    select m.id, m.title, m.updated_at
    from mine m
    where p_text = ''
      or position(lower(p_text) in lower(m.title)) > 0
      or exists (select 1 from unnest(m.tags) t where position(lower(p_text) in lower(t)) > 0)
    order by m.updated_at desc, m.id
    limit (select n from wanted)
  ),
  -- Read a page at a time, the most recently changed first: the limit
  -- ends the reading once it is reached. Only while titles and tags have
  -- not filled it, and of pages they did not find.
  texts as (
    select m.id, m.title, m.updated_at, c.content
    from (
      select * from mine
      where p_text <> ''
        and (select count(*) from named) < (select n from wanted)
        and id not in (select id from named)
      order by updated_at desc, id
      offset 0
    ) m
    cross join lateral (
      select c.content from public.page_content c
      where c.page_id = m.id and position(lower(p_text) in lower(c.plain_text)) > 0
      limit 1
    ) c
    limit (select n from wanted) - (select count(*) from named)
  )
  select h.id, h.title, h.snippet from (
    select n.id, n.title, null::text as snippet, 0 as rank, n.updated_at from named n
    union all
    select t.id, t.title,
      case when position(lower(p_text) in lower(o.own)) > 0 then
        substring(o.own from greatest(coalesce(length(substring(o.own from
          '(?i)^(.*?)' || regexp_replace(p_text, '([^[:alnum:][:space:]])', '\\\1', 'g'))), 0) + 1 - 40, 1)
          for length(p_text) + 80)
      end,
      1, t.updated_at
    from texts t
    cross join lateral (select public.page_own_text(t.content) as own) o
  ) h
  order by h.rank, h.updated_at desc, h.id
$$;

revoke execute on function public.agent_search_pages(text, text[], integer) from public, anon, service_role;
grant execute on function public.agent_search_pages(text, text[], integer) to authenticated;

-- ---------------------------------------------------------------------------
-- changed_at: when a row was changed where it was changed
-- ---------------------------------------------------------------------------

alter table public.pages add column if not exists changed_at timestamptz;
alter table public.folders add column if not exists changed_at timestamptz;
alter table public.comments add column if not exists changed_at timestamptz;
alter table public.files add column if not exists changed_at timestamptz;
alter table public.recordings add column if not exists changed_at timestamptz;

-- The rows there were last changed no later than the server last wrote
-- them. Their triggers stay out of this one update, so updated_at stays as
-- it was and no device pulls them again.
alter table public.pages disable trigger user;
update public.pages set changed_at = updated_at where changed_at is null;
alter table public.pages enable trigger user;
alter table public.folders disable trigger user;
update public.folders set changed_at = updated_at where changed_at is null;
alter table public.folders enable trigger user;
alter table public.comments disable trigger user;
update public.comments set changed_at = updated_at where changed_at is null;
alter table public.comments enable trigger user;
alter table public.files disable trigger user;
update public.files set changed_at = updated_at where changed_at is null;
alter table public.files enable trigger user;
alter table public.recordings disable trigger user;
update public.recordings set changed_at = updated_at where changed_at is null;
alter table public.recordings enable trigger user;

alter table public.pages alter column changed_at set default now(), alter column changed_at set not null;
alter table public.folders alter column changed_at set default now(), alter column changed_at set not null;
alter table public.comments alter column changed_at set default now(), alter column changed_at set not null;
alter table public.files alter column changed_at set default now(), alter column changed_at set not null;
alter table public.recordings alter column changed_at set default now(), alter column changed_at set not null;

-- A row's changed_at is the time its writer sends, no later than the
-- server's clock: a new row's, or one other than the row had. A write
-- that sends none, or the same, is a change now, as updated_at moves with
-- it. A write another trigger makes is the server's, no change where the
-- row was changed, and keeps it: the touch of a subtree a move brings
-- under a share, so that its readers pull it (pages_touch_moved), and a
-- foreign key's set null, as an account or a page goes. It gives no one
-- any right: it only orders a device's adoption of the server's rows over
-- its own copies.
create or replace function public.set_changed_at()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  if tg_op = 'INSERT' then
    new.changed_at := least(coalesce(new.changed_at, now()), now());
  elsif pg_trigger_depth() > 1 then
    new.changed_at := old.changed_at;
  elsif new.changed_at is not null and new.changed_at is distinct from old.changed_at then
    new.changed_at := least(new.changed_at, now());
  else
    new.changed_at := now();
  end if;
  return new;
end;
$$;

revoke execute on function public.set_changed_at() from public, anon, authenticated, service_role;

drop trigger if exists folders_set_changed_at on public.folders;
create trigger folders_set_changed_at before insert or update on public.folders
  for each row execute function public.set_changed_at();
drop trigger if exists comments_set_changed_at on public.comments;
create trigger comments_set_changed_at before insert or update on public.comments
  for each row execute function public.set_changed_at();
drop trigger if exists files_set_changed_at on public.files;
create trigger files_set_changed_at before insert or update on public.files
  for each row execute function public.set_changed_at();
drop trigger if exists recordings_set_changed_at on public.recordings;
create trigger recordings_set_changed_at before insert or update on public.recordings
  for each row execute function public.set_changed_at();
-- A page's moves with updated_at: a lease of its public link alone is no
-- change to it (pages_set_updated_at_on_update).
drop trigger if exists pages_set_changed_at on public.pages;
create trigger pages_set_changed_at before insert on public.pages
  for each row execute function public.set_changed_at();
drop trigger if exists pages_set_changed_at_on_update on public.pages;
create trigger pages_set_changed_at_on_update before update on public.pages
  for each row
  when (new.public_link_until is not distinct from old.public_link_until
    or (to_jsonb(new) - 'public_link_until') is distinct from (to_jsonb(old) - 'public_link_until'))
  execute function public.set_changed_at();

-- A file's page is in its space, or one its writer can edit as they set it;
-- its folder is in its space; and anyone but its space's owner (the owner
-- of the page it is on) only deletes it, with the changed_at the device
-- sends, which orders adoption and is no change to the file. Rows set
-- before 20261031000000 stay as they are until their page or folder
-- changes. created_by is who added it. Its page_id going to null once that
-- page is gone is the page's foreign key (on delete set null), which runs
-- as whoever deleted the page, not a change to the file.
create or replace function public.files_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_new jsonb;
  v_old jsonb;
begin
  -- Writes with no caller (the service role, and created_by's own
  -- on delete set null when an account is deleted) keep what they set.
  if v_uid is not null then
    if tg_op = 'INSERT' then
      new.created_by := v_uid;
    else
      new.created_by := old.created_by;
    end if;
  end if;

  if new.page_id is not null
     and (tg_op = 'INSERT' or new.page_id is distinct from old.page_id)
     and not exists (
       select 1 from public.pages p where p.id = new.page_id and p.space_id = new.space_id
     )
     and v_uid is not null
     and not public.can_edit_page(new.page_id)
     -- An upsert of a file row the server has tries the insert first: a
     -- row already on that page goes on to the update, which RLS checks.
     and not (tg_op = 'INSERT' and exists (
       select 1 from public.files f where f.id = new.id and f.page_id = new.page_id
     )) then
    raise exception 'forbidden_file_page' using errcode = '42501';
  end if;

  if new.folder_id is not null
     and (tg_op = 'INSERT' or new.folder_id is distinct from old.folder_id)
     and not exists (
       select 1 from public.folders f where f.id = new.folder_id and f.space_id = new.space_id
     ) then
    raise exception 'folder_in_other_space' using errcode = '23514';
  end if;

  if tg_op = 'UPDATE' and v_uid is not null and not public.is_space_owner(old.space_id) then
    v_new := to_jsonb(new) - 'deleted_at' - 'updated_at' - 'changed_at';
    v_old := to_jsonb(old) - 'deleted_at' - 'updated_at' - 'changed_at';
    -- A deleted file's row keeps none of what it was (scrub_deleted_file),
    -- which a device that deleted it sends again as it had it.
    if old.deleted_at is not null then
      v_new := v_new - 'name' - 'mime_type' - 'size_bytes' - 'storage_path';
      v_old := v_old - 'name' - 'mime_type' - 'size_bytes' - 'storage_path';
    end if;
    -- The deleted page is out of sight by now: the delete ran before its
    -- foreign key's action, in the same transaction.
    if new.page_id is null and old.page_id is not null
       and not exists (select 1 from public.pages p where p.id = old.page_id) then
      v_new := v_new - 'page_id';
      v_old := v_old - 'page_id';
    end if;
    if v_new is distinct from v_old
       or (new.deleted_at is distinct from old.deleted_at
           and (old.deleted_at is not null or new.deleted_at is null)) then
      raise exception 'forbidden_file_change' using errcode = '42501';
    end if;
  end if;

  return new;
end;
$$;
