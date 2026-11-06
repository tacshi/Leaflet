import { assert, assertEquals, assertThrows } from "jsr:@std/assert@1";
import {
  blankContent,
  collectIds,
  deltaToMarkdown,
  deltaToPlain,
  markdownToDelta,
  type NodeJSON,
  normalizeContent,
  pageViews,
  plainText,
  specToNode,
  storable,
  storableValue,
  tableRows,
} from "./blocks.ts";

// Inline markdown here writes and reads as internal/doc/markdown.go does,
// so text an agent reads and writes back through either keeps its formats.
Deno.test("inline markdown writes and reads as the Go side does", () => {
  const cases: [string, { insert: string; attributes?: Record<string, unknown> }[]][] = [
    // Links whose URL holds parentheses or spaces.
    [
      "see [docs](<https://e.com/a_(b)>) and [this](<https://e.com/x y>)",
      [
        { insert: "see " },
        { insert: "docs", attributes: { href: "https://e.com/a_(b)" } },
        { insert: " and " },
        { insert: "this", attributes: { href: "https://e.com/x y" } },
      ],
    ],
    // Bold over both runs, italic inside it, not ** then ***.
    ["__a*b*__", [{ insert: "a", attributes: { bold: true } }, {
      insert: "b",
      attributes: { bold: true, italic: true },
    }]],
    // Code with a backtick in it, between longer fences.
    ["run ``a`b``", [{ insert: "run " }, { insert: "a`b", attributes: { code: true } }]],
    // A link to a block of a page stays a link.
    ["[jump](leaflet://page/P1#B2)", [{ insert: "jump", attributes: { href: "leaflet://page/P1#B2" } }]],
    // A person mentioned, with what would read as markup in the name.
    [
      "Call [@Bob \\[B\\]](mention:user:u1)",
      [{ insert: "Call " }, {
        insert: "@Bob [B]",
        attributes: { mention: { kind: "user", id: "u1", name: "Bob [B]" } },
      }],
    ],
  ];
  for (const [md, delta] of cases) {
    assertEquals(deltaToMarkdown(delta), md);
    assertEquals(markdownToDelta(md), delta);
  }
  assertEquals(markdownToDelta("[x](https://e.com/a_(b))"), [{
    insert: "x",
    attributes: { href: "https://e.com/a_(b)" },
  }]);
  assertEquals(markdownToDelta("[See P](leaflet://page/P1)")[0].attributes?.mention, {
    kind: "page",
    id: "P1",
    name: "See P",
  });
});

Deno.test("a table's size goes only as far as its cells", () => {
  const cell: NodeJSON = {
    id: "c",
    type: "table/cell",
    data: { colPosition: 0, rowPosition: 0 },
    children: [{ id: "p", type: "paragraph", data: { delta: [{ insert: "x" }] } }],
  };
  for (const data of [{ colsLen: 2 ** 32, rowsLen: 1 }, { colsLen: 1e8, rowsLen: 1e8 }, { colsLen: 1, rowsLen: -5 }]) {
    const table: NodeJSON = { id: "t", type: "table", data, children: [cell] };
    const want = data.rowsLen > 0 ? [["x"]] : [];
    assertEquals(tableRows(table), want);
    assertEquals(
      pageViews({
        document: { id: "root", type: "page", children: [table] },
        meta: { v: 1, blocks: {}, deleted: {} },
      })[0]
        .rows,
      want,
    );
  }
});

Deno.test("a to-do is due on a day, or at a time of it", () => {
  const opts = { newId: () => crypto.randomUUID() };
  for (const due of ["2026-10-30", "2026-10-30T15:04", "2024-02-29"]) {
    assertEquals(specToNode({ type: "todo_list", text: "x", due }, opts).data?.due, due);
  }
  for (const due of ["2026-10-30T25:00", "2026-13-01", "2025-02-29", "2026-10-30 15:04", "Friday"]) {
    assert(specToNode({ type: "todo_list", text: "x", due }, opts).data?.due === undefined, due);
  }
});

// Blocks of one ID, as a paste can make, and blocks without one, read with
// the IDs the client reads them with (TestRepairedIDsAreTheSameEachRead),
// so the page the server saves matches the blocks up with each device's.
Deno.test("repaired IDs are the client's, the same each read", () => {
  const para = (id: string, text: string, children?: NodeJSON[]): NodeJSON => ({
    id,
    type: "paragraph",
    data: { delta: [{ insert: text }] },
    ...(children ? { children } : {}),
  });
  const stored = {
    document: {
      id: "root",
      type: "page",
      children: [para("a", "one"), para("a", "two"), para("a-2", "three"), para("", "four", [para("root", "five")])],
    },
    meta: { v: 1 },
  };
  const ids = collectIds(normalizeContent(structuredClone(stored)).document).slice(1);
  assertEquals(ids, ["a", "a-3", "a-2", "block-1", "root-1"]);
  assertEquals(collectIds(normalizeContent(structuredClone(stored)).document).slice(1), ids);
});

// An embed's HTML is held to 200 KB in UTF-8's bytes, as the client holds
// it (TestEmbedSizeInBytes): Chinese text a third as long as the limit in
// characters is over it.
Deno.test("an embed's size is in bytes, as the client's", () => {
  const embed = (html: string) => () => specToNode({ type: "embed", embed: { html } }, { newId: () => "x" });
  embed("x".repeat(200_000))();
  assertThrows(embed("字".repeat(Math.floor(200_000 / 3) + 1)));
});

const pageOf = (...children: NodeJSON[]) => ({
  document: { id: "root", type: "page", children },
  meta: { v: 1, blocks: {}, deleted: {} },
});

// A page's text for search is the app's (doc.Content.PlainText): a line
// for each block with more than spaces, and a link card's title, else its
// link.
Deno.test("a page's text is the app's", () => {
  const content = pageOf(
    { id: "a", type: "paragraph", data: { delta: [{ insert: "  " }] } },
    { id: "b", type: "link_preview", data: { url: "https://go.dev/", title: " " } },
    { id: "c", type: "file", data: { name: "notes.pdf" } },
    { id: "d", type: "diagram", data: { source: "graph TD", formula: "x" } },
    { id: "e", type: "math_equation", data: { formula: "e=mc^2" } },
    { id: "f", type: "image", data: { url: "https://e.com/a.png", alt: "A" } },
  );
  assertEquals(plainText(content), "https://go.dev/\nnotes.pdf\ngraph TD\ne=mc^2");
});

// A recording's transcript reads as the app's agents read it
// (agents.TranscriptText): "[m:ss] text", a speaker named, a line of only
// punctuation joined to the one before, and none when there is none.
Deno.test("a recording's transcript reads as the app's agents read it", () => {
  const content = pageOf({ id: "r", type: "recording", data: { recording_id: "rec" } }, {
    id: "s",
    type: "recording",
    data: { recording_id: "none" },
  });
  const recordings = new Map([
    ["rec", {
      id: "rec",
      status: "ready",
      transcript: [
        { start_ms: 5_000, text: " Hello " },
        { start_ms: 6_000, text: "?" },
        { start_ms: 754_000, text: "Next", speaker: "2" },
        { start_ms: 755_000, text: "  " },
      ],
    }],
    ["none", { id: "none", status: "recording", transcript: [] }],
  ]);
  const [rec, none] = pageViews(content, recordings);
  assertEquals(rec.recording?.transcript, "[0:05] Hello?\n[12:34] Speaker 2: Next");
  assertEquals(none.recording, { recording_id: "none", title: undefined, duration_ms: undefined, status: "recording" });
});

// A backtick in a link's address goes as %60, as the app writes it: what
// looks for code spans before it reads links finds none there.
Deno.test("a link's backtick is written as a url can hold one", () => {
  assertEquals(
    deltaToMarkdown([{ insert: "a", attributes: { href: "https://a.b/`x" } }]),
    "[a](https://a.b/%60x)",
  );
});

// Text whose markers close nowhere, or nest without end, reads in time
// about its length: each scan's end is found once, and formats nest at
// most so deep. Read again from each marker, 60 KB of "_a " takes
// seconds, past what an Edge Function may spend, and links nested 20,000
// deep run out of stack.
Deno.test("inline markdown reads in time about its length", () => {
  const texts = [
    "_a ".repeat(20_000),
    "*a ".repeat(20_000),
    "[a](b".repeat(12_000),
    "[".repeat(60_000),
    "``a`".repeat(15_000),
    "*_[`a".repeat(12_000),
    "[".repeat(20_000) + "x" + "](u)".repeat(20_000),
  ];
  const start = performance.now();
  for (const text of texts) {
    const delta = markdownToDelta(text);
    assert(delta.map((op) => op.insert).join("").length > 0);
  }
  // Seconds a text when read in the square of its length; tens of
  // milliseconds all of them here.
  assert(performance.now() - start < 3_000, `took ${Math.round(performance.now() - start)} ms`);
  // Within that, nesting reads as the app reads it; past it, as written.
  assertEquals(markdownToDelta("[".repeat(10) + "x" + "](u)".repeat(10)), [{ insert: "x", attributes: { href: "u" } }]);
  assert(deltaToPlain(markdownToDelta("[".repeat(40) + "x" + "](u)".repeat(40))).includes("x](u)"));
});

// What Postgres's text and jsonb refuse goes: NUL, and half a surrogate
// pair, as U+FFFD, in every string of a value.
Deno.test("text is kept as the database can hold it", () => {
  assertEquals(storable("a\u0000b \ud83d c 😀"), "ab \ufffd c 😀");
  assertEquals(storableValue({ text: "a\u0000", n: 1, list: ["\udc00", { deep: "\u0000b" }], none: null }), {
    text: "a",
    n: 1,
    list: ["\ufffd", { deep: "b" }],
    none: null,
  });
});

// A blank page holds a block to type in, stamped, as doc.Empty's does.
Deno.test("a blank page has one empty paragraph", () => {
  const c = blankContent(1000);
  const [p] = c.document.children!;
  assertEquals([c.document.children!.length, p.type, p.data, c.meta.blocks], [1, "paragraph", { delta: [] }, {
    [p.id]: 1000,
  }]);
});

Deno.test("colors read and write as spans", () => {
  const red = "#ef4444ff", mark = "#eab30840";
  const d = [
    { insert: "plain " },
    { insert: "red", attributes: { color: red } },
    { insert: " both", attributes: { color: red, background: mark } },
  ];
  const md = deltaToMarkdown(d);
  assertEquals(
    md,
    'plain <span style="color:#ef4444ff">red</span><span style="color:#ef4444ff;background:#eab30840"> both</span>',
  );
  assertEquals(markdownToDelta(md), d);
  assertEquals(markdownToDelta('<span style="color:red">x</span>'), [{ insert: '<span style="color:red">x</span>' }]);
  assertEquals(markdownToDelta('<span style="color:#ef4444">x'), [{ insert: '<span style="color:#ef4444">x' }]);
  assertEquals(deltaToMarkdown([{ insert: "</span>" }]), "\\</span>");
});
