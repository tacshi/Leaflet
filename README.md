<p align="center">
  <img src="resources/icon.png" width="128" height="128" alt="Leaflet">
</p>

<h1 align="center">Leaflet</h1>

<p align="center">
  A native desktop app where people and AI agents write living documents together.
</p>

<p align="center">
  <a href="https://github.com/tacshi/Leaflet/releases/latest"><img src="https://img.shields.io/github/v/release/tacshi/Leaflet?sort=semver&label=release" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT License"></a>
  <a href="#requirements"><img src="https://img.shields.io/badge/macOS-13%2B-000000?logo=apple&logoColor=white" alt="macOS 13 or later"></a>
  <a href="#requirements"><img src="https://img.shields.io/badge/Windows-x64%20%7C%20arm64-0078D4?logo=windows&logoColor=white" alt="Windows x64 and arm64"></a>
  <a href="#ios"><img src="https://img.shields.io/badge/iOS%20%7C%20iPadOS-26%2B%20(in%20progress)-000000?logo=apple&logoColor=white" alt="iOS and iPadOS 26 or later, in progress"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white" alt="Go 1.27"></a>
  <a href="#agents-outside-the-app-mcp"><img src="https://img.shields.io/badge/MCP-supported-6E56CF" alt="MCP supported"></a>
  <a href="https://github.com/egoist/mygo"><img src="https://img.shields.io/badge/built%20with-MyGo-F2545B" alt="Built with MyGo"></a>
  <a href="https://awesomemygo.dev/apps/leaflet"><img src="https://awesomemygo.dev/badge.svg" height="20" alt="Listed on Awesome MyGo"></a>
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
- [Command line](#command-line)
- [Build and release](#build-and-release)
- [Development](#development)
- [Project layout](#project-layout)
- [Known limitations](#known-limitations)
- [Acknowledgments](#acknowledgments)
- [License](#license)

## Features

### Write

- A block editor of Leaflet's own: headings, nested lists, to-dos with due
  dates, quotes, callouts, toggles, tables, code with syntax highlighting,
  math, diagrams, charts, images, files, videos, link cards and sandboxed
  interactive embeds.
- Markdown shortcuts, the `/` menu, `@` mentions of people, agents and
  pages, a selection toolbar with text colors, folding headings, an
  outline, and links to pages and to single blocks.
- Videos from YouTube, Vimeo, Bilibili, Dailymotion and Niconico, or video
  files, that play in the page, with notes under each, as a video's
  description has them: a time in them plays that video from there. A link
  to a moment of a video or a recording plays it from that moment, from any
  page or a chat.
- Nested pages and folders, templates, duplicate pages, search, and export
  to Markdown and PDF.
- Page history with named versions, and what changed since any of them,
  inline or side by side.

### Work with agents

- Ask an agent inline with `@agent`, from a selection, in the side chat or
  in a comment thread.
- Every edit is a validated tool call (`read_page`, `write_markdown`,
  `insert_blocks`, `replace_block`, `move_block`, `delete_blocks`,
  `create_subpage`, `create_page`, and `list_pages`, `read_other_page` and
  `list_folders` where reading other pages is on) that streams into the
  page as one undo step, after a history snapshot.
- Asked for a new page, an agent makes one of its own, at the top level or
  in a folder (`create_page`), and a subpage inside the page when asked for
  one there, its link at the end of the page (`create_subpage`). On a page
  someone else writes in, comments on as a guest or reads by a public link,
  it makes a subpage: a page of your own would take their writing into
  your pages.
- The workspace agent works on all your pages rather than one: the panel's
  Agent tab at All pages, and the panel itself with no page open. It finds
  and reads your pages, makes them, moves, renames and tags them, and makes
  folders, each change undoable. It changes no page someone else shares,
  and reads none that other people write in too.
- Agents read what is said in a YouTube or Vimeo video, from its captions
  (`read_video_transcript`), to write a video's notes with the time of each
  moment, and cite a moment of a video or a recording with a link to it.
- Agents use the web where you let them: `read_web_page` reads a page as
  text with its links, and `web_search` searches through the agent's own
  provider (OpenAI, Claude, xAI, OpenRouter, GLM or a ChatGPT plan). Once a
  run has read the web, it writes and reads only the links it found or you
  gave it, and once it reads your other pages, the web is closed to it.
- Any OpenAI-compatible provider: presets for OpenAI, Claude, xAI,
  OpenRouter, DeepSeek and GLM, a ChatGPT Plus or Pro sign-in, custom
  endpoints, and local servers such as Ollama, LM Studio, llama.cpp and
  vLLM, which run agents offline.
- Agents outside the app, such as Claude Code, Codex and Cursor, work on
  pages through [`leaflet mcp`](#agents-outside-the-app-mcp).
- A [`leaflet` command](#command-line) finds, reads, makes and files pages
  from a terminal and in scripts, and adds to them.

### Record

- Recording blocks with playback and a timestamped transcript.
- Real-time transcription with Soniox, or any OpenAI-compatible
  `/audio/transcriptions` endpoint, including one on your own network.
- Transcription on the device itself, offline and without a key, with the
  system's own speech recognition: on a Mac with macOS 26 or later, and on
  iPhone and iPad.

### Share and sync

- Comment threads anchored to blocks, and a temporary public link on the
  Mac and PC that shares a page with anyone who has it, or only with the
  addresses you name, while Leaflet is open. The server can also share a
  page with viewers or editors, inherited by its subpages, but no app
  offers that yet.
- Offline first: everything is saved to SQLite, and changes sync through
  an outbox when online. Edits made apart merge block by block, with a
  conflict notice and a way to bring back the other version. Through a
  Leaflet server, an open page shows what changes in it on another device
  as it is saved there, as an agent's edits, through Supabase Realtime.
- iCloud sync: your Macs and PCs sync through the Leaflet folder in iCloud
  Drive, with no server and no sign-in. Turn it on in Settings › Sync
  and storage; on Windows it needs iCloud for Windows.
- To-do reminders, a menu bar icon, light and dark themes, and updates
  from GitHub Releases.
- In English, Simplified Chinese and Japanese: Leaflet follows the
  system's language, or the one chosen in Settings › Appearance, and its
  welcome guide is written in the language of the first run.

## Requirements

- macOS 13 or later, on Apple silicon or Intel; or Windows on x64 or arm64.
- To build from source: Go 1.27.3 or later; the Go code needs no cgo or C
  compiler. The Mac app's on-device speech helper ([`speech`](speech))
  is Swift, which `scripts/build-speech.sh` builds with Xcode 26 or later
  (the macOS 26 SDK): debug builds go on without it where Xcode can't build
  it, and release builds need it, as they need Xcode to sign and notarize.
  Windows installers need NSIS.
- iCloud sync needs iCloud Drive turned on.
- Sync through a server and cloud models need a
  [Supabase](https://supabase.com) account: **Continue with Supabase**
  finds or creates a project in it and sets it up from
  [`supabase/`](supabase/README.md). Without one, Leaflet runs on your
  devices alone.

## Install

Download the disk image for macOS or the installer for Windows from the
[latest release](https://github.com/tacshi/Leaflet/releases/latest).
Installed copies update themselves from GitHub Releases. Windows installers
are not code signed yet, so SmartScreen may warn when one first runs.

## Run from source

```bash
git clone https://github.com/tacshi/Leaflet.git
cd Leaflet
go tool mygo dev
```

Without a server, Leaflet runs on this device only: pages, editing,
comments, public links, local models and transcription servers on your
network all work, and sign-in, sync, cloud models and cloud transcription
are hidden or explained in place.

Every build can sign in with Supabase: the OAuth app's client ID is built
in, and its secret stays in Leaflet's sign-in Worker (see
[`supabase/README.md`](supabase/README.md)).

To work against one server instead, such as the local stack, set it, and
sign in with a code sent by email:

```bash
SUPABASE_URL=http://127.0.0.1:54321 SUPABASE_PUBLISHABLE_KEY=<publishable key> go tool mygo dev
```

To try agents without an API key, run a local model server (for example
`ollama serve` with `qwen3:8b`) and add it in Settings › Agent providers.

To transcribe on the Mac itself under `go tool mygo dev`, build its speech
helper first with `scripts/build-speech.sh`, which needs Xcode 26 or later.

### iOS

The iPhone and iPad app is in [`ios/`](ios), and is being built. It
links the same Go core as the desktop app, which gomobile binds to Swift
through the [`mobile`](mobile) package. It needs Xcode 27 and
[XcodeGen](https://github.com/yonaskolb/XcodeGen):

```bash
make ios-core     # the Go core: ios/build/LeafletCore.xcframework
make ios-project  # ios/Leaflet.xcodeproj, from ios/project.yml
make ios-test     # the app's tests on an iPhone and an iPad simulator
```

Then open `ios/Leaflet.xcodeproj`. To run on a device, set your team in
`ios/Config/Local.xcconfig`, which git ignores:
`DEVELOPMENT_TEAM = <team ID>`.

`scripts/publish_testflight.sh <version> <build>` builds the app for the
App Store and publishes it to its internal TestFlight group, with
[asc](https://asccli.sh) signed in to App Store Connect; `--help` explains
what it needs.

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
| `list_pages`, `search_pages` | Find pages by title, tag and text, and recordings and comments |
| `current_page` | See the page open in the app, with the selected text and its text color |
| `read_page` | Read a page's blocks, with their IDs |
| `create_page`, `rename_page` | Make and rename pages |
| `set_page_tags` | Set a page's tags |
| `move_page` | Move a page into a folder, inside another page, or to the top level |
| `create_folder`, `rename_folder` | Make and rename folders |
| `write_markdown`, `insert_blocks`, `replace_block`, `move_block`, `delete_blocks` | Edit a page, as the in-app agents do |

The server opens the same database as the app, so it works with the app
open or closed. Pages shared with you to view are read-only. Edits keep a
history snapshot before an agent's first edit of a page, then one per ten
minutes of its edits, and sync like any other change. The open app picks up
what they write within a second and merges it with what is being typed.
With the app closed, or no page open, `current_page` gives the page opened
last on this device, with no selection.

Agents read and write text color and highlight in text fields as
`<span style="color:#ef4444;background:#fde68a">text</span>`, in hex.
`leaflet cat` and exports leave colors out.

To try it on a development build, give a binary the dev app's data folder:

```bash
go build -o leaflet . && claude mcp add -s user leaflet -e LEAFLET_DATA_DIR="$HOME/Library/Application Support/Leaflet Dev" -- "$PWD/leaflet" mcp
```

## Command line

`leaflet` finds, reads, makes and files your pages from a terminal, and
adds to them, with the same tools as `leaflet mcp`:

| Command | What it does |
| --- | --- |
| `leaflet ls` | List folders and pages, with their IDs and tags |
| `leaflet search QUERY` | Find pages by title, tag or text, recordings and comments |
| `leaflet current` | Show the page open in the app, with the selected text and its text color |
| `leaflet cat PAGE` | Print a page as Markdown |
| `leaflet new [TITLE]` | Make a page and print its ID; `--stdin` takes its content from the Markdown piped in |
| `leaflet append PAGE [TEXT...]` | Add Markdown to the end of a page: the text given, or what is piped in |
| `leaflet rename PAGE TITLE` | Change a page's title |
| `leaflet mv PAGE --in PAGE \| --folder FOLDER \| --top` | Move a page inside another, into a folder, or to the top level |
| `leaflet tag PAGE [TAG...]` | Set a page's tags, or print them; `--clear` takes them off |
| `leaflet folder new NAME`, `leaflet folder rename FOLDER NAME` | Make and rename folders |

A page is named by its title, its ID, or the start of its ID as `ls`
shows it. `--json` prints what the MCP tools return, and `leaflet
completion zsh` (or bash, fish, powershell) writes a completion script
that completes page titles.

```bash
make test 2>&1 | leaflet append "CI log"
leaflet new "Meeting notes" --folder Work --stdin < notes.md
leaflet cat Roadmap > roadmap.md
```

Settings › External agents › Command line installs it: on a Mac as a link
in `/usr/local/bin` to the app, which asks for an administrator's password
when that folder needs one; on Windows by adding the `bin` folder beside the
app, which holds the console program `leaflet.exe`, to your Path, which
terminals opened after read. Like `leaflet mcp`, it writes to the
database on this device, with the app open or closed: the open app shows
what it changes within a second, and sync sends that on while the app
runs. Each page keeps a version from before its changes in its history.

To try it on a development build, give a binary a data folder the app has
run with:

```bash
go build -o leaflet . && LEAFLET_DATA_DIR="$HOME/Library/Application Support/Leaflet Dev" ./leaflet ls
```

## Build and release

`go tool mygo build` builds the app into `build/`. The release scripts
build for macOS and Windows unless `--mac` or `--win` picks one; the debug
script builds for macOS, or for Windows with `--win`:

| Script | Builds |
| --- | --- |
| `scripts/build-debug.sh [--mac\|--win] [--open]` | A debug build named Leaflet Debug, with its own identifier and data folder, that never updates itself, so it runs beside a release copy |
| `scripts/build-release.sh [--mac] [--win] [<version>]` | Signed and notarized disk images for Apple silicon, Intel and universal, and installers for Windows x64 and arm64, with signed update archives |
| `scripts/publish.sh <version> [--mac\|--win] [--allow-partial] [--dry-run]` | A GitHub release for macOS and Windows: checks, release notes from `CHANGELOG.md`, the build, the tag and publishing. One platform named alone stays a draft until the other's build joins it, as a release without a platform's update manifest leaves that platform's installed copies no update; `--allow-partial` publishes it anyway |

On Windows, the PowerShell 7 scripts build the Windows app on that machine.
MyGo downloads NSIS if it isn't installed, and signing uses `signtool` from
the Windows SDK:

| Script | Builds |
| --- | --- |
| `scripts\build-debug.ps1 [-Arch arm64\|x64] [-Open]` | Leaflet Debug, as `build-debug.sh --win` does |
| `scripts\build-release.ps1 [<version>] [-Arch …] [-Upload]` | The x64 and arm64 installers with signed update archives, as `build-release.sh --win` does |

Run any script with `--help` for its options, or `Get-Help` for a `.ps1`
one. macOS releases need a Developer ID Application identity and a
`notarytool` keychain profile, Windows installers need NSIS (`brew install
makensis`), and every release needs the update signing key;
`scripts/build-release.sh --help` explains each one. Uploads refuse
unsigned Windows installers, and macOS ones built `--skip-notarize`, unless
`--allow-unsigned` (`-AllowUnsigned`) or `LEAFLET_ALLOW_UNSIGNED=1` says
to publish them anyway, and never go to a release that is published
already.

To build one server in, as `SUPABASE_URL` and `SUPABASE_PUBLISHABLE_KEY`
set one for a run, give its URL and Publishable key to the linker through
`MYGO_LDFLAGS`, which `go tool mygo build` and the scripts pass on:

```bash
MYGO_LDFLAGS="-X leaflet/internal/remote.DefaultURL=https://<ref>.supabase.co -X leaflet/internal/remote.DefaultPublishableKey=<publishable key>" scripts/build-release.sh
```

## Development

```bash
go test ./...                                # model, merge, store, sync, editor, agents
make check                                   # gofmt, golangci-lint, govulncheck and gosec
make ios-check                               # swift-format, SwiftLint, osv-scanner and mobsfscan
make clean                                   # remove build/ and the binary go build leaves
scripts/check-migrations.sh                  # every migration, in order, on Supabase's Postgres (Docker)
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
| `main.go` | Entry point: the app, and the command line, `leaflet mcp` among its commands |
| `cmd/leaflet` | The command line on Windows: `bin\leaflet.exe`, a console program, beside the app |
| `mobile` | The Go core's API for the iOS app, bound to Swift with gomobile |
| `internal/core` | Opening Leaflet's parts and wiring them together, for the desktop and iOS apps alike, and signing in and out |
| `internal/live` | A page open in the app: saving what is typed, and merging what arrives meanwhile from sync and agents |
| `internal/app` | Window, shell, sidebar, page screen, panels, dialogs and settings |
| `internal/editor` | The block editor: model, editing, layout, input and blocks |
| `internal/doc` | The page document model: blocks, deltas, inline Markdown, ops, per-block merge and Markdown export |
| `internal/menu`, `internal/shortcut` | What the `/` and `@` menus offer, and what Markdown typed in a page becomes, for both apps' editors |
| `internal/rendered` | The pages that draw math (KaTeX) and diagrams (Pele), in frames with no network |
| `internal/webpage` | A page as a web page that stands alone, as a public link shares it and Export › PDF prints it, on both apps |
| `internal/starters` | The templates that come with Leaflet |
| `internal/export` | Exporting every page as Markdown in a zip, with its pictures, files and recordings |
| `internal/web` | Link cards, pictures, video info, videos' captions and pages for agents, read from public addresses only |
| `internal/store` | SQLite (pure Go) tables, the outbox and settings |
| `internal/remote` | Supabase over HTTP: Auth, PostgREST, Storage and Edge Functions |
| `internal/sync` | Pushing the outbox, pulls, account adoption and the device heartbeat, through Supabase or iCloud Drive |
| `internal/supabaseapi` | The Supabase Management API, signed in to with the person's own account |
| `internal/provision` | Finding, creating and setting up their Leaflet project: migrations and Edge Functions |
| `internal/oauth` | The part of a browser sign-in on this device: loopback redirect and PKCE |
| `internal/icloud` | The Leaflet folder in iCloud Drive: its files, written whole, and asking iCloud to download them |
| `internal/keychain` | The app's secrets in the system keychain, read at most once a run |
| `internal/agents` | The OpenAI-compatible client, presets, prompt, page tools, loop and hub |
| `internal/mcp` | `leaflet mcp`: the page tools for agents outside the app |
| `internal/cli` | The `leaflet` command line, on those tools |
| `internal/recording` | Capture, WAV, Soniox streaming and endpoint transcription |
| `internal/macspeech`, `speech` | Transcription with the device's own speech recognition: `speech`, a Swift package the iOS app and `leaflet-speech`, the helper the Mac app ships, are built with |
| `internal/theme` | Colors, fonts and icons |
| `locales` | The app's texts in each language, and the Go code generated from them |
| `scripts` | Debug and release builds, publishing to GitHub Releases, and the migrations' check |
| `supabase` | Migrations (tables, row-level security, RPCs, Vault) and Edge Functions |
| `worker` | The sign-in Worker on Cloudflare: the OAuth redirect page and the token exchange that holds the client secret |
| `docs` | The protocol, user stories and the icon's sources |
| `ios` | The iPhone and iPad app: SwiftUI and UIKit, its XcodeGen project and build scripts |

Microphone capture runs in a small page on the app's own `leaflet-rec://`
scheme, a secure context the system's web view lets use the microphone, so
the app needs no native code to capture audio. Embeds, math and diagrams
run in sandboxed frames under a content security policy with no network,
and can never call the app's Go code. WebRTC, which such a policy does not cover,
is blocked where the web view knows CSP's `webrtc` directive and otherwise
taken away from the frame before the embed's script runs, as best it can
be.

## Known limitations

- Real-time co-editing is not built: pages sync and merge block by block.
- On-device speech recognition with open models (Qwen3-ASR, SenseVoice
  through sherpa-onnx) is not ported, as sherpa-onnx needs native
  libraries: the system's own speech recognition transcribes on the device
  instead, on a Mac with macOS 26 or later and on iPhone and iPad. On
  Windows and older Macs, serve a model behind an OpenAI-compatible
  endpoint on your network; the same goes for MOSS-Transcribe-Diarize.
- Claude account sign-in for third-party apps is unconfirmed, so Claude
  takes an API key; OpenAI takes one too, or a ChatGPT Plus or Pro
  sign-in.

## Acknowledgments

Leaflet is built on [MyGo](https://github.com/egoist/mygo). It bundles
[Figtree](https://github.com/erikdkennedy/figtree) and
[Source Serif 4](https://github.com/adobe-fonts/source-serif) under the SIL
Open Font License (`internal/theme/fonts/OFL-*.txt`),
[Lucide](https://lucide.dev) icons under the ISC license
(`internal/theme/icons/LICENSE`), and [KaTeX](https://katex.org) and
[Pele](https://github.com/obsidianmd/pele) under the MIT license for math
and diagrams ([`internal/rendered/third_party`](internal/rendered/third_party/README.md)).

## License

Leaflet is released under the [MIT License](LICENSE). The fonts, icons and
libraries it bundles keep their own licenses, as
[Acknowledgments](#acknowledgments) lists them.
