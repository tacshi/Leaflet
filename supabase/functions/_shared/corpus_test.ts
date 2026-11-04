// Corpora of random cases, each what this side makes of them, for the Go
// tests to check the app against (internal/parity reads them): any way
// the server's copy of the app's core reads, writes or edits a page apart
// from the app's fails a test there. Each corpus is drawn from a seed of
// its own, so it changes only as the code that makes it does. Run deno
// test with --allow-write=_shared/testdata to write them anew.

import {
  type BlockSpec,
  deltaToMarkdown,
  isVideoURL,
  markdownToDelta,
  type NodeJSON,
  normalizeContent,
  type PageContent,
  pageViews,
  plainText,
  specToNode,
  transcriptText,
  videoOf,
} from "./blocks.ts";
import { markdownSpecs } from "./lines.ts";
import { agentURL, webURL } from "./url.ts";
import { agentViews, applyOps, type Op, overwrites, PageEditSession, type ToolHooks, URL_RE } from "./tools.ts";
import { keyAfter } from "./sortkey.ts";
import { tokensToSegments } from "./segments.ts";
import { userMessage } from "./prompt.ts";
import { corpus } from "../_testing/fixture.ts";
import { Random } from "../_testing/random.ts";
import {
  allBlocks,
  anyValue,
  block,
  CHARS,
  delta,
  Ids,
  inline,
  markdown,
  page,
  position,
  spec,
  url,
} from "../_testing/corpus.ts";
import inlineCorpus from "./testdata/corpus/inline.json" with { type: "json" };
import deltasCorpus from "./testdata/corpus/deltas.json" with { type: "json" };
import markdownCorpus from "./testdata/corpus/markdown.json" with { type: "json" };
import specsCorpus from "./testdata/corpus/specs.json" with { type: "json" };
import urlsCorpus from "./testdata/corpus/urls.json" with { type: "json" };
import viewsCorpus from "./testdata/corpus/views.json" with { type: "json" };
import idsCorpus from "./testdata/corpus/ids.json" with { type: "json" };
import opsCorpus from "./testdata/corpus/ops.json" with { type: "json" };
import callsCorpus from "./testdata/corpus/calls.json" with { type: "json" };
import textsCorpus from "./testdata/corpus/texts.json" with { type: "json" };
import messagesCorpus from "./testdata/corpus/messages.json" with { type: "json" };
import sortKeysCorpus from "./testdata/corpus/sort_keys.json" with { type: "json" };
import segmentsCorpus from "./testdata/corpus/segments.json" with { type: "json" };
import transcriptsCorpus from "./testdata/corpus/transcripts.json" with { type: "json" };

/** A block without its IDs, which each side draws at random. */
function withoutIds(node: NodeJSON): unknown {
  const { id: _, children, ...rest } = node;
  return children ? { ...rest, children: children.map(withoutIds) } : rest;
}

/** Runs fn with IDs drawn from r, so the blocks it makes are the same each time. */
async function withSeededIds<T>(r: Random, fn: () => Promise<T>): Promise<T> {
  const random = crypto.getRandomValues;
  crypto.getRandomValues = (<A extends ArrayBufferView | null>(array: A): A => {
    if (array) {
      const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
      for (let i = 0; i < bytes.length; i++) bytes[i] = r.int(256);
    }
    return array;
  }) as typeof crypto.getRandomValues;
  try {
    return await fn();
  } finally {
    crypto.getRandomValues = random;
  }
}

// Inline Markdown reads as the same runs of text here and in the app
// (doc.FromMarkdown).
Deno.test("inline Markdown reads as the app reads it (testdata/corpus/inline.json)", async () => {
  const r = new Random(1);
  const made = Array.from({ length: 400 }, () => {
    const md = inline(r, 14);
    return { markdown: md, delta: markdownToDelta(md) };
  });
  await corpus("corpus/inline.json", made, inlineCorpus);
});

// Runs of text write as the same inline Markdown here and in the app
// (doc.Delta.ToMarkdown).
Deno.test("rich text writes as the app writes it (testdata/corpus/deltas.json)", async () => {
  const r = new Random(2);
  const made = Array.from({ length: 400 }, () => {
    const d = delta(r, 6);
    return { delta: d, markdown: deltaToMarkdown(d) };
  });
  await corpus("corpus/deltas.json", made, deltasCorpus);
});

// Markdown of many lines makes the blocks here that the app makes of it
// (doc.MarkdownBlocks).
Deno.test("Markdown makes the blocks the app makes (testdata/corpus/markdown.json)", async () => {
  const r = new Random(3);
  const made = Array.from({ length: 400 }, () => {
    const md = markdown(r);
    const nodes = markdownSpecs(md).map((s) => specToNode(s, { newId: () => "x", markdown: true }));
    return { markdown: md, nodes: nodes.map(withoutIds) };
  });
  await corpus("corpus/markdown.json", made, markdownCorpus);
});

// A spec makes the block here that it makes in the app, or is refused on
// both (doc.SpecToNode).
Deno.test("specs make the blocks the app makes (testdata/corpus/specs.json)", async () => {
  const r = new Random(4);
  const made = Array.from({ length: 500 }, () => {
    const s = spec(r);
    try {
      return {
        spec: s,
        node: withoutIds(specToNode(s as unknown as BlockSpec, { newId: () => "x", author: "Claude" })),
      };
    } catch {
      return { spec: s, error: true };
    }
  });
  await corpus("corpus/specs.json", made, specsCorpus);
});

// A link is a web link, one a block may load, and a video's, here as in
// the app (doc.WebURL, agentURL and ParseVideoURL).
Deno.test("links read as the app reads them (testdata/corpus/urls.json)", async () => {
  const r = new Random(5);
  const made = Array.from({ length: 400 }, () => {
    const u = url(r);
    return { url: u, web: webURL(u), agent: agentURL(u), video: isVideoURL(u), video_of: videoOf(u) };
  });
  await corpus("corpus/urls.json", made, urlsCorpus);
});

// A page's blocks read here as the app reads them for agents, and its
// plain text is the app's, whatever the blocks hold (doc.BlockViews and
// doc.Content.PlainText).
Deno.test("pages read as the app reads them (testdata/corpus/views.json)", async () => {
  const r = new Random(6);
  const made = Array.from({ length: 150 }, () => {
    const content = page(r, new Ids("b"), true, 5);
    return { content, views: pageViews(content), plain: plainText(content) };
  });
  await corpus("corpus/views.json", made, viewsCorpus);
});

// Blocks stored without an ID, or with one another has, get the IDs here
// that the app gives them (doc's repairIDs), in document order.
Deno.test("IDs are repaired as the app repairs them (testdata/corpus/ids.json)", async () => {
  const r = new Random(7);
  const pool = ["a", "a", "a-1", "a-2", "a-1-1", "block-1", "block-2", "block", "root", "b", "b-1", "", "-1", "a-"];
  const tree = (depth: number): unknown[] =>
    r.times(0, 4, () => {
      if (r.chance(0.08)) return null;
      const n: Record<string, unknown> = { type: "paragraph" };
      if (r.chance(0.85)) n.id = r.pick(pool);
      if (depth < 3 && r.chance(0.3)) n.children = r.chance(0.9) ? tree(depth + 1) : null;
      return n;
    });
  const made = Array.from({ length: 300 }, () => {
    const raw = { document: { type: "page", id: "root", children: tree(0) }, meta: { v: 1, blocks: {}, deleted: {} } };
    const content = normalizeContent(structuredClone(raw));
    return { content: raw, ids: allBlocks(content).map((n) => n.id) };
  });
  await corpus("corpus/ids.json", made, idsCorpus);
});

/**
 * A random op on a page whose blocks have these ids, as a run on a page
 * changed since may send one: made on the page as it is now, or as it was
 * before.
 */
function randomOp(r: Random, pages: PageContent[], ids: string[], made: Ids): Op {
  const known = allBlocks(r.pick(pages));
  const existing = (): NodeJSON => {
    const n = known.length ? r.pick(known) : null;
    return { id: n?.id ?? "gone", type: "paragraph", data: { delta: [{ insert: "again" }] } };
  };
  switch (r.int(4)) {
    case 0: {
      const nodes = r.times(1, 2, () => {
        if (r.chance(0.15)) return existing();
        if (r.chance(0.1)) {
          return {
            id: made.next(),
            type: "table/cell",
            data: { colPosition: 0, rowPosition: 0 },
            children: [{ id: made.next(), type: "paragraph", data: { delta: [] } }],
          };
        }
        const n = block(r, made, false);
        if (n.children && r.chance(0.3)) n.children.push(existing());
        return n;
      });
      return { op: "insert", position: position(r, ids), nodes };
    }
    case 1:
      return { op: "move", id: r.pick(ids), position: position(r, ids), from: { at: "end" } };
    case 2:
      return { op: "delete", ids: r.times(1, 2, () => r.pick(ids)), previous: [] };
    default: {
      const id = r.pick(ids);
      const previous = known.find((n) => n.id === id) ?? { id, type: "paragraph", data: { delta: [] } };
      const node = block(r, made, false);
      node.id = id;
      // Of the block's own type now and then, which may take what it holds.
      if (r.chance(0.3)) node.type = previous.type;
      // A replace that keeps the block's children carries them as read.
      if (r.chance(0.5)) {
        if (previous.children) node.children = structuredClone(previous.children);
        else delete node.children;
      }
      return { op: "replace", id, node, previous: structuredClone(previous) };
    }
  }
}

// Ops land where the app puts them, on a page that may have changed since
// they were made: next to blocks gone, in tables and their cells, and
// into blocks that take none (doc.Content.Apply). And they take away a
// change made since they were made where the app finds they do
// (agents' overwrites), which history keeps first.
Deno.test("ops land where the app puts them (testdata/corpus/ops.json)", async () => {
  const r = new Random(8);
  const made = Array.from({ length: 150 }, () => {
    // Some pages from other editors, whose blocks of any type may hold
    // others.
    const before = page(r, new Ids("b"), r.chance(0.3), 5);
    const ids = [...allBlocks(before).map((n) => n.id), "gone"];
    const newIds = new Ids("n");
    let content = before;
    const ops: Op[] = [];
    for (let i = r.between(1, 6); i > 0; i--) {
      const op = randomOp(r, [before, content], ids, newIds);
      ops.push(op);
      content = applyOps(content, [op], 1000);
      for (const n of allBlocks(content)) if (!ids.includes(n.id)) ids.push(n.id);
    }
    return { before, ops, after: applyOps(before, ops, 1000), overwrites: overwrites(before, ops) };
  });
  await corpus("corpus/ops.json", made, opsCorpus);
});

/** Blocks an agent writes, as tool calls give them: mostly ones the tools take. */
function callSpec(r: Random, urls: string[]): Record<string, unknown> {
  if (r.chance(0.15)) return spec(r);
  const text = inline(r, 6);
  switch (r.int(9)) {
    case 0:
      return { type: "heading", level: r.between(1, 3), text };
    case 1:
      return {
        type: "bulleted_list",
        text,
        children: r.times(0, 2, () => ({ type: "bulleted_list", text: inline(r, 3) })),
      };
    case 2:
      return { type: "todo_list", text, checked: r.chance(0.5) };
    case 3:
      return { type: "table", rows: [["a", "b"], [text, "c"]] };
    case 4:
      return { type: "image", image: { url: r.pick(urls) } };
    case 5:
      return { type: "link_preview", url: r.pick(urls) };
    case 6:
      return {
        type: "chart",
        chart: { chart_type: "bar", title: text, labels: ["a"], series: [{ name: "n", values: [1] }] },
      };
    case 7:
      return { type: "code", text: "x := 1\n", language: "go" };
    default:
      return { type: r.pick(["paragraph", "quote", "toggle_list", "callout"]), text };
  }
}

/** A position as a model gives one: mostly one of a block's, now and then one the tools refuse. */
function callPosition(r: Random, ids: string[]): unknown {
  if (r.chance(0.8)) return position(r, ids);
  const id = r.pick(ids);
  return r.pick([
    "start",
    "end",
    "middle",
    {},
    null,
    [],
    { after: id, at: "end" },
    { after: id, before: id },
    { replace: id },
    { after: "", at: "end" },
    { after: null, before: id },
    { at: "middle" },
    { after: 5 },
    { after: id, foo: 1 },
  ]);
}

/** A tool call, as a model makes one. */
function call(r: Random, ids: string[], empty: string, urls: string[]): { tool: string; args: string } {
  const id = () => r.chance(0.9) ? r.pick(ids) : r.pick([5, "", null]);
  const pick = r.int(20);
  let tool: string, args: unknown;
  if (pick < 4) {
    tool = "write_markdown";
    const where = r.chance(0.3)
      ? { replace: r.chance(0.6) ? empty : id(), ...(r.chance(0.2) ? { after: r.pick(ids) } : {}) }
      : callPosition(r, ids);
    args = { position: where, markdown: r.chance(0.95) ? markdown(r) : anyValue(r) };
  } else if (pick < 8) {
    tool = "insert_blocks";
    args = {
      position: callPosition(r, ids),
      blocks: r.chance(0.95) ? r.times(r.chance(0.9) ? 1 : 0, 3, () => callSpec(r, urls)) : anyValue(r),
    };
  } else if (pick < 11) {
    tool = "replace_block";
    args = { id: id(), block: r.chance(0.95) ? callSpec(r, urls) : anyValue(r) };
  } else if (pick < 14) {
    tool = "move_block";
    args = { id: id(), position: callPosition(r, ids) };
  } else if (pick < 16) {
    tool = "delete_blocks";
    args = { ids: r.chance(0.9) ? r.times(r.chance(0.9) ? 1 : 0, 2, id) : anyValue(r) };
  } else if (pick < 17) {
    tool = "create_subpage";
    args = {
      title: r.chance(0.9) ? r.pick(["Notes", "  Plan　", "\u0085Plan\u0085", "﻿BOM", " ", r.pick(urls)]) : anyValue(r),
      ...(r.chance(0.5) ? { blocks: r.chance(0.9) ? r.times(0, 2, () => callSpec(r, urls)) : anyValue(r) } : {}),
    };
  } else if (pick < 18) {
    tool = "read_page";
    args = {};
  } else if (pick < 19) {
    tool = r.pick(["list_pages", "read_other_page"]);
    args = tool === "list_pages" ? {} : { page_id: r.chance(0.9) ? "page-9" : 5 };
  } else {
    tool = r.pick(["nope", "insert_blocks"]);
    return { tool, args: r.pick(["", "{", "[]", "null", '"x"', "{}"]) };
  }
  return { tool, args: JSON.stringify(args) };
}

// Tool calls make the ops here that the app's agents make of them, take
// and refuse the same calls, and leave the page as the app leaves it
// (internal/agents' Executor): a run is the same in the cloud as on a
// device.
Deno.test("tool calls edit the page as the app's do (testdata/corpus/calls.json)", async () => {
  const r = new Random(9);
  const made: unknown[] = [];
  for (let k = 0; k < 150; k++) {
    const ids = new Ids("b");
    const before = page(r, ids, false, 4);
    const empty = ids.next();
    before.document.children!.splice(r.int(before.document.children!.length + 1), 0, {
      id: empty,
      type: "paragraph",
      data: { delta: [] },
    });
    const all = [...allBlocks(before).map((n) => n.id), "gone"];
    const cross = r.chance(0.4);
    // A viewer's run, which may only read.
    const readOnly = r.chance(0.1);
    const request = r.pick(["", "Add https://example.com/given.png", "See https://go.dev/doc and more"]);
    const urls = [
      "https://example.com/a.png",
      "https://example.com/given.png",
      "https://go.dev/doc",
      "https://elsewhere.com/x.png",
    ];
    const hooks: ToolHooks = {
      readPage: (content) => Promise.resolve({ blocks: agentViews(content) }),
      createSubpage: (title) =>
        Promise.resolve({ id: "page-2", space_id: "space-1", parent_id: "page-1", title, sort_key: "a1" }),
      listPages: () => Promise.resolve([]),
      readOtherPage: (id) => Promise.resolve({ id, title: "Other", blocks: [] }),
    };
    const session = new PageEditSession(before, hooks, {
      canEdit: !readOnly,
      crossPage: cross,
      request,
      author: "Claude",
      now: () => 1000,
    });
    const calls: Record<string, unknown>[] = [];
    await withSeededIds(r, async () => {
      for (let i = r.between(1, 6); i > 0; i--) {
        // A run that reads another page first has what it writes after
        // checked for URLs.
        const c = cross && i > 1 && r.chance(0.3)
          ? r.pick([{ tool: "list_pages", args: "{}" }, { tool: "read_other_page", args: '{"page_id":"page-9"}' }])
          : call(r, all, empty, urls);
        const out = await session.call(c.tool, c.args);
        const result = out.result as Record<string, unknown>;
        const ok = !result?.error;
        calls.push({
          ...c,
          ok,
          ...(ok && !["list_pages", "read_other_page"].includes(c.tool) ? { result } : {}),
          edits: out.edits.map((e) => e.summary),
        });
      }
    });
    made.push({ before, cross, readOnly, request, calls, ops: session.ops, after: session.content });
  }
  await corpus("corpus/calls.json", made, callsCorpus);
});

// Agents find the URLs in text where the app's find them, each to the
// first space Go's unicode.IsSpace has (urlRe).
Deno.test("URLs are found in text as the app finds them (testdata/corpus/texts.json)", async () => {
  const r = new Random(10);
  const made = Array.from({ length: 300 }, () => {
    const text = r.times(0, 4, () => r.chance(0.5) ? url(r) : r.text(CHARS, 4)).join("");
    return { text, urls: [...text.matchAll(URL_RE)].map((m) => m[0]) };
  });
  await corpus("corpus/texts.json", made, textsCorpus);
});

// The person's turn of a run is the app's to the character: the title,
// the blocks, the selection and the thread as JSON on lines of their own
// (agents.userMessage).
Deno.test("a run's request is worded as the app's (testdata/corpus/messages.json)", async () => {
  const r = new Random(11);
  const text = (max: number) => r.text(CHARS, max);
  // Views with their keys in order, as the app's maps write them.
  const view = (i: number) => ({ depth: r.int(3), id: `b${i}`, text: text(8), type: r.pick(["paragraph", "heading"]) });
  const made = Array.from({ length: 200 }, () => {
    const turn = {
      instruction: text(10),
      pageTitle: text(6),
      pageViews: r.times(0, 3, view),
      focus: r.times(0, 2, view),
      selectionText: r.chance(0.5) ? text(8) : "",
      thread: r.chance(0.5) ? r.times(1, 3, () => ({ author: text(3), body: text(8) })) : [],
    };
    return { turn, message: userMessage(turn) };
  });
  await corpus("corpus/messages.json", made, messagesCorpus);
});

// A key put after another is the app's (store.KeyBetween(after, "")),
// whatever characters the key before holds.
Deno.test("keys put last are the app's (testdata/corpus/sort_keys.json)", async () => {
  const r = new Random(12);
  const heads = ["a", "b", "c", "y", "z"];
  const digits = ["0", "1", "9", "A", "Z", "a", "y", "z"];
  const others = ["~", "-", " ", "é", "😀", "V", "!"];
  const made = Array.from({ length: 400 }, () => {
    const after = r.times(0, 3, () => r.pick(heads) + r.text(r.chance(0.8) ? digits : [...digits, ...others], 4))
      .join("") + (r.chance(0.2) ? r.pick(others) : "");
    return { after, key: keyAfter(after) };
  });
  await corpus("corpus/sort_keys.json", made, sortKeysCorpus);
});

// Soniox's tokens make the transcript's lines here that the app makes of
// them (recording.TokensToSegments).
Deno.test("tokens make the app's transcript lines (testdata/corpus/segments.json)", async () => {
  const r = new Random(13);
  const words = [
    " Hello",
    " world",
    "Hi",
    " there",
    "字",
    " 字",
    "。",
    ".",
    "?",
    "!",
    " ",
    "",
    ",",
    " ...",
    "！",
    "«",
    "x.",
    "\u0085",
    "-",
  ];
  const made = Array.from({ length: 300 }, () => {
    let at = r.int(3000);
    const tokens = r.times(0, 14, () => {
      const t: Record<string, unknown> = { text: r.pick(words) };
      at += r.pick([0, 50, 300, 900, 1001, 1500, 4500]);
      if (r.chance(0.95)) t.start_ms = at;
      if (r.chance(0.95)) t.end_ms = at + r.pick([0, 200, 600, -100]);
      if (r.chance(0.5)) t.speaker = r.pick(["1", "2", 1, 2, null, "", 1e8, 1.5]);
      return t;
    });
    return { tokens, segments: tokensToSegments(tokens as never) };
  });
  await corpus("corpus/segments.json", made, segmentsCorpus);
});

// A recording's transcript reads as the app's agents read it
// (agents.TranscriptText).
Deno.test("transcripts read as the app's agents read them (testdata/corpus/transcripts.json)", async () => {
  const r = new Random(14);
  const texts = ["Hello there.", " ", "", "...", "。", "!?", "字", "a\u2028b", "«»", "x.", "-", "\u0085", "Plan"];
  const made = Array.from({ length: 200 }, () => {
    const segments = r.times(0, 6, () => ({
      start_ms: r.pick([0, 999, 1000, 59999, 61000, 3599999, 3600000, -1500, -61000, 12345678]),
      end_ms: 0,
      text: r.text(texts, 2),
      ...(r.chance(0.4) ? { speaker: r.pick(["1", "2", "A"]) } : {}),
    }));
    return { segments, text: transcriptText(segments) };
  });
  await corpus("corpus/transcripts.json", made, transcriptsCorpus);
});
