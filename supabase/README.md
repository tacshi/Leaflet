# Leaflet backend (Supabase)

Postgres schema, row-level security, storage buckets, Realtime broadcasts of
page saves, and four Edge Functions.
The contract with the Go client is [`../docs/protocol.md`](../docs/protocol.md).

```
supabase/
  config.toml
  migrations/        one file per schema change, oldest first: 20261025000000_init.sql
                     has the tables, RLS, RPCs, Vault helpers and buckets, and each
                     later one says at its top what it changes and why
  functions/
    _shared/        cors, clients, OpenAI-compatible client, blocks, tools, prompt
    agent/          NDJSON agent worker (protocol §4)
    providers/      save / models / test / delete model providers
    stt/            Soniox temporary keys, Soniox key storage, file transcription
    account/        account deletion
    _testing/       the tests' own: a project in memory, the fixtures' writer
  embed.go          carries migrations/ and functions/ into the app

Each function's `index.ts` serves its `handler.ts` on the project it runs in;
the tests beside it run the handler on a project in memory
(`_testing/project.ts`), with the model, Soniox and the checks of typed
addresses stood in for.
```

Each schema change is a new migration: `supabase db push` never runs one a
project has applied again, so an edit to it reaches no existing project.

## Sign in with Supabase

People sync through a project in their own Supabase account. **Continue with
Supabase** signs in to the Management API, uses the account's Leaflet project
(one whose migration history has `20261025000000`), or creates one named
Leaflet, then sets it up from this folder, built into the app:

- the migrations it lacks, each recorded in `supabase_migrations` as
  `supabase db push` records them, each behind a lock, so two devices
  setting the project up at once apply each one once;
- sign-ups closed and addresses confirmed (Auth's `disable_signup`, and
  `mailer_autoconfirm` off), so the owner's account is the project's only
  one, where the sign-in may change Auth settings;
- the four functions, when the project's are older: a project secret
  named `LEAFLET_FUNCTIONS_V`, their version (`FunctionsVersion` in
  `internal/provision`, raised by one with every change to them), `_` and
  the first 16 hex digits of their sources' hash says which they are (the
  name, since the API lists secrets' values only as digests). A build
  never deploys over functions of its version or a newer one, so two
  builds never take turns; a development build replaces those of its own
  version, as they change while it is worked on;
- the Realtime policy for a page's saves, once Realtime has made the table
  it goes on: until then the setup runs again as the app starts;
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
  keys are read under Secrets); Storage write; Auth read and write (sign-ups;
  a sign-in from before the app had it leaves them as they are, until the
  person signs in with Supabase again).

Its client ID is built in (`ClientID` in
[`internal/supabaseapi`](../internal/supabaseapi/supabaseapi.go)). Its client
secret, which Supabase wants with every code and refresh token, PKCE or not,
ships in no app: the sign-in Worker in [`../worker`](../worker) holds it.

### The sign-in Worker

A Cloudflare Worker with three routes:

- `GET /auth/supabase`, the OAuth app's redirect URL. Supabase takes only
  https ones, which a desktop app can't answer, so the page sends the browser
  on to the app waiting on `http://127.0.0.1:51741/auth/callback`, with the
  code and state.
- `POST /token`, Supabase's token endpoint with the client secret added. It
  takes only Leaflet's client ID, codes with their PKCE verifier and its own
  redirect URL, and refresh tokens; at most 30 a minute from one address
  (`TOKEN_LIMIT`, a Workers rate limit in `wrangler.toml`), answering 429
  past them, and 502 in OAuth's JSON when Supabase can't be reached. Its
  logs are on (`observability`).
- `GET /.well-known/apple-app-site-association`, which ties the domain to the
  iOS app: its sign-in takes `/auth/supabase` as its https callback, so it
  never loads the page. It names the team in `APPLE_TEAM_ID` (in
  `wrangler.toml`), and is not found without one.

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
SUPABASE_URL=http://127.0.0.1:54321 SUPABASE_PUBLISHABLE_KEY=<publishable key from supabase start> go tool mygo dev
```

Without these variables the app uses the project it was built with
([`MYGO_LDFLAGS`](../README.md#build-and-release)), else the one chosen with
**Continue with Supabase** and kept on the device. With none, it runs on the
device only until then: pages, editing, recording, public links and local
models work; sync, cloud models and cloud speech to text are off.

### Tests

```bash
cd supabase/functions
deno test --frozen             # the shared modules, and each function's handler
deno test --frozen --allow-write=_shared/testdata   # writes the fixtures the Go tests read
deno check --frozen */index.ts
deno lint
```

`scripts/check-migrations.sh` applies every migration, oldest first, to a
throwaway container of the `supabase/postgres` image, as the app applies them
to a project; it needs only Docker, and CI runs it. Along the way,
`scripts/migrations/seed/<version>.sql` puts rows in right after the migration
of that version, as a project in use has, so the migrations that move data
move some. Then it runs the checks in `scripts/migrations`: `seeded.sql`, what
the later migrations made of those rows; `test.sql`, what single migrations
promise; and `access.sql`, the privileges each role has, and who of a page's
owner, editor, viewer, invitee and a stranger may read and change it, its
content, history, comments, files, recordings and images, and their Storage
objects; and last `realtime.sql`, on stand-ins for what Realtime makes in a
project, who hears a page's channel.

## Deploying by hand

A project the app does not set up, such as one a build fixes with
`SUPABASE_URL`:

```bash
supabase link --project-ref <ref>
supabase db push
supabase functions deploy agent providers stt account
```

The app takes the project URL and its Publishable key (Project Settings › API
Keys) as `SUPABASE_URL` and `SUPABASE_PUBLISHABLE_KEY`; it sends the key only
as `apikey`. The Secret key bypasses row-level security and never goes in the
app.

`SUPABASE_URL`, `SUPABASE_ANON_KEY` and `SUPABASE_SERVICE_ROLE_KEY` are
provided to functions automatically. There is no app-wide Soniox key: each
user saves their own (Settings › Speech to text), and Soniox streaming and
transcription are refused until they do.

An agent run stops calling the model at `AGENT_DEADLINE_MS` (default 120000,
under the 150 s wall-clock limit of the free plan) and saves what it did; each
model call times out after `AGENT_CALL_TIMEOUT_MS` (default 60000). On a paid
plan (400 s limit) the deadline may go up to 380000:
`supabase secrets set AGENT_DEADLINE_MS=360000`. The limit is the worker's, and
a worker serves many requests: a run on one already old stops sooner, as far
as its worker lets it, and at once, saving what it did and ending failed, as
the platform says it will shut the worker down (`beforeunload`). One stopped
with no word leaves its ops on its job, kept as it worked, for the caller's
next run to mark it failed; a job left `running` past the deadline plus a
minute can be claimed again. The plan isn't told: a worker younger than half
the free plan's limit is taken for the free plan's, which on a paid plan with
the default deadline only ends a run on a warm worker sooner than it had to.

Buckets cap uploads at 500 MiB for `recordings` (audio only), 200 MiB for
`files` and 50 MiB for `images` (images only); a hosted project's global upload
limit (Storage settings) must be at least as high for long recordings. The
`stt` function transcribes recordings up to `STT_MAX_AUDIO_MB` (default 100,
about 54 minutes of the app's WAV); longer ones transcribe on the device. It
streams a recording from Storage to the transcriber rather than holding it,
so the deadline bounds that size, not the function's memory. A call has
`STT_DEADLINE_MS` (default 120000, the most), on any plan: its answer comes
at its end, and the gateway gives up on one that has not begun at 150 s. Soniox
goes on with a long recording past that: its transcription is kept in
`stt_jobs` (service role only), and asking again picks it up, sending nothing
and spending nothing more, until it is done. What went to Soniox goes from it
once it is; Soniox deletes what is never asked for again in 30 days.

Keep email confirmation on (Authentication › Sign In / Providers › Email ›
Confirm email; `enable_confirmations = true` in `config.toml`, which local
development uses too, with the emails in Mailpit on :54324). Shares and
invites waiting for an address go to the account that confirmed it; without
confirmation, signing up confirms any address at once, so whoever signs up
with someone else's address gets the pages shared with them. A project the
app sets up also takes no sign-ups (Allow new users to sign up, off): the
app makes the owner's account with the secret key, which needs none, and
anyone else who learned the project's address and publishable key could
otherwise make one and use its functions and storage. A project deployed
by hand keeps sign-ups on: its people make their accounts with an emailed
code.

Supabase Vault (`supabase_vault`) is enabled on every Supabase project. Keys
go in it through `vault.create_secret` and `vault.update_secret`, and the
Functions read them from `vault.decrypted_secrets`; clearing a key
(`set_provider_secret`, `set_stt_secret`) and deleting a provider or a
speech-to-text key (the `model_providers_cleanup` and `stt_secrets_cleanup`
triggers) delete its row from `vault.secrets` directly, as `postgres`, who
owns those functions. The `supabase/postgres` image grants `postgres` that,
and `access.sql` checks it.

The migrations state every privilege the roles have (`20261103020000`): they
count on no default grant of a project's. Four things they assume of a
hosted project can't be checked here, and are worth checking once on one
(in its SQL editor, as `postgres`):

- that `postgres` may delete from `vault.secrets`, as above:
  `select has_table_privilege('vault.secrets', 'delete')`;
- that `postgres` may delete from `auth.sessions`, as `sign_out_device`
  does to end a signed-out device's session (its refresh tokens cascade):
  Auth owns the table, and the image's default privileges grant `postgres`
  what Auth makes, as `scripts/migrations/services.sql` has it, and as
  `supabase start` does: `select has_table_privilege('auth.sessions', 'delete')`;
- that `postgres` may make policies on `storage.objects`, and update its
  `owner` (and `owner_id`), as `release_kept_recordings` does: the image
  has `scripts/migrations/services.sql` make `postgres` own the Storage
  tables, which Supabase's Storage service owns in a project:
  `select has_table_privilege('storage.objects', 'update')`;
- that the Management API's query endpoint runs what it is sent as one
  transaction, so a migration that fails part way leaves nothing behind
  (`migrationSQL` in `internal/provision` sends a migration with the row
  that records it).

Realtime makes `realtime.messages` itself, in its own time, which may be after
the migrations ran. The policy on it, `page_saves_heard`, goes on it through
`ensure_page_saves_heard()`: the app calls it each time it sets a project up,
and runs its setup again until it reports the table is there. A project
deployed by hand calls it once Realtime is up, in its SQL editor, as `postgres`:
`select public.ensure_page_saves_heard()`.

## Notes for the client

- **model_providers**: clients can't read or write `encrypted_secret` /
  `refresh_token`, so select explicit columns (see `PROVIDER_COLUMNS` in
  `functions/_shared/supabase.ts`), never `*`. `has_secret` says whether a key
  is saved. Clients read and delete their rows but cannot insert or update
  them: rows go through the `providers` function (`save`), which checks the
  base URL. Keys go through `save` with `api_key` (a string) or the
  `set_provider_secret` RPC; an empty key clears it. A key stays with the
  address it was saved for: when a row's `base_url` origin, `preset` or
  `is_local` changes, the database drops its keys, whoever writes it, so a
  provider moved keeps a key only when one is saved with the move. A base
  URL is printable ASCII with no backslash, which a URL parser would read
  as a slash.
- **Base URLs** the server calls (cloud providers, OpenAI-compatible speech to
  text) must be `https://` to a public host: names that resolve to private,
  loopback, link-local or CGNAT addresses are refused, redirects are errors,
  and errors show the status and the provider's JSON message, never the raw
  body (`functions/_shared/net.ts`).
- **Local providers** (`ollama`, `lmstudio`, `local`) may be saved without a
  key for listing, but the server never calls them, for models, tests or
  transcriptions, and rejects their keys; one saved local loses the key it
  had. Clients call local endpoints directly.
- **Account sign-in** (`auth_kind: "account"`) is rejected by `providers.save`
  until provider terms for third-party apps are confirmed (open question in the
  design); use API keys.
- **Tree depth**: a tree goes at most 256 pages deep (`page_too_deep`); one
  already deeper stays, read to its deepest page, and its pages may change,
  or move no deeper.
- **Soniox keys** live in `stt_secrets` (service role only), not in
  `model_providers`. `has_stt_secret('soniox')` tells the client whether one is
  saved.
- **updated_at** is always set by the server (`clock_timestamp()`), on insert
  and update. Pull with `updated_at > cursor`; soft-deleted rows
  (`deleted_at`) come through the same pull. What is on a page deleted
  before someone was shared it (its content, comments, recordings, files,
  images) is not theirs: they pull the page's row, and nothing on it.
- **Devices**: a device upserts its own `devices` row (its heartbeat), which
  the server stamps with the session it was written with; Account › Devices
  signs another out with `sign_out_device`, which revokes the row and ends
  that session. Clients neither set nor clear `revoked_at`, nor delete a row;
  a write from a new session, as after signing in on the device again,
  clears a revocation that was for another.
- **Comments** keep `body` and `quote` to 100,000 characters, `mentions`
  to 64 KB and `block_id` to 256 characters; on someone else's page, one
  person writes at most 2,000 comments, of 10 MB in all.
- **ai_jobs**: viewers may create jobs; the worker then runs without edit
  tools. The worker claims a job by moving it from `queued` to `running`; a
  second call returns 409.
- **Subpages created by agents** get a `sort_key` after the last sibling's, as
  the app makes one (`functions/_shared/sortkey.ts`, `internal/store/sortkey.go`):
  the digit halfway between that key's first digit and the last one, so the
  first child gets `V` and the next `l`.
- **stt_settings.provider** values: `on-device` (the device's own speech
  recognition), `soniox`, `openai-compatible`; rows from earlier versions may
  have `qwen3-asr`, `moss-transcribe-diarize` or `sensevoice`. The server
  transcribes only `soniox` and OpenAI-compatible endpoints (`provider_id` or
  `base_url` set); the others transcribe on the device. It saves the
  transcript as the caller, so only an editor of the recording's page may ask.
- **Sharing** invites: `share_page` adds a pending `page_permissions` row
  (`accepted_at` null) that grants nothing until the person accepts it with
  `accept_page_invite` (or removes it with `decline_page_invite`);
  `page_invites()` lists the caller's pending ones. Access checks count only
  accepted rows. Clients cannot insert or update `page_permissions`: sharing
  again through `share_page` changes the role. No app calls these RPCs yet:
  the apps pull the pages shared with their account, and the desktop app
  shares a page otherwise only through a public link, which needs no server.
- **Moving pages**: anyone but the space owner moves a page only between
  parents that the same shares reach (`forbidden_move` otherwise), and only
  the owner puts a page in a folder, as it is made or moved
  (`forbidden_folder`).
- **Speech to text with a provider's key** (`stt_settings.provider_id`) goes
  only to that provider's base URL; a `base_url` that names another address
  is refused rather than sent the key.
- **Storage**: viewers of a shared page can read its recordings, the files
  on it and the images on it; only the space owner writes `files/` (a file
  on a page only while they can edit that page) and `images/`. Images go at `images/{space_id}/{page_id}/{image_id}.{ext}`, the
  page they are on; an image at `{space_id}/{image_id}.{ext}` is the owner's
  alone.
  `recordings/{user_id}/{recording_id}.wav` is written only by the user who
  made that `recordings` row, while they can edit its page, so upsert the
  row before uploading its audio. The row's `created_by` stays theirs: an
  update cannot change it, and only deleting their account clears it. Its
  `audio_path` is that path or null: any other a client sends is dropped.
  A deleted file (`deleted_at`) is read only by its space's owner and its
  page's owner, who may remove its object.
