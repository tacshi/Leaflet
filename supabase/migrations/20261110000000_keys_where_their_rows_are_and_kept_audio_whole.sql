-- * The functions read a provider's row, then its key by the provider's id
--   alone (get_provider_secret): a save that moved the provider and gave it
--   a new key between the two had the new key sent to the old address,
--   once. get_provider_secret now takes the base URL and preset the caller
--   read, and gives the key, read with the row in one statement, only while
--   the row still has them: provider_moved otherwise.
-- * The providers function writes a provider's row, then its key
--   (set_provider_secret), which read the row by id alone, unlocked: two
--   saves of one provider at once could leave the key one sent on the
--   address the other wrote. set_provider_secret now locks the row and
--   takes, as well, the base URL and preset the key is for: it saves, or
--   clears, the key only while the row has them (provider_moved
--   otherwise). A call without them, as the app may make, saves the key as
--   before, the row locked.
-- * release_kept_recordings answered its paths a row each, and the Data
--   API answers with no more than 1,000 rows (max_rows): the account
--   function, which keeps only the audio it is told of, removed the rest
--   of an account's kept recordings past a thousand. It answers them now
--   as one array, in one row, which no row limit cuts.

-- ---------------------------------------------------------------------------
-- model_providers: a key goes only where its row is
-- ---------------------------------------------------------------------------

drop function if exists public.get_provider_secret(uuid);

-- Decrypt a provider's API key, for the address and preset the caller read
-- of its row. Edge Functions (service role) only.
create or replace function public.get_provider_secret(p_provider_id uuid, p_base_url text, p_preset text)
returns text
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_base_url text;
  v_preset text;
  v_is_local boolean;
  v_key text;
begin
  select mp.base_url, mp.preset, mp.is_local, ds.decrypted_secret
  into v_base_url, v_preset, v_is_local, v_key
  from public.model_providers mp
  left join vault.decrypted_secrets ds on ds.id = mp.encrypted_secret
  where mp.id = p_provider_id;
  if not found then
    return null;
  end if;
  if v_base_url is distinct from p_base_url or v_preset is distinct from p_preset or v_is_local then
    raise exception 'provider_moved' using errcode = '40001';
  end if;
  return v_key;
end;
$$;

drop function if exists public.set_provider_secret(uuid, text);

-- Store (or clear, with an empty secret) a provider's API key in Vault,
-- while the provider has the base URL and preset given, when they are.
create or replace function public.set_provider_secret(
  p_provider_id uuid,
  p_secret text,
  p_base_url text default null,
  p_preset text default null
)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_owner uuid;
  v_secret_id uuid;
  v_base_url text;
  v_preset text;
begin
  select mp.user_id, mp.encrypted_secret, mp.base_url, mp.preset
  into v_owner, v_secret_id, v_base_url, v_preset
  from public.model_providers mp
  where mp.id = p_provider_id
  for update;

  if v_owner is null or v_owner is distinct from auth.uid() then
    raise exception 'provider_not_found' using errcode = 'P0002';
  end if;

  -- Another save moved the provider since this one wrote it: the key is
  -- for an address it no longer has, and a clear would take the other's.
  if (p_base_url is not null and v_base_url is distinct from p_base_url)
     or (p_preset is not null and v_preset is distinct from p_preset) then
    raise exception 'provider_moved' using errcode = '40001';
  end if;

  if p_secret is null or p_secret = '' then
    update public.model_providers set encrypted_secret = null where id = p_provider_id;
    if v_secret_id is not null then
      delete from vault.secrets where id = v_secret_id;
    end if;
    return;
  end if;

  if v_secret_id is null then
    v_secret_id := vault.create_secret(
      p_secret,
      'model_provider:' || p_provider_id::text,
      'API key for a Leaflet model provider'
    );
    update public.model_providers set encrypted_secret = v_secret_id where id = p_provider_id;
  else
    perform vault.update_secret(v_secret_id, p_secret);
    -- Bump updated_at so other devices see the provider changed.
    update public.model_providers set encrypted_secret = v_secret_id where id = p_provider_id;
  end if;
end;
$$;

revoke execute on function public.get_provider_secret(uuid, text, text) from public, anon, authenticated, service_role;
grant execute on function public.get_provider_secret(uuid, text, text) to service_role;
revoke execute on function public.set_provider_secret(uuid, text, text, text) from public, anon, authenticated, service_role;
grant execute on function public.set_provider_secret(uuid, text, text, text) to authenticated;

-- ---------------------------------------------------------------------------
-- recordings: the audio kept, all of it
-- ---------------------------------------------------------------------------

drop function if exists public.release_kept_recordings(uuid);

-- The audio of the recordings p_user_id made on pages in others' spaces,
-- at its own path ({user}/{recording}.{ext}), which stays when the account
-- goes: the account no longer owns the objects after this, and their
-- paths are returned, as one array.
create or replace function public.release_kept_recordings(p_user_id uuid)
returns text[]
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_paths text[];
begin
  select coalesce(array_agg(r.audio_path), '{}') into v_paths
  from public.recordings r
  join public.pages p on p.id = r.page_id
  join public.spaces s on s.id = p.space_id
  where r.created_by = p_user_id
    and s.owner_id <> p_user_id
    and split_part(r.audio_path, '/', 1) = p_user_id::text
    and split_part(split_part(r.audio_path, '/', 2), '.', 1) = r.id::text
    and split_part(r.audio_path, '/', 3) = '';
  update storage.objects set owner = null
  where bucket_id = 'recordings' and name = any (v_paths);
  -- Newer Storage keeps the owner in owner_id as well.
  if exists (
    select 1 from information_schema.columns
    where table_schema = 'storage' and table_name = 'objects' and column_name = 'owner_id'
  ) then
    execute 'update storage.objects set owner_id = null where bucket_id = ''recordings'' and name = any ($1)'
      using v_paths;
  end if;
  return v_paths;
end;
$$;

revoke execute on function public.release_kept_recordings(uuid) from public, anon, authenticated, service_role;
grant execute on function public.release_kept_recordings(uuid) to service_role;
