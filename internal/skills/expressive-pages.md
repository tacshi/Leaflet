---
name: expressive-pages
description: Make a page clear and expressive, choosing which block shows which content best (tables, charts, cards, maps, diagrams, callouts, toggles, to-dos, formulas, embeds), how to shape a page, and how to use emphasis, text color and highlight. Read it before you write or reshape more than a few lines of a page, or make a page with content.
---

# Expressive pages

A Leaflet page is more than text. Its blocks show what prose can only describe: numbers as a chart, options side by side in a table, the figures that matter on a card, places on a map, a process as a diagram. Write so a reader gets the point in seconds, then finds the detail where they look for it.

The form follows the content. A letter, a story, a poem or a short answer stays prose; notes, plans, comparisons, reports and explainers gain from structure. When you add to a page, write in its style and language, and use the blocks it already uses the way it uses them.

## Pick the block that fits

| When the content is | Use | Rather than |
| --- | --- | --- |
| Things compared on the same points: products, options, plans, results | `table`: one row each, the points as columns | paragraphs that repeat "X has…, Y has…" |
| Numbers worth seeing: a trend, a ranking, a share of a whole | `chart`: `line` for change over time, `bar` to compare amounts, `pie` for the parts of one whole | a list of figures |
| Two to four key figures: a time, a price, a distance, a score | `card` with layout `figures` | numbers buried in a sentence |
| Facts about one thing: a place's address, hours and phone, a product's specs | `card` with layout `details`, with a picture when there is one | "Label: value" bullets |
| Where places are, or how to get from one to another | `map`, with the route told in words or a card too | addresses alone |
| A process, a decision, how parts relate, who talks to whom, a schedule | `diagram` (Mermaid); a `numbered_list` for a few simple steps | a long paragraph |
| Things to do | `todo_list`, with `due` for the dated ones | bullets |
| Things of a kind, in no order | `bulleted_list` | a paragraph of commas |
| Steps or a ranking, where order matters | `numbered_list` | bullets |
| A takeaway, a tip, a caveat, a risk | `callout`: `info` for context, `tip` for advice, `warning` for a caveat, `danger` for what can go wrong | a bold paragraph |
| Detail most readers skip: raw data, a long log, a derivation, a full transcript | `toggle_list`, its summary as its text, the detail in its children, `collapsed` | a wall of text |
| A formula | `math_equation` on a line of its own, `$…$` in a line of text | ASCII math |
| Code, a command, a config | `code` with its `language` | backticks around several lines |
| Words someone said or wrote | `quote` | quotation marks around a paragraph |
| A web page worth opening | `link_preview` | a bare link |
| A picture: a photo, a screenshot, a figure from a paper | `image` with a web link and what it shows as its alt; outside the app, a file on this computer with upload_files or the leaflet command's upload, where you have them | a link to it |
| A video | `video`, its chapters or key moments in its notes | a link |
| Something to try: a simulation, a calculator, an explorable explainer, a small game | `embed` (read the skill interactive-embeds first) | a description of it |
| A long page's parts | `heading`s, level 1 to 3; a `divider` between major parts, sparingly | one long scroll |

## Shape the page

- Lead with what matters: a sentence or two of summary, the answer, or a card of the key figures, before the detail.
- Give a page longer than a screen or two headings: short, plain phrases, two levels usually, three at most.
- Keep each `paragraph` short, one idea. Use lists for things of a kind, not to chop up reasoning.
- Mix forms: a heading, a sentence that says why the table, chart or diagram below matters, the block, then what to notice in it. A chart or a table never stands alone without a word of what it shows.
- Link what is related: another page as [Title](leaflet://page/<id>), a moment of a video or a recording as [1:23](leaflet://page/<id>#<block id>?t=83).
- Every fact, figure, place and time comes from the user, a page, a source or a tool, never from a guess. Say where it comes from: a card's sources, a chart's caption, a link.

## Tables

- The header row names the columns, the first column names the rows. Put units in the header ("Price (€)"), not in every cell.
- Right-align columns of numbers, prices and times (`|--:|` in Markdown, or `align`).
- `table_style` `lines` (simple lines, a bold header) suits reading tables in prose pages, such as schedules and specs; the default grid suits dense data.
- Keep cells short. To point at the best or the worst, bold it or give it a color (below), in one or two cells, not every row.
- More than about seven columns reads badly on a phone: split the table, or turn it so the options are the columns.

## Charts

- `labels` are the categories or the times; each series is a named set of values, one per label. A pie takes one series.
- `bar` compares amounts across categories, a few series side by side; `line` shows change over time; `pie` shows the parts of one whole, in six slices or fewer.
- Give it a `title` that says what it shows, and a `caption` with the source, the units, or the one thing to notice.
- Two or three numbers need no chart: a `figures` card or a sentence says them better. Where readers need the exact values, keep them in a table too, in a toggle when they are long.

## Cards

- `figures`: two to four short values with short labels ("Duration", "Fare", "Budget"), shown large side by side. Bold a value to make it stand out.
- `details`: label: value lines, as an address, opening hours and a phone number, beside a picture (a web link) of the place or thing.
- Pick the `icon` that fits the subject: `plane` for a flight, `bed-double` for a hotel, `utensils` for a restaurant, `banknote` for costs, `calendar` for dates, `trending-up` for growth.
- A `note` says what the facts assume or leave out ("Prices as of November"), and `sources` are the pages the facts come from.

## Maps

- Find each place with find_place and a route with find_route, where you have them, then make a `map` with the places and the route's id, as "r1". Never guess coordinates, times or distances.
- Tell the route in words or a `figures` card too: its time, its distance, its legs.
- A map numbers its places 1, 2, 3 on their pins, in the order you give them, as the list under it in a web page of it does: name each by its name alone ("南京博物院", not "① 南京博物院"), and refer to them by those numbers in the title or the text where it helps.

## Diagrams

- `flowchart LR` for a pipeline, `flowchart TD` for a hierarchy or a decision, `sequenceDiagram` for who talks to whom, `stateDiagram` for states, `gantt` or `timeline` for a schedule, `mindmap` for a brainstorm, `erDiagram` or `classDiagram` for data and types.
- Keep one to about fifteen nodes with short labels; split a bigger one into two.

## Emphasis, text color and highlight

- **Bold** the few words a skimmer must not miss: a decision, a key term, a total. A few in a section, never whole paragraphs.
- *Italic* for a title, a term as it is defined, or a light stress.
- Color and highlight carry meaning. Use the app's palette, the colors of its color picker, which keep text readable in the light and the dark look:

  | Color | Hex | Means, by convention |
  | --- | --- | --- |
  | red | `#ef4444` | a risk, an error, a loss, overdue, no |
  | orange | `#f97316` | a caution, at risk |
  | yellow | `#eab308` | pending, to check; as a highlight, the line to remember |
  | green | `#22c55e` | a gain, done, on track, yes |
  | teal | `#14b8a6` | |
  | blue | `#3b82f6` | a note, information |
  | indigo | `#6366f1` | |
  | purple | `#a855f7` | |
  | pink | `#ec4899` | |
  | brown | `#92400e` | |
  | gray | `#71717a` | an aside, less important |

- Text color is `<span style="color:#ef4444">over budget</span>`, a palette hex. A highlight is a palette hex with `40` after it, so it stays see-through: `<span style="background:#eab30840">read this first</span>`. Never highlight with an opaque color, such as `#fde68a`: in the dark look it puts light text on a light ground.
- Keep one meaning for each color on a page, and say what the colors mean where it is not plain. Color a few words, not whole paragraphs. Never let color carry the meaning alone: the words say it too ("down 12%", "blocked").
- Keep the colors you read: rewriting text, keep its spans.

## Hold back

- One block per point. Show a set of numbers as a chart or a table, not both, unless both help.
- Callouts, colors and bold lose their pull when everywhere: a callout or two on a page, a few colored words on a screen.
- Do not turn the user's own writing into another form unless they ask. Add to it, in its style.
- Do not invent data to fill a chart, a table or a card. Leave the cell empty or ask.

## Writing the blocks

In Markdown (write_markdown, a page's markdown, or outside the app the leaflet command's new and append), each line is a block:

- `#` to `###` headings, `-` and `1.` lists (indent two spaces to nest), `- [ ]` to-dos, `>` quotes, `---` dividers, `|` tables with `|--:|` to right-align a column, and fenced code with its language.
- A callout: `> [!NOTE]` (info), `> [!TIP]`, `> [!WARNING]` or `> [!CAUTION]` (danger) on a line, its text on the `>` lines after it.
- A toggle: `<details><summary>Its summary</summary>`, the blocks inside it, then `</details>`; `<details open>` starts it unfolded.
- A formula between `$$` lines, a diagram in a ```` ```mermaid ```` fence, a picture as `![what it shows](https://…)`.
- A chart in a ```` ```chart ```` fence holding its JSON, `{"chart_type": "bar", "line" or "pie", "title", "labels": [...], "series": [{"name", "values": [...]}], "caption"}`, a value for each label in each series. A chart, card or map fence whose JSON is not one is refused, with the reason, and nothing is written: fix it and write again.
- A card in a ```` ```card ```` fence holding its JSON, `{"layout": "figures" or "details", "icon", "title", "image", "facts": [{"label", "value"}], "note", "sources": [{"url", "title"}]}`; a map in a ```` ```map ```` fence holding `{"title", "places": [{"name", "lat", "lng"}], "route": "r1"}`.

Embeds, videos, link previews, a lines table and a to-do's due day are blocks Markdown does not write: make them with insert_blocks or replace_block, or in a page's blocks as it is made, where you have those tools.

For example, the start of a trip page:

````markdown
# Kyoto in spring

Five days, **3–7 April**, two people, by train from Tokyo.

```card
{"layout": "figures", "icon": "calendar", "title": "At a glance", "facts": [{"label": "Days", "value": "5"}, {"label": "Budget", "value": "**¥180,000**"}, {"label": "Train", "value": "2 h 15 min"}]}
```

> [!TIP]
> Book the Shinkansen seats a month ahead: cherry blossom week sells out.

| Day | Plan | Stay |
| --- | --- | --- |
| 1 | Arrive, Gion at dusk | Hotel near Shijo |
| 2 | Fushimi Inari early, Tofuku-ji | Hotel near Shijo |

```chart
{"chart_type": "bar", "title": "Budget by day (¥ thousand)", "labels": ["Day 1", "Day 2", "Day 3"], "series": [{"name": "Spend", "values": [42, 31, 38]}], "caption": "Hotel, food and transport; tickets not included."}
```

- [ ] Book the hotel <span style="color:#ef4444">by 1 March</span>
- [ ] Buy the rail passes
````

## Recipes

- **Meeting notes**: a line on what it was for; Decisions as bullets, each decision in bold; Action items as to-dos with their due days; Discussion in short bullets; open questions in a `warning` callout.
- **A trip**: a `figures` card (dates, budget, travel time), a map of the stops and the route, the plan day by day in a lines table, a `details` card for each hotel, tips in a `tip` callout, bookings as to-dos.
- **A comparison or research**: the verdict first, in a sentence, bold; a table of the options, numbers right-aligned, the winner's cells colored green; a bar chart when one number decides; caveats in a callout; the sources as links or link previews.
- **Study notes or an explainer**: a short summary up top; a heading per idea; terms bold where they are defined; formulas as `math_equation`; processes as diagrams; an embed to play with the idea; worked answers in toggles.
- **A project's status**: a `figures` card (progress, due day, budget); milestones in a gantt diagram or a table; next steps as to-dos; blockers in a `danger` callout, the blocked items in red.
- **A report on numbers**: the key figures on a card, the trend in a line chart with its source in the caption, what it means in a few sentences, the full data in a table inside a toggle.

## Before you finish

- Could a reader get the point in ten seconds, from the top of the page?
- Is each set of numbers in a table or a chart, right-aligned and titled, with its source?
- Does each color mean one thing, from the palette, used sparingly?
- Is every fact from the user, a page, a source or a tool?
