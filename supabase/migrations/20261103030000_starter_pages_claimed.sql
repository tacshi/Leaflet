-- A new device starts with starter pages, which are a library's only when
-- the device starts it: one that joins a library other devices made
-- leaves its own out. A device told the two apart by whether the space
-- had pages yet, so two devices signing in to a new account within the
-- same few seconds both found none, and the library got two sets. The
-- first device to ask now claims the space for its starter pages
-- (claim_starter_pages), in one update that only one caller wins; any
-- other is told the library is another's. The claim names the device, so
-- the one that won still has it when it asks again, as after a sign-in
-- that did not finish.

alter table public.spaces add column if not exists starters_device uuid;

-- Claims the caller's space for the starter pages of device p_device:
-- true when it has them, the first to ask or the same device again, and
-- false when another device has, or the space is not the caller's. It
-- runs as the caller, whose own space spaces_update lets them change.
create or replace function public.claim_starter_pages(p_space_id uuid, p_device uuid)
returns boolean
language sql
set search_path = ''
as $$
  with claimed as (
    update public.spaces s
    set starters_device = p_device
    where s.id = p_space_id
      and s.owner_id = (select auth.uid())
      and (s.starters_device is null or s.starters_device = p_device)
    returning 1
  )
  select exists (select 1 from claimed);
$$;

revoke execute on function public.claim_starter_pages(uuid, uuid) from public, anon, authenticated, service_role;
grant execute on function public.claim_starter_pages(uuid, uuid) to authenticated;
