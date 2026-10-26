-- Leaflet v1 schema: personal spaces, nested pages, comments, recordings,
-- agents and model providers. Contract: docs/protocol.md.
--
-- Conventions
-- * Every table with updated_at gets it from the server (clock_timestamp())
--   on insert and on every update; clients pull with `updated_at > cursor`.
-- * page_content saves are compare-and-set on its revision: update where
--   the revision is the one read; zero rows means read, merge and retry.
-- * pages, folders, comments and files are soft-deleted (deleted_at) so the
--   deletion reaches other devices through the same pull.
-- * Access: the space owner has full access; page_permissions grants viewer
--   or editor on a page and all of its subpages.

create extension if not exists pgcrypto with schema extensions;

-- ---------------------------------------------------------------------------
-- updated_at
-- ---------------------------------------------------------------------------

create or replace function public.set_updated_at()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  new.updated_at := clock_timestamp();
  return new;
end;
$$;

-- ---------------------------------------------------------------------------
-- Tables
-- ---------------------------------------------------------------------------

create table public.profiles (
  id uuid primary key references auth.users (id) on delete cascade,
  display_name text not null default '',
  avatar_url text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table public.spaces (
  id uuid primary key default gen_random_uuid(),
  name text not null default 'My space',
  owner_id uuid not null references auth.users (id) on delete cascade,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  -- One personal space per user in v1.
  constraint spaces_one_per_owner unique (owner_id)
);

create table public.folders (
  id uuid primary key default gen_random_uuid(),
  space_id uuid not null references public.spaces (id) on delete cascade,
  parent_folder_id uuid references public.folders (id) on delete cascade,
  name text not null default '',
  sort_key text not null default '',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);

create table public.pages (
  id uuid primary key default gen_random_uuid(),
  space_id uuid not null references public.spaces (id) on delete cascade,
  parent_id uuid references public.pages (id) on delete cascade,
  folder_id uuid references public.folders (id) on delete set null,
  title text not null default '',
  icon text,
  sort_key text not null default '',
  created_by uuid default auth.uid() references auth.users (id) on delete set null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz,
  -- A template, or a subpage of one: kept to start pages from, out of the
  -- page list.
  is_template boolean not null default false,
  constraint pages_not_own_parent check (parent_id is distinct from id)
);

create table public.page_content (
  page_id uuid primary key references public.pages (id) on delete cascade,
  -- Editor JSON in v1 (see protocol §1); a CRDT blob once collaboration lands.
  content jsonb not null default
    '{"document":{"type":"page","id":"root","children":[]},"meta":{"v":1,"blocks":{},"deleted":{}}}'::jsonb,
  -- Flattened text of the page, for search.
  plain_text text not null default '',
  updated_by uuid default auth.uid() references auth.users (id) on delete set null,
  -- Moves on with every update; saves are compare-and-set on it (protocol §1).
  revision bigint not null default 0,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

-- Incremental CRDT updates (collaboration phase). Unused in v1.
create table public.page_updates (
  page_id uuid not null references public.pages (id) on delete cascade,
  seq bigint not null,
  update bytea not null,
  created_by uuid default auth.uid() references auth.users (id) on delete set null,
  created_at timestamptz not null default now(),
  primary key (page_id, seq)
);

create table public.page_permissions (
  page_id uuid not null references public.pages (id) on delete cascade,
  user_id uuid not null references auth.users (id) on delete cascade,
  role text not null check (role in ('viewer', 'editor')),
  invited_by uuid default auth.uid() references auth.users (id) on delete set null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (page_id, user_id)
);

create table public.comments (
  id uuid primary key default gen_random_uuid(),
  page_id uuid not null references public.pages (id) on delete cascade,
  -- Stable editor block ID (protocol §1); survives edits elsewhere on the page.
  block_id text not null,
  parent_comment_id uuid references public.comments (id) on delete cascade,
  -- For agent replies this is the user who invoked the agent.
  author_id uuid default auth.uid() references auth.users (id) on delete set null,
  author_kind text not null default 'user' check (author_kind in ('user', 'agent')),
  agent_name text,
  body text not null,
  -- The text the thread was started on, if any.
  quote text,
  mentions jsonb not null default '[]'::jsonb,
  resolved boolean not null default false,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);

create table public.recordings (
  id uuid primary key default gen_random_uuid(),
  page_id uuid not null references public.pages (id) on delete cascade,
  block_id text,
  -- Object path in the `recordings` bucket: {user_id}/{recording_id}.m4a
  audio_path text,
  duration_ms integer not null default 0,
  stt_provider text,
  stt_model text,
  status text not null default 'recording'
    check (status in ('recording', 'uploading', 'transcribing', 'ready', 'failed')),
  -- [{start_ms, end_ms, text, speaker?}]
  transcript jsonb not null default '[]'::jsonb,
  language text,
  error text,
  created_by uuid default auth.uid() references auth.users (id) on delete set null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table public.files (
  id uuid primary key default gen_random_uuid(),
  space_id uuid not null references public.spaces (id) on delete cascade,
  folder_id uuid references public.folders (id) on delete set null,
  page_id uuid references public.pages (id) on delete set null,
  name text not null,
  -- Object path in the `files` bucket: {space_id}/{file_id}/{name}
  storage_path text,
  mime_type text,
  size_bytes bigint,
  created_by uuid default auth.uid() references auth.users (id) on delete set null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deleted_at timestamptz
);

create table public.model_providers (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null default auth.uid() references auth.users (id) on delete cascade,
  preset text not null check (preset in (
    'openai', 'claude', 'xai', 'openrouter', 'deepseek', 'glm',
    'custom', 'ollama', 'lmstudio', 'local'
  )),
  name text not null default '',
  base_url text not null default '',
  auth_kind text not null default 'api_key' check (auth_kind in ('account', 'api_key', 'none')),
  -- Vault secret IDs. Never selectable or writable by clients (grants below).
  encrypted_secret uuid,
  refresh_token uuid,
  expires_at timestamptz,
  -- Lets clients show "key saved" without seeing the secret.
  has_secret boolean generated always as (encrypted_secret is not null) stored,
  default_model text,
  is_local boolean not null default false,
  -- null = not tested yet; false = chat-only.
  supports_tools boolean,
  models jsonb not null default '[]'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table public.ai_jobs (
  id uuid primary key default gen_random_uuid(),
  page_id uuid not null references public.pages (id) on delete cascade,
  user_id uuid not null default auth.uid() references auth.users (id) on delete cascade,
  surface text not null check (surface in ('inline', 'selection', 'sidebar', 'comment')),
  focus_block_ids text[] not null default '{}',
  comment_id uuid references public.comments (id) on delete set null,
  -- Sidebar chat turns share a conversation_id.
  conversation_id uuid,
  provider_id uuid references public.model_providers (id) on delete set null,
  model text,
  status text not null default 'queued'
    check (status in ('queued', 'running', 'done', 'failed', 'reverted')),
  -- {instruction, selection_text?, allow_cross_page}
  request jsonb not null default '{}'::jsonb,
  -- {reply, ops, edits}
  result jsonb,
  error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table public.page_history (
  id uuid primary key default gen_random_uuid(),
  page_id uuid not null references public.pages (id) on delete cascade,
  content jsonb not null,
  reason text not null default 'autosave'
    check (reason in ('agent_run', 'autosave', 'restore', 'manual', 'conflict')),
  ai_job_id uuid references public.ai_jobs (id) on delete set null,
  created_by uuid default auth.uid() references auth.users (id) on delete set null,
  created_at timestamptz not null default now()
);

create table public.stt_settings (
  user_id uuid primary key default auth.uid() references auth.users (id) on delete cascade,
  -- qwen3-asr | moss-transcribe-diarize | sensevoice | soniox | openai-compatible
  provider text not null default 'qwen3-asr',
  model text,
  base_url text,
  -- Model provider whose key an OpenAI-compatible endpoint uses.
  provider_id uuid references public.model_providers (id) on delete set null,
  -- 'auto' or a language code (en, zh, yue, ...)
  language text not null default 'auto',
  -- auto | gpu | cpu
  run_on text not null default 'auto',
  live_transcript boolean not null default true,
  keep_audio boolean not null default true,
  speaker_labels boolean not null default false,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

-- Speech-to-text keys that do not belong to a model provider (Soniox).
-- Service role only: RLS on, no policies, no grants.
create table public.stt_secrets (
  user_id uuid not null references auth.users (id) on delete cascade,
  provider text not null,
  secret_id uuid not null,
  created_at timestamptz not null default now(),
  primary key (user_id, provider)
);

create table public.devices (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null default auth.uid() references auth.users (id) on delete cascade,
  name text not null default '',
  platform text,
  last_active_at timestamptz not null default now(),
  -- Set when signed out from another device; the device signs itself out.
  revoked_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

-- ---------------------------------------------------------------------------
-- Indexes
-- ---------------------------------------------------------------------------

create index folders_space_idx on public.folders (space_id);
create index folders_updated_idx on public.folders (updated_at);
create index pages_space_idx on public.pages (space_id);
create index pages_parent_idx on public.pages (parent_id);
create index pages_folder_idx on public.pages (folder_id);
create index pages_updated_idx on public.pages (updated_at);
create index page_content_updated_idx on public.page_content (updated_at);
create index page_permissions_user_idx on public.page_permissions (user_id);
create index page_permissions_updated_idx on public.page_permissions (updated_at);
create index comments_page_idx on public.comments (page_id, block_id);
create index comments_parent_idx on public.comments (parent_comment_id);
create index comments_updated_idx on public.comments (updated_at);
create index recordings_page_idx on public.recordings (page_id);
create index recordings_updated_idx on public.recordings (updated_at);
create index files_space_idx on public.files (space_id);
create index files_updated_idx on public.files (updated_at);
create index model_providers_user_idx on public.model_providers (user_id);
create index ai_jobs_page_idx on public.ai_jobs (page_id, created_at desc);
create index ai_jobs_conversation_idx on public.ai_jobs (conversation_id, created_at);
create index ai_jobs_user_idx on public.ai_jobs (user_id, updated_at);
create index page_history_page_idx on public.page_history (page_id, created_at desc);
create index devices_user_idx on public.devices (user_id);

-- ---------------------------------------------------------------------------
-- updated_at triggers
-- ---------------------------------------------------------------------------

do $$
declare
  t text;
begin
  foreach t in array array[
    'profiles', 'spaces', 'folders', 'pages', 'page_content', 'page_permissions',
    'comments', 'recordings', 'files', 'model_providers', 'ai_jobs',
    'stt_settings', 'devices'
  ] loop
    execute format(
      'create trigger %I before insert or update on public.%I
         for each row execute function public.set_updated_at()',
      t || '_set_updated_at', t
    );
  end loop;
end;
$$;

-- page_content's revision moves on with every update, whatever the writer
-- sent, so a writer that does not check it still makes the others retry.
create or replace function public.bump_revision()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  new.revision := old.revision + 1;
  return new;
end;
$$;

create trigger page_content_bump_revision before update on public.page_content
  for each row execute function public.bump_revision();

-- ---------------------------------------------------------------------------
-- Access helpers (security definer: they read past RLS, keyed on auth.uid())
-- ---------------------------------------------------------------------------

create or replace function public.is_space_owner(p_space_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select exists (
    select 1 from public.spaces s
    where s.id = p_space_id and s.owner_id = (select auth.uid())
  );
$$;

-- 'owner' | 'editor' | 'viewer' | null for the current user on a page.
-- A permission on any ancestor applies to its subpages.
create or replace function public.page_role(p_page_id uuid)
returns text
language sql
stable
security definer
set search_path = ''
as $$
  with recursive chain as (
    select p.id, p.parent_id, p.space_id, 0 as depth
    from public.pages p
    where p.id = p_page_id
    union all
    select p.id, p.parent_id, p.space_id, c.depth + 1
    from public.pages p
    join chain c on p.id = c.parent_id
    where c.depth < 64
  )
  select case
    when (select auth.uid()) is null then null
    when exists (
      select 1 from chain c
      join public.spaces s on s.id = c.space_id
      where c.depth = 0 and s.owner_id = (select auth.uid())
    ) then 'owner'
    when exists (
      select 1 from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.user_id = (select auth.uid()) and pp.role = 'editor'
    ) then 'editor'
    when exists (
      select 1 from chain c
      join public.page_permissions pp on pp.page_id = c.id
      where pp.user_id = (select auth.uid()) and pp.role = 'viewer'
    ) then 'viewer'
    else null
  end;
$$;

create or replace function public.can_view_page(p_page_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select public.page_role(p_page_id) is not null;
$$;

create or replace function public.can_edit_page(p_page_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select coalesce(public.page_role(p_page_id) in ('owner', 'editor'), false);
$$;

-- True when the current user owns the space or has been shared a page in it.
create or replace function public.has_space_access(p_space_id uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select public.is_space_owner(p_space_id) or exists (
    select 1
    from public.page_permissions pp
    join public.pages p on p.id = pp.page_id
    where p.space_id = p_space_id and pp.user_id = (select auth.uid())
  );
$$;

-- True when the current user and p_other share at least one page, either way.
create or replace function public.shares_page_with(p_other uuid)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select exists (
    select 1
    from public.page_permissions pp
    join public.pages p on p.id = pp.page_id
    join public.spaces s on s.id = p.space_id
    where (pp.user_id = (select auth.uid()) and s.owner_id = p_other)
       or (pp.user_id = p_other and s.owner_id = (select auth.uid()))
  ) or exists (
    select 1
    from public.page_permissions a
    join public.pages pa on pa.id = a.page_id
    join public.pages pb on pb.space_id = pa.space_id
    join public.page_permissions b on b.page_id = pb.id
    where a.user_id = (select auth.uid()) and b.user_id = p_other
  );
$$;

-- First path segment of a storage object, as a space ID the user owns.
create or replace function public.owns_space_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_first text := split_part(p_name, '/', 1);
begin
  if v_first !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then
    return false;
  end if;
  return public.is_space_owner(v_first::uuid);
end;
$$;

create or replace function public.can_access_space_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_first text := split_part(p_name, '/', 1);
begin
  if v_first !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' then
    return false;
  end if;
  return public.has_space_access(v_first::uuid);
end;
$$;

-- ---------------------------------------------------------------------------
-- Integrity triggers
-- ---------------------------------------------------------------------------

-- Pages stay in their space; a subpage lives in its parent's space; no cycles;
-- moving a page under a new parent needs edit access to that parent (the
-- update policy already requires edit access to the page itself).
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
    return new;
  end if;

  if new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;

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
      end if;
    end if;
  end if;

  if new.folder_id is distinct from old.folder_id
     and v_uid is not null
     and not public.is_space_owner(new.space_id) then
    raise exception 'forbidden_folder' using errcode = '42501';
  end if;

  return new;
end;
$$;

create trigger pages_guard
  before insert or update on public.pages
  for each row execute function public.pages_guard();

-- Folders stay in their space and nest only within it.
create or replace function public.folders_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  if tg_op = 'UPDATE' and new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;
  if new.parent_folder_id is not null and not exists (
    select 1 from public.folders f
    where f.id = new.parent_folder_id and f.space_id = new.space_id and f.id <> new.id
  ) then
    raise exception 'parent_folder_in_other_space' using errcode = '23514';
  end if;
  return new;
end;
$$;

create trigger folders_guard
  before insert or update on public.folders
  for each row execute function public.folders_guard();

-- Editors may resolve or reopen anyone's thread, but only the author changes
-- what a comment says, and only the author or the space owner deletes it.
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
    if new.parent_comment_id is not null and not exists (
      select 1 from public.comments c
      where c.id = new.parent_comment_id and c.page_id = new.page_id
    ) then
      raise exception 'parent_comment_on_other_page' using errcode = '23514';
    end if;
    return new;
  end if;
  if (new.page_id, new.block_id, new.parent_comment_id, new.author_id, new.author_kind)
     is distinct from
     (old.page_id, old.block_id, old.parent_comment_id, old.author_id, old.author_kind) then
    raise exception 'comment_immutable' using errcode = '42501';
  end if;
  if old.author_id is distinct from v_uid then
    if (new.body, new.quote, new.mentions, new.agent_name)
       is distinct from (old.body, old.quote, old.mentions, old.agent_name) then
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

create trigger comments_guard
  before insert or update on public.comments
  for each row execute function public.comments_guard();

-- Deleting a provider deletes its Vault secrets.
create or replace function public.model_providers_cleanup()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  delete from vault.secrets
  where id in (old.encrypted_secret, old.refresh_token);
  return old;
end;
$$;

create trigger model_providers_cleanup
  after delete on public.model_providers
  for each row execute function public.model_providers_cleanup();

create or replace function public.stt_secrets_cleanup()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
begin
  delete from vault.secrets where id = old.secret_id;
  return old;
end;
$$;

create trigger stt_secrets_cleanup
  after delete on public.stt_secrets
  for each row execute function public.stt_secrets_cleanup();

-- New users get a profile, their personal space, and default STT settings.
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

  insert into public.stt_settings (user_id)
  values (new.id)
  on conflict (user_id) do nothing;

  return new;
end;
$$;

create trigger on_auth_user_created
  after insert on auth.users
  for each row execute function public.handle_new_user();

-- ---------------------------------------------------------------------------
-- Row-level security
-- ---------------------------------------------------------------------------

alter table public.profiles enable row level security;
alter table public.spaces enable row level security;
alter table public.folders enable row level security;
alter table public.pages enable row level security;
alter table public.page_content enable row level security;
alter table public.page_updates enable row level security;
alter table public.page_history enable row level security;
alter table public.page_permissions enable row level security;
alter table public.comments enable row level security;
alter table public.recordings enable row level security;
alter table public.files enable row level security;
alter table public.ai_jobs enable row level security;
alter table public.model_providers enable row level security;
alter table public.stt_settings enable row level security;
alter table public.stt_secrets enable row level security;
alter table public.devices enable row level security;

-- profiles
create policy profiles_select on public.profiles for select to authenticated
  using (id = (select auth.uid()) or public.shares_page_with(id));
create policy profiles_insert on public.profiles for insert to authenticated
  with check (id = (select auth.uid()));
create policy profiles_update on public.profiles for update to authenticated
  using (id = (select auth.uid())) with check (id = (select auth.uid()));

-- spaces
create policy spaces_select on public.spaces for select to authenticated
  using (owner_id = (select auth.uid()) or public.has_space_access(id));
create policy spaces_insert on public.spaces for insert to authenticated
  with check (owner_id = (select auth.uid()));
create policy spaces_update on public.spaces for update to authenticated
  using (owner_id = (select auth.uid())) with check (owner_id = (select auth.uid()));
create policy spaces_delete on public.spaces for delete to authenticated
  using (owner_id = (select auth.uid()));

-- folders: the space owner only
create policy folders_all on public.folders for all to authenticated
  using (public.is_space_owner(space_id)) with check (public.is_space_owner(space_id));

-- pages
create policy pages_select on public.pages for select to authenticated
  using (public.can_view_page(id));
create policy pages_insert on public.pages for insert to authenticated
  with check (
    public.is_space_owner(space_id)
    or (parent_id is not null and public.can_edit_page(parent_id))
  );
create policy pages_update on public.pages for update to authenticated
  using (public.can_edit_page(id)) with check (public.can_edit_page(id));
create policy pages_delete on public.pages for delete to authenticated
  using (public.is_space_owner(space_id));

-- page_content
create policy page_content_select on public.page_content for select to authenticated
  using (public.can_view_page(page_id));
create policy page_content_insert on public.page_content for insert to authenticated
  with check (public.can_edit_page(page_id));
create policy page_content_update on public.page_content for update to authenticated
  using (public.can_edit_page(page_id)) with check (public.can_edit_page(page_id));
create policy page_content_delete on public.page_content for delete to authenticated
  using (public.page_role(page_id) = 'owner');

-- page_updates
create policy page_updates_select on public.page_updates for select to authenticated
  using (public.can_view_page(page_id));
create policy page_updates_insert on public.page_updates for insert to authenticated
  with check (public.can_edit_page(page_id));

-- page_history: append-only
create policy page_history_select on public.page_history for select to authenticated
  using (public.can_view_page(page_id));
create policy page_history_insert on public.page_history for insert to authenticated
  with check (public.can_edit_page(page_id));

-- page_permissions: people see their own grants; the owner manages sharing
create policy page_permissions_select on public.page_permissions for select to authenticated
  using (user_id = (select auth.uid()) or public.page_role(page_id) = 'owner');
create policy page_permissions_insert on public.page_permissions for insert to authenticated
  with check (public.page_role(page_id) = 'owner' and user_id <> (select auth.uid()));
create policy page_permissions_update on public.page_permissions for update to authenticated
  using (public.page_role(page_id) = 'owner') with check (public.page_role(page_id) = 'owner');
create policy page_permissions_delete on public.page_permissions for delete to authenticated
  using (public.page_role(page_id) = 'owner');

-- comments: anyone who can view a page can comment on it
create policy comments_select on public.comments for select to authenticated
  using (public.can_view_page(page_id));
create policy comments_insert on public.comments for insert to authenticated
  with check (public.can_view_page(page_id) and author_id = (select auth.uid()));
create policy comments_update on public.comments for update to authenticated
  using (author_id = (select auth.uid()) or public.can_edit_page(page_id))
  with check (public.can_view_page(page_id));
create policy comments_delete on public.comments for delete to authenticated
  using (author_id = (select auth.uid()) or public.page_role(page_id) = 'owner');

-- recordings
create policy recordings_select on public.recordings for select to authenticated
  using (public.can_view_page(page_id));
create policy recordings_insert on public.recordings for insert to authenticated
  with check (public.can_edit_page(page_id));
create policy recordings_update on public.recordings for update to authenticated
  using (public.can_edit_page(page_id)) with check (public.can_edit_page(page_id));
create policy recordings_delete on public.recordings for delete to authenticated
  using (public.can_edit_page(page_id));

-- files: the space owner only
create policy files_all on public.files for all to authenticated
  using (public.is_space_owner(space_id)) with check (public.is_space_owner(space_id));

-- ai_jobs: viewers may ask (the worker then runs without edit tools)
create policy ai_jobs_select on public.ai_jobs for select to authenticated
  using (user_id = (select auth.uid()));
create policy ai_jobs_insert on public.ai_jobs for insert to authenticated
  with check (user_id = (select auth.uid()) and public.can_view_page(page_id));
create policy ai_jobs_update on public.ai_jobs for update to authenticated
  using (user_id = (select auth.uid())) with check (user_id = (select auth.uid()));
create policy ai_jobs_delete on public.ai_jobs for delete to authenticated
  using (user_id = (select auth.uid()));

-- model_providers / stt_settings / devices: per user
create policy model_providers_all on public.model_providers for all to authenticated
  using (user_id = (select auth.uid())) with check (user_id = (select auth.uid()));
create policy stt_settings_all on public.stt_settings for all to authenticated
  using (user_id = (select auth.uid())) with check (user_id = (select auth.uid()));
create policy devices_all on public.devices for all to authenticated
  using (user_id = (select auth.uid())) with check (user_id = (select auth.uid()));

-- stt_secrets: no policies (service role only)

-- ---------------------------------------------------------------------------
-- Grants
-- ---------------------------------------------------------------------------

-- Clients never read or write Vault secret IDs. Table-level privileges would
-- override column-level revokes, so drop them and grant column by column.
-- Clients therefore select explicit columns from model_providers (no `*`).
revoke all on public.model_providers from anon, authenticated;
grant select (
  id, user_id, preset, name, base_url, auth_kind, expires_at, has_secret,
  default_model, is_local, supports_tools, models, created_at, updated_at
) on public.model_providers to authenticated;
-- id/user_id/timestamps are updatable so PostgREST upserts work; RLS keeps
-- user_id pinned to the caller and the trigger sets updated_at.
grant insert (
  id, user_id, preset, name, base_url, auth_kind, default_model, is_local,
  supports_tools, models, created_at, updated_at
) on public.model_providers to authenticated;
grant update (
  id, user_id, preset, name, base_url, auth_kind, default_model, is_local,
  supports_tools, models, created_at, updated_at
) on public.model_providers to authenticated;
grant delete on public.model_providers to authenticated;

revoke all on public.stt_secrets from anon, authenticated;

-- Nothing is readable without signing in.
revoke all on all tables in schema public from anon;

-- ---------------------------------------------------------------------------
-- RPCs
-- ---------------------------------------------------------------------------

-- Share a page (and its subpages) with an existing user by email.
create or replace function public.share_page(p_page_id uuid, p_email text, p_role text)
returns table (user_id uuid, display_name text, email text, role text)
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_user uuid;
  v_email text;
begin
  if public.page_role(p_page_id) is distinct from 'owner' then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  if p_role not in ('viewer', 'editor') then
    raise exception 'invalid_role' using errcode = '22023';
  end if;

  select u.id, u.email into v_user, v_email
  from auth.users u
  where lower(u.email) = lower(trim(p_email))
  limit 1;

  if v_user is null then
    raise exception 'no_user' using errcode = 'P0002';
  end if;
  if v_user = auth.uid() then
    raise exception 'cannot_share_with_self' using errcode = '22023';
  end if;

  insert into public.page_permissions (page_id, user_id, role, invited_by)
  values (p_page_id, v_user, p_role, auth.uid())
  on conflict on constraint page_permissions_pkey
    do update set role = excluded.role;

  return query
    select v_user, coalesce(pr.display_name, ''), v_email, p_role
    from (select 1) one
    left join public.profiles pr on pr.id = v_user;
end;
$$;

create or replace function public.unshare_page(p_page_id uuid, p_user_id uuid)
returns void
language plpgsql
security definer
set search_path = ''
as $$
begin
  if public.page_role(p_page_id) is distinct from 'owner' then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  delete from public.page_permissions pp
  where pp.page_id = p_page_id and pp.user_id = p_user_id;
end;
$$;

-- Everyone with access to a page: the owner, plus grants on the page or any
-- ancestor (the highest role wins). Callers must be able to view the page.
create or replace function public.page_members(p_page_id uuid)
returns table (user_id uuid, display_name text, email text, role text)
language plpgsql
stable
security definer
set search_path = ''
as $$
begin
  if not public.can_view_page(p_page_id) then
    raise exception 'forbidden' using errcode = '42501';
  end if;

  return query
  with recursive chain as (
    select p.id, p.parent_id, p.space_id, 0 as depth
    from public.pages p where p.id = p_page_id
    union all
    select p.id, p.parent_id, p.space_id, c.depth + 1
    from public.pages p join chain c on p.id = c.parent_id
    where c.depth < 64
  ),
  grants as (
    select pp.user_id as uid,
           max(case pp.role when 'editor' then 2 else 1 end) as rank
    from chain c
    join public.page_permissions pp on pp.page_id = c.id
    group by pp.user_id
  ),
  members as (
    select s.owner_id as uid, 'owner'::text as member_role, 0 as ord
    from chain c join public.spaces s on s.id = c.space_id
    where c.depth = 0
    union all
    select g.uid, case g.rank when 2 then 'editor' else 'viewer' end, 1
    from grants g
  )
  select m.uid, coalesce(pr.display_name, ''), u.email::text, m.member_role
  from members m
  join auth.users u on u.id = m.uid
  left join public.profiles pr on pr.id = m.uid
  order by m.ord, coalesce(pr.display_name, u.email::text);
end;
$$;

-- Store (or clear, with an empty secret) a provider's API key in Vault.
create or replace function public.set_provider_secret(p_provider_id uuid, p_secret text)
returns void
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_owner uuid;
  v_secret_id uuid;
begin
  select mp.user_id, mp.encrypted_secret into v_owner, v_secret_id
  from public.model_providers mp
  where mp.id = p_provider_id;

  if v_owner is null or v_owner is distinct from auth.uid() then
    raise exception 'provider_not_found' using errcode = 'P0002';
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

-- Decrypt a provider's API key. Edge Functions (service role) only.
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

-- Store (or clear) a speech-to-text key that has no model provider (Soniox).
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

create or replace function public.has_stt_secret(p_provider text)
returns boolean
language sql
stable
security definer
set search_path = ''
as $$
  select exists (
    select 1 from public.stt_secrets s
    where s.user_id = (select auth.uid()) and s.provider = p_provider
  );
$$;

-- Decrypt a user's speech-to-text key. Edge Functions (service role) only.
create or replace function public.get_stt_secret(p_user_id uuid, p_provider text)
returns text
language sql
stable
security definer
set search_path = ''
as $$
  select ds.decrypted_secret
  from public.stt_secrets s
  join vault.decrypted_secrets ds on ds.id = s.secret_id
  where s.user_id = p_user_id and s.provider = p_provider;
$$;

revoke execute on function public.get_provider_secret(uuid) from public, anon, authenticated;
revoke execute on function public.get_stt_secret(uuid, text) from public, anon, authenticated;
grant execute on function public.get_provider_secret(uuid) to service_role;
grant execute on function public.get_stt_secret(uuid, text) to service_role;

revoke execute on function public.share_page(uuid, text, text) from public, anon;
revoke execute on function public.unshare_page(uuid, uuid) from public, anon;
revoke execute on function public.page_members(uuid) from public, anon;
revoke execute on function public.set_provider_secret(uuid, text) from public, anon;
revoke execute on function public.set_stt_secret(text, text) from public, anon;
revoke execute on function public.has_stt_secret(text) from public, anon;
grant execute on function public.share_page(uuid, text, text) to authenticated;
grant execute on function public.unshare_page(uuid, uuid) to authenticated;
grant execute on function public.page_members(uuid) to authenticated;
grant execute on function public.set_provider_secret(uuid, text) to authenticated;
grant execute on function public.set_stt_secret(text, text) to authenticated;
grant execute on function public.has_stt_secret(text) to authenticated;

-- ---------------------------------------------------------------------------
-- Storage
-- ---------------------------------------------------------------------------

insert into storage.buckets (id, name, public)
values
  ('recordings', 'recordings', false),
  ('files', 'files', false),
  ('images', 'images', false)
on conflict (id) do nothing;

-- recordings/{user_id}/{recording_id}.m4a — written by its recorder; readable
-- by anyone who can view the page the recording belongs to.
create policy recordings_objects_own on storage.objects for all to authenticated
  using (bucket_id = 'recordings' and split_part(name, '/', 1) = (select auth.uid())::text)
  with check (bucket_id = 'recordings' and split_part(name, '/', 1) = (select auth.uid())::text);

create policy recordings_objects_shared_read on storage.objects for select to authenticated
  using (
    bucket_id = 'recordings'
    and exists (
      select 1 from public.recordings r
      where r.audio_path = storage.objects.name and public.can_view_page(r.page_id)
    )
  );

-- files/{space_id}/{file_id}/{name} — the space owner only.
create policy files_objects_owner on storage.objects for all to authenticated
  using (bucket_id = 'files' and public.owns_space_path(name))
  with check (bucket_id = 'files' and public.owns_space_path(name));

-- images/{space_id}/{image_id}.{ext} — written by the owner, readable by
-- anyone the space shares a page with.
create policy images_objects_owner on storage.objects for all to authenticated
  using (bucket_id = 'images' and public.owns_space_path(name))
  with check (bucket_id = 'images' and public.owns_space_path(name));

create policy images_objects_shared_read on storage.objects for select to authenticated
  using (bucket_id = 'images' and public.can_access_space_path(name));
