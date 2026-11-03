-- A guest's comment, deleted, from before deleting a comment emptied it:
-- scripts/check-migrations.sh runs it, as postgres, right after
-- 20261029010000_guest_comments.sql.

insert into public.comments (id, page_id, block_id, author_id, author_kind, agent_name, body, deleted_at) values
  ('5eed0000-0000-0000-0000-0000000000c2', '5eed0000-0000-0000-0000-0000000000a1', 'b1',
   '5eed0000-0000-0000-0000-000000000001', 'guest', 'Ada Lovelace', 'Hello from the link', now());
