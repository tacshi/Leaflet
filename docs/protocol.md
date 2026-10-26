# Leaflet protocol

The contracts shared by the Go client (`internal/`) and the Supabase backend
(`supabase/`). Both sides must agree on everything here; change it in one place
and update both.

## 1. Page content

`page_content.content` (jsonb) holds one editor document plus per-block sync
metadata:

```json
{
  "document": {
    "type": "page",
    "id": "root",
    "children": [
      { "id": "a1b2c3", "type": "heading", "data": { "level": 2, "delta": [{ "insert": "Goals" }] } },
      { "id": "d4e5f6", "type": "paragraph", "data": { "delta": [{ "insert": "Ship ", "attributes": { "bold": true } }, { "insert": "v1" }] } }
    ]
  },
  "meta": {
    "v": 1,
    "blocks":  { "a1b2c3": 1761400000000, "d4e5f6": 1761400005000 },
    "deleted": { "zz9999": 1761400009000 }
  }
}
```

- Node JSON is appflowy_editor's (`type`, `data`, `children`) **plus `id`**, the
  stable block ID. Comments, focus lists, and agent ops address blocks by ID.
- `meta.blocks[id]` is the last time (ms since epoch) that block's own content,
  type, or position changed. `meta.deleted[id]` is a tombstone.
- Until CRDTs land, two versions of a page merge **last-write-wins per block**
  (see `internal/doc/merge.go`).

### Block types

| type | data | notes |
| --- | --- | --- |
| `paragraph` | `delta` | |
| `heading` | `delta`, `level` 1–6 | |
| `bulleted_list`, `numbered_list` | `delta` | nested items go in `children` |
| `todo_list` | `delta`, `checked` | |
| `quote` | `delta` | |
| `code` | `delta`, `language` | the delta is the code as plain text, line breaks and all; `language` is an ID such as `go`, or `""` for plain text |
| `callout` | `delta`, `tone` (`info`\|`tip`\|`warning`\|`danger`) | a tinted box; Markdown writes it as a GitHub alert |
| `toggle_list` | `delta`, `collapsed` | any blocks nest in `children`, hidden while collapsed |
| `divider` | — | |
| `image` | `url`, `align`, `width`, `height` | |
| `table` | `colsLen`, `rowsLen`, … | children are `table/cell` nodes with `colPosition`, `rowPosition`, each holding one `paragraph` |
| `chart` | `chart_type` (`bar`\|`line`\|`pie`), `title`, `labels` [string], `series` [{`name`, `values` [number]}], `caption`, `author` | drawn by the client |
| `embed` | `title`, `html`, `height` | sandboxed HTML, no access to app storage or tokens |
| `video` | `url`, `title` | a YouTube or Vimeo link, played in the page |
| `link_preview` | `url`, `title`, `description`, `image`, `site` | a card for a web page; the client fills in what the page says about itself |
| `file` | `file_id`, `url`, `name`, `size`, `mime` | a file kept with the page, as a card |
| `math_equation` | `formula` | TeX, drawn in display style by KaTeX |
| `diagram` | `source` | Mermaid, drawn by Pele |
| `recording` | `recording_id`, `title`, `duration_ms` | transcript lives in `recordings` |
| `subpage` | `page_id` | link card to a child page |

Delta attributes: `bold`, `italic`, `underline`, `strikethrough`, `code`,
`href`, `mention` (`{ "kind": "user"|"agent", "id": "...", "name": "..." }`).

## 2. Agent tools

Every surface (inline `@`, selection, sidebar chat, comment `@`) runs the same
loop with the same tools. Tool definitions live in
`internal/agents/tools.go` and `supabase/functions/_shared/tools.ts`.

### BlockSpec (what a model writes)

```json
{
  "type": "paragraph|heading|bulleted_list|numbered_list|todo_list|toggle_list|quote|callout|code|divider|image|table|chart|embed|video|link_preview|math_equation|diagram",
  "text": "inline markdown: **bold**, *italic*, `code`, ~~strike~~, [link](https://…); for code, the code verbatim",
  "language": "go",
  "tone": "tip",
  "collapsed": false,
  "formula": "e^{i\\pi} + 1 = 0",
  "source": "flowchart LR\n  Idea --> Draft --> Publish",
  "level": 2,
  "checked": false,
  "children": [ { "type": "bulleted_list", "text": "nested item" } ],
  "rows": [["Header A", "Header B"], ["a", "b"]],
  "chart": { "chart_type": "bar", "title": "Risks by phase", "labels": ["Editor", "Agents"], "series": [{ "name": "Open risks", "values": [1, 4] }] },
  "embed": { "title": "Phase timeline", "html": "<!doctype html>…", "height": 320 },
  "image": { "url": "https://…", "alt": "…" },
  "video": { "url": "https://www.youtube.com/watch?v=…", "title": "…" },
  "link": { "url": "https://…" }
}
```

### Position

Exactly one of: `{ "after": "<block id>" }`, `{ "before": "<block id>" }`,
`{ "parent": "<block id>" }` (append as last child), `{ "at": "start" }`,
`{ "at": "end" }`.

### Tools

| name | arguments | returns |
| --- | --- | --- |
| `read_page` | — | `{ page: {id, title}, blocks: [BlockView], subpages: [{id, title}], comments: [{block_id, resolved, thread: [{author, body}]}] }` |
| `insert_blocks` | `position`, `blocks: [BlockSpec]` | `{ inserted_ids: [...] }` |
| `replace_block` | `id`, `block: BlockSpec` | `{ id }` (the ID is kept) |
| `move_block` | `id`, `position` | `{ id }` |
| `delete_blocks` | `ids: [...]` | `{ deleted_ids: [...] }` |
| `create_subpage` | `title`, `blocks?: [BlockSpec]` | `{ page_id }` |
| `list_pages` | — | `{ pages: [{id, title, parent_id}] }` (only when cross-page reading is on) |
| `read_other_page` | `page_id` | same shape as `read_page` |

`BlockView` = `{ id, type, text, depth, parent_id?, level?, checked?, language?, tone?, collapsed?, formula?, source?, rows?, chart?, embed?, recording? }`
(the client's also has `video`, `link` and `file` objects; the server's a `url`);
a code block's `text` is its code, not markdown.

Validation before applying any call: the tool is known; every referenced block
ID exists in the page; the block type is allowed; nesting is legal (only list
types take children); the invoking user can edit the page (agents act with the
invoking user's permissions). An invalid call returns `{ "error": "..." }` to
the model and changes nothing.

## 3. Ops

Each applied tool call produces one or more ops. Ops are what the server
streams to the client, what the client applies to the open editor, and what
"Undo run" inverts. They carry enough to be inverted by block ID.

```json
{ "op": "insert",  "position": { "after": "a1b2c3" }, "nodes": [NodeJSON] }
{ "op": "replace", "id": "a1b2c3", "node": NodeJSON, "previous": NodeJSON }
{ "op": "move",    "id": "a1b2c3", "position": { "before": "d4e5f6" }, "from": Position }
{ "op": "delete",  "ids": ["a1b2c3"], "previous": [{ "node": NodeJSON, "position": Position }] }
{ "op": "create_page", "page": { "id", "space_id", "parent_id", "title", "sort_key" }, "content": PageContent }
```

## 4. Agent run (cloud model)

1. Client flushes unsynced edits of the page, then inserts an `ai_jobs` row:
   `{ id, page_id, user_id, surface, focus_block_ids, comment_id?, conversation_id?, provider_id, model, status: "queued", request: { instruction, selection_text?, allow_cross_page } }`.
2. Client calls `POST /functions/v1/agent` with `{ "job_id": "…" }` and the
   user's JWT. The response is NDJSON, one event per line:
   - `{ "type": "status", "status": "running" }`
   - `{ "type": "op", "op": Op, "summary": "Inserted chart" }`
   - `{ "type": "text", "delta": "…" }`
   - `{ "type": "done", "reply": "…", "edits": [{ "summary": "…", "block_ids": [...] }] }`
   - `{ "type": "error", "message": "…" }`
3. The worker loads the page, focus blocks, the last 10 turns of the
   conversation, and (if allowed) titles of other pages; calls the model with
   the tools above, up to 12 rounds; validates and applies each call; streams
   the op; finally saves `page_content`, writes a `page_history` row
   (`reason: "agent_run"`, the content from before the run), updates the job
   (`status: done`, `result: { reply, ops, edits }`), and for the `comment`
   surface inserts the reply as a comment by the agent.
4. The client applies streamed ops to the open editor as one undo step.

Local models (`is_local`) run the identical loop on the client against the
local endpoint, so they work offline.

## 5. Edge Functions

All take the user's JWT (`Authorization: Bearer`). Bodies are JSON.

| function | body | response |
| --- | --- | --- |
| `agent` | `{ job_id }` | NDJSON stream (above) |
| `providers` | `{ action: "save", provider: { id?, preset, name, base_url, auth_kind, api_key?, default_model } }` | `{ provider }` (no secret) |
| | `{ action: "models", provider_id? , base_url?, api_key? }` | `{ models: [id], supports_tools?: bool }` |
| | `{ action: "test", provider_id? , base_url?, api_key?, model }` | `{ ok, models: [id], supports_tools, message }` |
| | `{ action: "delete", id }` | `{ ok }` |
| `stt` | `{ action: "soniox_key" }` | `{ api_key, expires_at }` (short-lived Soniox key) |
| | `{ action: "transcribe", recording_id, language? }` | `{ segments: [{start_ms, end_ms, text, speaker?}] }` |
| | `{ action: "save_key", provider: "soniox", api_key }` | `{ ok }` |
| `account` | `{ action: "delete" }` | `{ ok }` |

## 6. Postgres tables

See `supabase/migrations/`. Row-level security: the space owner has full
access; `page_permissions` grants `viewer` or `editor` on a page and all its
subpages; moving a page under a new parent requires edit access to both.
`model_providers.encrypted_secret` and `refresh_token` hold Vault secret IDs
and are not selectable by clients.

Storage buckets: `recordings` (`{user_id}/{recording_id}.m4a`), `files`
(`{space_id}/{file_id}/{name}`), `images` (`{space_id}/{image_id}.{ext}`).

RPCs: `share_page(p_page_id, p_email, p_role)`, `unshare_page(p_page_id, p_user_id)`,
`page_members(p_page_id)`, `set_provider_secret(p_provider_id, p_secret)`
(authenticated), `get_provider_secret(p_provider_id)` (service role only).
