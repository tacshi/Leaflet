# Leaflet user stories

A walkthrough of the app as an ordinary user on macOS (2026-10-26). The app
was built from `mygo` at 8a6293c, ran signed out (this build has no server),
used ChatGPT as the agent provider and Soniox for speech to text, and kept its
data in the folder where both were already set up.

✅ works · ⚠️ works, with issues · ❌ broken · ➖ not testable in this build

| # | Story | Result |
|---|-------|--------|
| 1 | First launch shows the welcome tour | ✅ |
| 2 | Scroll the whole welcome page: to-dos, formats, table, image, 3 charts, 3 embeds, subpage | ✅ (the embeds don't follow dark theme, see B9) |
| 3 | New page (⌘N), type a title and text, it shows in the sidebar | ⚠️ the "Start with" options are misaligned (B5) |
| 4 | Add a subpage and open it | ⚠️ B2, M3 |
| 5 | Folders: create, rename, move a page in, page list under it | ⚠️ rename doesn't preselect the old name (B16) |
| 6 | Rename, move, delete a page, then Undo the delete | ⚠️ the Move list is misaligned (B5) |
| 7 | Back/Forward, ⌘[, hide/show the sidebar, reopen on the last page after a relaunch | ⚠️ B14 |
| 8 | ⌘P search across titles, text, transcripts, comments; recents; empty state | ⚠️ folders aren't searchable (M8) |
| 9 | Markdown shortcuts `#` `##` `-` `1.` `[]` `>` `**` `*` `` ` `` and Tab/Shift-Tab | ⚠️ `---` doesn't become a divider (B3) |
| 10 | `/` menu with filtering | ✅ |
| 11 | Nested lists, to-dos, ending a list with Enter | ✅ |
| 12 | Selection toolbar: bold, link, comment, ask agent | ✅ (no strikethrough button) |
| 13 | ⌘Z / ⇧⌘Z | ✅ |
| 14 | Select across blocks, copy, paste into another page | ❌ paste flattens nesting and degrades blocks (B4) |
| 15 | Table: edit cells, Tab, + Row | ✅ |
| 16 | Chart: insert, set data, pick Pie, save | ✅ |
| 17 | Image: upload from disk, delete, undo | ✅ |
| 18 | Interactive embed: insert, run, click | ⚠️ can't be edited afterwards (B9) |
| 19 | Inline `@ChatGPT` writes into the page | ⚠️ tiny input box (B6) |
| 20 | Selection › Ask agent › "Make it shorter" | ✅ |
| 21 | Agent panel: edit a table, answer a question, Undo run / Redo | ⚠️ Return doesn't send (B1) |
| 22 | `@ChatGPT` in a comment answers in the thread | ⚠️ no @ autocomplete (M2) |
| 23 | Record from the header with a live transcript, stop, play | ⚠️ B12, B13 |
| 24 | Recording › Summarize with agent | ✅ |
| 25 | Comments: add, reply, resolve, show resolved, reopen, delete | ⚠️ B1 (reply), M4 |
| 26 | Page history: snapshots before each agent run, restore | ⚠️ B10, B11 |
| 27 | Export one page as Markdown; export all as a zip | ⚠️ B15 |
| 28 | Settings › Account: name, theme, text size apply immediately and persist | ✅ |
| 29 | Settings › Agent providers: open, Test | ⚠️ B7, B8 |
| 30 | Settings › Speech to text: Test key, Record a test | ✅ key test · ➖ mic test inconclusive (speaker audio from `say` didn't reach the mic) |
| 31 | Settings › Sync and storage | ✅ (only "Data folder" and "Image cache" in this build) |
| 32 | Share while signed out explains that a server is needed | ✅ |

Not testable here: sign-in, sync, conflicts, sharing roles, devices, delete
account (no server in this build); dragging to resize the sidebar wasn't tried.

## Bugs

All 16 are fixed, with tests for B3, B4, B9 (welcome migration), B14
(stored agent editors) and B15. Checked in the app: B1, B2, B5, B6, B7,
B8, B9, B14, B16. B10–B13 are fixed in code but weren't run through in the
app; B12 needs a real recording to confirm.

**B1. Return doesn't send in the agent panel or in comment replies.** It adds
a newline; only the send button works. The code means Return to send
(`internal/app/panel.go:110`, `:374`), but the text area seems to handle
Return itself before `Shortcut` sees it. ⌘Return for a new comment works.

**B2. Page actions › Add subpage doesn't focus the new page's title.** Typing
straight away goes nowhere. ⌘N and the sidebar's + do focus it. All three
call `titleIn.Focus()` (`internal/app/app.go:576`), so focus is probably lost
when the native menu closes.

**B3. `---` doesn't turn into a divider.** The comment on `markdownShortcut`
(`internal/editor/edit.go:132`) says it does, but the map has no `---` entry.
Pasting `---` does work.

**B4. Copy and paste inside the app loses structure.** `copyText` writes
nested items indented, but `parseLines` strips the indentation, so nesting is
lost (`internal/editor/layout.go:249`). Tables, charts, images, embeds and
recordings copy as Markdown and paste back as plain text lines.

**B5. Rows centered instead of left-aligned.** The "Start with" options on an
empty page are centered and staggered under a left-aligned label, and so is
the Move-to list. `ui.ButtonBase` centers its content unless given
`.Justify(ui.Start)`, which the provider rows already use
(`internal/app/page.go:276`, the move targets in `internal/app/dialogs.go`).

**B6. The inline "Ask ChatGPT" input is about 100 px wide** inside a
full-width card. The placeholder is cut off ("Ask the agent to write, rewrite
o") and longer prompts scroll out of view.

**B7. The chevron on a provider row sits right after the text,** not at the
card's right edge (Settings › Agent providers).

**B8. The ChatGPT provider is labelled "chat only" while Test says "tool
calling works"** and agents edit pages fine. `supports_tools` is stored as 0
from an earlier test. The hub ignores it for account providers, but the label
doesn't (`internal/app/settings.go:367`), and Save only records a test result
when the value was never set (`:869`).

**B9. Embeds:**
- There's no way to edit one after inserting it: no edit control and no menu.
- Agents can't change one either. ChatGPT replied that the page reader "shows
  the embed's title and size, but not its HTML", so `read_page` leaves the
  source out.
- The WebView is cached per block ID and loaded once (`internal/app/host.go:508`),
  so a change to `html` from an agent run, undo or history restore wouldn't
  show until the page is reopened.
- The background is hard-coded to `#ffffff`, and embeds get no hint of the
  app's theme. In dark mode the welcome page's savings calculator and piano
  are bright white boxes.

**B10. Agent toasts.**
- "ChatGPT is working…" draws on top of the previous "edited N blocks · Undo
  run" toast, and "Restored the earlier version" overlaps it the same way.
- The Undo run toast never goes away and survives a history restore. Clicking
  it afterwards says "Undid ChatGPT's edits" and changes nothing.

**B11. After a history restore, the old selection range stays highlighted**
over different text.

**B12. The final transcript dropped words the live transcript had.** The live
view ended "…yeah, that's it. 哎。 It didn't record". After "Adding
timestamps…" the stored transcript ended at "哎。".

**B13. The recording's play button still says "Play" in its tooltip while
playing.**

**B14. After a relaunch:**
- The sidebar doesn't expand to the open page, so a page inside a folder
  isn't highlighted.
- The byline changed from "Edited by Rocky and ChatGPT" to "Edited by Rocky".

**B15. Export:**
- The "Export all" zip is flat: folders and nesting are gone.
- Images are absolute `file://` links into the data folder and aren't in the
  zip.
- Entries are dated 1980.
- The single-page export writes an embed as "*Interactive embed (interactive
  embed)*".

**B16. Rename doesn't select the old name.** Rename folder appends to the old
name ("TravelJapan 2027"), and page Rename puts the caret at the end of the
title.

## Missing features

- **M1.** `@` in the page only lists agents. There's no way to mention or link
  another page.
- **M2.** Comments have no `@` autocomplete. You have to type the agent's exact
  name.
- **M3.** Subpages made with the sidebar's + or Page actions › Add subpage
  aren't linked from the parent page; only `/subpage` and "Add a subpage" add
  a link block. The welcome page presents them as the same thing, and the
  parent's Markdown export leaves those subpages out.
- **M4.** The commented words aren't highlighted in the page (only a count on
  the block), and comments can't be edited.
- **M5.** No Trash. A deleted page can only be recovered from the Undo toast.
- **M6.** No import (Markdown or zip), although export exists.
- **M7.** No Duplicate page.
- **M8.** Search doesn't find folders, and result snippets show the start of
  the page rather than the match.
- **M9.** No Settings… (⌘,) in the app menu.
- **M10.** Links open only with ⌘-click, and nothing shows the URL on hover,
  so there's no hint that links can be opened.
- **M11.** The selection toolbar has no strikethrough, though `~~text~~` works.
- **M12.** The chart data dialog has no preview.
