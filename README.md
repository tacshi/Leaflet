<p align="center">
  <img src="resources/icon.png" width="128" height="128" alt="Leaflet">
</p>

<h1 align="center">Leaflet</h1>

<p align="center">
  A native desktop app where people and AI agents write living documents together.
</p>

<p align="center">
  <a href="https://github.com/tacshi/Leaflet/releases/latest"><img src="https://img.shields.io/github/v/release/tacshi/Leaflet?sort=semver&label=release" alt="Latest release"></a>
  <a href="#requirements"><img src="https://img.shields.io/badge/macOS-12%2B-000000?logo=apple&logoColor=white" alt="macOS 12 or later"></a>
  <a href="#requirements"><img src="https://img.shields.io/badge/Windows-x64%20%7C%20arm64-0078D4?logo=windows&logoColor=white" alt="Windows x64 and arm64"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white" alt="Go 1.27"></a>
  <a href="#agents-outside-the-app-mcp"><img src="https://img.shields.io/badge/MCP-supported-6E56CF" alt="MCP supported"></a>
  <a href="https://github.com/egoist/mygo"><img src="https://img.shields.io/badge/built%20with-MyGo-F2545B" alt="Built with MyGo"></a>
</p>

<p align="center">
  <b>English</b> | <a href="README.zh.md">简体中文</a> | <a href="README.ja.md">日本語</a>
</p>

Leaflet keeps your notes, specs and meeting records as pages of blocks, and
lets AI agents work in them beside you. Ask one inline, from a selection,
in a chat or in a comment thread: it reads the page and edits it with
validated tools, as one step you can undo. Pages are saved on your device
first and sync through iCloud Drive, or a Leaflet server when you sign in,
so Leaflet works offline, with local models too.

It is written in Go with [MyGo](https://github.com/egoist/mygo), which
draws the whole interface natively on the GPU, with no web view for the app
itself.

## Contents

- [Features](#features)
- [Requirements](#requirements)
- [Install](#install)
- [Run from source](#run-from-source)
- [Agents outside the app (MCP)](#agents-outside-the-app-mcp)
- [Build and release](#build-and-release)
- [Development](#development)
- [Project layout](#project-layout)
- [Known limitations](#known-limitations)
- [Acknowledgments](#acknowledgments)

## Features

### Write

- A block editor of Leaflet's own: headings, nested lists, to-dos with due
  dates, quotes, callouts, toggles, tables, code with syntax highlighting,
  math, diagrams, charts, images, files, videos, link cards and sandboxed
  interactive embeds.
- Markdown shortcuts, the `/` menu, `@` mentions of people, agents and
  pages, a selection toolbar with text colors, folding headings, an
  outline, and links to pages and to single blocks.
- Nested pages and folders, templates, duplicate pages, search, and export
  to Markdown.
- Page history with named versions, and what changed since any of them,
  inline or side by side.

### Work with agents

- Ask an agent inline with `@agent`, from a selection, in the side chat or
  in a comment thread.
- Every edit is a validated tool call (`read_page`, `insert_blocks`,
  `replace_block`, `move_block`, `delete_blocks`, `create_subpage`) that
  streams into the page as one undo step, after a history snapshot.
- Any OpenAI-compatible provider: presets for OpenAI, Claude, xAI,
  OpenRouter, DeepSeek and GLM, a ChatGPT Plus or Pro sign-in, custom
  endpoints, and local servers such as Ollama, LM Studio, llama.cpp and
  vLLM, which run agents offline.
- Agents outside the app, such as Claude Code, Codex and Cursor, work on
  pages through [`leaflet mcp`](#agents-outside-the-app-mcp).

### Record

- Recording blocks with playback and a timestamped transcript.
- Real-time transcription with Soniox, or any OpenAI-compatible
  `/audio/transcriptions` endpoint, including one on your own network.

### Share and sync

- Comment threads anchored to blocks, and sharing a page with viewers or
  editors, inherited by its subpages.
- Offline first: everything is saved to SQLite, and changes sync through
  an outbox when online. Edits made apart merge block by block, with a
  conflict notice and a way to bring back the other version.
- iCloud sync: your Macs and PCs sync through the Leaflet folder in iCloud
  Drive, with no server and no sign-in. Turn it on in Settings › Sync
  and storage; on Windows it needs iCloud for Windows.
- To-do reminders, a menu bar icon, light and dark themes, and updates
  from GitHub Releases.
- In English, Simplified Chinese and Japanese: Leaflet follows the
  system's language, or the one chosen in Settings › Appearance, and its
  welcome guide is written in the language of the first run.

## Requirements

- macOS 12 or later, on Apple silicon or Intel; or Windows on x64 or arm64.
- To build from source: Go 1.27.3 or later. No cgo or C compiler is
  needed. Release builds also need Xcode for macOS, and NSIS for Windows
  installers.
- iCloud sync needs iCloud Drive turned on.
- Sign-in, sharing, cloud models and sync with other people need a Leaflet
  server: a [Supabase](https://supabase.com) project set up from
  [`supabase/`](supabase/README.md). Without one, Leaflet runs on your
  devices alone.

## Install

Download the disk image for macOS or the installer for Windows from the
[latest release](https://github.com/tacshi/Leaflet/releases/latest).
Installed copies update themselves from GitHub Releases.

## Run from source

```bash
git clone https://github.com/tacshi/Leaflet.git
cd Leaflet
go tool mygo dev
```

Without a server, Leaflet runs on this device only: pages, editing,
comments, local models and transcription servers on your network all
work, and sign-in, sync, sharing, cloud models and cloud transcription are
hidden or explained in place.

To use a Supabase backend (see [`supabase/README.md`](supabase/README.md)),
set it at run time:

```bash
SUPABASE_URL=http://127.0.0.1:54321 SUPABASE_ANON_KEY=<anon key> go tool mygo dev
```

or build it in:

```bash
MYGO_LDFLAGS="-X leaflet/internal/remote.DefaultURL=https://… -X leaflet/internal/remote.DefaultAnonKey=…" go tool mygo build
```

To try agents without an API key, run a local model server (for example
`ollama serve` with `qwen3:8b`) and add it in Settings › Agent providers.

### Data

Leaflet keeps its database in the app's data folder, such as
`~/Library/Application Support/Leaflet` on macOS, or in the folder that
`LEAFLET_DATA_DIR` names.

## Agents outside the app (MCP)

`leaflet mcp` is a [Model Context Protocol](https://modelcontextprotocol.io)
server on stdio for Claude Code, Claude Desktop, Codex, Cursor, VS Code and
other clients. Settings › External agents gives the setup for each, with
this app's executable and data folder filled in. For Claude Code:

```bash
claude mcp add -s user leaflet -e LEAFLET_DATA_DIR=<data folder> -- <executable> mcp
```

| Tool | What it does |
| --- | --- |
| `list_pages`, `search_pages` | Find pages by title and text |
| `read_page` | Read a page's blocks, with their IDs |
| `create_page`, `rename_page` | Make and rename pages |
| `insert_blocks`, `replace_block`, `move_block`, `delete_blocks` | Edit a page, as the in-app agents do |

The server opens the same database as the app, so it works with the app
open or closed. Pages shared with you to view are read-only. Edits keep a
history snapshot before an agent's first edit of a page, then one per ten
minutes of its edits, and sync like any other change. The open app picks up
what they write within a second and merges it with what is being typed.

To try it on a development build, give a binary the dev app's data folder:

```bash
go build -o leaflet . && claude mcp add -s user leaflet -e LEAFLET_DATA_DIR="$HOME/Library/Application Support/Leaflet Dev" -- "$PWD/leaflet" mcp
```

## Build and release

`go tool mygo build` builds the app into `build/`. The scripts build for
macOS by default, or for Windows with `--win`:

| Script | Builds |
| --- | --- |
| `scripts/build-debug.sh [--mac\|--win] [--open]` | A debug build named Leaflet Debug, with its own identifier and data folder, that never updates itself, so it runs beside a release copy |
| `scripts/build-release.sh [--mac] [--win] [<version>]` | Signed and notarized disk images for Apple silicon, Intel and universal, and installers for Windows x64 and arm64, with signed update archives |
| `scripts/publish.sh <version> [--mac] [--win] [--dry-run]` | A GitHub release: checks, release notes from `CHANGELOG.md`, the build, the tag and publishing |

Run any script with `--help` for its options. macOS releases need a
Developer ID Application identity and a `notarytool` keychain profile,
Windows installers need NSIS (`brew install makensis`), and every release
needs the update signing key; `scripts/build-release.sh --help` explains
each one.

## Development

```bash
go test ./...                                # model, merge, store, sync, editor, agents
make check                                   # gofmt, golangci-lint, govulncheck and gosec
make clean                                   # remove build/ and the binary go build leaves
cd supabase/functions && deno test _shared/  # the Edge Functions' shared code
```

The editor's tests run its view headless with `ui.NewTester`.

The app's texts are MyGo catalogs in [`locales/`](locales), one file per
language (`en.json`, `zh-Hans.json`, `ja.json`), from which `go tool mygo
dev` and `go tool mygo build` write `locales/catalog_gen.go`; commit it
with the catalogs. `go tool mygo i18n check` checks them, and
`go tool mygo dev -lang ja` runs the app in a language without changing
the one it keeps. To add a language, add its catalog, named after its
BCP 47 tag.
[`docs/protocol.md`](docs/protocol.md) is the contract between the app and
the backend: the document format, the agent tools, and the save and sync
rules.

## Project layout

| Path | Contents |
| --- | --- |
| `main.go` | Entry point: the app, and `leaflet mcp` |
| `internal/app` | Window, shell, sidebar, page screen, panels, dialogs and settings |
| `internal/editor` | The block editor: model, editing, layout, input and blocks |
| `internal/doc` | The page document model: blocks, deltas, inline Markdown, ops, per-block merge and Markdown export |
| `internal/store` | SQLite (pure Go) tables, the outbox and settings |
| `internal/remote` | Supabase over HTTP: Auth, PostgREST, Storage and Edge Functions |
| `internal/sync` | Pushing the outbox, pulls, account adoption and the device heartbeat, through Supabase or iCloud Drive |
| `internal/icloud` | The Leaflet folder in iCloud Drive: its files, written whole, and asking iCloud to download them |
| `internal/agents` | The OpenAI-compatible client, presets, prompt, page tools, loop and hub |
| `internal/mcp` | `leaflet mcp`: the page tools for agents outside the app |
| `internal/recording` | Capture, WAV, Soniox streaming and endpoint transcription |
| `internal/theme` | Colors, fonts and icons |
| `locales` | The app's texts in each language, and the Go code generated from them |
| `scripts` | Debug and release builds, and publishing to GitHub Releases |
| `supabase` | Migrations (tables, row-level security, RPCs, Vault) and Edge Functions |
| `docs` | The protocol, user stories and the icon's sources |

Microphone capture runs in a small page on the app's own `leaflet-rec://`
scheme, a secure context the system's web view lets use the microphone, so
the app needs no native audio code. Embeds, math and diagrams run in
sandboxed frames under a content security policy with no network, and can
never call the app's Go code.

## Known limitations

- Real-time co-editing is not built: pages sync and merge block by block.
- On-device speech recognition (Qwen3-ASR, SenseVoice through sherpa-onnx)
  is not ported, as sherpa-onnx needs native libraries. Serve a model
  behind an OpenAI-compatible endpoint on your network instead; the same
  goes for MOSS-Transcribe-Diarize.
- OpenAI and Claude account sign-in for third-party apps is unconfirmed,
  so those providers take API keys.

## Acknowledgments

Leaflet is built on [MyGo](https://github.com/egoist/mygo). It bundles
[Figtree](https://github.com/erikdkennedy/figtree) and
[Source Serif 4](https://github.com/adobe-fonts/source-serif) under the SIL
Open Font License (`internal/theme/fonts/OFL-*.txt`),
[Lucide](https://lucide.dev) icons under the ISC license
(`internal/theme/icons/LICENSE`), and [KaTeX](https://katex.org) and
[Pele](https://github.com/obsidianmd/pele) under the MIT license for math
and diagrams ([`internal/app/third_party`](internal/app/third_party/README.md)).
