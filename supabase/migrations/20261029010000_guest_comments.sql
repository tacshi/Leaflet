-- Visitors of a page's public link comment as guests. The link is served
-- from the owner's device, which stores the comment and syncs it as the
-- owner's, so author_id names the owner and agent_name keeps the name the
-- visitor gave (at most 80 characters), as it does an agent's.

alter table public.comments drop constraint if exists comments_author_kind_check;
alter table public.comments add constraint comments_author_kind_check
  check (author_kind in ('user', 'agent', 'guest'));

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
