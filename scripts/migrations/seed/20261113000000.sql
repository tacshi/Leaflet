-- What the functions of the builds before agents ran on the device left in
-- a project: scripts/check-migrations.sh runs it, as postgres, right after
-- 20261113000000_restored_pages_reach_their_readers.sql. A provider with
-- its key and refresh token in Vault, a speech-to-text key there, and a
-- page with a job and a history snapshot that names it; and a secret in
-- Vault that is none of theirs. seeded.sql checks the keys are gone, and
-- that the snapshot and the other secret stay.

insert into public.model_providers (id, user_id, preset, encrypted_secret, refresh_token) values (
  '5eed0000-0000-0000-0000-0000000000c1', '5eed0000-0000-0000-0000-000000000001', 'openai',
  vault.create_secret('seed-provider-key', 'seed_provider_key'),
  vault.create_secret('seed-provider-refresh', 'seed_provider_refresh')
);
insert into public.stt_secrets (user_id, provider, secret_id) values (
  '5eed0000-0000-0000-0000-000000000001', 'soniox', vault.create_secret('seed-stt-key', 'seed_stt_key')
);
select vault.create_secret('seed-other', 'seed_other');

insert into public.pages (id, space_id, title)
select '5eed0000-0000-0000-0000-0000000000c4', s.id, 'An agent wrote here'
from public.spaces s where s.owner_id = '5eed0000-0000-0000-0000-000000000001';
insert into public.ai_jobs (id, user_id, page_id, surface, provider_id) values (
  '5eed0000-0000-0000-0000-0000000000c2', '5eed0000-0000-0000-0000-000000000001',
  '5eed0000-0000-0000-0000-0000000000c4', 'sidebar', '5eed0000-0000-0000-0000-0000000000c1'
);
insert into public.page_history (id, page_id, content, reason, ai_job_id, created_by) values (
  '5eed0000-0000-0000-0000-0000000000c3', '5eed0000-0000-0000-0000-0000000000c4', '{}', 'agent_run',
  '5eed0000-0000-0000-0000-0000000000c2', '5eed0000-0000-0000-0000-000000000001'
);
