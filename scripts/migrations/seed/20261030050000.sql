-- Subpages of the seeded page that its owner deleted before
-- 20261031000000 gave every page there was a placed_at, the time it ran:
-- scripts/check-migrations.sh runs it, as postgres, right after
-- 20261030050000_cap_named_history.sql. One, under a subpage still there,
-- was deleted after the editor's share was accepted (2026-01-02), and
-- stays theirs to read; the other before, and is not. Each has content
-- and a comment.

insert into public.pages (id, space_id, parent_id, title, created_at)
select '5eed0000-0000-0000-0000-0000000000a3', s.id, '5eed0000-0000-0000-0000-0000000000a1', 'Kept', '2026-01-01'
from public.spaces s where s.owner_id = '5eed0000-0000-0000-0000-000000000001';
insert into public.pages (id, space_id, parent_id, title, created_at, deleted_at)
select '5eed0000-0000-0000-0000-0000000000a4', s.id, '5eed0000-0000-0000-0000-0000000000a3',
       'Deleted after the share', '2026-01-01', '2026-06-01'
from public.spaces s where s.owner_id = '5eed0000-0000-0000-0000-000000000001';
insert into public.pages (id, space_id, parent_id, title, created_at, deleted_at)
select '5eed0000-0000-0000-0000-0000000000a5', s.id, '5eed0000-0000-0000-0000-0000000000a1',
       'Deleted before the share', '2026-01-01', '2026-01-01 12:00'
from public.spaces s where s.owner_id = '5eed0000-0000-0000-0000-000000000001';

insert into public.page_content (page_id) values
  ('5eed0000-0000-0000-0000-0000000000a4'), ('5eed0000-0000-0000-0000-0000000000a5');
insert into public.comments (page_id, block_id, author_id, body) values
  ('5eed0000-0000-0000-0000-0000000000a4', 'b1', '5eed0000-0000-0000-0000-000000000001', 'After'),
  ('5eed0000-0000-0000-0000-0000000000a5', 'b1', '5eed0000-0000-0000-0000-000000000001', 'Before');
