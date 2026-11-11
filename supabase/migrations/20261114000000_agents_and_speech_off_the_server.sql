-- Agents and transcription run on the device, which keeps its providers,
-- speech settings and keys, since 20261110000000's build. What the agent,
-- providers and stt functions of the builds before it worked through
-- stayed for the devices still on those builds; it goes now, and the app
-- deletes the three functions from the project as it sets it up.
--
-- * The keys those functions kept in Vault go: each provider's key and
--   refresh token, and each speech-to-text key. Nothing else in Vault is
--   touched.
-- * The RPCs only they, or the apps that called them, used go:
--   set_provider_secret, get_provider_secret, set_stt_secret,
--   get_stt_secret, has_stt_secret, agent_shared_pages, agent_card_pages,
--   agent_search_pages, and page_own_text, which cut a search's words.
-- * Their tables go, with their rows: ai_jobs, model_providers,
--   stt_settings, stt_secrets, stt_jobs and stt_file_jobs, and the trigger
--   functions that were theirs alone.
-- * A new account no longer gets a row of speech settings
--   (handle_new_user), and a history snapshot no longer names a job
--   (page_history.ai_job_id): no app has sent one since a run's job stayed
--   on its device.

-- ---------------------------------------------------------------------------
-- Vault: the keys of the providers and of speech to text
-- ---------------------------------------------------------------------------

delete from vault.secrets s
where s.id in (
  select mp.encrypted_secret from public.model_providers mp where mp.encrypted_secret is not null
  union all
  select mp.refresh_token from public.model_providers mp where mp.refresh_token is not null
  union all
  select ss.secret_id from public.stt_secrets ss
);

-- ---------------------------------------------------------------------------
-- The RPCs
-- ---------------------------------------------------------------------------

drop function if exists public.set_provider_secret(uuid, text);
drop function if exists public.set_provider_secret(uuid, text, text, text);
drop function if exists public.get_provider_secret(uuid);
drop function if exists public.get_provider_secret(uuid, text, text);
drop function if exists public.set_stt_secret(text, text);
drop function if exists public.get_stt_secret(uuid, text);
drop function if exists public.has_stt_secret(text);
drop function if exists public.agent_shared_pages();
drop function if exists public.agent_card_pages(uuid[]);
drop function if exists public.agent_search_pages(text, text[], integer);
drop function if exists public.page_own_text(jsonb);

-- ---------------------------------------------------------------------------
-- auth.users: a new account gets a profile and its space
-- ---------------------------------------------------------------------------

create or replace function public.handle_new_user()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  insert into public.profiles (id, display_name)
  values (
    new.id,
    coalesce(
      nullif(new.raw_user_meta_data ->> 'display_name', ''),
      nullif(new.raw_user_meta_data ->> 'name', ''),
      split_part(coalesce(new.email, ''), '@', 1)
    )
  )
  on conflict (id) do nothing;

  insert into public.spaces (owner_id, name)
  values (new.id, 'My space')
  on conflict (owner_id) do nothing;

  return new;
end;
$$;

-- ---------------------------------------------------------------------------
-- page_history: a snapshot names no job
-- ---------------------------------------------------------------------------

-- created_by is the caller, and a snapshot is never dated after it reached
-- the server (one made offline keeps its earlier time), as before.
create or replace function public.stamp_page_history_writer()
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
  new.created_by := v_uid;
  new.created_at := least(coalesce(new.created_at, now()), now());
  return new;
end;
$$;

drop index if exists public.page_history_ai_job_idx;
alter table public.page_history drop column if exists ai_job_id;

-- ---------------------------------------------------------------------------
-- The tables, and the trigger functions that were theirs
-- ---------------------------------------------------------------------------

drop table if exists public.stt_file_jobs;
drop table if exists public.stt_jobs;
drop table if exists public.stt_secrets;
drop table if exists public.stt_settings;
drop table if exists public.ai_jobs;
drop table if exists public.model_providers;

drop function if exists public.ai_jobs_in_bounds();
drop function if exists public.model_providers_key_stays();
drop function if exists public.model_providers_cleanup();
drop function if exists public.stt_secrets_cleanup();
drop function if exists public.base_url_origin(text);
