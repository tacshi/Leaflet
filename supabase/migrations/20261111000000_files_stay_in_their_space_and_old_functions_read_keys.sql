-- * A file's space was the writer's to change: the owner of the page it is
--   on, who passes files_update by that alone, could move the row into
--   another space (any they know the id of, as from a share), where it
--   showed in that space's files and went to its owner's devices. A file
--   now keeps its space, as a page and a folder do (space_immutable).
-- * 20261110000000 dropped the one-argument get_provider_secret, which the
--   functions deployed before it call: a project has its migrations
--   applied before its functions are deployed, and until the deploy is
--   done, which a failure leaves for the next start, the agent, providers
--   and stt functions it has could read no key. The one-argument form is
--   back, as it was, for them; Setup drops it once it has deployed the
--   functions that call the three-argument one (provision.retired).

-- ---------------------------------------------------------------------------
-- files: a file stays in its space
-- ---------------------------------------------------------------------------

-- A file stays in its space; its page is in its space, or one its writer
-- can edit as they set it; its folder is in its space; and anyone but its
-- space's owner (the owner of the page it is on) only deletes it, with the
-- changed_at the device sends, which orders adoption and is no change to
-- the file. Rows set before 20261031000000 stay as they are until their
-- page or folder changes. created_by is who added it. Its page_id going to
-- null once that page is gone is the page's foreign key (on delete set
-- null), which runs as whoever deleted the page, not a change to the file.
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
  -- For everyone, the service role too, as pages_guard and folders_guard
  -- keep a page's and a folder's: nothing moves a file between spaces.
  if tg_op = 'UPDATE' and new.space_id is distinct from old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;

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

-- ---------------------------------------------------------------------------
-- model_providers: the key, for the functions deployed before 20261110000000
-- ---------------------------------------------------------------------------

-- Decrypt a provider's API key by its id alone, as before 20261110000000:
-- the functions deployed before it call this, until Setup deploys the ones
-- that call the three-argument form, and drops this. Edge Functions
-- (service role) only.
create or replace function public.get_provider_secret(p_provider_id uuid)
returns text
language sql
stable
security definer
set search_path = ''
as $$
  select ds.decrypted_secret
  from public.model_providers mp
  join vault.decrypted_secrets ds on ds.id = mp.encrypted_secret
  where mp.id = p_provider_id;
$$;

revoke execute on function public.get_provider_secret(uuid) from public, anon, authenticated, service_role;
grant execute on function public.get_provider_secret(uuid) to service_role;
