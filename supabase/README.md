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
  embed.go          carries migrations/ and functions/ into the app
```

Each schema change is a new migration: `supabase db push` never runs one a
project has applied again, so an edit to it reaches no existing project.

## Sign in with Supabase

People sync through a project in their own Supabase account. **Continue with
Supabase** signs in to the Management API, uses the account's Leaflet project
(one whose migration history has `20261025000000`), or creates one named
Leaflet, then sets it up from this folder, built into the app:

- the migrations it lacks, each recorded in `supabase_migrations` as
  `supabase db push` records them;
- the four functions, unless the `LEAFLET_FUNCTIONS_HASH` secret says they
  are current;
- a 500 MiB upload limit, where the plan allows it.

It then signs in to the project's Auth as the account's email, making that
user with the project's secret key, which is used once and not kept. The
account's refresh token stays in the keychain, and a newer app brings the
project up to date when it starts.

This needs one OAuth app, registered once in a Supabase organization
(Organization settings › OAuth Apps):

- Redirect URL: `https://leaflet-auth.capyvibe.app/auth/supabase`, the sign-in
  Worker's page (below).
- Scopes: Organizations read; Projects read and write; Database read and
  write; Edge Functions write; Secrets read and write (the project's API
  keys are read under Secrets); Storage write.

Its client ID is built in (`ClientID` in
[`internal/supabaseapi`](../internal/supabaseapi/supabaseapi.go)). Its client
secret, which Supabase wants with every code and refresh token, PKCE or not,
ships in no app: the sign-in Worker in [`../worker`](../worker) holds it.

### The sign-in Worker

A Cloudflare Worker with two routes:

- `GET /auth/supabase`, the OAuth app's redirect URL. Supabase takes only
  https ones, which a desktop app can't answer, so the page sends the browser
  on to the app waiting on `http://127.0.0.1:51741/auth/callback`, with the
  code and state.
- `POST /token`, Supabase's token endpoint with the client secret added. It
  takes only Leaflet's client ID, codes with their PKCE verifier and its own
  redirect URL, and refresh tokens.

Deploy it from [`../worker`](../worker):

```bash
npx wrangler login
npx wrangler secret put CLIENT_SECRET
npx wrangler deploy
```

Its address goes in `Auth` in
[`internal/supabaseapi`](../internal/supabaseapi/supabaseapi.go), and its
`/auth/supabase` in the OAuth app's redirect URL. To rotate the secret,
rotate it in the organization's settings and put it again: no app changes.
Its tests: `cd worker && deno test --allow-net src/`.

## Local development

Needs the [Supabase CLI](https://supabase.com/docs/guides/cli) and Docker.

```bash
supabase start                 # Postgres, Auth, Storage, Studio on :54321–54324
supabase db reset              # apply migrations/ to a fresh database
supabase functions serve       # serve every function with hot reload
```

`supabase start` prints the local API URL and Publishable key (`sb_publishable_…`;
the older anon key works too). Run the app against it:

```bash
SUPABASE_URL=http://127.0.0.1:54321 SUPABASE_ANON_KEY=<publishable key from supabase start> go tool mygo dev
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

## Deploying by hand

A project the app does not set up, such as one a build fixes with
`SUPABASE_URL`:

```bash
supabase link --project-ref <ref>
supabase db push
supabase functions deploy agent providers stt account
```

The app takes the project URL and its Publishable key (Project Settings › API
Keys) as `SUPABASE_URL` and `SUPABASE_ANON_KEY`; it sends the key only as
`apikey`. The Secret key bypasses row-level security and never goes in the app.

`SUPABASE_URL`, `SUPABASE_ANON_KEY` and `SUPABASE_SERVICE_ROLE_KEY` are
provided to functions automatically. There is no app-wide Soniox key: each
user saves their own (Settings › Speech to text), and Soniox streaming and
transcription are refused until they do.

An agent run stops calling the model at `AGENT_DEADLINE_MS` (default 120000,
under the 150 s wall-clock limit of the free plan) and saves what it did; each
model call times out after `AGENT_CALL_TIMEOUT_MS` (default 60000). On a paid
plan (400 s limit) the deadline may go up to 380000:
`supabase secrets set AGENT_DEADLINE_MS=360000`. A job left `running` past the
deadline plus a minute (the platform stopped it) can be claimed again.

Buckets cap uploads at 500 MiB for `recordings` (audio only), 200 MiB for
`files` and 50 MiB for `images` (images only); a hosted project's global upload
limit (Storage settings) must be at least as high for long recordings. The
`stt` function transcribes recordings up to `STT_MAX_AUDIO_MB` (default 100,
about 54 minutes of the app's WAV); longer ones transcribe on the device.

Keep email confirmation on (Authentication › Sign In / Providers › Email ›
Confirm email; `enable_confirmations = true` in `config.toml`, which local
development uses too, with the emails in Inbucket on :54324). Shares and
invites waiting for an address go to the account that confirmed it; without
confirmation, signing up confirms any address at once, so whoever signs up
with someone else's address gets the pages shared with them.

Supabase Vault (`supabase_vault`) is enabled on every Supabase project; the
migration relies on `vault.create_secret`, `vault.update_secret` and
`vault.decrypted_secrets`.

## Notes for the client

- **model_providers**: clients can't read or write `encrypted_secret` /
  `refresh_token`, so select explicit columns (see `PROVIDER_COLUMNS` in
  `functions/_shared/supabase.ts`), never `*`. `has_secret` says whether a key
  is saved. Clients read and delete their rows but cannot insert or update
  them: rows go through the `providers` function (`save`), which checks the
  base URL. Keys go through `save` with `api_key` or the
  `set_provider_secret` RPC; an empty key clears it.
- **Base URLs** the server calls (cloud providers, OpenAI-compatible speech to
  text) must be `https://` to a public host: names that resolve to private,
  loopback, link-local or CGNAT addresses are refused, redirects are errors,
  and errors show the status and the provider's JSON message, never the raw
  body (`functions/_shared/net.ts`).
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
- **Sharing** invites: `share_page` adds a pending `page_permissions` row
  (`accepted_at` null) that grants nothing until the person accepts it with
  `accept_page_invite` (or removes it with `decline_page_invite`);
  `page_invites()` lists the caller's pending ones. Access checks count only
  accepted rows. Clients cannot insert or update `page_permissions`: sharing
  again through `share_page` changes the role.
- **Moving pages**: anyone but the space owner moves a page only between
  parents that the same shares reach (`forbidden_move` otherwise).
- **Speech to text with a provider's key** (`stt_settings.provider_id`) goes
  only to that provider's base URL; a `base_url` that names another address
  is refused rather than sent the key.
- **Storage**: viewers of a shared page can read its recordings, the files
  on it and the images on it; only the space owner writes `files/` and
  `images/`. Images go at `images/{space_id}/{page_id}/{image_id}.{ext}`, the
  page they are on; an image at `{space_id}/{image_id}.{ext}` is the owner's
  alone.
  `recordings/{user_id}/{recording_id}.{ext}` is written only by the user who
  made that `recordings` row, so upsert the row before uploading its audio.
