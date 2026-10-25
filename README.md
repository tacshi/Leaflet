# Leaflet

A desktop app where people and AI co-write living documents ("pages") in a
personal space. Written in Go with [MyGo](https://github.com/egoist/mygo)'s
native UI: everything is drawn by MyGo on the GPU, with no web view for the
app itself. Runs on macOS, Windows and Linux.

- **Pages**: nested pages and folders, and a block editor of Leaflet's own,
  built on MyGo's text-input primitives: headings, lists with nesting,
  to-dos, quotes, tables, images, charts (bar, line, pie), sandboxed
  interactive embeds, recordings and subpage links. Markdown shortcuts
  (`# `, `- `, `[] `, `> `, `**bold**`, `` `code` ``), the `/` menu, `@`
  mentions, a selection toolbar, undo and redo, and selections across
  blocks.
- **Agents**: inline `@agent`, selection › Ask agent, sidebar chat, and
  `@agent` in comment threads. Every edit is a validated tool call
  (`read_page`, `insert_blocks`, `replace_block`, `move_block`,
  `delete_blocks`, `create_subpage`, plus cross-page reads), applied as ops
  that stream into the open page as one undo step, with Undo run / Redo and
  a page-history snapshot before each run.
- **Models**: any OpenAI-compatible provider. Built-in presets (OpenAI,
  Claude, xAI, OpenRouter, DeepSeek, GLM), custom endpoints, and local
  servers (Ollama, LM Studio, llama.cpp, vLLM). Cloud keys stay on the
  server (Vault) and cloud models run in the `agent` Edge Function; local
  models run the same loop on this device, so agents work offline on them.
- **Recording**: 16 kHz capture into a recording block with playback and a
  timestamped transcript. Soniox real-time streaming with short-lived keys,
  or any OpenAI-compatible `/audio/transcriptions` endpoint (one on your
  network is called straight from the device and works offline).
- **Comments and sharing**: threads anchored to stable block IDs, resolve
  and reopen, per-page sharing with viewer or editor roles (inherited by
  subpages).
- **Offline first**: everything is saved to SQLite first; changes go through
  an outbox and sync when online. Offline edits to the same block merge
  last-write-wins per block, with a conflict notice and a way to restore the
  other version.
- **Settings**: Account (name, theme, text size, devices, export, delete),
  Models, Speech to text, Offline and storage.

Real-time co-editing (CRDTs over Supabase Realtime) is the deferred phase
of the design and is not built.

## Run it

Go 1.27.3 or later. No cgo, no C compiler.

```bash
go tool mygo dev
```

Without a server the app runs on this device only: pages, editing,
comments, local models and transcription servers on your network work;
sign-in, sync, sharing, cloud models and cloud transcription are hidden or
explained in place.

With Supabase (see [`supabase/README.md`](supabase/README.md) for the
backend), set the project at run time:

```bash
SUPABASE_URL=http://127.0.0.1:54321 SUPABASE_ANON_KEY=<anon key> go tool mygo dev
```

or build it in:

```bash
MYGO_LDFLAGS="-X leaflet/internal/remote.DefaultURL=https://… -X leaflet/internal/remote.DefaultAnonKey=…" go tool mygo build
```

`LEAFLET_DATA_DIR` keeps the database somewhere other than the app's data
folder, for trying things out.

To try agents without an API key, run a local model server (for example
`ollama serve` with `qwen3:8b`) and add it in Settings › Models.

## Build

```bash
go tool mygo build
```

The packaged app lands in `build/`. Recording asks for the microphone; the
usage description is in `mygo.json`.

## Tests

```bash
go test ./...                        # doc model, merge, ops/undo, store, editor, agent loop
cd supabase/functions && deno test _shared/
```

The editor tests run its view headless with `ui.NewTester`.

## Layout

```
main.go
internal/
  app/        window, shell, sidebar, page screen, panels, dialogs, settings
  editor/     the block editor: model, editing, layout, input, blocks
  doc/        page document model: blocks, deltas, inline markdown, ops,
              per-block merge, Markdown export (docs/protocol.md)
  store/      SQLite (pure Go) tables, repositories, outbox, settings
  remote/     Supabase over HTTP: Auth, PostgREST, Storage, Edge Functions
  sync/       outbox push, pulls, space adoption, device heartbeat
  agents/     OpenAI-compatible client, presets, prompt, page tools, loop, hub
  recording/  capture page, WAV, Soniox streaming, endpoint transcription
  theme/      colors, Figtree and Source Serif 4, Lucide icons
supabase/     migrations (tables, RLS, RPCs, Vault), Edge Functions
docs/protocol.md the contract between the client and the backend
```

Microphone capture runs in a small page served on the app's own
`leaflet-rec://` scheme: such pages are secure contexts the system's web
view lets use the microphone, so the app needs no native audio code. Embeds
run in a sandboxed frame under a content security policy with no network,
and frames can never call the app's Go code.

## Open items

- On-device speech recognition (Qwen3-ASR, SenseVoice through sherpa-onnx)
  is not ported: sherpa-onnx needs native libraries. Serve a model behind an
  OpenAI-compatible endpoint on your network instead.
- OpenAI and Claude account sign-in for third-party apps is unconfirmed, so
  the Models screen uses API keys.
- MOSS-Transcribe-Diarize has no on-device runtime; it can be served behind
  an OpenAI-compatible endpoint.

## Third-party assets

Figtree and Source Serif 4 are under the SIL Open Font License
(`internal/theme/fonts/OFL-*.txt`); Lucide icons are under the ISC license
(`internal/theme/icons/LICENSE`).
