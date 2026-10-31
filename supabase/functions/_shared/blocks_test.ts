import { assert, assertEquals } from "jsr:@std/assert@1";
import { deltaToMarkdown, markdownToDelta, type NodeJSON, pageViews, specToNode, tableRows } from "./blocks.ts";

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
