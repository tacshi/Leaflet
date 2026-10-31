<p align="center">
  <img src="resources/icon.png" width="128" height="128" alt="Leaflet">
</p>

<h1 align="center">Leaflet</h1>

<p align="center">
  人と AI エージェントが一緒に「生きたドキュメント」を書く、ネイティブのデスクトップアプリ。
</p>

<p align="center">
  <a href="https://github.com/tacshi/Leaflet/releases/latest"><img src="https://img.shields.io/github/v/release/tacshi/Leaflet?sort=semver&label=release" alt="最新リリース"></a>
  <a href="#動作環境"><img src="https://img.shields.io/badge/macOS-12%2B-000000?logo=apple&logoColor=white" alt="macOS 12 以降"></a>
  <a href="#動作環境"><img src="https://img.shields.io/badge/Windows-x64%20%7C%20arm64-0078D4?logo=windows&logoColor=white" alt="Windows x64 と arm64"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white" alt="Go 1.27"></a>
  <a href="#アプリ外のエージェント-mcp"><img src="https://img.shields.io/badge/MCP-supported-6E56CF" alt="MCP 対応"></a>
  <a href="https://github.com/egoist/mygo"><img src="https://img.shields.io/badge/built%20with-MyGo-F2545B" alt="MyGo で構築"></a>
</p>

<p align="center">
  <a href="README.md">English</a> | <a href="README.zh.md">简体中文</a> | <b>日本語</b>
</p>

Leaflet は、メモや仕様書、議事録をブロックでできたページとして保存し、AI
エージェントがあなたの隣でそこに書き込めるようにします。行内で、選択範囲から、
チャットで、あるいはコメントスレッドで頼むと、エージェントはページを読み、
検証済みのツールで編集します。その編集は 1 回の操作として取り消せます。ページは
まずデバイスに保存され、iCloud Drive を通じて、またはサインインすると Leaflet サーバーを通じて同期されるので、Leaflet はオフラインでも、
ローカルモデルでも使えます。

Leaflet は Go と [MyGo](https://github.com/egoist/mygo) で書かれています。MyGo
はインターフェイス全体を GPU でネイティブに描画し、アプリ自体には Web
ビューを使いません。

## 目次

- [機能](#機能)
- [動作環境](#動作環境)
- [インストール](#インストール)
- [ソースから実行](#ソースから実行)
- [アプリ外のエージェント (MCP)](#アプリ外のエージェント-mcp)
- [ビルドとリリース](#ビルドとリリース)
- [開発](#開発)
- [プロジェクト構成](#プロジェクト構成)
- [既知の制限](#既知の制限)
- [謝辞](#謝辞)

## 機能

### 書く

- Leaflet 独自のブロックエディタ：見出し、入れ子のリスト、期日付きの ToDo、
  引用、コールアウト、トグル、表、シンタックスハイライト付きのコード、数式、
  ダイアグラム、グラフ、画像、ファイル、動画、リンクカード、サンドボックス内で
  動くインタラクティブな埋め込み。
- Markdown のショートカット、`/` メニュー、メンバー・エージェント・ページへの
  `@` メンション、文字色を変えられる選択ツールバー、折りたためる見出し、
  アウトライン、ページや個々のブロックへのリンク。
- 入れ子のページとフォルダ、テンプレート、ページの複製、検索、Markdown への
  書き出し。
- 名前付きバージョンを持つページ履歴と、任意のバージョンからの変更点の表示
  （インラインまたは左右に並べて）。

### エージェントと働く

- `@agent` で、行内、選択範囲、サイドチャット、コメントスレッドからエージェント
  に頼めます。
- すべての編集は検証済みのツール呼び出し（`read_page`、`insert_blocks`、
  `replace_block`、`move_block`、`delete_blocks`、`create_subpage`）で、
  履歴のスナップショットを取ってからページにストリーミングされ、1 回の取り消し
  単位になります。
- OpenAI 互換のプロバイダならどれでも使えます：OpenAI、Claude、xAI、
  OpenRouter、DeepSeek、GLM のプリセット、ChatGPT Plus / Pro でのサインイン、
  カスタムエンドポイント、そしてオフラインでエージェントを動かせる Ollama、
  LM Studio、llama.cpp、vLLM などのローカルサーバー。
- Claude Code、Codex、Cursor などアプリ外のエージェントも
  [`leaflet mcp`](#アプリ外のエージェント-mcp) を通じてページを扱えます。

### 録音

- 再生とタイムスタンプ付きの文字起こしを備えた録音ブロック。
- Soniox、または自分のネットワーク内のものを含む任意の OpenAI 互換
  `/audio/transcriptions` エンドポイントによるリアルタイム文字起こし。

### 共有と同期

- ブロックに紐づくコメントスレッドと、閲覧者・編集者とのページ共有（サブページ
  に継承されます）。
- オフラインファースト：すべて SQLite に保存され、オンラインになると送信トレイ
  を通じて同期されます。別々に行われた編集はブロック単位でマージされ、競合が
  あれば通知とともにもう一方のバージョンを戻す手段が示されます。
- iCloud 同期：Mac や PC が iCloud Drive の Leaflet フォルダを通じて同期
  します。サーバーもサインインも不要です。「設定 › 同期とストレージ」で
  オンにします。Windows では iCloud for Windows が必要です。
- ToDo のリマインダー、メニューバーのアイコン、ライトとダークのテーマ、
  GitHub Releases からのアップデート。
- 英語・簡体字中国語・日本語に対応：Leaflet はシステムの言語に従うほか、
  設定 › 外観 で選ぶこともできます。ウェルカムガイドは初回起動時の言語で
  作成されます。

## 動作環境

- macOS 12 以降（Apple シリコンまたは Intel）、または x64 / arm64 の Windows。
- ソースからビルドするには Go 1.27.3 以降。cgo や C コンパイラは不要です。
  リリースビルドには、macOS では Xcode、Windows インストーラには NSIS も
  必要です。
- iCloud 同期には iCloud Drive をオンにしておく必要があります。
- サーバー経由の同期、共有、クラウドモデルには [Supabase](https://supabase.com)
  のアカウントが必要です：**Supabase で続ける** がその中のプロジェクトを見つけるか作成し、
  [`supabase/`](supabase/README.md) の内容で設定します。アカウントがなければ、
  Leaflet はデバイス上だけで動作します。

## インストール

[最新リリース](https://github.com/tacshi/Leaflet/releases/latest)から macOS
用のディスクイメージ、または Windows 用のインストーラをダウンロードしてください。
インストールしたアプリは GitHub Releases から自動でアップデートされます。

## ソースから実行

```bash
git clone https://github.com/tacshi/Leaflet.git
cd Leaflet
go tool mygo dev
```

サーバーがない場合、Leaflet はこのデバイスだけで動作します。ページ、編集、
コメント、ローカルモデル、ネットワーク内の文字起こしサーバーはすべて使え、
サインイン、同期、共有、クラウドモデル、クラウドの文字起こしは非表示になるか、
その場で理由が説明されます。

どのビルドでも Supabase でサインインできます。OAuth アプリのクライアント ID は組み込まれ、
シークレットは Leaflet のサインイン用 Worker が保持しています（[`supabase/README.md`](supabase/README.md) を参照）。

ローカルスタックなど特定のサーバーを使う場合は、それを指定し、メールで届くコードでサインインします：

```bash
SUPABASE_URL=http://127.0.0.1:54321 SUPABASE_ANON_KEY=<publishable key> go tool mygo dev
```

API キーなしでエージェントを試すには、ローカルのモデルサーバー（たとえば
`ollama serve` と `qwen3:8b`）を動かし、設定 › エージェントプロバイダ で追加します。

保存されている言語を変えずに、別の言語でアプリを起動するには：

```bash
go tool mygo dev -lang ja
```

### データ

Leaflet はデータベースをアプリのデータフォルダ（macOS では
`~/Library/Application Support/Leaflet` など）、または `LEAFLET_DATA_DIR`
で指定したフォルダに保存します。

## アプリ外のエージェント (MCP)

`leaflet mcp` は stdio で動く [Model Context Protocol](https://modelcontextprotocol.io)
サーバーで、Claude Code、Claude Desktop、Codex、Cursor、VS Code などの
クライアントから使えます。設定 › 外部エージェント に、このアプリの実行ファイルと
データフォルダを埋め込んだ各クライアント向けの設定方法があります。Claude Code
の場合：

```bash
claude mcp add -s user leaflet -e LEAFLET_DATA_DIR=<データフォルダ> -- <実行ファイル> mcp
```

| ツール | 内容 |
| --- | --- |
| `list_pages`、`search_pages` | タイトルと本文でページを探す |
| `read_page` | ページのブロックを ID 付きで読む |
| `create_page`、`rename_page` | ページの作成と名前の変更 |
| `move_page` | ページをフォルダや別のページの中、またはトップレベルへ移動する |
| `create_folder`、`rename_folder` | フォルダの作成と名前の変更 |
| `insert_blocks`、`replace_block`、`move_block`、`delete_blocks` | アプリ内のエージェントと同じようにページを編集する |

このサーバーはアプリと同じデータベースを開くので、アプリが開いていても閉じて
いても動作します。閲覧のみで共有されたページは読み取り専用です。エージェントが
あるページを初めて編集する前に履歴のスナップショットを 1 つ保存し、その後は
編集 10 分ごとに 1 つ保存し、ほかの変更と同じように同期します。開いている
アプリは 1 秒以内にその内容を取り込み、入力中の内容とマージします。

開発ビルドで試すには、開発版アプリのデータフォルダをバイナリに渡します：

```bash
go build -o leaflet . && claude mcp add -s user leaflet -e LEAFLET_DATA_DIR="$HOME/Library/Application Support/Leaflet Dev" -- "$PWD/leaflet" mcp
```

## ビルドとリリース

`go tool mygo build` はアプリを `build/` にビルドします。スクリプトはデフォルト
で macOS 向けに、`--win` を付けると Windows 向けにビルドします：

| スクリプト | ビルドするもの |
| --- | --- |
| `scripts/build-debug.sh [--mac\|--win] [--open]` | Leaflet Debug という名前のデバッグビルド。独自の識別子とデータフォルダを持ち、自動アップデートしないため、リリース版と並べて動かせます |
| `scripts/build-release.sh [--mac] [--win] [<version>]` | 署名・公証済みの Apple シリコン用、Intel 用、ユニバーサルのディスクイメージと、Windows x64 / arm64 用インストーラ、署名付きのアップデートアーカイブ |
| `scripts/publish.sh <version> [--mac] [--win] [--dry-run]` | GitHub リリース：チェック、`CHANGELOG.md` からのリリースノート、ビルド、タグ付け、公開 |

どのスクリプトも `--help` でオプションを表示します。macOS のリリースには
Developer ID Application の証明書と `notarytool` のキーチェーンプロファイル、
Windows インストーラには NSIS（`brew install makensis`）、すべてのリリースには
アップデート署名鍵が必要です。それぞれ `scripts/build-release.sh --help` で
説明しています。

## 開発

```bash
go test ./...                                # モデル、マージ、ストア、同期、エディタ、エージェント
make check                                   # gofmt、golangci-lint、govulncheck、gosec
make clean                                   # build/ と go build が残すバイナリを削除
cd supabase/functions && deno test _shared/  # Edge Functions の共有コード
```

エディタのテストは `ui.NewTester` でビューをヘッドレスに動かします。
[`docs/protocol.md`](docs/protocol.md) はアプリとバックエンドの取り決めです：
ドキュメント形式、エージェントのツール、保存と同期のルール。

アプリのテキストは [`locales/`](locales) にある MyGo のカタログで、言語ごとに
1 ファイル（`en.json`、`zh-Hans.json`、`ja.json`）です。`go tool mygo dev`
と `go tool mygo build` がここから `locales/catalog_gen.go` を生成するので、
カタログと一緒にコミットしてください。`go tool mygo i18n check` でカタログを
検査できます。言語を追加するには、BCP 47 タグを名前にしたカタログを追加します。

## プロジェクト構成

| パス | 内容 |
| --- | --- |
| `main.go` | エントリポイント：アプリと `leaflet mcp` |
| `internal/app` | ウィンドウ、シェル、サイドバー、ページ画面、パネル、ダイアログ、設定 |
| `internal/editor` | ブロックエディタ：モデル、編集、レイアウト、入力、ブロック |
| `internal/doc` | ページのドキュメントモデル：ブロック、差分、インライン Markdown、操作、ブロック単位のマージ、Markdown 書き出し |
| `internal/store` | SQLite（純 Go）のテーブル、送信トレイ、設定 |
| `internal/remote` | HTTP 経由の Supabase：Auth、PostgREST、Storage、Edge Functions |
| `internal/sync` | 送信トレイのプッシュ、プル、アカウントの引き継ぎ、デバイスのハートビート（Supabase または iCloud Drive 経由） |
| `internal/supabaseapi` | 本人の Supabase アカウントでサインインする Supabase Management API |
| `internal/provision` | 本人の Leaflet プロジェクトの検索、作成、設定：マイグレーションと Edge Functions |
| `internal/oauth` | ブラウザでのサインインのうちこのデバイス側の部分：ループバックのリダイレクトと PKCE |
| `internal/icloud` | iCloud Drive の Leaflet フォルダ：まるごと書き込むファイルと、iCloud へのダウンロードの依頼 |
| `internal/agents` | OpenAI 互換クライアント、プリセット、プロンプト、ページツール、ループ、ハブ |
| `internal/mcp` | `leaflet mcp`：アプリ外のエージェント向けページツール |
| `internal/recording` | キャプチャ、WAV、Soniox ストリーミング、エンドポイントでの文字起こし |
| `internal/theme` | 色、フォント、アイコン |
| `locales` | 各言語のアプリのテキストと、そこから生成される Go コード |
| `scripts` | デバッグビルドとリリースビルド、GitHub Releases への公開 |
| `supabase` | マイグレーション（テーブル、行レベルセキュリティ、RPC、Vault）と Edge Functions |
| `worker` | Cloudflare 上のサインイン用 Worker：OAuth のリダイレクトページと、クライアントシークレットを持つトークン交換 |
| `docs` | プロトコル、ユーザーストーリー、アイコンのソース |

マイクのキャプチャは、アプリ独自の `leaflet-rec://` スキーム上の小さなページで
動きます。これはシステムの Web ビューがマイクの使用を許すセキュアコンテキストな
ので、アプリにネイティブの音声コードは要りません。埋め込み、数式、ダイアグラムは
ネットワークを許さないコンテンツセキュリティポリシーの下、サンドボックス化された
フレームで動き、アプリの Go コードを呼び出すことはできません。

## 既知の制限

- リアルタイムの共同編集は未実装です：ページはブロック単位で同期・マージされます。
- オンデバイスの音声認識（Qwen3-ASR、sherpa-onnx 経由の SenseVoice）は、
  sherpa-onnx がネイティブライブラリを必要とするため移植していません。代わりに
  ネットワーク内の OpenAI 互換エンドポイントでモデルを提供してください。
  MOSS-Transcribe-Diarize も同様です。
- サードパーティアプリでの OpenAI と Claude のアカウントによるサインインは確認
  できていないため、これらのプロバイダは API キーを使います。

## 謝辞

Leaflet は [MyGo](https://github.com/egoist/mygo) の上に作られています。
SIL Open Font License の [Figtree](https://github.com/erikdkennedy/figtree)
と [Source Serif 4](https://github.com/adobe-fonts/source-serif)
（`internal/theme/fonts/OFL-*.txt`）、ISC ライセンスの
[Lucide](https://lucide.dev) アイコン（`internal/theme/icons/LICENSE`）、
数式とダイアグラムのための MIT ライセンスの [KaTeX](https://katex.org) と
[Pele](https://github.com/obsidianmd/pele)
（[`internal/app/third_party`](internal/app/third_party/README.md)）を同梱
しています。
