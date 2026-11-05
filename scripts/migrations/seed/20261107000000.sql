-- A tree under the seeded page 300 pages deep, as a project could have one
-- before a tree was kept to 256: scripts/check-migrations.sh runs it, as
-- postgres, right after 20261107000000_page_saves_on_realtime.sql.
-- seeded.sql checks it stays read to its deepest page. And a branch of the
-- shared page whose pages are moved and deleted there, past when the editor
-- had them: Moved, with Gone, and Top and Under inside it; There, for Moved
-- to go under; and Behind.

insert into public.pages (id, space_id, parent_id, title)
select v.id, p.space_id, v.parent, v.title
from public.pages p
cross join (values
  ('5eed0000-0000-0000-0000-0000000000d1'::uuid, '5eed0000-0000-0000-0000-0000000000a1'::uuid, 'Moved'),
  ('5eed0000-0000-0000-0000-0000000000d2', '5eed0000-0000-0000-0000-0000000000a1', 'There'),
  ('5eed0000-0000-0000-0000-0000000000d3', '5eed0000-0000-0000-0000-0000000000d1', 'Gone'),
  ('5eed0000-0000-0000-0000-0000000000d4', '5eed0000-0000-0000-0000-0000000000d1', 'Top'),
  ('5eed0000-0000-0000-0000-0000000000d5', '5eed0000-0000-0000-0000-0000000000d4', 'Under'),
  ('5eed0000-0000-0000-0000-0000000000d6', '5eed0000-0000-0000-0000-0000000000a1', 'Behind')
) v (id, parent, title)
where p.id = '5eed0000-0000-0000-0000-0000000000a1';

do $$
declare
  v_parent uuid := '5eed0000-0000-0000-0000-0000000000a1';
  v_space uuid := (select space_id from public.pages where id = v_parent);
  v_page uuid;
begin
  for i in 1 .. 300 loop
    insert into public.pages (space_id, parent_id, title)
    values (v_space, v_parent, 'Deep ' || i)
    returning id into v_page;
    v_parent := v_page;
  end loop;
end;
$$;
