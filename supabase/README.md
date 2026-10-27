# Leaflet backend (Supabase)

Postgres schema, row-level security, storage buckets, and four Edge Functions.
The contract with the Go client is [`../docs/protocol.md`](../docs/protocol.md).

```
supabase/
  config.toml
  migrations/20261025000000_init.sql   tables, RLS, RPCs, Vault helpers, buckets
  migrations/20261027000000_bind_attachment_paths.sql   attachment paths bound to their rows
  migrations/20261027120000_catch_up_init.sql           what init gained after it shipped
  functions/
    _shared/        cors, clients, OpenAI-compatible client, blocks, tools, prompt
    agent/          NDJSON agent worker (protocol §4)
    providers/      save / models / test / delete model providers
    stt/            Soniox temporary keys, Soniox key storage, file transcription
    account/        account deletion
```

Each schema change is a new migration: `supabase db push` never runs one a
project has applied again, so an edit to it reaches no existing project.

## Local development

Needs the [Supabase CLI](https://supabase.com/docs/guides/cli) and Docker.

```bash
supabase start                 # Postgres, Auth, Storage, Studio on :54321–54324
supabase db reset              # apply migrations/ to a fresh database
supabase functions serve       # serve every function with hot reload
```

`supabase start` prints the local API URL and anon key. Run the app against it:

```bash
SUPABASE_URL=http://127.0.0.1:54321 SUPABASE_ANON_KEY=<anon key from supabase start> go tool mygo dev
```

Without these variables the app runs local-only: pages, editing, recording and
local models work; sign-in, sync, sharing, cloud models and cloud speech to
text are off.

### Tests

```bash
cd supabase/functions
deno test _shared/             # blocks, inline markdown, tools, op replay/undo
deno check */index.ts
deno lint
```

## Deploying

```bash
supabase link --project-ref <ref>
supabase db push
supabase functions deploy agent providers stt account
supabase secrets set SONIOX_API_KEY=<optional app-wide Soniox key>
```

`SUPABASE_URL`, `SUPABASE_ANON_KEY` and `SUPABASE_SERVICE_ROLE_KEY` are
provided to functions automatically. `SONIOX_API_KEY` is optional: users can
save their own Soniox key (Settings › Speech to text), and the app-wide key is
the fallback for minting short-lived streaming keys.

Supabase Vault (`supabase_vault`) is enabled on every Supabase project; the
migration relies on `vault.create_secret`, `vault.update_secret` and
`vault.decrypted_secrets`.

## Notes for the client

- **model_providers**: clients can't read or write `encrypted_secret` /
  `refresh_token`, so select explicit columns (see `PROVIDER_COLUMNS` in
  `functions/_shared/supabase.ts`), never `*`. `has_secret` says whether a key
  is saved. Keys go through the `providers` function (`save` with `api_key`)
  or the `set_provider_secret` RPC; an empty key clears it.
- **Local providers** (`ollama`, `lmstudio`, `local`) may be saved without a
  key for listing, but the server never calls them and rejects their keys.
  Clients call local endpoints directly.
- **Account sign-in** (`auth_kind: "account"`) is rejected by `providers.save`
  until provider terms for third-party apps are confirmed (open question in the
  design); use API keys.
- **Soniox keys** live in `stt_secrets` (service role only), not in
  `model_providers`. `has_stt_secret('soniox')` tells the client whether one is
  saved.
- **updated_at** is always set by the server (`clock_timestamp()`), on insert
  and update. Pull with `updated_at > cursor`; soft-deleted rows
  (`deleted_at`) come through the same pull.
- **ai_jobs**: viewers may create jobs; the worker then runs without edit
  tools. The worker claims a job by moving it from `queued` to `running`; a
  second call returns 409.
- **Subpages created by agents** get `sort_key` = the last sibling's key + `m`
  (or `a0` for the first child), which sorts after every existing sibling.
- **stt_settings.provider** values: `qwen3-asr`, `moss-transcribe-diarize`,
  `sensevoice`, `soniox`, `openai-compatible`. The server transcribes only
  `soniox` and OpenAI-compatible endpoints (`provider_id` or `base_url` set);
  on-device models transcribe on the device.
- **Storage**: viewers of a shared page can read its recordings, the files
  on it and the owner's images; only the space owner writes `files/` and
  `images/`.
