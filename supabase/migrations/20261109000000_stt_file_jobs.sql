-- stt_file_jobs: an audio file being transcribed, as stt_jobs is for a
-- recording, by the person whose key it spends. The stt function alone
-- reads and writes it. claimed_at says since when a call has it; the rest
-- is the transcription Soniox has, kept until the app says it has its
-- transcript, as nothing else holds it: the function saves it nowhere. It
-- goes with its file, or its caller's account; what is at Soniox then goes
-- by Soniox's own retention, in 30 days.
create table if not exists public.stt_file_jobs (
  source_file_id uuid not null references public.files (id) on delete cascade,
  user_id uuid not null references auth.users (id) on delete cascade,
  file_id text,
  transcription_id text,
  model text,
  claimed_at timestamptz,
  created_at timestamptz not null default now(),
  primary key (source_file_id, user_id)
);

create index if not exists stt_file_jobs_user_idx on public.stt_file_jobs (user_id);

alter table public.stt_file_jobs enable row level security;
revoke all on public.stt_file_jobs from public, anon, authenticated, service_role;
grant select, insert, update, delete on public.stt_file_jobs to service_role;
