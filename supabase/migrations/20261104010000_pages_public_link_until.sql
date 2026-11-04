-- A public link is served by the desktop app that opened it, and only it
-- knew: an agent run on another device, or in the agent function, read
-- the user's other pages into a page anyone with the link reads. The app
-- that serves a link now keeps public_link_until on its page while it
-- does, a lease it renews well before it runs out and clears as the link
-- stops (agents' ServePublicLink); an app that quit without clearing it
-- leaves a lease that runs out within the hour. While the time is to
-- come, the agent function's runs and the apps' read none of the user's
-- other pages into the page. Pages sync without it: the apps ask for it
-- as a run starts. The lease is set through lease_public_link, below, by
-- the server's clock, which everyone compares it with. An editor who sets
-- it by hand can do no more than keep agents from reading other pages
-- into a page shared with them, which they do not on a shared page
-- anyway.

alter table public.pages add column if not exists public_link_until timestamptz;

-- The lease moves no page on: updated_at is when a page last changed, and
-- what the apps pull by, and a lease renewed every quarter of an hour
-- would have every device pull the page again each time, and show it as
-- just edited. An update that changes the lease and nothing else keeps
-- updated_at; any other update, one of updated_at alone among them, moves
-- it on as before.
drop trigger if exists pages_set_updated_at on public.pages;
create trigger pages_set_updated_at before insert on public.pages
  for each row execute function public.set_updated_at();
create trigger pages_set_updated_at_on_update before update on public.pages
  for each row
  when (new.public_link_until is not distinct from old.public_link_until
    or (to_jsonb(new) - 'public_link_until') is distinct from (to_jsonb(old) - 'public_link_until'))
  execute function public.set_updated_at();

-- Renews page p_page_id's public link lease, to an hour on by the
-- server's clock, and returns it: a device's own clock may be off by
-- more than the lease. Given p_ending, the lease the caller last got, it
-- ends that lease instead, and only while the page still has it: a newer
-- one, as of the link started again for other addresses, whose renewal
-- may land first, stays. Only someone who may edit the page leases it;
-- it runs as its owner to write the lease alone, whatever else the
-- caller's update of the page would have to pass.
create or replace function public.lease_public_link(p_page_id uuid, p_ending timestamptz default null)
returns timestamptz
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_until timestamptz;
begin
  if not public.can_edit_page(p_page_id) then
    raise exception 'forbidden_public_link' using errcode = '42501';
  end if;
  if p_ending is not null then
    update public.pages set public_link_until = null
    where id = p_page_id and public_link_until = p_ending;
    return null;
  end if;
  update public.pages set public_link_until = now() + interval '1 hour'
  where id = p_page_id
  returning public_link_until into v_until;
  return v_until;
end;
$$;

revoke execute on function public.lease_public_link(uuid, timestamptz) from public, anon, authenticated, service_role;
grant execute on function public.lease_public_link(uuid, timestamptz) to authenticated;
