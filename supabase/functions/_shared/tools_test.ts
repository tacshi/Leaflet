import { assert, assertEquals } from "jsr:@std/assert@1";
import {
  deltaToMarkdown,
  markdownToDelta,
  type NodeJSON,
  type PageContent,
  pageViews,
  plainText,
  specToNode,
  tableRows,
} from "./blocks.ts";
import { applyOps, type Op, PageEditSession, type ToolHooks } from "./tools.ts";
import { tokensToSegments } from "./segments.ts";

const hooks: ToolHooks = {
  readPage: (content) => Promise.resolve({ blocks: pageViews(content) }),
  createSubpage: (title) =>
    Promise.resolve({ id: "page-2", space_id: "space-1", parent_id: "page-1", title, sort_key: "a0" }),
  listPages: () => Promise.resolve([]),
  readOtherPage: () => Promise.resolve({}),
};

function para(id: string, text: string, children?: NodeJSON[]): NodeJSON {
  return { id, type: "paragraph", data: { delta: [{ insert: text }] }, ...(children ? { children } : {}) };
}

function sample(): PageContent {
  return {
    document: {
      id: "root",
      type: "page",
      children: [
        { id: "h1", type: "heading", data: { level: 2, delta: [{ insert: "Goals" }] } },
        {
          id: "l1",
          type: "bulleted_list",
          data: { delta: [{ insert: "Create pages" }] },
          children: [{ id: "l2", type: "bulleted_list", data: { delta: [{ insert: "nested" }] } }],
        },
        para("p1", "Offline editing"),
        para("p2", "Risks"),
      ],
    },
    meta: { v: 1, blocks: {}, deleted: {} },
  };
}

/** Structure only: ids, types and data, without meta timestamps. */
function shape(content: PageContent) {
  return content.document;
}

Deno.test("inline markdown round-trips through delta", () => {
  const src = "Ship **v1** with *offline* `drift`, [docs](https://example.com) and ~~old~~ snake_case";
  const delta = markdownToDelta(src);
  assertEquals(delta, [
    { insert: "Ship " },
    { insert: "v1", attributes: { bold: true } },
    { insert: " with " },
    { insert: "offline", attributes: { italic: true } },
    { insert: " " },
    { insert: "drift", attributes: { code: true } },
    { insert: ", " },
    { insert: "docs", attributes: { href: "https://example.com" } },
    { insert: " and " },
    { insert: "old", attributes: { strikethrough: true } },
    { insert: " snake_case" },
  ]);
  assertEquals(markdownToDelta(deltaToMarkdown(delta)), delta);
});

Deno.test("nested formatting, escapes and mentions", () => {
  assertEquals(markdownToDelta("**bold _and italic_**"), [
    { insert: "bold ", attributes: { bold: true } },
    { insert: "and italic", attributes: { bold: true, italic: true } },
  ]);
  assertEquals(markdownToDelta("2 \\* 3 = 6"), [{ insert: "2 * 3 = 6" }]);
  const mention = [{ insert: "@Mina Park", attributes: { mention: { kind: "user", id: "u1", name: "Mina Park" } } }];
  assertEquals(markdownToDelta(deltaToMarkdown(mention)), mention);
  const literal = [{ insert: "a * b and [x] _y_" }];
  assertEquals(markdownToDelta(deltaToMarkdown(literal)), literal);
});

Deno.test("tables use appflowy's col-major cell layout", () => {
  let n = 0;
  const node = specToNode({ type: "table", rows: [["Phase", "Risks"], ["Agents", "4"]] }, { newId: () => `id${n++}` });
  assertEquals(node.data?.colsLen, 2);
  assertEquals(node.data?.rowsLen, 2);
  assertEquals(
    node.children!.map((c) => [c.data?.colPosition, c.data?.rowPosition, c.children![0].type]),
    [[0, 0, "paragraph"], [0, 1, "paragraph"], [1, 0, "paragraph"], [1, 1, "paragraph"]],
  );
  assertEquals(tableRows(node), [["Phase", "Risks"], ["Agents", "4"]]);
});

Deno.test("edit session applies tools and records replayable ops", async () => {
  const session = new PageEditSession(sample(), hooks, {
    canEdit: true,
    crossPage: false,
    author: "Claude",
    now: () => 1000,
  });
  const inserted = await session.call(
    "insert_blocks",
    JSON.stringify({
      position: { after: "h1" },
      blocks: [
        { type: "paragraph", text: "Intro **text**" },
        {
          type: "chart",
          chart: { chart_type: "bar", title: "Risks", labels: ["A", "B"], series: [{ name: "n", values: [1, 4] }] },
        },
      ],
    }),
  );
  const ids = (inserted.result as { inserted_ids: string[] }).inserted_ids;
  assertEquals(ids.length, 2);
  assertEquals(inserted.summary, "Added 2 blocks");

  const replaced = await session.call("replace_block", {
    id: "p1",
    block: { type: "heading", level: 3, text: "Offline" },
  });
  assertEquals(replaced.result, { id: "p1" });
  const keptChildren = await session.call("replace_block", {
    id: "l1",
    block: { type: "todo_list", text: "Create pages" },
  });
  assertEquals(keptChildren.result, { id: "l1" });

  const moved = await session.call("move_block", { id: "p2", position: { before: "h1" } });
  assertEquals(moved.result, { id: "p2" });
  const nested = await session.call("move_block", { id: ids[0], position: { parent: "l1" } });
  assertEquals(nested.result, { id: ids[0] });

  const deleted = await session.call("delete_blocks", { ids: ["l2", ids[1]] });
  assertEquals(deleted.result, { deleted_ids: ["l2", ids[1]] });

  const top = session.content.document.children!.map((c) => c.id);
  assertEquals(top, ["p2", "h1", "l1", "p1"]);
  assertEquals(session.content.document.children![2].type, "todo_list");
  assertEquals(session.content.document.children![2].children!.map((c) => c.id), [ids[0]]);
  assertEquals(session.content.meta.deleted["l2"], 1000);
  assertEquals(session.content.meta.blocks["p1"], 1000);

  // Replaying the ops on the original page gives the same page (the client
  // applies streamed ops this way)…
  const replayed = applyOps(session.before, session.ops, 1000);
  assertEquals(shape(replayed), shape(session.content));

  // …and inverting them in reverse order gives the original back (Undo run).
  const inverse: Op[] = [];
  for (const op of [...session.ops].reverse()) {
    switch (op.op) {
      case "insert":
        inverse.push({ op: "delete", ids: op.nodes.map((n) => n.id), previous: [] });
        break;
      case "replace":
        inverse.push({ op: "replace", id: op.id, node: op.previous, previous: op.node });
        break;
      case "move":
        inverse.push({ op: "move", id: op.id, position: op.from, from: op.position });
        break;
      case "delete":
        for (const prev of [...op.previous].reverse()) {
          inverse.push({ op: "insert", position: prev.position, nodes: [prev.node] });
        }
        break;
    }
  }
  assertEquals(shape(applyOps(session.content, inverse, 2000)), shape(session.before));
});

Deno.test("invalid calls return errors and change nothing", async () => {
  const session = new PageEditSession(sample(), hooks, { canEdit: true, crossPage: false });
  const cases: [string, unknown][] = [
    ["replace_block", { id: "nope", block: { type: "paragraph", text: "x" } }],
    ["insert_blocks", { position: { after: "h1", before: "p1" }, blocks: [{ type: "paragraph" }] }],
    ["insert_blocks", { position: { parent: "p1" }, blocks: [{ type: "paragraph" }] }],
    ["insert_blocks", { position: { at: "end" }, blocks: [{ type: "paragraph", children: [{ type: "paragraph" }] }] }],
    ["insert_blocks", { position: { at: "end" }, blocks: [{ type: "toggle" }] }],
    ["insert_blocks", {
      position: { at: "end" },
      blocks: [{
        type: "chart",
        chart: { chart_type: "bar", labels: ["a", "b"], series: [{ name: "s", values: [1] }] },
      }],
    }],
    ["move_block", { id: "l1", position: { after: "l2" } }],
    ["replace_block", { id: "l1", block: { type: "quote", text: "x" } }],
    ["list_pages", {}],
    ["nonexistent", {}],
  ];
  for (const [name, args] of cases) {
    const outcome = await session.call(name, args as Record<string, unknown>);
    assert((outcome.result as { error?: string }).error, `${name} should fail: ${JSON.stringify(args)}`);
    assertEquals(outcome.ops, []);
  }
  assertEquals(shape(session.content), shape(sample()));
  assertEquals((await session.call("read_page", "")).ops, []);

  const viewer = new PageEditSession(sample(), hooks, { canEdit: false, crossPage: true });
  const denied = await viewer.call("delete_blocks", { ids: ["p1"] });
  assert((denied.result as { error?: string }).error?.includes("view access"));
});

Deno.test("create_subpage records a create_page op", async () => {
  const session = new PageEditSession(sample(), hooks, { canEdit: true, crossPage: false, now: () => 5 });
  const outcome = await session.call("create_subpage", { title: "Notes", blocks: [{ type: "paragraph", text: "hi" }] });
  assertEquals(outcome.result, { page_id: "page-2" });
  const op = outcome.ops[0] as Extract<Op, { op: "create_page" }>;
  assertEquals(op.page.title, "Notes");
  assertEquals(plainText(op.content), "hi");
  assertEquals(session.changed, false);
});

Deno.test("views flatten nesting and fold tables", () => {
  const content = sample();
  content.document.children!.push(
    specToNode({ type: "table", rows: [["a", "b"]] }, { newId: () => crypto.randomUUID() }),
  );
  const views = pageViews(content);
  assertEquals(views.map((v) => [v.id, v.depth, v.parent_id]).slice(0, 3), [
    ["h1", 0, undefined],
    ["l1", 0, undefined],
    ["l2", 1, "l1"],
  ]);
  assertEquals(views.at(-1)!.rows, [["a", "b"]]);
});

Deno.test("soniox tokens group into segments", () => {
  const segments = tokensToSegments([
    { text: "Hello", start_ms: 0, end_ms: 400, speaker: 1 },
    { text: " there.", start_ms: 400, end_ms: 3500, speaker: 1 },
    { text: " Next", start_ms: 3600, end_ms: 3900, speaker: 1 },
    { text: "你好", start_ms: 4000, end_ms: 4500, speaker: 2 },
  ]);
  assertEquals(segments, [
    { start_ms: 0, end_ms: 3500, text: "Hello there.", speaker: "1" },
    { start_ms: 3600, end_ms: 3900, text: "Next", speaker: "1" },
    { start_ms: 4000, end_ms: 4500, text: "你好", speaker: "2" },
  ]);
});

Deno.test("code blocks keep their code verbatim", () => {
  let n = 0;
  const code = 'func main() {\n\tfmt.Println("**hi**")\n}';
  const node = specToNode({ type: "code", text: code + "\n", language: "Golang" }, { newId: () => `id${n++}` });
  assertEquals(node.data, { delta: [{ insert: code }], language: "go" });
  const content: PageContent = {
    document: { id: "root", type: "page", children: [node] },
    meta: { v: 1, blocks: {}, deleted: {} },
  };
  const [view] = pageViews(content);
  assertEquals(view.text, code);
  assertEquals(view.language, "go");
  assertEquals(plainText(content), code);
  assertEquals(
    specToNode({ type: "code", language: "constructor" }, { newId: () => "x" }).data?.language,
    "constructor",
  );
});

Deno.test("callouts, toggles, videos, link cards, math and diagrams", () => {
  let n = 0;
  const opts = { newId: () => `id${n++}` };
  const toggle = specToNode({
    type: "toggle_list",
    text: "Details",
    collapsed: true,
    children: [{ type: "paragraph", text: "Hidden" }, { type: "code", text: "x" }],
  }, opts);
  assertEquals(toggle.data?.collapsed, true);
  assertEquals(toggle.children?.length, 2);
  assertEquals(specToNode({ type: "callout", text: "Careful", tone: "WARNING" }, opts).data?.tone, "warning");
  assertEquals(specToNode({ type: "callout", text: "x", tone: "loud" }, opts).data?.tone, "info");
  const video = specToNode({ type: "video", video: { url: "https://youtu.be/dQw4w9WgXcQ" } }, opts);
  assertEquals(video.data?.url, "https://youtu.be/dQw4w9WgXcQ");
  assertEquals(specToNode({ type: "link_preview", link: { url: "https://go.dev/doc" } }, opts).data, {
    url: "https://go.dev/doc",
  });
  assertEquals(specToNode({ type: "math_equation", formula: " e^{i\\pi}+1=0 " }, opts).data, {
    formula: "e^{i\\pi}+1=0",
  });
  assertEquals(specToNode({ type: "diagram", source: "\ngraph TD\n  A-->B\n" }, opts).data, {
    source: "graph TD\n  A-->B",
  });
  for (
    const bad of [
      { type: "video", video: { url: "https://example.com/v.mp4" } },
      { type: "link_preview", link: { url: "ftp://x" } },
      { type: "math_equation" },
      { type: "diagram", source: " " },
      { type: "callout", children: [{ type: "paragraph" }] },
    ]
  ) {
    let threw = false;
    try {
      specToNode(bad, opts);
    } catch {
      threw = true;
    }
    assert(threw, JSON.stringify(bad));
  }
  const content: PageContent = {
    document: { id: "root", type: "page", children: [toggle, video] },
    meta: { v: 1, blocks: {}, deleted: {} },
  };
  const views = pageViews(content);
  assertEquals(views.map((v) => v.type), ["toggle_list", "paragraph", "code", "video"]);
  assertEquals(views[0].collapsed, true);
  assertEquals(views[3].url, "https://youtu.be/dQw4w9WgXcQ");
});

Deno.test("a page mention reads and writes as a link to the page", () => {
  const delta = markdownToDelta("See [RFC: Sync v2](leaflet://page/p-9) first");
  assertEquals(delta[1], {
    insert: "RFC: Sync v2",
    attributes: {
      href: "leaflet://page/p-9",
      mention: { kind: "page", id: "p-9", name: "RFC: Sync v2" },
    },
  });
  assertEquals(deltaToMarkdown(delta), "See [RFC: Sync v2](leaflet://page/p-9) first");
});

Deno.test("a to-do keeps its due date", () => {
  const opts = { newId: () => crypto.randomUUID() };
  const node = specToNode({ type: "todo_list", text: "Send the draft", due: "2026-10-30" }, opts);
  assertEquals(node.data?.due, "2026-10-30");
  assertEquals(
    pageViews({ document: { type: "page", id: "root", children: [node] }, meta: { v: 1, blocks: {}, deleted: {} } })[0]
      .due,
    "2026-10-30",
  );
  const bad = specToNode({ type: "todo_list", text: "x", due: "Friday" }, opts);
  assertEquals(bad.data?.due, undefined);
});

Deno.test("embed and image blocks match the Go spec", () => {
  const embed = specToNode({ type: "embed", embed: { html: "<p>hi</p>" } }, { newId: () => "e", author: "Claude" });
  assertEquals(embed.data, { title: "Interactive embed", html: "<p>hi</p>", height: 320, author: "Claude" });
  const titled = specToNode({ type: "embed", embed: { html: "<p>hi</p>", title: "Timer", height: 50 } }, {
    newId: () => "e",
  });
  assertEquals(titled.data, { title: "Timer", html: "<p>hi</p>", height: 120, author: "" });
  const image = specToNode({ type: "image", image: { url: "https://example.com/a.png", alt: "A chart" } }, {
    newId: () => "i",
  });
  assertEquals(image.data, { url: "https://example.com/a.png", align: "center", alt: "A chart" });
  assertEquals(
    specToNode({ type: "image", image: { url: "https://example.com/a.png" } }, { newId: () => "i" }).data?.alt,
    "",
  );
});
