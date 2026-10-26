# Leaflet at a software company

User stories from a week at a small software company: a product manager, an
engineer, an on-call engineer, an engineering manager and a new hire use
Leaflet for the documents their work makes. Each story is something a team
does every week, and together they cover the app's blocks, agents,
recordings, comments, history, search and export.

✅ works · ⚠️ works, with issues · ❌ broken · ➖ not testable in this build

## Setting up the team space

1. **Team folders** — As an engineering manager, I make folders for
   *Product*, *Engineering*, *Incidents* and *Meetings*, so each kind of
   document has a home.
2. **Templates by copying** — I keep a "PRD template" page and start a new
   PRD from it.

## Product manager

3. **Write a PRD** — In *Product*, I write "PRD: Offline sync for mobile"
   with a problem statement, goals and non-goals, a requirements table
   (ID, requirement, priority), a ⚠️ warning callout for a known risk, a
   toggle holding the FAQ, and to-dos for open questions.
4. **Competitor research** — I paste links to two competitors' pages and
   they become link cards with title and picture.
5. **Agent drafts a section** — I ask the agent to draft "Success metrics"
   from the goals, then to turn the requirements table into a chart of
   requirements per priority.
6. **Stakeholder review** — A reviewer comments on a requirement
   (`@ChatGPT` is asked whether it is testable), I reply and resolve.

## Engineer

7. **Write an RFC** — In *Engineering*, "RFC: Sync protocol v2" has a
   Mermaid sequence diagram of client and server, a Go code block with the
   new API, a SQL code block for the migration, a formula for the expected
   conflict rate in TeX, and a trade-offs table.
8. **Paste from GitHub** — I paste Markdown copied from a GitHub README: a
   fenced code block, a table, a `> [!NOTE]` alert and nested lists, and
   they become the matching blocks.
9. **Ask the agent to review** — "Find gaps or open questions" on the RFC,
   and an inline `@ChatGPT` to write the rollback plan as a numbered list.
10. **Link the RFC from the PRD** — The PRD links to the RFC page.

## On-call engineer

11. **Incident postmortem** — In *Incidents*, "2026-10-24 Sync outage"
    has a 🔴 danger callout with the impact, a timeline table, a line chart
    of the error rate by minute, the fix as a code block, a diagram of the
    failure, and action items as to-dos with owners.
12. **Attach evidence** — I attach a log file and a screenshot to the
    postmortem.
13. **Restore after a bad edit** — An agent rewrites the timeline badly; I
    undo the run, then later restore yesterday's version from page history.

## Meetings

14. **Record sprint planning from the menu bar** — With the window closed,
    I start a recording from the menu bar, talk, stop it from the menu bar,
    and get a page with the transcript.
15. **Turn the meeting into action items** — I summarize the recording with
    the agent and ask it for action items with owners as to-dos.
16. **Weekly standup notes** — In *Meetings*, a "Standups" page holds one
    subpage per week; I find last week's blockers with search.

## Shipping

17. **Release notes** — I ask the agent to write release notes for v1.4
    from the PRD and RFC (it reads other pages), in Markdown sections.
18. **Export for the website** — I export the release notes as Markdown,
    and the whole space as a zip for an archive, with files and pictures.

## Onboarding

19. **Onboarding guide for a new hire** — "Welcome to the team" has the
    YouTube recording of our architecture talk as a video block, the
    handbook PDF as a file, link cards to our tools, a checklist of
    first-week to-dos, and a toggle per team.
20. **New hire finds things** — The new hire searches for "deploy" and
    finds the RFC, the postmortem and the onboarding page.

## Everyday use

21. **Keyboard-first writing** — I write a whole page with Markdown
    shortcuts, ``` fences, `$$`, Tab nesting, Emacs keys and ⌘Z.
22. **Present in a meeting** — I switch to dark mode and larger text to
    share my screen.
23. **Quick capture** — From the menu bar, I add a new page for an idea
    without the window open.

## Results (2026-10-26)

Walked through on macOS from `main` at d63c1c5, signed out, with ChatGPT and
Soniox set up again after the bundle ID change. Stories 14, 22 and 23 were
checked in the walkthrough before (`docs/user-stories.md` and the menu bar
work) and not run again.

| # | Story | Result |
|---|-------|--------|
| 1 | Team folders | ✅ (they list in the order made; each new one opens on "Empty folder") |
| 2 | Start a page from a template | ❌ no Duplicate or templates (M1) |
| 3 | Write a PRD | ⚠️ callout tones, toggle, table and to-dos work; Tab out of a table's last cell loses what is typed next (T1) |
| 4 | Competitor link cards | ✅ title, description, picture and site from `/ › Link card` |
| 5 | Agent drafts metrics and a chart | ✅ |
| 6 | Stakeholder review in comments | ⚠️ no comment or Ask agent inside a table cell (T2); the agent edits the page when only asked a question (T3) |
| 7 | Write an RFC | ✅ Go code with highlighting and indent, TeX and a Mermaid sequence diagram render well; math and diagrams are written in a dialog with no preview (M2) |
| 8 | Paste Markdown from GitHub | ⚠️ alert → callout, nested list, code fence and table all come in; blank lines become empty paragraphs (T4) |
| 9 | Agent reviews the RFC | ✅ inline `@ChatGPT` wrote the rollback plan |
| 10 | Link the RFC from the PRD | ❌ no way to link an existing page (M3) |
| 11 | Incident postmortem | ⚠️ the agent drafted all of it, callout, table, line chart, SQL, flowchart, to-dos; long table cells are cut off and show raw Markdown (T2) |
| 12 | Attach evidence | ✅ file card; the bar at the bottom covers the last lines and the `/` menu there (T5) |
| 13 | Undo a bad agent run | ✅ one ⌘Z step undoes a whole run |
| 14 | Record from the menu bar | ✅ (before) |
| 15 | Meeting → action items | ✅ the agent read the transcript |
| 16 | Standup notes and search | ✅ search finds text in tables and transcripts, with the match bold in context |
| 17 | Release notes from other pages | ✅ the agent read the PRD and the RFC |
| 18 | Export | ⚠️ code, math, Mermaid and callouts export as standard Markdown; a bullet under a numbered item is indented 2 spaces, which GitHub doesn't nest (T6) |
| 19 | Onboarding with a video | ⚠️ the YouTube block shows and plays; after using the player, the first click back on the page is lost and typing goes nowhere (T7) |
| 20 | New hire searches "deploy" | ✅ |
| 21 | Keyboard-first writing | ✅ Markdown shortcuts, ``` and `$$`, Tab nesting, Emacs Control keys |
| 22 | Dark mode and larger text | ✅ (before) |
| 23 | Quick capture from the menu bar | ✅ (before) |

### Bugs

Fixed after the walkthrough, except T7, which did not happen again when
clicking at a person's pace (it came from a click straight after the
player's). Tests cover T1, T2's moves, T3, T4 and T6; T1, T2 and T5 were
checked in the app.

**T1. Tab out of a table's last cell loses typing.** Tab moves cell to cell
and row to row, but from the last cell it goes to the "+ Row" button, and
what is typed next goes nowhere. Editors add a row there.

**T2. Table cells are single-line inputs** (`tableCell` in
`internal/editor/blocks.go`), a fixed 160 points wide:
- long text is cut off instead of wrapping, as in the postmortem's timeline;
- formats show as raw Markdown (`**Owner:**`);
- selecting text in a cell shows no toolbar, so a reviewer can't comment on
  a requirement in a table or ask the agent about it.

**T3. An agent asked a question in a comment also edits the page.** "Is this
risk testable? Suggest one test." got an answer in the thread and a new
paragraph in the PRD. A comment question should get a reply; an edit only
when asked for one. (Seen in the first walkthrough too.)

**T4. Pasted Markdown keeps blank lines as empty paragraphs.** `parseLines`
in `internal/editor/layout.go` makes a paragraph of every line, blank ones
too, so each blank line between Markdown blocks adds an empty line in the
page, and in its Markdown export.

**T5. The bar at the bottom covers the end of the page.** "ChatGPT edited N
blocks · Undo" sits over the last lines, and over the `/` menu when you write
on the last line; the page doesn't leave room to scroll them clear of it.

**T6. Export indents nested items 2 spaces whatever the parent.** Under
"1. Clone the repo" the bullet needs 3 spaces to nest in GitHub's Markdown;
with 2 it becomes a list of its own.

**T7. After using a video or embed, the first click on the page is lost.**
Playing and pausing the YouTube player, then clicking below it and typing,
lost the typing: keyboard focus stayed in the player, where letters are its
shortcuts. A second click works.

### Missing

- **M1.** Duplicate a page, or templates, to start a PRD or postmortem from
  one.
- **M2.** A live preview while writing TeX or Mermaid in their dialogs.
- **M3.** Links to existing pages: `@` lists only agents and people, and only
  `/ › Subpage` links a page, a new one. Added since: `@` lists pages too and
  puts in a link that opens the page and follows its title, and a subpage
  made from the sidebar gets a link at the end of its parent.
