import { assertEquals } from "jsr:@std/assert@1";
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
  assertEquals(tableRow("| a\\|b | c |"), ["a|b", "c"]);
  assertEquals(tableRow("| --- | :---: |"), null);
});
