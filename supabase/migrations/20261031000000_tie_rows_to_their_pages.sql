-- Four rules a second review found loose:
-- * A file row may sit in its writer's own space and name any page, since
--   the page_id foreign key looks past row-level security: anyone, even a
--   viewer, or someone a page is no longer shared with, could put a file on
--   it for its owner and viewers to read. A file is now on a page of its
--   own space, or on one its writer can edit when they put it there, as an
--   editor's upload to a page shared with them is; and its folder is one of
--   its space's.
-- * Anyone who could view a page could comment as a guest. Guests are the
--   visitors of a public link, which only the page's owner opens, and
--   whose device posts their comments; only the owner posts them now.
-- * pages.created_by was stamped on insert only, so an editor could change
--   who made a page. It now stays as it was made.
-- * Someone shared a page read its subpages' history from when they
--   accepted the share, even a page the owner moved under it later with
--   history from when it was private. pages.placed_at is when a page was
--   put under its parent, and the history of a page under a shared one
--   starts no earlier than when it, and each page between it and the
--   shared one, got there. Pages made before this keep the time it runs:
--   when they were moved is not known.
-- And recordings' audio_path gets the index that recordings_objects_shared_read
-- looks a recording up by.

-- ---------------------------------------------------------------------------
-- files: their page and folder
-- ---------------------------------------------------------------------------

-- A file's page is in its space, or one its writer can edit as they set it;
-- its folder is in its space. Rows set before this stay as they are until
-- their page or folder changes.
create or replace function public.files_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
begin
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

  return new;
end;
$$;

drop trigger if exists files_guard on public.files;
create trigger files_guard
  before insert or update on public.files
  for each row execute function public.files_guard();

-- ---------------------------------------------------------------------------
-- comments: guests through the owner only
-- ---------------------------------------------------------------------------

create or replace function public.comments_guard()
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
    return new;
  end if;
  new.agent_name := old.agent_name;
  if (new.page_id, new.block_id, new.parent_comment_id, new.author_id, new.author_kind)
     is distinct from
     (old.page_id, old.block_id, old.parent_comment_id, old.author_id, old.author_kind) then
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
  return new;
end;
$$;

-- ---------------------------------------------------------------------------
-- pages: who made it, and when it was put where it is
-- ---------------------------------------------------------------------------

alter table public.pages add column if not exists placed_at timestamptz not null default now();

-- Pages stay in their space; a subpage lives in its parent's space; no cycles;
-- moving a page under a new parent needs edit access to that parent (the
-- update policy already requires edit access to the page itself), and,
-- for anyone but the space's owner, the same shares on both parents.
-- created_by is who made the page, and placed_at when it was put under its
-- parent (made, if it never moved): the server sets both.
create or replace function public.pages_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_parent_space uuid;
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
    if v_uid is not null then
      new.created_by := v_uid;
    end if;
    -- A page made offline keeps its earlier time, as its snapshots do.
    new.placed_at := least(coalesce(new.created_at, now()), now());
    return new;
  end if;

  if new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;

  if v_uid is not null then
    new.created_by := old.created_by;
  end if;
  new.placed_at := old.placed_at;

  if new.parent_id is distinct from old.parent_id then
    if new.parent_id is not null and exists (
      with recursive anc as (
        select p.id, p.parent_id, 0 as depth from public.pages p where p.id = new.parent_id
        union all
        select p.id, p.parent_id, a.depth + 1
        from public.pages p join anc a on p.id = a.parent_id
        where a.depth < 64
      )
      select 1 from anc where anc.id = new.id
    ) then
      raise exception 'page_cycle' using errcode = '23514';
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

-- When the current user's history of a page starts: -infinity for the
-- owner, and for anyone else the earliest of their accepted shares on the
-- page's chain, each from when it was accepted or, if later, from when the
-- page came under the shared page (the latest placed_at of the pages below
-- it on the chain); null for someone it is not shared with.
create or replace function public.page_history_since(p_page_id uuid)
returns timestamptz
language sql
stable
security definer
set search_path = ''
as $$
  with recursive chain as (
    select p.id, p.parent_id, p.placed_at, 0 as depth,
           '-infinity'::timestamptz as under_since
    from public.pages p
    where p.id = p_page_id
    union all
    select p.id, p.parent_id, p.placed_at, c.depth + 1,
           greatest(c.under_since, c.placed_at)
    from public.pages p
    join chain c on p.id = c.parent_id
    where c.depth < 64
  )
  select case
    when (select auth.uid()) is null then null
    when public.page_role(p_page_id) = 'owner' then '-infinity'::timestamptz
    else (
      select min(greatest(pp.accepted_at, c.under_since))
      from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.user_id = (select auth.uid()) and pp.accepted_at is not null
    )
  end;
$$;

-- ---------------------------------------------------------------------------
-- recordings: the index recordings_objects_shared_read reads
-- ---------------------------------------------------------------------------

create index if not exists recordings_audio_path_idx on public.recordings (audio_path);
