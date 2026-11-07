# Changelog: Leaflet for iPhone and iPad

The iOS app's releases, tagged `ios-v<version>`, apart from the desktop
app's `v<version>` releases.

## Unreleased

The first version of the app, for iOS and iPadOS 26 or later, on the same
Go core as the desktop app.

### Library

- The desktop's sidebar: pages nested in pages, folders, pages shared with
  you, and the Tags and To-dos tabs, with search over pages, templates,
  recordings and comments. A to-do or a search hit opens its page at its
  block.
- New pages and subpages, from the template a folder or a page names for
  them; moving, duplicating, renaming and deleting pages, with Undo; saving
  one as a template; page history with named versions and what changed
  since each; export to Markdown; and keeping a page on the device.
- Pages dragged into another order or into a folder, and files in folders,
  uploaded, shown and deleted from the sidebar.
- Reminders of to-dos due, at their time or at 9:00, as Time Sensitive
  notifications that open the to-do.
- On iPad, more than one window, a page opening in a window of its own, and
  the desktop's keyboard shortcuts in the menu bar.

### Write

- A block editor of its own: headings, nested lists, to-dos with due dates,
  quotes, callouts, toggles, code with syntax highlighting, tables, charts,
  math, diagrams, pictures, videos, files, embeds, link cards, subpage
  cards and recordings.
- The `/` and `@` menus, Markdown shortcuts, folding headings and the
  outline, links to pages and blocks, and a keyboard bar for the blocks.
- A video's notes under it, as the desktop shows them: a time in them
  plays that video from there, a long press on a line copies a link to
  its moment, and its menu writes them. A link to a moment of a video or
  a recording, in a page or a chat reply, plays it from that moment.
- A page's title, tags and byline, as the desktop shows them.

### Agents and comments

- The Agent panel: the chat, each run's edits as they stream into the page,
  with Stop, Undo and Redo, the agent and model it asks, and a page's past
  conversations.
- Asking an agent from the editor, about the text chosen or at the caret,
  and to summarize a YouTube or Vimeo video in its notes, from what is
  said in it.
- The Comments panel, with replies, resolving and reopening.
- The desktop's agent providers, a ChatGPT Plus or Pro sign-in among them,
  and its defaults.

### Record

- Recording blocks, from the device's microphone, going on with the screen
  locked, transcribed on the device or through Soniox or an
  OpenAI-compatible endpoint, showing the last two lines of the words as
  they come. A long transcript folds to its first lines, and a block can
  be renamed, summarized by an agent or deleted from its menu, and its
  notice of what went wrong closed. How long audio stays on the device once
  uploaded is a choice in Settings.

### Sync and settings

- Sync through a Leaflet server, signed in with Supabase, or through
  iCloud Drive, also now and then while the app is in the background, with
  working offline as a choice.
- The account: the name on your edits and comments, the devices signed in,
  exporting every page, and deleting the account.
- The desktop's themes, accents, text size and languages: English,
  Japanese and Simplified Chinese.
