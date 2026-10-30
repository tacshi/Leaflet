# Leaflet protocol

The contracts shared by the Go client (`internal/`) and the Supabase backend
(`supabase/`), and the format of the folder iCloud sync goes through (§8),
which any app reading it must keep to. Both sides must agree on everything
here; change it in one place and update both.

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
  (see `internal/doc/merge.go`): a block only one side changed since the
  version both last agreed on takes that side's change, whatever the times;
  the times decide only blocks both sides changed. A block one side deleted
  stays deleted if the other side left it (and everything under it) as it
  was; if the other side changed any of it, it comes back whole, and the
  conflict is kept with the deleted side's node null, so the user can delete
  it again.
- Saves are compare-and-set on `page_content.revision`, which the server
  moves on with every update. A writer reads `content, revision`, merges,
  then updates `where page_id = … and revision = <what it read>`. Zero rows
  updated means someone saved in between: read again, merge again, retry. A
  page with no row yet is inserted; a conflict on insert means the same.

### Block types

| type | data | notes |
| --- | --- | --- |
| `paragraph` | `delta` | |
| `heading` | `delta`, `level` 1–6 | |
| `bulleted_list`, `numbered_list` | `delta` | nested items go in `children` |
| `todo_list` | `delta`, `checked`, `due?` | `due` is the day it is due, `YYYY-MM-DD`, or the day and time, `YYYY-MM-DDTHH:MM`, in the user's own time |
| `quote` | `delta` | |
| `code` | `delta`, `language` | the delta is the code as plain text, line breaks and all; `language` is an ID such as `go`, or `""` for plain text |
| `callout` | `delta`, `tone` (`info`\|`tip`\|`warning`\|`danger`) | a tinted box; Markdown writes it as a GitHub alert |
| `toggle_list` | `delta`, `collapsed` | any blocks nest in `children`, hidden while collapsed |
| `divider` | — | |
| `image` | `url`, `align`, `width`, `height` | |
| `table` | `colsLen`, `rowsLen`, … | children are `table/cell` nodes with `colPosition`, `rowPosition`, each holding one `paragraph` |
| `chart` | `chart_type` (`bar`\|`line`\|`pie`), `title`, `labels` [string], `series` [{`name`, `values` [number]}], `caption`, `author` | drawn by the client |
| `embed` | `title`, `html`, `height` | sandboxed HTML, no access to app storage or tokens |
| `video` | `url`, `title` | a YouTube or Vimeo link, or a link to a video file (.mp4, .m4v, .mov, .webm, .ogv), played in the page |
| `link_preview` | `url`, `title`, `description`, `image`, `site` | a card for a web page; the client fills in what the page says about itself |
| `file` | `file_id`, `url`, `name`, `size`, `mime` | a file kept with the page, as a card |
| `math_equation` | `formula` | TeX, drawn in display style by KaTeX |
| `diagram` | `source` | Mermaid, drawn by Pele |
| `recording` | `recording_id`, `title`, `duration_ms` | transcript lives in `recordings` |
| `subpage` | `page_id` | link card to a child page |

Delta attributes: `bold`, `italic`, `underline`, `strikethrough`, `code`,
`href`, `mention` (`{ "kind": "user"|"agent"|"page", "id": "...", "name": "..." }`),
and `color` and `background`, the text's color and highlight as hex
(`"#rrggbbaa"`), which Markdown leaves out, as MyGo's rich text does.
A page mention is the page's title with `href` `leaflet://page/<id>` and a
`mention` of kind `page`; in agents' inline Markdown it is
`[Title](leaflet://page/<id>)`, and such a link read back is a page mention.
A link to a block of a page is `leaflet://page/<id>#<block id>`, which
stays a link.
Leaflet shows it as a chip, opens the page on a click, and writes the page's
current title into it when the page opens.

## 2. Agent tools

Every surface (inline `@`, selection, sidebar chat, comment `@`) runs the same
loop with the same tools. Tool definitions live in
`internal/agents/tools.go` and `supabase/functions/_shared/tools.ts`.
`leaflet mcp` (`internal/mcp`) offers the same page tools, each with a
`page_id`, to agents outside the app.

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

## 6. Sync

- The outbox keeps one entry per changed row. Replacing a queued payload
  moves the entry's revision on, and an upload takes the entry off the queue
  only if its revision is still the one sent.
- A page or folder changed on a device goes up as the columns it changed
  there (an update), so a change another device made to another column
  meanwhile stays: an offline rename does not undo a move or a deletion. A
  row the server does not have yet, or one sent again whole, goes up whole.
- Each table is pulled in `(updated_at, key)` order from a cursor of the
  last row stored here (`key` is `id`, or `page_id` for `page_content`). A
  row that cannot be stored stops that table's pull, and the next pull starts
  with it. Each pull reads again from 10 seconds behind its cursor, since a
  row is stamped before its transaction commits and one that commits late
  can carry an earlier time; rows already stored here are skipped.
- Page content from the server that a device cannot read (a newer format)
  is neither merged nor stored, so it never stands in for an empty page:
  that page's change here stays queued, and its content is asked for again
  each pull, without stopping the pull of the others.
- Page history goes up and is never pulled. A device keeps the snapshots
  of a page that its History shows, the newest 50 and every named
  (`manual`) one, and removes the others as it takes a new one, with any of
  them still waiting to go up. The server keeps each writer's newest 50
  unnamed snapshots of a page, and of its named ones all of the page
  owner's and each other writer's newest 50: a trigger prunes as each is
  inserted, only among the inserting writer's own, so one editor's
  snapshots never push out anyone else's.
- Deleting a file cannot be undone, so its bytes go with it. The deleting
  device removes its own copy (only one the app made in its data folder,
  never the file the user added) and queues the removal of the stored
  copy, which runs once the row with `deleted_at` has gone up: the object
  in the `files` bucket through Storage's `DELETE /storage/v1/object/files`
  (an object already gone counts as removed), or the blob in iCloud Drive
  once a batch has the deletion. An upload still waiting is dropped. A
  device that pulls a deleted file removes its copy in its data folder.
- Sharing a page invites: `share_page` adds a `page_permissions` row with
  `accepted_at` null for the account that has confirmed the address, or,
  when none has, holds the invite for the address (`page_email_invites`)
  until an account confirms it; the page reaches the person only once they
  accept.
  Each pass reads the account's pending invites with `page_invites()`, which
  the sidebar offers while any wait; `accept_page_invite` sets `accepted_at` (moving the row's
  `updated_at` on), and `decline_page_invite` removes the row. The shares
  are pulled by `updated_at` like the tables, filtered to
  `accepted_at=not.is.null`, and each one pulled brings its page and the
  pages under it, so a page accepted after the cursor passed its share
  comes with the next pass.
- A device syncs one way at a time: through the server while signed in, or
  through iCloud Drive (§8) while that is on; signing in turns iCloud sync
  off.
- A device keeps one account's pages. Pages made before signing in are moved
  into the account's space on its first sync. Signing out uploads what is
  waiting, then removes the account's pages, queue and cursors from the
  device; signing in as another account does the same first.

## 7. Postgres tables

See `supabase/migrations/`. Row-level security: the space owner has full
access; `page_permissions` grants `viewer` or `editor` on a page and all its
subpages once accepted (`accepted_at` set): every access check counts only
accepted rows. The person invited reads their own rows, pending or not, and
the space owner reads a page's accepted ones; only the person invited sets
`accepted_at`, through `accept_page_invite` (a trigger keeps it otherwise).
Clients never insert or update `page_permissions` rows: shares are made and
changed through `share_page`. The owner may delete the accepted ones, the
ones they read; a share or invite still waiting is withdrawn only through
`unshare_page`. Shares and invites go only to an account that has
confirmed the address, so the project must require email confirmation
(see `supabase/README.md`).
`page_history` is the owner's to read in full; anyone else reads the
snapshots taken from when they accepted a share of the page, or of a page
it is inside, on, and of a page under the shared one, no earlier than
when it came under it: the latest `pages.placed_at` of the page and of
each page between it and the shared one. `placed_at` is when a page was
put under its parent (when it was made, if it never moved; for a page
made before migration 20261031000000, when that ran). The server sets
`placed_at`, and keeps `pages.created_by` as the page was made: a client's
value for either is ignored on update. A snapshot's `created_at` is never
later than when it reached the server; each writer keeps their newest 50
unnamed snapshots of a page, and each writer but the page's owner their
newest 50 named (`manual`) ones.
Moving a page under a new parent requires edit access to both, and anyone
but the space owner moves a page only between parents that the same shares
reach (`forbidden_move` otherwise), so a move never shows a page to people
it was not shared with. A recording stays on its page: an update that
changes `recordings.page_id` is refused. A `files` row is its space owner's
to write; its `folder_id` is a folder of its space (`folder_in_other_space`
otherwise), and its `page_id`, as it is set, a page of its space or one
its writer can edit, as an editor's upload to a page shared with them is
(`forbidden_file_page` otherwise). A comment with `author_kind` `guest`,
which a public link's visitor posts through the owner's device, is
inserted only by the page's owner (`guest_comment_not_owner` otherwise).
A comment is emptied as it is deleted: once `deleted_at` is set, `body` is
empty, `quote` null and `mentions` empty.
`pages.is_template` marks a template and every subpage of one (a subpage
made under a template is one too); templates stay out of the page list,
search and `list_pages`. `pages.tags` is a page's tags (`text[]`, at most 50,
each 1 to 64 characters), tidied by the app: no leading `#`, no repeats in
any case; they sync with the row as its title does.
`folders.default_template_id` and `pages.default_template_id` name the
template a folder's new pages, or a page's new subpages, start from (null
for blank ones); there is no foreign key, and a template the app can't
find leaves new pages blank. `model_providers.encrypted_secret` and `refresh_token` hold Vault secret IDs
and are not selectable by clients.

Storage buckets: `recordings` (`{user_id}/{recording_id}.m4a`), `files`
(`{space_id}/{file_id}/{name}`), `images` (`{space_id}/{page_id}/{image_id}.{ext}`).

RPCs: `share_page(p_page_id, p_email, p_role)` (an invite to the account
that confirmed the address, or one waiting for it; it returns no rows
either way, and sharing again changes only the role),
`unshare_page(p_page_id, p_user_id)` (also withdraws an invite, by the id
`page_members` gives it), `page_members(p_page_id)` (`user_id, display_name,
email, role, accepted`; invites still waiting are listed only to the owner,
and alike whether or not the address has an account: `user_id` is the
invite's id, not the account's, `email` the address as shared, and
`display_name` empty), `page_invites()` (the
caller's pending invites: `page_id, title, role, inviter_name, created_at`,
of pages not deleted), `accept_page_invite(p_page_id)`,
`decline_page_invite(p_page_id)`, `set_provider_secret(p_provider_id, p_secret)`
(authenticated), `get_provider_secret(p_provider_id)` (service role only).

## 8. iCloud Drive folder

iCloud sync needs no server: the devices of one person sync through the
folder `Leaflet` in their iCloud Drive (`~/Library/Mobile Documents/
com~apple~CloudDocs/Leaflet` on macOS, `%USERPROFILE%\iCloudDrive\Leaflet`
with iCloud for Windows). Every file has one writer, so iCloud never keeps
two versions of one; versions are in the files, never in their times; and
nothing depends on being told of changes, which a reader finds by listing.

```
Leaflet/
  space.json                 {format, space_id, created_at, created_by_device}
  devices/<device-id>/       written by that device alone
    device.json              {format, id, name, platform, last_active_at, log_from}
    log/<seq>.json           a batch: seq is 16 digits, from 1, one more each batch
    pages/<page-id>.json     the device's version of a page
  blobs/files/<file-id>/<name>
  blobs/recordings/<recording-id>.wav
```

- **Every file** is JSON with `format` (now 1). A reader leaves alone a file
  of a newer format, and stops syncing until it is updated. Files are
  written whole: to a temporary file, moved into place. Readers pass over
  names that start with a dot, and names that fit no pattern above, such as
  the copies iCloud makes ("space 2.json").
- **space.json** names the space the devices sync: every device files its
  pages, folders and files under that `space_id`, whatever a row says. The
  first device makes it from its own space. When two made it at once, the
  lowest `space_id` among `space*.json` wins; a device that made a losing
  one moves its pages into the winner, sends them all again, and removes it.
  A folder that goes, or whose space changes, after a device joined it
  stops that device's sync until the user turns iCloud sync off and on.
- **A batch** is `{format, device, seq, written_at, snapshot, entries}`,
  never changed once written. An entry is a row,
  `{kind: "row", table, id, at, by, row, changed}`, for `pages`, `folders`,
  `files`, `comments` and `recordings` in their tables' shape (§7) with
  `updated_at` and, for a comment, `author_name`; or a page's new content,
  `{kind: "content", page_id, version}`. Page history and agent runs stay on
  each device. `row` is always the whole row; `changed` lists the columns
  the device changed, as §6 sends them up, and is left out when the whole
  row changed (a new row, one sent again whole, and every row of batches
  from before it). A row's version is `(at, by)`, compared by `at` then
  `by`, and kept for each column: where a reader has the row, an entry
  applies only the columns it changed (all, without `changed`) whose
  version there is older, so an offline rename on one device undoes no
  move or deletion made on another. A reader without the row takes it
  whole, its other columns older than any change. A device that changed a
  row it has not sent yet keeps its change, and sends it with an `at` after
  every version it has seen; what came for the row's other columns
  meanwhile applies once its change has gone.
- **A page file** is `{format, page_id, version, merged, content}`: the
  device's whole version of the page (§1), `version` when it wrote it (ms,
  one more than its last at least), and `merged`, the version of each other
  device's page file it had merged then. A device merges each other
  device's newer page file into its own (§1's merge), with the base the
  file of that device it merged last, and writes its own when the result is
  new to both. A conflict is noted on the device whose edit lost, when the
  other device had not merged it.
- **Order.** A device writes blobs and page files before the batch that
  names them, and reads each device's batches in order from the last it
  applied, stopping at one that is missing, still downloading, or does not
  parse: it reads that one first next time. iCloud may still bring a batch
  before the page file it names: a reader merges that page once the file
  has at least the version the batch named. A version is published once
  its batch is written; until then it is sent again. Files are synced to
  disk before they are moved into place.
- **A damaged batch.** A device reads its own last batch once a run, and
  rewrites it empty and sends everything again if it does not parse. A
  reader skips a batch that stays damaged only when later batches exist,
  after 20 passes and a day.
- **Compaction.** Past 500 batches, a device writes the latest entry of each
  row and page of its log as one batch with `snapshot: true` (for a row, with
  each earlier entry that changed a column the later ones did not), sets
  `log_from` to its seq, and removes the batches before it. A reader that
  finds batches missing goes on from the first snapshot after the gap, as
  a compaction cut short leaves batches before its snapshot.
- **A device that finds** in its own folder a batch after the last it wrote
  (other than the one it notes it is writing), or a copy iCloud made, shares its ID with another device (a restored
  backup): it takes a new ID, reads the old ID's folder as another device's,
  and sends everything again.
- **Blobs** are written by a device that has the file or audio. A device
  that deletes a file removes its blob, whoever wrote it, once a batch of
  its own has the deletion; one that sends everything again sends deleted
  files' rows too, so a deletion in a batch that was lost goes again.
- **Not downloaded.** A file iCloud has not downloaded (on macOS before 14,
  `.<name>.icloud` beside where it goes; after, a file flagged dataless) is
  asked for, and read on a later pass.
