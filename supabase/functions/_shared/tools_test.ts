import { assert, assertEquals } from "jsr:@std/assert@1";
import {
  type BlockSpec,
  codeLanguage,
  deltaToMarkdown,
  isPageContent,
  markdownToDelta,
  MAX_SPEC_DEPTH,
  MAX_SPEC_NODES,
  type NodeJSON,
  type PageContent,
  pageViews,
  plainText,
  specToNode,
  tableRows,
} from "./blocks.ts";
import { applyOps, type Op, overwrites, PageEditSession, type ToolHooks, URL_RE } from "./tools.ts";
import { tokensToSegments } from "./segments.ts";
import runOps from "./testdata/run_ops.json" with { type: "json" };
import views from "./testdata/views.json" with { type: "json" };
import specs from "./testdata/specs.json" with { type: "json" };
import markdown from "./testdata/markdown.json" with { type: "json" };
import agent from "./testdata/agent.json" with { type: "json" };
import { markdownSpecs } from "./lines.ts";

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

  // …and inverting them gives the original back (Undo run).
  assertEquals(shape(applyOps(session.content, invertRun(session.ops), 2000)), shape(session.before));
});

/**
 * The ops that undo a run, as doc.InvertRun makes them on the client: the
 * ops in reverse order, each inverted, a delete's blocks put back in the
 * order it recorded them.
 */
function invertRun(ops: Op[]): Op[] {
  const inverse: Op[] = [];
  for (const op of [...ops].reverse()) {
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
        for (const prev of op.previous) {
          inverse.push({ op: "insert", position: prev.position, nodes: [prev.node] });
        }
        break;
    }
  }
  return inverse;
}

Deno.test("a replace that kept children keeps the newer revision's children", async () => {
  const session = new PageEditSession(sample(), hooks, { canEdit: true, crossPage: false, now: () => 1000 });
  await session.call("replace_block", { id: "l1", block: { type: "bulleted_list", text: "Create more pages" } });

  // Someone saved the page meanwhile: the child edited, another added.
  const latest = sample();
  const l1 = latest.document.children![1];
  l1.children![0].data = { delta: [{ insert: "nested, edited" }] };
  l1.children!.push({ id: "l3", type: "bulleted_list", data: { delta: [{ insert: "added" }] } });
  latest.meta.blocks = { l2: 500, l3: 500 };

  const out = applyOps(latest, session.ops, 2000);
  const block = out.document.children![1];
  assertEquals(block.data!.delta, [{ insert: "Create more pages" }]);
  assertEquals(block.children, l1.children);
  assertEquals(out.meta.blocks, { l1: 2000, l2: 500, l3: 500 });

  // Children given anew still replace the old ones.
  const fresh = new PageEditSession(sample(), hooks, { canEdit: true, crossPage: false });
  await fresh.call("replace_block", {
    id: "l1",
    block: { type: "bulleted_list", text: "Create pages", children: [{ type: "bulleted_list", text: "new" }] },
  });
  const children = applyOps(latest, fresh.ops).document.children![1].children!;
  assertEquals(children.map((c) => c.data!.delta), [[{ insert: "new" }]]);
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
      { type: "video", video: { url: "https://example.com/v" } },
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
  assertEquals(views[3].video?.url, "https://youtu.be/dQw4w9WgXcQ");
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

Deno.test("a replay that replaces or deletes a block changed since is told apart", () => {
  const para = (id: string, text: string): NodeJSON => ({ id, type: "paragraph", data: { delta: [{ insert: text }] } });
  const page = (...children: NodeJSON[]): PageContent => ({
    document: { id: "root", type: "page", children },
    meta: { v: 1, blocks: {}, deleted: {} },
  });
  const read = para("p", "original");
  const replace: Op = { op: "replace", id: "p", previous: read, node: para("p", "agent edit") };
  const remove: Op = { op: "delete", ids: ["p"], previous: [{ node: read, position: { at: "end" } }] };
  const insert: Op = { op: "insert", position: { at: "end" }, nodes: [para("n", "new")] };
  // jsonb keeps no key order: the same block read back is the same.
  const reordered: NodeJSON = { data: { delta: [{ insert: "original" }] }, type: "paragraph", id: "p" };
  assert(!overwrites(page(reordered, para("q", "typed elsewhere")), [replace, insert]));
  assert(overwrites(page(para("p", "concurrent human edit")), [replace]));
  assert(overwrites(page(para("p", "concurrent human edit")), [remove]));
  // Only the agent's first op on a block has it as read.
  const again: Op = { op: "replace", id: "p", previous: para("p", "agent edit"), node: para("p", "agent edit 2") };
  assert(!overwrites(page(para("p", "original")), [replace, again]));
});

Deno.test("stored content is a page's only with its document, of a known version", () => {
  assert(!isPageContent(null));
  assert(!isPageContent({}));
  assert(!isPageContent({ document: null }));
  assert(!isPageContent({ document: { children: [] }, meta: { v: 2 } }));
  assert(
    isPageContent({ document: { id: "root", type: "page", children: [] }, meta: { v: 1, blocks: {}, deleted: {} } }),
  );
  assert(isPageContent({ document: { children: [] } }));
});

Deno.test("specs nest only so deep, and make only so many blocks per call", async () => {
  let n = 0;
  const nested = (depth: number): BlockSpec => {
    let spec: BlockSpec = { type: "bulleted_list", text: "leaf" };
    for (let i = 1; i < depth; i++) spec = { type: "bulleted_list", text: `level ${i}`, children: [spec] };
    return spec;
  };
  specToNode(nested(MAX_SPEC_DEPTH), { newId: () => `id${n++}` });
  let threw = false;
  try {
    specToNode(nested(MAX_SPEC_DEPTH + 1), { newId: () => `id${n++}` });
  } catch {
    threw = true;
  }
  assert(threw, "a spec nested too deep was made");
  threw = false;
  try {
    specToNode({ type: "bulleted_list", text: "x", children: "nope" as unknown as BlockSpec[] }, { newId: () => "x" });
  } catch {
    threw = true;
  }
  assert(threw, "children that are not an array were taken");

  // Within the 200 top-level blocks a call takes, nested ones count too.
  const wide: BlockSpec = {
    type: "bulleted_list",
    text: "x",
    children: Array.from({ length: Math.ceil(MAX_SPEC_NODES / 100) }, () => ({ type: "bulleted_list", text: "y" })),
  };
  const session = new PageEditSession(sample(), hooks, { canEdit: true, crossPage: false });
  const outcome = await session.call("insert_blocks", { position: { at: "end" }, blocks: Array(100).fill(wide) });
  assert((outcome.result as { error?: string }).error?.includes("in all"), JSON.stringify(outcome.result));
  assertEquals(outcome.ops, []);
  assertEquals(shape(session.content), shape(sample()));
});

Deno.test("after reading another page, blocks that load a URL use only this page's or the request's", async () => {
  const content = sample();
  content.document.children!.push({ id: "img", type: "image", data: { url: "https://example.com/here.png" } });
  const reading: ToolHooks = {
    ...hooks,
    readOtherPage: () =>
      Promise.resolve({ blocks: [{ id: "x", type: "paragraph", text: "see https://example.com/there.png for it" }] }),
  };
  const session = new PageEditSession(content, reading, {
    canEdit: true,
    crossPage: true,
    request: "Add the chart from https://example.com/asked.png",
  });
  const image = (url: string) => ({ type: "image", image: { url } });
  const end = { at: "end" };
  // Before reading another page, any URL goes.
  let outcome = await session.call("insert_blocks", { position: end, blocks: [image("https://a.example/1.png")] });
  assert(!(outcome.result as { error?: string }).error, JSON.stringify(outcome.result));

  await session.call("read_other_page", { page_id: "page-9" });
  for (const url of ["https://example.com/here.png", "https://example.com/asked.png"]) {
    outcome = await session.call("insert_blocks", { position: end, blocks: [image(url)] });
    assert(!(outcome.result as { error?: string }).error, `${url}: ${JSON.stringify(outcome.result)}`);
  }
  const leaks: [string, Record<string, unknown>][] = [
    // What the run wrote is not what it found, though it is on the page.
    ["insert_blocks", { position: end, blocks: [image("https://a.example/1.png")] }],
    // Only the page it read has this one: which of those it picked could
    // spell out what it read.
    ["insert_blocks", { position: end, blocks: [image("https://example.com/there.png")] }],
    ["insert_blocks", { position: end, blocks: [image("https://evil.example/x.png?notes=secret")] }],
    ["insert_blocks", {
      position: end,
      blocks: [{
        type: "bulleted_list",
        text: "x",
        children: [{ type: "link_preview", link: { url: "https://evil.example/" } }],
      }],
    }],
    ["replace_block", { id: "img", block: { type: "video", video: { url: "https://evil.example/v.mp4" } } }],
    ["create_subpage", { title: "Notes", blocks: [image("https://evil.example/y.png")] }],
  ];
  for (const [name, args] of leaks) {
    const before = structuredClone(shape(session.content));
    outcome = await session.call(name, args);
    const error = (outcome.result as { error?: string }).error;
    assert(error?.includes("Link to it in text instead"), JSON.stringify(outcome.result));
    assertEquals(outcome.ops, []);
    assertEquals(shape(session.content), before);
  }
  // Links in text stay free.
  outcome = await session.call("insert_blocks", {
    position: end,
    blocks: [{ type: "paragraph", text: "[x](https://evil.example/)" }],
  });
  assert(!(outcome.result as { error?: string }).error, JSON.stringify(outcome.result));
});

Deno.test("URLs only on pages read do not count, however many there are", async () => {
  // Someone who can edit a page the agent reads puts a URL there for every
  // byte; one on this page as the run began still counts.
  const many = Array.from({ length: 256 }, (_, i) => `https://evil.example/${i}.png`);
  const content = sample();
  content.document.children!.push({ id: "img", type: "image", data: { url: "https://example.com/here.png" } });
  const reading: ToolHooks = {
    ...hooks,
    readOtherPage: (id) =>
      Promise.resolve(
        id === "private"
          ? { blocks: [{ id: "s", type: "paragraph", text: "secret" }] }
          : { page: { id, title: many[0] }, blocks: many.map((u, i) => ({ id: `b${i}`, type: "image", url: u })) },
      ),
  };
  const session = new PageEditSession(content, reading, { canEdit: true, crossPage: true });
  await session.call("read_other_page", { page_id: "shared" });
  await session.call("read_other_page", { page_id: "private" });
  const image = (url: string) => ({ position: { at: "end" }, blocks: [{ type: "image", image: { url } }] });
  for (const url of [many[0], many[115], many[255]]) {
    const outcome = await session.call("insert_blocks", image(url));
    assert((outcome.result as { error?: string }).error?.includes("Link to it in text instead"), url);
  }
  const outcome = await session.call("insert_blocks", image("https://example.com/here.png"));
  assert(!(outcome.result as { error?: string }).error, JSON.stringify(outcome.result));
});

Deno.test("URLs the run wrote stay unseen when it reads them back", async () => {
  const leak = "https://evil.example/x.png?d=secret";
  const written = { blocks: [{ id: "b", type: "paragraph", text: `[x](${leak})` }] };
  const reading: ToolHooks = {
    ...hooks,
    readOtherPage: (id) =>
      Promise.resolve(
        id === "private"
          ? { blocks: [{ id: "s", type: "paragraph", text: "secret" }] }
          // This page, as the store has it, lists the subpage the run made.
          : id === "page-1"
          ? { ...written, subpages: [{ id: "page-2", title: leak }] }
          : written,
      ),
  };
  const session = new PageEditSession(sample(), reading, { canEdit: true, crossPage: true });
  const image = { position: { at: "end" }, blocks: [{ type: "image", image: { url: leak } }] };
  await session.call("read_other_page", { page_id: "private" });
  const made = await session.call("create_subpage", {
    title: leak,
    blocks: [{ type: "paragraph", text: `[x](${leak})` }],
  });
  assert(!(made.result as { error?: string }).error, JSON.stringify(made.result));
  for (const id of ["page-2", "page-1"]) {
    await session.call("read_other_page", { page_id: id });
    const outcome = await session.call("insert_blocks", image);
    assert((outcome.result as { error?: string }).error, `read back from ${id}: ${JSON.stringify(outcome.result)}`);
  }
});

Deno.test("listing pages starts checking URLs", async () => {
  const content = sample();
  content.document.children!.push({ id: "img", type: "image", data: { url: "https://example.com/here.png" } });
  const listing: ToolHooks = {
    ...hooks,
    listPages: () => Promise.resolve([{ id: "p", title: "Salary review", parent_id: null }]),
  };
  const session = new PageEditSession(content, listing, { canEdit: true, crossPage: true });
  await session.call("list_pages", {});
  const image = (url: string) => ({ position: { at: "end" }, blocks: [{ type: "image", image: { url } }] });
  let outcome = await session.call("insert_blocks", image("https://evil.example/x.png?t=Salary+review"));
  assert((outcome.result as { error?: string }).error, JSON.stringify(outcome.result));
  outcome = await session.call("insert_blocks", image("https://example.com/here.png"));
  assert(!(outcome.result as { error?: string }).error, JSON.stringify(outcome.result));
});

Deno.test("a replace with children: [] keeps the nested items", async () => {
  const session = new PageEditSession(sample(), hooks, { canEdit: true, crossPage: false });
  let outcome = await session.call("replace_block", {
    id: "l1",
    block: { type: "bulleted_list", text: "Renamed", children: [] },
  });
  assert(!(outcome.result as { error?: string }).error, JSON.stringify(outcome.result));
  assertEquals(session.content.document.children![1].children!.map((c) => c.id), ["l2"]);
  outcome = await session.call("replace_block", { id: "l1", block: { type: "paragraph", text: "Flat", children: [] } });
  assert((outcome.result as { error?: string }).error?.includes("nested"), JSON.stringify(outcome.result));
  assertEquals(session.content.document.children![1].children!.map((c) => c.id), ["l2"]);
});

Deno.test("write_markdown writes in place of an empty block, a block a line", async () => {
  const content = sample();
  content.document.children!.splice(3, 0, para("e1", ""));
  const session = new PageEditSession(content, hooks, { canEdit: true, crossPage: false });
  const out = await session.call("write_markdown", {
    // As models that write every key of a schema give it.
    position: { replace: "e1", after: null, before: "", parent: null, at: "end" },
    markdown: "## Tide\nThe sea went.\n\nMara ran.",
  });
  const ids = (out.result as { inserted_ids: string[] }).inserted_ids;
  assertEquals(ids.length, 3);
  assertEquals(ids[0], "e1");
  assertEquals(out.ops.map((o) => o.op), ["replace", "insert"]);
  const kids = session.content.document.children!;
  assertEquals(kids.slice(3, 6).map((n) => [n.type, deltaToMarkdown(n.data!.delta as never)]), [
    ["heading", "Tide"],
    ["paragraph", "The sea went."],
    ["paragraph", "Mara ran."],
  ]);
  const over = await session.call("write_markdown", { position: { replace: "p1" }, markdown: "x" });
  assert((over.result as { error?: string }).error?.includes("empty block"));
  const end = await session.call("write_markdown", { position: { at: "end" }, markdown: "Last." });
  assertEquals(end.ops.map((o) => o.op), ["insert"]);
  // A space is text the person typed, as the app's own agents take it.
  const spaced = sample();
  spaced.document.children!.splice(3, 0, para("s1", " "));
  const other = new PageEditSession(spaced, hooks, { canEdit: true, crossPage: false });
  const space = await other.call("write_markdown", { position: { replace: "s1" }, markdown: "x" });
  assert((space.result as { error?: string }).error?.includes("empty block"));
});

Deno.test("a write_markdown in place of a block that fails part way changes nothing", async () => {
  const content = sample();
  content.document.children!.splice(3, 0, para("e1", ""));
  const failing: [string, string][] = [
    // The heading takes e1's place before the table is refused.
    [
      "a table too large",
      "# Title\n| a |\n| - |\n" + Array.from({ length: MAX_SPEC_NODES }, (_, i) => `| ${i} |`).join("\n"),
    ],
    ["too many lines", Array.from({ length: MAX_SPEC_NODES + 2 }, (_, i) => `Line ${i}`).join("\n\n")],
  ];
  for (const [what, markdown] of failing) {
    const session = new PageEditSession(content, hooks, { canEdit: true, crossPage: false });
    const out = await session.call("write_markdown", { position: { replace: "e1" }, markdown });
    assert((out.result as { error?: string }).error, `${what} should fail`);
    assertEquals(out.ops, []);
    assertEquals(session.ops, [], what);
    assertEquals(session.changed, false, what);
    assertEquals(session.content, content, what);
  }

  // A URL refused once the run has read beyond this page.
  const session = new PageEditSession(content, hooks, { canEdit: true, crossPage: true });
  await session.call("list_pages", {});
  const out = await session.call("write_markdown", {
    position: { replace: "e1" },
    markdown: "# Title\n\n![x](https://example.com/x.png?notes=secret)",
  });
  assert((out.result as { error?: string }).error?.includes("URL"), JSON.stringify(out.result));
  assertEquals(session.ops, []);
  assertEquals(session.content, content);
  // And the next call still works on the page as it was.
  const ok = await session.call("write_markdown", { position: { replace: "e1" }, markdown: "Done." });
  assertEquals((ok.result as { inserted_ids: string[] }).inserted_ids, ["e1"]);
  assertEquals(session.ops.map((o) => o.op), ["replace"]);
});

// The fixtures in testdata hold what this side makes, for the Go tests to
// check the client against (internal/doc/parity_test.go): a cloud run's
// ops, which the client applies and undoes; the blocks as agents read
// them; and the blocks specs make. Run with --allow-write=_shared/testdata
// to write them anew.

/** Checks a fixture against what this side makes now, or writes it, when allowed to. */
async function fixture(name: string, made: unknown, stored: unknown): Promise<void> {
  const url = new URL(`./testdata/${name}`, import.meta.url);
  const json = JSON.stringify(made, null, 2) + "\n";
  if ((await Deno.permissions.query({ name: "write", path: url })).state === "granted") {
    await Deno.writeTextFile(url, json);
    return;
  }
  assertEquals(
    stored,
    JSON.parse(json),
    `testdata/${name} is out of date: run deno test --allow-write=_shared/testdata`,
  );
}

function pageOf(...children: NodeJSON[]): PageContent {
  return { document: { id: "root", type: "page", children }, meta: { v: 1, blocks: {}, deleted: {} } };
}

/** Runs fn with block IDs drawn in turn rather than at random, so what it makes is the same each time. */
async function withSteadyIds<T>(fn: () => Promise<T>): Promise<T> {
  const random = crypto.getRandomValues;
  let next = 0;
  crypto.getRandomValues = (<A extends ArrayBufferView | null>(array: A): A => {
    if (array) {
      const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength);
      for (let i = 0; i < bytes.length; i++) bytes[i] = (next++ * 7) % 251;
    }
    return array;
  }) as typeof crypto.getRandomValues;
  try {
    return await fn();
  } finally {
    crypto.getRandomValues = random;
  }
}

Deno.test("a run's ops undo as the client undoes them (testdata/run_ops.json)", async () => {
  const item = (id: string, text: string, children?: NodeJSON[]): NodeJSON => ({
    ...para(id, text, children),
    type: "bulleted_list",
  });
  const before = pageOf(
    { id: "h1", type: "heading", data: { level: 2, delta: [{ insert: "Goals" }] } },
    item("l1", "Lists", [item("l2", "first"), item("l3", "second")]),
    para("p1", "one"),
    para("p2", "two"),
    para("p3", "three"),
  );
  const session = new PageEditSession(before, hooks, { canEdit: true, crossPage: false, now: () => 1000 });
  await withSteadyIds(async () => {
    const inserted = await session.call("insert_blocks", {
      position: { after: "h1" },
      blocks: [{ type: "paragraph", text: "Intro" }, { type: "table", rows: [["a", "b"], ["1", "2"]] }],
    });
    const [intro] = (inserted.result as { inserted_ids: string[] }).inserted_ids;
    await session.call("replace_block", { id: "p1", block: { type: "heading", level: 3, text: "One" } });
    await session.call("move_block", { id: "p3", position: { before: "h1" } });
    // Blocks side by side, and a list's every child: each goes back after
    // the one before it.
    await session.call("delete_blocks", { ids: ["p1", "p2"] });
    await session.call("delete_blocks", { ids: [intro, "l2", "l3"] });
    // A first block, which goes back before the next; then all of them,
    // the first of which goes back at the start.
    await session.call("delete_blocks", { ids: ["p3"] });
    await session.call("delete_blocks", { ids: session.content.document.children!.map((n) => n.id) });
  });
  const ops = session.ops;
  assertEquals(ops.length, 7);
  const positions = ops.flatMap((op) => op.op === "delete" ? op.previous.map((p) => Object.keys(p.position)[0]) : []);
  for (const key of ["after", "before", "parent", "at"]) assert(positions.includes(key), key);
  assertEquals(applyOps(session.content, invertRun(ops), 2000).document, before.document);
  await fixture("run_ops.json", { before, ops, after: session.content }, runOps);
});

Deno.test("blocks read as the client reads them (testdata/views.json)", async () => {
  const delta = (text: string) => ({ delta: [{ insert: text }] });
  const content = pageOf(
    {
      id: "a",
      type: "paragraph",
      data: { delta: [{ insert: "Ship " }, { insert: "v1", attributes: { bold: true } }] },
    },
    { id: "b", type: "heading", data: { level: 2, ...delta("Plan") } },
    {
      id: "c",
      type: "bulleted_list",
      data: delta("Item"),
      children: [{ id: "c1", type: "todo_list", data: { ...delta("Do it"), checked: true, due: "2026-10-30" } }],
    },
    { id: "d", type: "quote", data: delta("Said") },
    { id: "e", type: "code", data: { delta: [{ insert: "fmt.Println(1)" }], language: "go" } },
    { id: "f", type: "callout", data: { ...delta("Careful"), tone: "warning" } },
    {
      id: "g",
      type: "toggle_list",
      data: { ...delta("More"), collapsed: true },
      children: [{ id: "g1", type: "paragraph", data: delta("Hidden") }],
    },
    { id: "h", type: "divider" },
    { id: "i", type: "image", data: { url: "https://example.com/a.png", align: "center", alt: "A chart" } },
    {
      id: "j",
      type: "chart",
      data: {
        chart_type: "bar",
        title: "Risks",
        labels: ["A", "B"],
        series: [{ name: "n", values: [1, 4] }],
        caption: "By phase",
        author: "Claude",
      },
    },
    { id: "k", type: "embed", data: { title: "Timer", height: 320, html: "<p>hi</p>", author: "Claude" } },
    { id: "l", type: "video", data: { url: "https://youtu.be/dQw4w9WgXcQ", title: "Song" } },
    { id: "m", type: "link_preview", data: { url: "https://go.dev/", title: "Go", description: "The language" } },
    { id: "n", type: "file", data: { name: "notes.pdf", mime: "application/pdf", size: 1234 } },
    { id: "o", type: "math_equation", data: { formula: "e^{i\\pi}+1=0" } },
    { id: "p", type: "diagram", data: { source: "graph TD\n  A-->B" } },
    { id: "q", type: "subpage", data: { page_id: "page-2" } },
    { id: "r", type: "numbered_list", data: delta("First") },
    { id: "s", type: "recording", data: { recording_id: "rec-1", title: "Standup", duration_ms: 60000 } },
    // Data a block lacks is left out of its view, not null.
    { id: "u", type: "image", data: { url: "https://example.com/b.png" } },
    { id: "v", type: "subpage", data: {} },
    // A heading's level shows within 1 and 6, however far past it is stored.
    { id: "w1", type: "heading", data: { level: 1e21, ...delta("Far") } },
    { id: "w2", type: "heading", data: { level: 1e300, ...delta("Farther") } },
    { id: "w3", type: "heading", data: { level: -1e21, ...delta("Below") } },
  );
  await withSteadyIds(() => {
    content.document.children!.push(
      specToNode({ type: "table", rows: [["x", "y"], ["1", "2"]] }, { newId: () => "t" }),
    );
    return Promise.resolve();
  });
  content.document.children!.at(-1)!.children!.forEach((cell, i) => {
    cell.id = `t${i}`;
    cell.children![0].id = `t${i}p`;
  });
  await fixture("views.json", { content, views: pageViews(content) }, views);
});

/** A block without its IDs, which each side draws at random. */
function withoutIds(node: NodeJSON): unknown {
  const { id: _, children, ...rest } = node;
  return children ? { ...rest, children: children.map(withoutIds) } : rest;
}

Deno.test("specs make the blocks the client makes (testdata/specs.json)", async () => {
  const cases: unknown[] = [
    // No type, or an empty one, is a paragraph.
    { text: "plain" },
    { type: "", text: "empty type" },
    // Numbers are cut to their whole part.
    { type: "heading", level: 1.5, text: "Title" },
    { type: "embed", embed: { html: "<p>x</p>", height: 500.5 } },
    // An image, video or link card may give its URL at the top.
    { type: "image", url: " https://example.com/a.png " },
    { type: "image", image: { url: "HTTPS://example.com/b.png", alt: "B" } },
    { type: "image", image: { url: "http:/example.com/c.png" } },
    { type: "image", image: { url: "https://" } },
    { type: "video", url: "https://youtu.be/dQw4w9WgXcQ" },
    { type: "link_preview", url: "https://go.dev/doc" },
    // A table's empty cells are empty, and its numbers text.
    { type: "table", rows: [["a", null], [1, true]] },
    // A series without a name is named by its place.
    { type: "chart", chart: { chart_type: "line", labels: ["a", null], series: [{ values: [1, "2"] }] } },
    { type: "chart", chart: { chart_type: "bar", labels: ["a"], series: [{ name: "n", values: ["x"] }] } },
    // A pie takes one series.
    { type: "chart", chart: { chart_type: "pie", labels: ["a"], series: [{ values: [1] }, { values: [2] }] } },
    // Children are blocks.
    { type: "bulleted_list", text: "list", children: [{ text: "child" }] },
    { type: "bulleted_list", text: "list", children: ["not a block"] },
    { type: "paragraph", text: "---" },
    // Numbers written as JavaScript writes them.
    { type: "table", rows: [[1250000, 0.1, 1e21, 1e-7, -2.5e-7, 123456789012]] },
    { type: "chart", chart: { labels: [1250000, 1e21], series: [{ name: 1e-7, values: [1, 2] }] } },
    // A chart's value is a number, or a string of a decimal one.
    {
      type: "chart",
      chart: {
        chart_type: "",
        labels: ["a", "b", "c", "d", "e"],
        series: [{ values: ["2abc", "0x10", "1_000", " 3.5 ", "1e3"] }],
      },
    },
    // A string field of another kind is empty.
    { type: "chart", chart: { title: 5, caption: true, labels: ["a"], series: [{ values: [1] }] } },
    { type: "code", text: 5, language: 7 },
    { type: "paragraph", text: 5 },
    { type: "video", video: { url: "https://youtu.be/dQw4w9WgXcQ", title: 5 } },
    // Children and a table's rows are lists.
    { type: "bulleted_list", text: "list", children: {} },
    { type: "table", rows: [["a"], "b"] },
    // A link's host has a name, and no space of any kind.
    { type: "image", image: { url: "https://:80/a.png" } },
    { type: "image", image: { url: "https://exa\u00a0mple.com/a.png" } },
    { type: "link_preview", link: { url: "https://:80/" } },
    // A rule is of one mark, with spaces or tabs only.
    { type: "paragraph", text: "-\t-\t-" },
    { type: "paragraph", text: " - - - " },
    { type: "paragraph", text: "\u00a0---" },
    { type: "paragraph", text: "-*-" },
    // Spaces are Go's unicode.IsSpace: NEL is one, a BOM is not.
    { type: "image", image: { url: "\u0085https://example.com/a.png\u2028" } },
    { type: "image", image: { url: "\ufeffhttps://example.com/a.png" } },
    { type: "code", text: "x", language: "Visual\u2029Basic\u0085" },
    { type: "math_equation", formula: "\u0085E = mc^2\u3000" },
    { type: "callout", text: "x", tone: "\u0085warning\u0085" },
    { type: "link_preview", link: { url: "https://go.dev/\u0085" } },
    // A list or an object is no text.
    { type: "table", rows: [[["a"], { b: 1 }, "x"]] },
    { type: "chart", chart: { labels: [["a"], { b: 1 }], series: [{ name: ["n"], values: [1, 2] }] } },
  ];
  const made = cases.map((spec) => {
    try {
      return { spec, node: withoutIds(specToNode(spec as BlockSpec, { newId: () => "x" })) };
    } catch {
      return { spec, error: true };
    }
  });
  await fixture("specs.json", made, specs);
});

Deno.test("a call takes as many blocks, and a table as many cells, as the client's", async () => {
  const session = new PageEditSession(sample(), hooks, { canEdit: true, crossPage: false });
  const row = Array.from({ length: 20 }, (_, i) => `c${i}`);
  const blocks = [
    ...Array.from({ length: 300 }, () => ({ type: "paragraph", text: "x" })),
    { type: "table", rows: Array.from({ length: 21 }, () => row) },
  ];
  const outcome = await session.call("insert_blocks", { position: { at: "end" }, blocks });
  assert(!(outcome.result as { error?: string }).error, JSON.stringify(outcome.result));
  assertEquals(session.content.document.children!.at(-1)!.children!.length, 420);
});

Deno.test("Markdown reads and writes as the client's does (testdata/markdown.json)", async () => {
  const blocks = [
    // Only the line of dashes under the header is no row.
    "| a | b |\n| --- | --- |\n| --- | x |",
    "| --- | --- |\n| a | b |",
    // In a code span only \\| is an escape.
    "| `a\\b\\|c` | \\* |\n| - | - |",
    // A number of four digits or more is an item only after the one before it.
    "998. a\n999. b\n1000. c",
    "1. a\n\n1000. b",
    "1999. A year to remember",
    "123456789. nine digits\n1234567890. ten digits",
    // A </summary> in a code span is the code's.
    "<details>\n<summary>`</summary>` and more</summary>\nInside\n</details>",
    // A lone \r ends a line too.
    "line one\rline two\r\n\r- item",
    "```Visual Basic\nx = 1\n```",
    // A rule is of one mark, with spaces or tabs only; other spaces are text.
    " ---",
    "-\t-\t-",
    "\v---",
    "a\n\u00a0---",
    // Only spaces and tabs indent a fence; a line after another space is text.
    "\u00a0```go\nx := 1\n```",
    "\u3000```\ncode\n```",
    // A link's address in a cell holds no <br>, nor a code span.
    "| [a<br>b](https://x.com/`<br>`) | c |\n| - | - |",
    "| [x](https://x.com/a<br>b) | `c<br>d` e<br>f |\n| - | - |",
    // Spaces are Go's unicode.IsSpace: NEL is one, a BOM is not; U+2028
    // and U+2029 are text in a line.
    "| a\u0085 | \ufeffb |\n| - | - |",
    "- item\u2028more\n# Title\u2029x\n1. one\u2028two",
    "<details>\u0085\n<summary>Sum</summary>\u0085\nInside\n</details>",
    "```go\u0085\nx\n```",
    "\ufeff---",
    // An image may have spaces or tabs after it; only those separate its
    // title.
    "![a](https://example.com/a.png) \t",
    '![a](https://example.com/a.png "t")',
    "![a](https://example.com/a.png)\u00a0",
    // A no-break space before a title puts it in the address, which no
    // picture's may hold.
    '![a](https://example.com/a.png\u00a0"t")',
  ];
  const inline = [
    "See [**Plan**](leaflet://page/p1) now",
    "**[Plan](leaflet://page/p2)**",
    "[**a** b](leaflet://page/p3)",
    "[*a* *b*](leaflet://page/p4)",
    "Wow\\![x](https://x.com)",
    // A run of spaces only has no say in a mention's formats.
    "[ *0*](leaflet://page/p5)",
    "*[ 0](leaflet://page/p6)*",
  ];
  const deltas: DeltaOpLike[][] = [
    [{ insert: "Wow!" }, { insert: "site", attributes: { href: "https://x.com" } }],
    [{ insert: "Hi !" }, { insert: "@Ann", attributes: { mention: { kind: "user", id: "u1", name: "Ann" } } }],
    [{ insert: "!" }, {
      insert: "Plan",
      attributes: { href: "leaflet://page/p1", mention: { kind: "page", id: "p1", name: "Plan" } },
    }],
    [{ insert: "![not a picture]" }],
    // A line break in code at a format's end is off the format.
    [{ insert: "a" }, { insert: "\nb", attributes: { bold: true, code: true } }],
    [{ insert: "x\n", attributes: { italic: true, code: true } }, { insert: "y" }],
  ];
  const languages = ["Visual Basic", "  Objective   C ", "JS", "c++", "Golang"];
  const made = {
    blocks: blocks.map((md) => ({
      markdown: md,
      nodes: markdownSpecs(md).map((spec) => withoutIds(specToNode(spec, { newId: () => "x" }))),
    })),
    inline: inline.map((md) => ({ markdown: md, delta: markdownToDelta(md) })),
    deltas: deltas.map((delta) => ({ delta, markdown: deltaToMarkdown(delta as never) })),
    languages: Object.fromEntries(languages.map((l) => [l, codeLanguage(l)])),
  };
  await fixture("markdown.json", made, markdown);
});

type DeltaOpLike = { insert: string; attributes?: Record<string, unknown> };

Deno.test("agents find URLs and title subpages as the client's do (testdata/agent.json)", async () => {
  // A URL ends at the first space, as unicode.IsSpace has them.
  const texts = [
    "see https://example.com/a.png\u3000then",
    "see https://example.com/b.png\u0085then",
    "see https://example.com/c.png\ufeffthen",
    "(https://example.com/d.png) and <https://example.com/e>",
  ];
  const titles = ["  Notes\u3000", "\u0085Plan\u0085", "\ufeffBOM", " \u3000 "];
  const made: (string | null)[] = [];
  for (const title of titles) {
    let got: string | null = null;
    const session = new PageEditSession(sample(), {
      ...hooks,
      createSubpage: (t) => {
        got = t;
        return Promise.resolve({ id: "page-2", space_id: "s", parent_id: "page-1", title: t, sort_key: "a0" });
      },
    }, { canEdit: true, crossPage: false });
    await session.call("create_subpage", { title });
    made.push(got);
  }
  await fixture("agent.json", {
    urls: texts.map((text) => ({ text, urls: [...text.matchAll(URL_RE)].map((m) => m[0]) })),
    titles: titles.map((title, i) => ({ title, made: made[i] })),
  }, agent);
});
