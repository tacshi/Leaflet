-- What Supabase's Auth and Storage services add to a project's database,
-- as far as Leaflet's migrations use it. A hosted project, and supabase
-- start, have the services make these themselves; the supabase/postgres
-- image alone has an early auth.users and an empty storage schema.
-- scripts/check-migrations.sh runs this as supabase_admin first.

alter table auth.users add column if not exists email_confirmed_at timestamptz;

-- Auth's sessions, which its refresh tokens belong to, and auth.jwt(),
-- the caller's claims. Made as supabase_auth_admin, as Auth makes them,
-- so the image's default privileges give postgres what a project gives
-- it: sign_out_device deletes a session, as postgres.
set role supabase_auth_admin;
create table if not exists auth.sessions (
  id uuid primary key,
  user_id uuid not null references auth.users (id) on delete cascade,
  created_at timestamptz default now(),
  updated_at timestamptz default now()
);
alter table auth.refresh_tokens
  add column if not exists session_id uuid references auth.sessions (id) on delete cascade;
do $$
begin
  if to_regprocedure('auth.jwt()') is null then
    create function auth.jwt() returns jsonb
    language sql stable
    as $f$
      select coalesce(
        nullif(current_setting('request.jwt.claim', true), ''),
        nullif(current_setting('request.jwt.claims', true), '')
      )::jsonb
    $f$;
  end if;
end;
$$;
reset role;

create table if not exists storage.buckets (
  id text primary key,
  name text not null unique,
  owner uuid,
  public boolean default false,
  file_size_limit bigint,
  allowed_mime_types text[],
  created_at timestamptz default now(),
  updated_at timestamptz default now()
);

create table if not exists storage.objects (
  id uuid primary key default gen_random_uuid(),
  bucket_id text references storage.buckets (id),
  name text,
  owner uuid,
  metadata jsonb,
  created_at timestamptz default now(),
  updated_at timestamptz default now(),
  unique (bucket_id, name)
);

alter table storage.objects enable row level security;
alter table storage.buckets owner to postgres;
alter table storage.objects owner to postgres;
grant usage on schema storage to anon, authenticated, service_role;
grant all on storage.buckets, storage.objects to anon, authenticated, service_role;
