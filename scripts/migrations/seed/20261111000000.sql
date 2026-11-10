-- Folders in loops, as two devices that each moved one of two folders into
-- the other left them before a folder was kept out of its own:
-- scripts/check-migrations.sh runs it, as postgres, right after
-- 20261111000000_files_stay_in_their_space_and_old_functions_read_keys.sql.
-- seeded.sql checks each loop is opened at its folder of the smallest id.
-- And a recording the editor made on the owner's page, with its audio,
-- which seeded.sql checks stays when the editor's account goes.
-- Two folders in each other, with one inside that is in no loop itself;
-- three in a ring, the smallest id not the first made; a folder in
-- another, as folders are; and three in a ring one of which is deleted,
-- which is no loop: the folder in the deleted one is at the top level.

insert into public.folders (id, space_id, name)
select v.id, s.id, v.name
from public.spaces s
cross join (values
  ('5eed0000-0000-0000-0000-0000000000f2'::uuid, 'Two, second'),
  ('5eed0000-0000-0000-0000-0000000000f1', 'Two, first'),
  ('5eed0000-0000-0000-0000-0000000000f3', 'Inside the two'),
  ('5eed0000-0000-0000-0000-0000000000f6', 'Three, third'),
  ('5eed0000-0000-0000-0000-0000000000f5', 'Three, second'),
  ('5eed0000-0000-0000-0000-0000000000f4', 'Three, first'),
  ('5eed0000-0000-0000-0000-0000000000f7', 'Outer'),
  ('5eed0000-0000-0000-0000-0000000000f8', 'Inner'),
  ('5eed0000-0000-0000-0000-0000000000fa', 'Ring, first'),
  ('5eed0000-0000-0000-0000-0000000000fb', 'Ring, second'),
  ('5eed0000-0000-0000-0000-0000000000fc', 'Ring, deleted')
) v (id, name)
where s.owner_id = '5eed0000-0000-0000-0000-000000000001';

update public.folders f set parent_folder_id = v.parent
from (values
  ('5eed0000-0000-0000-0000-0000000000f1'::uuid, '5eed0000-0000-0000-0000-0000000000f2'::uuid),
  ('5eed0000-0000-0000-0000-0000000000f2', '5eed0000-0000-0000-0000-0000000000f1'),
  ('5eed0000-0000-0000-0000-0000000000f3', '5eed0000-0000-0000-0000-0000000000f2'),
  ('5eed0000-0000-0000-0000-0000000000f4', '5eed0000-0000-0000-0000-0000000000f5'),
  ('5eed0000-0000-0000-0000-0000000000f5', '5eed0000-0000-0000-0000-0000000000f6'),
  ('5eed0000-0000-0000-0000-0000000000f6', '5eed0000-0000-0000-0000-0000000000f4'),
  ('5eed0000-0000-0000-0000-0000000000f8', '5eed0000-0000-0000-0000-0000000000f7'),
  ('5eed0000-0000-0000-0000-0000000000fa', '5eed0000-0000-0000-0000-0000000000fb'),
  ('5eed0000-0000-0000-0000-0000000000fb', '5eed0000-0000-0000-0000-0000000000fc'),
  ('5eed0000-0000-0000-0000-0000000000fc', '5eed0000-0000-0000-0000-0000000000fa')
) v (id, parent)
where f.id = v.id;
update public.folders set deleted_at = now() where id = '5eed0000-0000-0000-0000-0000000000fc';

insert into seed.marks values ('folders_looped', clock_timestamp());

insert into public.recordings (id, page_id, audio_path, status, created_by) values (
  '5eed0000-0000-0000-0000-0000000000b1', '5eed0000-0000-0000-0000-0000000000a1',
  '5eed0000-0000-0000-0000-000000000002/5eed0000-0000-0000-0000-0000000000b1.wav', 'ready',
  '5eed0000-0000-0000-0000-000000000002'
);
insert into storage.objects (bucket_id, name, owner) values (
  'recordings', '5eed0000-0000-0000-0000-000000000002/5eed0000-0000-0000-0000-0000000000b1.wav',
  '5eed0000-0000-0000-0000-000000000002'
);
