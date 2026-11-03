-- Invites from when they went to any account: scripts/check-migrations.sh
-- runs it, as postgres, right after 20261028070000_share_invites.sql. The
-- seeded page waits for the viewer, who confirmed their address, and for
-- someone who has not, whose invite 20261029030000 moves to wait for the
-- address instead.

insert into public.page_permissions (page_id, user_id, role, invited_by, created_at) values
  ('5eed0000-0000-0000-0000-0000000000a1', '5eed0000-0000-0000-0000-000000000004', 'viewer',
   '5eed0000-0000-0000-0000-000000000001', '2026-03-01'),
  ('5eed0000-0000-0000-0000-0000000000a1', '5eed0000-0000-0000-0000-000000000003', 'editor',
   '5eed0000-0000-0000-0000-000000000001', '2026-03-02');
insert into seed.marks
select 'invite_updated_at', updated_at from public.page_permissions
where page_id = '5eed0000-0000-0000-0000-0000000000a1' and user_id = '5eed0000-0000-0000-0000-000000000004';
