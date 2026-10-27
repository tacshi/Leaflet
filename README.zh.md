<p align="center">
  <img src="resources/icon.png" width="128" height="128" alt="Leaflet">
</p>

<h1 align="center">Leaflet</h1>

<p align="center">
  一款原生桌面应用，让人与 AI 智能体一起书写活文档。
</p>

<p align="center">
  <a href="https://github.com/tacshi/Leaflet/releases/latest"><img src="https://img.shields.io/github/v/release/tacshi/Leaflet?sort=semver&label=release" alt="最新版本"></a>
  <a href="#系统要求"><img src="https://img.shields.io/badge/macOS-12%2B-000000?logo=apple&logoColor=white" alt="macOS 12 或更高版本"></a>
  <a href="#系统要求"><img src="https://img.shields.io/badge/Windows-x64%20%7C%20arm64-0078D4?logo=windows&logoColor=white" alt="Windows x64 和 arm64"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white" alt="Go 1.27"></a>
  <a href="#应用外的智能体-mcp"><img src="https://img.shields.io/badge/MCP-supported-6E56CF" alt="支持 MCP"></a>
  <a href="https://github.com/egoist/mygo"><img src="https://img.shields.io/badge/built%20with-MyGo-F2545B" alt="基于 MyGo 构建"></a>
</p>

<p align="center">
  <a href="README.md">English</a> | <b>简体中文</b> | <a href="README.ja.md">日本語</a>
</p>

Leaflet 把你的笔记、规格文档和会议记录保存为由块组成的页面，并让 AI
智能体在你身边一起工作。你可以在行内、从选中的文字、在聊天中或在评论串里
向它提问：它会阅读页面，并通过经过校验的工具来编辑页面，整个修改只算一步，
可以一键撤销。页面先保存在你的设备上，再通过 iCloud 云盘同步，或在登录后通过 Leaflet 服务器同步，所以 Leaflet 可以离线
使用，也可以搭配本地模型。

它用 Go 和 [MyGo](https://github.com/egoist/mygo) 编写，MyGo 在 GPU
上原生绘制整个界面，应用本身不使用网页视图。

## 目录

- [功能](#功能)
- [系统要求](#系统要求)
- [安装](#安装)
- [从源码运行](#从源码运行)
- [应用外的智能体 (MCP)](#应用外的智能体-mcp)
- [构建与发布](#构建与发布)
- [开发](#开发)
- [项目结构](#项目结构)
- [已知限制](#已知限制)
- [致谢](#致谢)

## 功能

### 书写

- Leaflet 自己的块编辑器：标题、嵌套列表、带截止日期的待办、引用、标注、
  折叠块、表格、带语法高亮的代码、数学公式、图表与示意图、图片、文件、
  视频、链接卡片，以及在沙盒中运行的交互式嵌入。
- Markdown 快捷输入、`/` 菜单、用 `@` 提及成员、智能体和页面、带文字颜色
  的选区工具栏、可折叠的标题、大纲，以及指向页面和单个块的链接。
- 嵌套页面与文件夹、模板、复制页面、搜索，以及导出为 Markdown。
- 页面历史，支持命名版本，可以行内或并排查看与任一版本相比的改动。

### 与智能体协作

- 用 `@agent` 在行内、从选区、在侧边聊天或在评论串中向智能体提问。
- 每一次编辑都是经过校验的工具调用（`read_page`、`insert_blocks`、
  `replace_block`、`move_block`、`delete_blocks`、`create_subpage`），
  在保存历史快照后以流式写入页面，并合为一步撤销。
- 支持任何兼容 OpenAI 的服务商：内置 OpenAI、Claude、xAI、OpenRouter、
  DeepSeek 和 GLM 预设，支持 ChatGPT Plus 或 Pro 登录、自定义端点，以及
  Ollama、LM Studio、llama.cpp、vLLM 等本地服务，可离线运行智能体。
- 应用外的智能体，例如 Claude Code、Codex 和 Cursor，可以通过
  [`leaflet mcp`](#应用外的智能体-mcp) 处理页面。

### 录音

- 录音块，支持回放和带时间戳的转写文本。
- 使用 Soniox 或任何兼容 OpenAI 的 `/audio/transcriptions` 端点进行实时
  转写，包括你自己网络中的端点。

### 共享与同步

- 锚定在块上的评论串，以及与查看者或编辑者共享页面，子页面会继承共享设置。
- 离线优先：所有内容都保存在 SQLite 中，联网后通过发件箱同步。分开进行的
  编辑会按块合并，出现冲突时会给出提示，并可以恢复另一个版本。
- iCloud 同步：你的 Mac 和 PC 通过 iCloud 云盘中的 Leaflet 文件夹同步，
  无需服务器，也无需登录。在“设置 › 同步与存储”中打开；Windows 上需要
  iCloud for Windows。
- 待办提醒、菜单栏图标、浅色与深色主题，以及来自 GitHub Releases 的更新。
- 支持英文、简体中文和日文：Leaflet 跟随系统语言，也可以在
  设置 › 外观 中选择；欢迎指南会以首次运行时的语言写成。

## 系统要求

- macOS 12 或更高版本（Apple 芯片或 Intel）；或 x64、arm64 上的 Windows。
- 从源码构建：Go 1.27.3 或更高版本。不需要 cgo 或 C 编译器。发布构建还
  需要 Xcode（macOS）和 NSIS（Windows 安装程序）。
- iCloud 同步需要打开 iCloud 云盘。
- 登录、共享、云端模型以及与他人同步需要一个 Leaflet 服务器：一个按
  [`supabase/`](supabase/README.md) 配置的 [Supabase](https://supabase.com)
  项目。没有服务器时，Leaflet 仅在你的设备上运行。

## 安装

从[最新版本](https://github.com/tacshi/Leaflet/releases/latest)下载
macOS 磁盘映像或 Windows 安装程序。安装后的应用会从 GitHub Releases
自动更新。

## 从源码运行

```bash
git clone https://github.com/tacshi/Leaflet.git
cd Leaflet
go tool mygo dev
```

没有服务器时，Leaflet 只在本机运行：页面、编辑、评论、本地模型以及你网络
中的转写服务都可以使用；登录、同步、共享、云端模型和云端转写会被隐藏，或在
原处说明原因。

要使用 Supabase 后端（见 [`supabase/README.md`](supabase/README.md)），
可以在运行时设置：

```bash
SUPABASE_URL=http://127.0.0.1:54321 SUPABASE_ANON_KEY=<anon key> go tool mygo dev
```

或者编译进应用：

```bash
MYGO_LDFLAGS="-X leaflet/internal/remote.DefaultURL=https://… -X leaflet/internal/remote.DefaultAnonKey=…" go tool mygo build
```

想在没有 API 密钥的情况下试用智能体，可以运行一个本地模型服务（例如用
`ollama serve` 运行 `qwen3:8b`），然后在 设置 › 智能体服务商 中添加。

要以其他语言运行应用而不改变它保存的语言：

```bash
go tool mygo dev -lang zh-Hans
```

### 数据

Leaflet 把数据库保存在应用的数据文件夹中，例如 macOS 上的
`~/Library/Application Support/Leaflet`，或者 `LEAFLET_DATA_DIR` 指定的
文件夹。

## 应用外的智能体 (MCP)

`leaflet mcp` 是一个基于 stdio 的 [Model Context Protocol](https://modelcontextprotocol.io)
服务器，适用于 Claude Code、Claude Desktop、Codex、Cursor、VS Code 等客户端。
设置 › 外部智能体 中给出了每个客户端的配置方法，并已填好本应用的可执行文件
和数据文件夹。以 Claude Code 为例：

```bash
claude mcp add -s user leaflet -e LEAFLET_DATA_DIR=<数据文件夹> -- <可执行文件> mcp
```

| 工具 | 作用 |
| --- | --- |
| `list_pages`、`search_pages` | 按标题和正文查找页面 |
| `read_page` | 读取页面的块及其 ID |
| `create_page`、`rename_page` | 新建和重命名页面 |
| `insert_blocks`、`replace_block`、`move_block`、`delete_blocks` | 像应用内的智能体一样编辑页面 |

该服务器与应用打开同一个数据库，所以无论应用是否打开都能工作。仅以查看
权限共享给你的页面是只读的。智能体首次编辑某个页面前会保存一份历史快照，
之后每十分钟的编辑保存一份，并像其他修改一样同步。打开着的应用会在一秒内
读到这些修改，并与正在输入的内容合并。

要在开发构建上试用，把开发版应用的数据文件夹交给一个二进制文件：

```bash
go build -o leaflet . && claude mcp add -s user leaflet -e LEAFLET_DATA_DIR="$HOME/Library/Application Support/Leaflet Dev" -- "$PWD/leaflet" mcp
```

## 构建与发布

`go tool mygo build` 会把应用构建到 `build/`。脚本默认为 macOS 构建，
加 `--win` 则为 Windows 构建：

| 脚本 | 构建内容 |
| --- | --- |
| `scripts/build-debug.sh [--mac\|--win] [--open]` | 名为 Leaflet Debug 的调试版本，有自己的标识符和数据文件夹，从不自动更新，因此可以与正式版并存 |
| `scripts/build-release.sh [--mac] [--win] [<version>]` | 已签名并公证的 Apple 芯片、Intel 和通用磁盘映像，以及 Windows x64 和 arm64 安装程序，附带已签名的更新包 |
| `scripts/publish.sh <version> [--mac] [--win] [--dry-run]` | 一个 GitHub 发布：检查、取自 `CHANGELOG.md` 的发布说明、构建、打标签和发布 |

任何脚本加 `--help` 都会列出其选项。macOS 发布需要 Developer ID
Application 身份和一个 `notarytool` 钥匙串配置，Windows 安装程序需要
NSIS（`brew install makensis`），所有发布都需要更新签名密钥；
`scripts/build-release.sh --help` 对每一项都有说明。

## 开发

```bash
go test ./...                                # 模型、合并、存储、同步、编辑器、智能体
make check                                   # gofmt、golangci-lint、govulncheck 和 gosec
make clean                                   # 删除 build/ 和 go build 留下的二进制文件
cd supabase/functions && deno test _shared/  # Edge Functions 的共享代码
```

编辑器的测试通过 `ui.NewTester` 以无界面方式运行其视图。
[`docs/protocol.md`](docs/protocol.md) 是应用与后端之间的约定：文档格式、
智能体工具，以及保存与同步规则。

应用的文本是 [`locales/`](locales) 中的 MyGo 目录文件，每种语言一个
（`en.json`、`zh-Hans.json`、`ja.json`），`go tool mygo dev` 和
`go tool mygo build` 会据此生成 `locales/catalog_gen.go`，请把它和目录
文件一起提交。`go tool mygo i18n check` 用于检查目录文件。要添加一种语言，
添加以其 BCP 47 标签命名的目录文件即可。

## 项目结构

| 路径 | 内容 |
| --- | --- |
| `main.go` | 入口：应用本身，以及 `leaflet mcp` |
| `internal/app` | 窗口、外壳、侧边栏、页面、面板、对话框和设置 |
| `internal/editor` | 块编辑器：模型、编辑、布局、输入和块 |
| `internal/doc` | 页面文档模型：块、增量、行内 Markdown、操作、按块合并和 Markdown 导出 |
| `internal/store` | SQLite（纯 Go）表、发件箱和设置 |
| `internal/remote` | 基于 HTTP 的 Supabase：Auth、PostgREST、Storage 和 Edge Functions |
| `internal/sync` | 推送发件箱、拉取、账户接管和设备心跳，经由 Supabase 或 iCloud 云盘 |
| `internal/icloud` | iCloud 云盘中的 Leaflet 文件夹：完整写入的文件，以及请 iCloud 下载它们 |
| `internal/agents` | 兼容 OpenAI 的客户端、预设、提示词、页面工具、循环和中枢 |
| `internal/mcp` | `leaflet mcp`：供应用外智能体使用的页面工具 |
| `internal/recording` | 采集、WAV、Soniox 流式传输和端点转写 |
| `internal/theme` | 颜色、字体和图标 |
| `locales` | 各语言的应用文本，以及由其生成的 Go 代码 |
| `scripts` | 调试与发布构建，以及发布到 GitHub Releases |
| `supabase` | 迁移（表、行级安全、RPC、Vault）和 Edge Functions |
| `docs` | 协议、用户故事和图标源文件 |

麦克风采集运行在应用自有的 `leaflet-rec://` 协议下的一个小页面中，系统的
网页视图允许这种安全上下文使用麦克风，因此应用无需原生音频代码。嵌入、数学
公式和示意图运行在沙盒框架中，受禁止联网的内容安全策略约束，永远无法调用
应用的 Go 代码。

## 已知限制

- 尚未实现实时协同编辑：页面按块同步与合并。
- 设备端语音识别（Qwen3-ASR、通过 sherpa-onnx 的 SenseVoice）尚未移植，
  因为 sherpa-onnx 需要原生库。可以改为在你的网络中通过兼容 OpenAI 的端点
  提供模型；MOSS-Transcribe-Diarize 同理。
- OpenAI 和 Claude 账户能否用于第三方应用登录尚未确认，因此这些服务商使用
  API 密钥。

## 致谢

Leaflet 基于 [MyGo](https://github.com/egoist/mygo) 构建。它内置了以 SIL
Open Font License 授权的 [Figtree](https://github.com/erikdkennedy/figtree)
和 [Source Serif 4](https://github.com/adobe-fonts/source-serif)
（`internal/theme/fonts/OFL-*.txt`）、以 ISC 许可证授权的
[Lucide](https://lucide.dev) 图标（`internal/theme/icons/LICENSE`），以及
以 MIT 许可证授权、用于数学公式和示意图的 [KaTeX](https://katex.org) 和
[Pele](https://github.com/obsidianmd/pele)
（[`internal/app/third_party`](internal/app/third_party/README.md)）。
