-- Four rules a fourth review found loose:
-- * A comment's author could delete it outright (comments_delete), and its
--   replies, everyone's, went with it (parent_comment_id cascades), past
--   comments_guard, which keeps others' comments from all but the page's
--   owner. The app only ever deletes a comment by setting deleted_at, so
--   the deletion reaches the other devices: no caller deletes rows now.
-- * An editor's upload to a page shared with them is in their own space,
--   and stayed theirs to write once the page was no longer shared with
--   them: they could change the bytes, name or path that the page's
--   people still read, and the page's owner could not take it off the
--   page. Now a file row on a page, and its object, are written only by
--   someone who can edit that page, a recording's audio only by its
--   recorder while they can edit its page, and the page's owner may delete
--   a file on it (its deleted_at, and then its object).
-- * Moving a page under a shared one changed only the moved page's row, so
--   the pulls of those it now reached (updated_at past their cursor)
--   passed over the pages under it and what they hold. A move that brings
--   a page to more shares now moves the updated_at of the pages under it,
--   and of the content, comments, files and recordings of all of them, on.
-- * shares_page_with counted two people as sharing a page when they were
--   shared any pages of the same space, so those shared unrelated pages
--   read each other's profile. Now one's share must reach the other's: the
--   same page, or one inside the other.

-- ---------------------------------------------------------------------------
-- comments: deleted by deleted_at only
-- ---------------------------------------------------------------------------

drop policy if exists comments_delete on public.comments;
revoke delete on public.comments from anon, authenticated;

-- ---------------------------------------------------------------------------
-- files: written by who can edit their page
-- ---------------------------------------------------------------------------

-- The space's owner reads and adds their rows as before; they change or
-- delete one on a page only while they can edit that page. The page's
-- owner may delete one on their page, as files_guard keeps it: only
-- deleted_at, from unset to set.
drop policy if exists files_all on public.files;
drop policy if exists files_select_own on public.files;
drop policy if exists files_insert on public.files;
drop policy if exists files_update on public.files;
drop policy if exists files_delete on public.files;

create policy files_select_own on public.files for select to authenticated
  using (public.is_space_owner(space_id));
create policy files_insert on public.files for insert to authenticated
  with check (public.is_space_owner(space_id));
create policy files_update on public.files for update to authenticated
  using (
    (public.is_space_owner(space_id) and (page_id is null or public.can_edit_page(page_id)))
    or (page_id is not null and public.page_role(page_id) = 'owner')
  )
  with check (
    public.is_space_owner(space_id)
    or (page_id is not null and public.page_role(page_id) = 'owner')
  );
create policy files_delete on public.files for delete to authenticated
  using (
    (public.is_space_owner(space_id) and (page_id is null or public.can_edit_page(page_id)))
    or (page_id is not null and public.page_role(page_id) = 'owner')
  );

-- A file's page is in its space, or one its writer can edit as they set it;
-- its folder is in its space; and anyone but its space's owner (the owner
-- of the page it is on) only deletes it. Rows set before 20261031000000
-- stay as they are until their page or folder changes.
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

  if tg_op = 'UPDATE' and v_uid is not null and not public.is_space_owner(old.space_id)
     and (
       (to_jsonb(new) - 'deleted_at' - 'updated_at') is distinct from (to_jsonb(old) - 'deleted_at' - 'updated_at')
       or (new.deleted_at is distinct from old.deleted_at
           and (old.deleted_at is not null or new.deleted_at is null))
     ) then
    raise exception 'forbidden_file_change' using errcode = '42501';
  end if;

  return new;
end;
$$;

-- files/{space_id}/{file_id}/{name}: the space's owner writes an object
-- whose row is on no page, or on one they can edit, or that has no row
-- yet (no one else reads it then). They still read and delete anything in
-- their space.
create or replace function public.can_write_file_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_id text := split_part(p_name, '/', 2);
begin
  if not public.owns_space_path(p_name) then
    return false;
  end if;
  if v_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then
    return true;
  end if;
  return not exists (
    select 1 from public.files f
    where f.id = v_id::uuid
      and f.space_id = split_part(p_name, '/', 1)::uuid
      and f.page_id is not null
      and not public.can_edit_page(f.page_id)
  );
end;
$$;

drop policy if exists files_objects_owner on storage.objects;
drop policy if exists files_objects_owner_read on storage.objects;
drop policy if exists files_objects_owner_delete on storage.objects;
drop policy if exists files_objects_owner_insert on storage.objects;
drop policy if exists files_objects_owner_update on storage.objects;
drop policy if exists files_objects_page_owner_delete on storage.objects;

create policy files_objects_owner_read on storage.objects for select to authenticated
  using (bucket_id = 'files' and public.owns_space_path(name));
create policy files_objects_owner_delete on storage.objects for delete to authenticated
  using (bucket_id = 'files' and public.owns_space_path(name));
create policy files_objects_owner_insert on storage.objects for insert to authenticated
  with check (bucket_id = 'files' and public.can_write_file_path(name));
create policy files_objects_owner_update on storage.objects for update to authenticated
  using (bucket_id = 'files' and public.can_write_file_path(name))
  with check (bucket_id = 'files' and public.can_write_file_path(name));

-- The page's owner removes the object of a file on their page, as the
-- device that deleted it does once the row is up.
create policy files_objects_page_owner_delete on storage.objects for delete to authenticated
  using (
    bucket_id = 'files'
    and exists (
      select 1 from public.files f
      where f.storage_path = storage.objects.name
        and split_part(storage.objects.name, '/', 1) = f.space_id::text
        and split_part(storage.objects.name, '/', 2) = f.id::text
        and f.page_id is not null and public.page_role(f.page_id) = 'owner'
    )
  );

-- ---------------------------------------------------------------------------
-- recordings: audio written while its page is editable
-- ---------------------------------------------------------------------------

-- recordings/{user_id}/{recording_id}.{ext}: the caller writes only an
-- object for a recording row they made, on a page they can still edit, so
-- the bucket holds no audio without a row. They still read and delete
-- anything in their folder.
create or replace function public.owns_recording_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_id text := split_part(split_part(p_name, '/', 2), '.', 1);
begin
  if v_uid is null
     or split_part(p_name, '/', 1) <> v_uid::text
     or split_part(p_name, '/', 3) <> ''
     or v_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then
    return false;
  end if;
  return exists (
    select 1 from public.recordings r
    where r.id = v_id::uuid and r.created_by = v_uid and public.can_edit_page(r.page_id)
  );
end;
$$;

-- ---------------------------------------------------------------------------
-- pages: a move that reaches more shares brings what is under it
-- ---------------------------------------------------------------------------

-- When a page comes under shares its old parent was not under, the rows
-- under it and on it, which did not change, move their updated_at on, so
-- the pulls of those the move reached bring them. A move within the same
-- shares, or out of some, reaches no one new and leaves them.
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
  with recursive tree as (
    select p.id, 0 as depth from public.pages p where p.parent_id = new.id
    union all
    select p.id, t.depth + 1
    from public.pages p join tree t on p.parent_id = t.id
    where t.depth < 64
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

drop trigger if exists pages_touch_moved on public.pages;
create trigger pages_touch_moved
  after update of parent_id on public.pages
  for each row when (old.parent_id is distinct from new.parent_id)
  execute function public.pages_touch_moved();

-- page_content.updated_by is who last wrote the content: an update that
-- leaves the content as it was (the touch above) keeps it.
create or replace function public.stamp_page_content_writer()
returns trigger
language plpgsql
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
begin
  if v_uid is not null
     and (tg_op = 'INSERT' or new.content is distinct from old.content) then
    new.updated_by := v_uid;
  end if;
  return new;
end;
$$;

-- ---------------------------------------------------------------------------
-- profiles: shared a page means one share reaches the other's
-- ---------------------------------------------------------------------------

-- True when the current user and p_other can both view at least one page,
-- by shares that were accepted: the space's owner and someone shared a
-- page of it, or two people whose shares are of the same page or of a
-- page and one it is inside.
create or replace function public.shares_page_with(p_other uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  with recursive mine as (
    select pp.page_id as id, 0 as depth
    from public.page_permissions pp
    where pp.user_id = (select auth.uid()) and pp.accepted_at is not null
    union all
    select p.parent_id, m.depth + 1
    from mine m join public.pages p on p.id = m.id
    where p.parent_id is not null and m.depth < 64
  ),
  theirs as (
    select pp.page_id as id, 0 as depth
    from public.page_permissions pp
    where pp.user_id = p_other and pp.accepted_at is not null
    union all
    select p.parent_id, t.depth + 1
    from theirs t join public.pages p on p.id = t.id
    where p.parent_id is not null and t.depth < 64
  )
  select exists (
    select 1
    from public.page_permissions pp
    join public.pages p on p.id = pp.page_id
    join public.spaces s on s.id = p.space_id
    where pp.accepted_at is not null
      and ((pp.user_id = (select auth.uid()) and s.owner_id = p_other)
        or (pp.user_id = p_other and s.owner_id = (select auth.uid())))
  ) or exists (
    -- Their share is of one of my shared pages or a page above one.
    select 1 from public.page_permissions b
    where b.user_id = p_other and b.accepted_at is not null
      and b.page_id in (select m.id from mine m)
  ) or exists (
    -- Mine is of one of theirs or a page above one.
    select 1 from public.page_permissions a
    where a.user_id = (select auth.uid()) and a.accepted_at is not null
      and a.page_id in (select t.id from theirs t)
  );
$$;
