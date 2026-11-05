-- Checks ensure_page_saves_heard once Realtime has made its messages, as
-- it does on a project in its own time: scripts/check-migrations.sh runs
-- it last, as supabase_admin, since the image has no Realtime. It stands in
-- what the policy names, then goes on as postgres, as the app does. It
-- changes nothing: it rolls back at the end. A check that fails raises, and
-- psql stops there.

\set ON_ERROR_STOP on
\set VERBOSITY terse

begin;

create schema if not exists realtime;
grant usage on schema realtime to postgres, authenticated;
create table realtime.messages (
  id uuid primary key default gen_random_uuid(),
  topic text not null,
  extension text not null,
  payload jsonb
);
alter table realtime.messages owner to postgres;
alter table realtime.messages enable row level security;
grant select on realtime.messages to authenticated;
create function realtime.topic() returns text
language sql stable
as $$ select nullif(current_setting('realtime.topic', true), '') $$;
grant execute on function realtime.topic() to authenticated;
insert into realtime.messages (topic, extension) values ('page:any', 'broadcast');

set local role postgres;

-- Made once, and only once, whenever it is asked.
do $$
begin
  if not public.ensure_page_saves_heard() or not public.ensure_page_saves_heard() then
    raise exception 'the policy for a page''s saves was not made with the messages there';
  end if;
  if (select count(*) from pg_policy where polrelid = 'realtime.messages'::regclass and polname = 'page_saves_heard') <> 1 then
    raise exception 'the policy for a page''s saves is not there once';
  end if;
end;
$$;

-- Who may read a page hears its channel, a stranger none, and no channel
-- that is no page's.
insert into auth.users (id, email, email_confirmed_at) values
  ('ea100000-0000-0000-0000-000000000001', 'realtime-owner@example.com', now()),
  ('ea100000-0000-0000-0000-000000000002', 'realtime-stranger@example.com', now());
insert into public.pages (id, space_id, title)
select 'ea100000-0000-0000-0000-0000000000a1', s.id, 'Heard'
from public.spaces s where s.owner_id = 'ea100000-0000-0000-0000-000000000001';

create function pg_temp.hears(p_user uuid, p_topic text) returns boolean
language plpgsql as $$
declare
  v_hears boolean;
begin
  perform set_config('request.jwt.claim.sub', p_user::text, true);
  perform set_config('realtime.topic', p_topic, true);
  set local role authenticated;
  select exists (select 1 from realtime.messages) into v_hears;
  reset role;
  return v_hears;
end;
$$;

do $$
declare
  v_owner constant uuid := 'ea100000-0000-0000-0000-000000000001';
  v_topic constant text := 'page:ea100000-0000-0000-0000-0000000000a1';
begin
  if not pg_temp.hears(v_owner, v_topic) then
    raise exception 'the page''s owner does not hear its channel';
  end if;
  if pg_temp.hears('ea100000-0000-0000-0000-000000000002', v_topic) then
    raise exception 'a stranger hears a page''s channel';
  end if;
  if pg_temp.hears(v_owner, 'page:not-a-page') then
    raise exception 'a channel that is no page''s is heard';
  end if;
end;
$$;

rollback;
