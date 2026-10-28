-- Who wrote a row is the signed-in caller, not what the client sends: the
-- columns that name a writer are set from auth.uid() here. Writes with no
-- caller (the service role, and the ON DELETE SET NULL cascades when an
-- account is deleted) keep what they set.

-- page_content.updated_by: the last writer.
create or replace function public.stamp_page_content_writer()
returns trigger
language plpgsql
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
begin
  if v_uid is not null then
    new.updated_by := v_uid;
  end if;
  return new;
end;
$$;

drop trigger if exists page_content_stamp_writer on public.page_content;
create trigger page_content_stamp_writer before insert or update on public.page_content
  for each row execute function public.stamp_page_content_writer();

-- page_history.created_by, and ai_job_id only for one of the caller's own
-- jobs on the same page (anything else is dropped, not refused, so a
-- snapshot is never lost over it).
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
  if new.ai_job_id is not null and not exists (
    select 1 from public.ai_jobs j
    where j.id = new.ai_job_id and j.user_id = v_uid and j.page_id = new.page_id
  ) then
    new.ai_job_id := null;
  end if;
  return new;
end;
$$;

drop trigger if exists page_history_stamp_writer on public.page_history;
create trigger page_history_stamp_writer before insert on public.page_history
  for each row execute function public.stamp_page_history_writer();

-- page_permissions.invited_by: who shared the page, set once.
create or replace function public.stamp_page_permission_inviter()
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
  if tg_op = 'INSERT' then
    new.invited_by := v_uid;
  else
    new.invited_by := old.invited_by;
  end if;
  return new;
end;
$$;

drop trigger if exists page_permissions_stamp_inviter on public.page_permissions;
create trigger page_permissions_stamp_inviter before insert or update on public.page_permissions
  for each row execute function public.stamp_page_permission_inviter();

-- comments: the author is the caller (as before). A client may post an agent
-- comment, since agents that run on the device reply through it, but it is
-- always the caller's: author_id names who asked, agent_name is set once
-- (at most 80 characters; later changes are ignored), and a user's comment
-- has none.
-- The agent function posts its replies with the service role.
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
