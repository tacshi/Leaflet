-- Four rules a fifth review found loose:
-- * unshare_page deletes the rows on the page it is given, but page_members
--   lists the grants on the pages above it and the invites waiting on them
--   too, and the app offers to remove each: removing one of those from a
--   subpage did nothing and said nothing. It now refuses with
--   'inherited_share' when what it was asked to remove is on a page above,
--   so the app can say to remove it there.
-- * page_content.updated_by was stamped only when the content changed, and
--   an update that left the content as it was kept what the client sent,
--   so an editor could name anyone as its last writer. Now such an update
--   keeps the writer it had.
-- * files.created_by was the column's default, auth.uid(), which a client
--   could send over, and could change later. It is now the caller on
--   insert and stays as it was, as pages_guard keeps pages.created_by.
-- * files.page_id is set null when its page is deleted, and files_guard
--   saw that as the caller changing the file: for a file in someone else's
--   space, as an editor's upload to a shared page is, it refused, so the
--   page's owner could not delete the page, a page above it, or its space.
--   A file's page_id may now go to null once its page is gone.

-- ---------------------------------------------------------------------------
-- unshare_page: a share inherited from a page above says so
-- ---------------------------------------------------------------------------

-- Withdraw a share, or an invite by the id page_members gives it. A share
-- or invite page_members lists for the page but that is on a page above it
-- is withdrawn on that page, not here: when nothing here matched and one
-- there does, it raises 'inherited_share' rather than doing nothing.
create or replace function public.unshare_page(p_page_id uuid, p_user_id uuid)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_grants integer;
  v_invites integer;
begin
  if public.page_role(p_page_id) is distinct from 'owner' then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  delete from public.page_permissions pp
  where pp.page_id = p_page_id
    and (pp.user_id = p_user_id or (pp.invite_id = p_user_id and pp.accepted_at is null));
  get diagnostics v_grants = row_count;
  delete from public.page_email_invites i
  where i.page_id = p_page_id and i.id = p_user_id;
  get diagnostics v_invites = row_count;

  if v_grants + v_invites = 0 and exists (
    with recursive above as (
      select p.parent_id as id, 1 as depth
      from public.pages p where p.id = p_page_id and p.parent_id is not null
      union all
      select p.parent_id, a.depth + 1
      from public.pages p join above a on p.id = a.id
      where p.parent_id is not null and a.depth < 64
    )
    select 1 from above a
    where exists (
      select 1 from public.page_permissions pp
      where pp.page_id = a.id
        and (pp.user_id = p_user_id or (pp.invite_id = p_user_id and pp.accepted_at is null))
    ) or exists (
      select 1 from public.page_email_invites i
      where i.page_id = a.id and i.id = p_user_id
    )
  ) then
    raise exception 'inherited_share' using errcode = '22023';
  end if;
end;
$$;

-- ---------------------------------------------------------------------------
-- page_content: updated_by is who last wrote the content
-- ---------------------------------------------------------------------------

-- An update that leaves the content as it was (pages_touch_moved's, or a
-- client's that sends only updated_by) keeps the writer it had.
create or replace function public.stamp_page_content_writer()
returns trigger
language plpgsql
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
begin
  if v_uid is null then
    return new;
  end if;
  if tg_op = 'INSERT' or new.content is distinct from old.content then
    new.updated_by := v_uid;
  else
    new.updated_by := old.updated_by;
  end if;
  return new;
end;
$$;

-- ---------------------------------------------------------------------------
-- files: who added them, and their page deleted
-- ---------------------------------------------------------------------------

-- A file's page is in its space, or one its writer can edit as they set it;
-- its folder is in its space; and anyone but its space's owner (the owner
-- of the page it is on) only deletes it. Rows set before 20261031000000
-- stay as they are until their page or folder changes. created_by is who
-- added it. Its page_id going to null once that page is gone is the page's
-- foreign key (on delete set null), which runs as whoever deleted the
-- page, not a change to the file.
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
    v_new := to_jsonb(new) - 'deleted_at' - 'updated_at';
    v_old := to_jsonb(old) - 'deleted_at' - 'updated_at';
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
