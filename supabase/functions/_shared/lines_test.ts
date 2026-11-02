import { assertEquals } from "jsr:@std/assert@1";
import { specToNode } from "./blocks.ts";
import { markdownSpecs, tableRow } from "./lines.ts";

Deno.test("markdown reads as a block a line", () => {
  const specs = markdownSpecs(
    "## Start\nFirst line.\nSecond line.\n\n> [!TIP]\n> Use Go\n- one\n  - nested\n- [x] done\n1. first\n---\n```go\na := 1\n\nb := 2\n```\n| a | b |\n| - | - |\n| 1 | 2 |\n![A cat](https://x.dev/cat.png)",
  );
  assertEquals(specs, [
    { type: "heading", level: 2, text: "Start" },
    { type: "paragraph", text: "First line." },
    { type: "paragraph", text: "Second line." },
    { type: "callout", tone: "tip", text: "Use Go" },
    { type: "bulleted_list", text: "one", children: [{ type: "bulleted_list", text: "nested" }] },
    { type: "todo_list", checked: true, text: "done" },
    { type: "numbered_list", text: "first" },
    { type: "divider" },
    { type: "code", text: "a := 1\n\nb := 2", language: "go" },
    { type: "table", rows: [["a", "b"], ["1", "2"]] },
    { type: "image", image: { url: "https://x.dev/cat.png", alt: "A cat" } },
  ]);
});

Deno.test("an open fence runs to the end, and math and Mermaid make their blocks", () => {
  assertEquals(markdownSpecs("$$\nE = mc^2\n$$\n```mermaid\ngraph TD; A-->B"), [
    { type: "math_equation", formula: "E = mc^2" },
    { type: "diagram", source: "graph TD; A-->B" },
  ]);
  assertEquals(markdownSpecs("\n\n"), []);
});

Deno.test("table rows split on unescaped pipes", () => {
  assertEquals(tableRow("| a\\|b | c |"), { cells: ["a|b", "c"], rule: false });
  assertEquals(tableRow("| --- | :---: |"), { cells: ["---", ":---:"], rule: true });
  // In a code span only \| is an escape: a backslash is the code's.
  assertEquals(tableRow("| `a\\b\\|c` | \\* |").cells, ["`a\\b|c`", "\\*"]);
});

// The cases internal/doc/lines_test.go reads, read the same way.

Deno.test("an item nests under the one it is indented under, by that item's indent", () => {
  assertEquals(
    markdownSpecs("- a\n    - b\n        - c\n    - d"),
    [{
      type: "bulleted_list",
      text: "a",
      children: [
        { type: "bulleted_list", text: "b", children: [{ type: "bulleted_list", text: "c" }] },
        { type: "bulleted_list", text: "d" },
      ],
    }],
  );
  assertEquals(markdownSpecs("1. Step\n   1. Sub\n      - detail\n        - more\n      - detail2"), [{
    type: "numbered_list",
    text: "Step",
    children: [{
      type: "numbered_list",
      text: "Sub",
      children: [
        { type: "bulleted_list", text: "detail", children: [{ type: "bulleted_list", text: "more" }] },
        { type: "bulleted_list", text: "detail2" },
      ],
    }],
  }]);
  assertEquals(markdownSpecs("- a\n - b"), [{ type: "bulleted_list", text: "a" }, {
    type: "bulleted_list",
    text: "b",
  }]);
  assertEquals(markdownSpecs("1. a\n  - b\n\t- c"), [{
    type: "numbered_list",
    text: "a",
    children: [{ type: "bulleted_list", text: "b" }, { type: "bulleted_list", text: "c" }],
  }]);
  assertEquals(markdownSpecs("- a\nnot in it\n  - b"), [
    { type: "bulleted_list", text: "a" },
    { type: "paragraph", text: "not in it" },
    { type: "bulleted_list", text: "b" },
  ]);
  assertEquals(markdownSpecs("- a\n  more of a\n\n  under a\n\nafter"), [
    { type: "bulleted_list", text: "a\nmore of a", children: [{ type: "paragraph", text: "under a" }] },
    { type: "paragraph", text: "after" },
  ]);
});

Deno.test("hard breaks, quotes and items carry lines on, and a marker ending a line is an empty block", () => {
  assertEquals(markdownSpecs("one\\\ntwo\\\n\\\nthree"), [{ type: "paragraph", text: "one\ntwo\n\nthree" }]);
  assertEquals(markdownSpecs("one\\\n- two"), [{ type: "paragraph", text: "one\\" }, {
    type: "bulleted_list",
    text: "two",
  }]);
  assertEquals(markdownSpecs("ends in two  \nspaces"), [
    { type: "paragraph", text: "ends in two  " },
    { type: "paragraph", text: "spaces" },
  ]);
  assertEquals(markdownSpecs("> a\n> b\n>\n> c\n\n> d"), [{ type: "quote", text: "a\nb\n\nc" }, {
    type: "quote",
    text: "d",
  }]);
  assertEquals(markdownSpecs("> a\\\nlazy\nnot"), [{ type: "quote", text: "a\nlazy" }, {
    type: "paragraph",
    text: "not",
  }]);
  assertEquals(markdownSpecs("> [!NOTE]\n> a\n> [!TIP]\n> b"), [
    { type: "callout", tone: "info", text: "a" },
    { type: "callout", tone: "tip", text: "b" },
  ]);
  assertEquals(markdownSpecs("-\n- [ ]\n- [x]\n1.\n>\n#"), [
    { type: "bulleted_list", text: "" },
    { type: "todo_list", checked: false, text: "" },
    { type: "todo_list", checked: true, text: "" },
    { type: "numbered_list", text: "" },
    { type: "quote", text: "" },
    { type: "heading", level: 1, text: "" },
  ]);
  assertEquals(markdownSpecs("- - -\n--\n-x"), [{ type: "divider" }, { type: "paragraph", text: "--" }, {
    type: "paragraph",
    text: "-x",
  }]);
});

Deno.test("a <details> is a toggle holding what is up to its </details>", () => {
  assertEquals(markdownSpecs("<details><summary>Hi</summary>\n\nin\n</details>\nout"), [
    { type: "toggle_list", text: "Hi", collapsed: true, children: [{ type: "paragraph", text: "in" }] },
    { type: "paragraph", text: "out" },
  ]);
  assertEquals(markdownSpecs("<DETAILS open>\n\n<summary>two\nlines</summary>\n- a\n\n</details>"), [
    { type: "toggle_list", text: "two\nlines", collapsed: false, children: [{ type: "bulleted_list", text: "a" }] },
  ]);
  assertEquals(markdownSpecs("- item\n\n  <details>\n  <summary>t</summary>\n\n  - x\n\n  </details>\n\n  after"), [{
    type: "bulleted_list",
    text: "item",
    children: [
      { type: "toggle_list", text: "t", collapsed: true, children: [{ type: "bulleted_list", text: "x" }] },
      { type: "paragraph", text: "after" },
    ],
  }]);
  assertEquals(markdownSpecs("</details>"), [{ type: "paragraph", text: "</details>" }]);
});

Deno.test("a fence names its language in any case, and an empty diagram or formula makes no block", () => {
  assertEquals(markdownSpecs("```Mermaid\ngraph TD\n```"), [{ type: "diagram", source: "graph TD" }]);
  assertEquals(markdownSpecs("```JS\nx\n```"), [{ type: "code", text: "x", language: "javascript" }]);
  assertEquals(markdownSpecs("```mermaid\n\n```\n$$\n $$\nx"), [{ type: "paragraph", text: "x" }]);
  assertEquals(markdownSpecs("- a\n\n  ```go\n  x := 1\n    y\n  ```"), [{
    type: "bulleted_list",
    text: "a",
    children: [{ type: "code", text: "x := 1\n  y", language: "go" }],
  }]);
});

Deno.test("a picture line may have a title and a link with parentheses", () => {
  for (
    const [line, url, alt] of [
      ['![cat](https://x.dev/a.png "A cat")', "https://x.dev/a.png", "cat"],
      ["![x](https://x.dev/a_(1).png)", "https://x.dev/a_(1).png", "x"],
      ["![*a* b](<https://x.dev/a(.png>)", "https://x.dev/a(.png", "a b"],
      ["![a\\]b](https://x.dev/a.png 'single')", "https://x.dev/a.png", "a]b"],
    ]
  ) {
    assertEquals(markdownSpecs(line), [{ type: "image", image: { url, alt } }], line);
  }
  assertEquals(markdownSpecs("![a](<https://e.com/a b.png>)"), [{
    type: "paragraph",
    text: "![a](<https://e.com/a b.png>)",
  }]);
});

Deno.test("a <br> in a table cell is a line break, but escaped or in code it is text", () => {
  assertEquals(tableRow("| x<br>y \\<br> `a<br>b` | a<BR/>b | c<br />d |").cells, [
    "x\ny \\<br> `a<br>b`",
    "a\nb",
    "c\nd",
  ]);
});

// What PageMarkdown writes for a page of every kind of block Markdown has
// (internal/doc/lines_test.go's TestPageMarkdownRoundTrips) reads as the
// app reads it.
Deno.test("a page's Markdown from the app reads back as its blocks", () => {
  const md = [
    "# Title",
    "",
    "### ",
    "",
    "one\\",
    "two **bold\\",
    "across**\\",
    "\\",
    "after a blank line\\",
    "\\- not a list\\",
    "ends in \\\\",
    "",
    "- item\\",
    "  second line",
    "  - nested",
    "    - [x] deep",
    "",
    "  a paragraph in it\\",
    "  of two lines",
    "",
    "  > quoted",
    "",
    "  ```go",
    "  if a {",
    "  \treturn",
    "  }",
    "  ```",
    "",
    "  > [!WARNING]",
    "  > careful",
    "",
    "-",
    "1. first",
    "   1. sub",
    "2.",
    "- [ ]",
    "- [x] done",
    "",
    ">",
    "",
    "> a quote\\",
    "> of lines\\",
    "> \\",
    "> and a gap",
    "",
    "> [!NOTE]",
    ">",
    "",
    "<details open>",
    "<summary>open *toggle*",
    "on lines</summary>",
    "",
    "inside",
    "",
    "<details>",
    "<summary>folded</summary>",
    "",
    "- deeper",
    "",
    "</details>",
    "",
    "</details>",
    "",
    "---",
    "",
    "![A cat](https://x.dev/cat.png)",
    "",
    "| a\\|b | two<br>lines |",
    "| --- | --- |",
    "| `x\\|y` | \\<br> stays |",
    "",
    "$$",
    "E = mc^2",
    "$$",
    "",
    "```mermaid",
    "graph TD",
    "  A-->B",
    "```",
    "",
    "\\<details> is text, and <summary>",
  ].join("\n");
  const specs = markdownSpecs(md);
  // write_markdown makes every one of them a block.
  let id = 0;
  for (const spec of specs) specToNode(spec, { newId: () => `b${id++}` });
  assertEquals(specs, [
    { type: "heading", level: 1, text: "Title" },
    { type: "heading", level: 3, text: "" },
    { type: "paragraph", text: "one\ntwo **bold\nacross**\n\nafter a blank line\n\\- not a list\nends in \\\\" },
    {
      type: "bulleted_list",
      text: "item\nsecond line",
      children: [
        { type: "bulleted_list", text: "nested", children: [{ type: "todo_list", checked: true, text: "deep" }] },
        { type: "paragraph", text: "a paragraph in it\nof two lines" },
        { type: "quote", text: "quoted" },
        { type: "code", text: "if a {\n\treturn\n}", language: "go" },
        { type: "callout", tone: "warning", text: "careful" },
      ],
    },
    { type: "bulleted_list", text: "" },
    { type: "numbered_list", text: "first", children: [{ type: "numbered_list", text: "sub" }] },
    { type: "numbered_list", text: "" },
    { type: "todo_list", checked: false, text: "" },
    { type: "todo_list", checked: true, text: "done" },
    { type: "quote", text: "" },
    { type: "quote", text: "a quote\nof lines\n\nand a gap" },
    { type: "callout", tone: "info", text: "" },
    {
      type: "toggle_list",
      text: "open *toggle*\non lines",
      collapsed: false,
      children: [
        { type: "paragraph", text: "inside" },
        { type: "toggle_list", text: "folded", collapsed: true, children: [{ type: "bulleted_list", text: "deeper" }] },
      ],
    },
    { type: "divider" },
    { type: "image", image: { url: "https://x.dev/cat.png", alt: "A cat" } },
    { type: "table", rows: [["a|b", "two\nlines"], ["`x|y`", "\\<br> stays"]] },
    { type: "math_equation", formula: "E = mc^2" },
    { type: "diagram", source: "graph TD\n  A-->B" },
    { type: "paragraph", text: "\\<details> is text, and <summary>" },
  ]);
});

Deno.test("only spaces and tabs indent a fence, as CommonMark has it", () => {
  assertEquals(markdownSpecs("\u00a0```go\nx"), [
    { type: "paragraph", text: "\u00a0```go" },
    { type: "paragraph", text: "x" },
  ]);
  assertEquals(markdownSpecs("\u3000```"), [{ type: "paragraph", text: "\u3000```" }]);
  assertEquals(markdownSpecs("  ```go\nx\n```"), [{ type: "code", text: "x", language: "go" }]);
});
