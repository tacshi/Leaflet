-- Rows a project had before the migrations after init, so that those
-- that move data move some: scripts/check-migrations.sh runs it, as
-- postgres, right after 20261025000000_init.sql, and seeded.sql checks
-- what the later migrations made of the rows. It keeps them: test.sql and
-- access.sql use other people and pages.
--
-- An owner, the editor they shared a page with (before invites, so the
-- share stands), a viewer and someone who has not confirmed their address,
-- whom seed/20261028070000.sql invites.

insert into auth.users (id, email, email_confirmed_at) values
  ('5eed0000-0000-0000-0000-000000000001', 'seed-owner@example.com', now()),
  ('5eed0000-0000-0000-0000-000000000002', 'seed-editor@example.com', now()),
  ('5eed0000-0000-0000-0000-000000000003', 'seed-unconfirmed@example.com', null),
  ('5eed0000-0000-0000-0000-000000000004', 'seed-viewer@example.com', now());

-- What seeded.sql compares with: when this ran, and what it set that a
-- migration should leave as it was.
create schema seed;
create table seed.marks (name text primary key, at timestamptz not null);
insert into seed.marks values ('seeded', now());

-- A page and its subpage, made long before.
insert into public.pages (id, space_id, title, created_at)
select '5eed0000-0000-0000-0000-0000000000a1', s.id, 'Seeded', '2026-01-01'
from public.spaces s where s.owner_id = '5eed0000-0000-0000-0000-000000000001';
insert into public.pages (id, space_id, parent_id, title, created_at)
select '5eed0000-0000-0000-0000-0000000000a2', s.id, '5eed0000-0000-0000-0000-0000000000a1', 'Seeded subpage', '2026-01-01'
from public.spaces s where s.owner_id = '5eed0000-0000-0000-0000-000000000001';

-- Its content, at a revision someone set out of all reach.
insert into public.page_content (page_id, revision)
values ('5eed0000-0000-0000-0000-0000000000a1', 1152921504606846976);

-- The share with the editor.
insert into public.page_permissions (page_id, user_id, role, invited_by, created_at)
values ('5eed0000-0000-0000-0000-0000000000a1', '5eed0000-0000-0000-0000-000000000002', 'editor',
        '5eed0000-0000-0000-0000-000000000001', '2026-01-02');
insert into seed.marks
select 'share_updated_at', updated_at from public.page_permissions
where page_id = '5eed0000-0000-0000-0000-0000000000a1' and user_id = '5eed0000-0000-0000-0000-000000000002';

-- A comment deleted with its text still in it.
insert into public.comments (id, page_id, block_id, author_id, body, quote, mentions, deleted_at) values
  ('5eed0000-0000-0000-0000-0000000000c1', '5eed0000-0000-0000-0000-0000000000a1', 'b1',
   '5eed0000-0000-0000-0000-000000000002', 'A secret', 'quoted', '["5eed0000-0000-0000-0000-000000000001"]', now());

-- 55 unnamed snapshots of the page by its owner, 52 named ones by the
-- editor, and one of the subpage dated ahead.
insert into public.page_history (page_id, content, reason, created_by, created_at)
select '5eed0000-0000-0000-0000-0000000000a1', '{}', 'autosave', '5eed0000-0000-0000-0000-000000000001',
       '2026-01-01'::timestamptz + n * interval '1 minute'
from generate_series(1, 55) n;
insert into public.page_history (page_id, content, reason, created_by, created_at)
select '5eed0000-0000-0000-0000-0000000000a1', '{}', 'manual', '5eed0000-0000-0000-0000-000000000002',
       '2026-02-01'::timestamptz + n * interval '1 minute'
from generate_series(1, 52) n;
insert into public.page_history (page_id, content, reason, created_by, created_at)
values ('5eed0000-0000-0000-0000-0000000000a2', '{}', 'autosave', '5eed0000-0000-0000-0000-000000000001', '2999-01-01');

-- And a row of the table that goes unused.
insert into public.page_updates (page_id, seq, update)
values ('5eed0000-0000-0000-0000-0000000000a1', 1, '\x00');
