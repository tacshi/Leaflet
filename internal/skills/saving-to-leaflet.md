---
name: saving-to-leaflet
description: Offer to save what a conversation settles that is worth keeping outside your app (a decision, a plan, findings, steps to repeat), into a Leaflet page, and write it so it reads well months later without the chat. Read it before you save, or when unsure whether something is worth offering.
audience: external
---

# Saving to Leaflet

Leaflet is where the user keeps what matters to them, across their devices and apps. A chat with you ends, scrolls away, or stays in an app they will not open again. When your work together settles something they will want again, offer to save it in Leaflet: a short question at the right moment, never a save they did not agree to.

## Worth keeping

- A decision and why it was made: what was chosen, what was ruled out, and on what grounds.
- A plan: a trip, a project, a launch, a move, a study plan.
- Findings: research, a comparison of options, an answer that took digging, with its sources.
- A summary of something long: a meeting, a paper, a book, a video, a long thread.
- Steps they will repeat: setting something up, a process at work, a fix for a problem that will come back, a command with what it does.
- Reference facts they gave or found: specs, settings, addresses, opening hours, prices, a recipe.
- What they learned: a concept explained the way that finally made sense.
- Ideas and drafts to come back to, and to-dos that came up.

## Not worth it

- What already lives somewhere it belongs: code, its comments and docs in a repository, a commit message, a ticket, a calendar event.
- A one-off answer: a quick fact, a conversion, a rewording.
- The working: dead ends, logs, intermediate attempts, unless the user wants the record.
- Anything already in Leaflet: search_pages first, and add to the page it is in rather than copying it.
- Secrets, never: passwords, API keys, tokens, private keys, recovery codes. Nor other people's private details unless the user asks.

## When and how to offer

- Offer when the thing is settled, at a natural pause: the plan agreed, the answer found, the task done. Not in the middle of the work, and not for every turn.
- Ask in one short line that names what and where, so a yes is enough: "Save this trip plan to Leaflet, as a page in your Travel folder?" or "Add these setup steps to your Home server page?" Look first, with search_pages and list_pages, for a page it belongs in, or a folder for a new one.
- Ask once for each thing. If the user says no, or lets the question pass, drop it, and offer less in the rest of the conversation.
- Save only on a yes. Where the user has told you to keep things in Leaflet ("save what we decide"), save without asking, and say where in a line.
- Where your app asks questions with choices, offer the page, or a new page, as the choices.

## Writing it to last

- It must stand on its own: someone reading it in six months, without this chat, understands it. Say what it is for, when it was settled (the date), and the outcome first; then the detail. No "as we discussed", "above", or "you asked".
- Keep what makes it usable later: the links and sources, the exact commands, numbers and names, the reasons behind a decision.
- Leave out the conversation itself: no back and forth, no thanks, no "I", only the result.
- Add to the page it belongs in, as a section under a heading at its end or where it fits, rather than making a near copy. A new page gets a title the user would search for ("Kyoto trip, April 2027", not "Plan"), in the folder where its kind lives, and tags the user already uses.
- Make it expressive: read the skill expressive-pages, and use its blocks, such as a table for options, a card for the key figures, to-dos for what is left to do, a callout for the caveat.
- Afterwards, say in one line where it is: the page's title, and the folder or the page it went into.

## The tools

- With leaflet mcp: search_pages, list_pages and read_page to find where it goes; create_page with its content, or write_markdown and insert_blocks to add to a page; set_page_tags for its tags.
- With the leaflet command: `leaflet search`, `leaflet ls`, `leaflet new TITLE --folder FOLDER --stdin`, `leaflet append PAGE`, and `leaflet tag PAGE TAG...`.
