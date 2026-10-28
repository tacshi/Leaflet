-- share_page answered "no_user" for an email with no account, so anyone
-- could find out who has one. It now returns no rows whether or not the
-- email has an account; the page is shared directly when it does, and the
-- caller reads who has access with page_members.

create or replace function public.share_page(p_page_id uuid, p_email text, p_role text)
returns table (user_id uuid, display_name text, email text, role text)
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_user uuid;
begin
  if public.page_role(p_page_id) is distinct from 'owner' then
    raise exception 'forbidden' using errcode = '42501';
  end if;
  if p_role not in ('viewer', 'editor') then
    raise exception 'invalid_role' using errcode = '22023';
  end if;

  select u.id into v_user
  from auth.users u
  where lower(u.email) = lower(trim(p_email))
  limit 1;

  if v_user is null then
    return;
  end if;
  if v_user = auth.uid() then
    raise exception 'cannot_share_with_self' using errcode = '22023';
  end if;

  insert into public.page_permissions (page_id, user_id, role, invited_by)
  values (p_page_id, v_user, p_role, auth.uid())
  on conflict on constraint page_permissions_pkey
    do update set role = excluded.role;
end;
$$;
