-- What a project could hold before files named only their own objects and
-- no live page stayed under one deleted for good: scripts/check-migrations.sh
-- runs it, as postgres, right after
-- 20261125000000_deletions_for_good_only_of_what_is_still_in_the_bin.sql.
-- A file of the owner's that names the editor's object, one that names its
-- own, and a page deleted for good with a live page and a deleted one
-- still under it. seeded.sql checks what 20261126000000 made of them.

insert into public.files (id, space_id, name, storage_path)
select '5eed0000-0000-0000-0000-0000000000e1', s.id, 'theirs.txt',
  e.id || '/5eed0000-0000-0000-0000-0000000000e9/theirs.txt'
from public.spaces s, public.spaces e
where s.owner_id = '5eed0000-0000-0000-0000-000000000001' and e.owner_id = '5eed0000-0000-0000-0000-000000000002';
insert into public.files (id, space_id, name, storage_path)
select '5eed0000-0000-0000-0000-0000000000e2', s.id, 'own.txt', s.id || '/5eed0000-0000-0000-0000-0000000000e2/own.txt'
from public.spaces s where s.owner_id = '5eed0000-0000-0000-0000-000000000001';

insert into public.pages (id, space_id, title, deleted_at, purged_at)
select '5eed0000-0000-0000-0000-0000000000e3', s.id, '', now(), now()
from public.spaces s where s.owner_id = '5eed0000-0000-0000-0000-000000000001';
insert into public.pages (id, space_id, parent_id, title, deleted_at)
select v.id, s.id, '5eed0000-0000-0000-0000-0000000000e3', v.title, v.deleted_at
from public.spaces s,
  (values ('5eed0000-0000-0000-0000-0000000000e4'::uuid, 'Live under it', null::timestamptz),
          ('5eed0000-0000-0000-0000-0000000000e5'::uuid, 'Deleted under it', now())) v (id, title, deleted_at)
where s.owner_id = '5eed0000-0000-0000-0000-000000000001';
