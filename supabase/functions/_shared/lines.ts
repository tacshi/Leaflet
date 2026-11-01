// Markdown as blocks, for write_markdown, as the app's doc.ParseLines
// reads it: a line each, with Markdown block prefixes. Items indented
// under one nest in it, the lines of a table make a table, a fenced block
// a code block (a Mermaid fence a diagram, lines between $$ a formula),
// a GitHub alert a callout, and a line that is only a picture an image.

import { type BlockSpec, isRule } from "./blocks.ts";

const ALERT_TONES: Record<string, string> = {
  "[!NOTE]": "info",
  "[!IMPORTANT]": "info",
  "[!TIP]": "tip",
  "[!WARNING]": "warning",
  "[!CAUTION]": "danger",
};

/** The tone of a line opening a GitHub alert, as "> [!TIP]". */
function alertTone(line: string): string | null {
  const t = line.trim();
  if (!t.startsWith(">")) return null;
  return ALERT_TONES[t.slice(1).trim().toUpperCase()] ?? null;
}

/** The language of a line opening a code fence, null for another line. */
function codeFence(line: string): string | null {
  const t = line.trim();
  const rest = t.replace(/^`+/, "");
  if (t.length - rest.length < 3 || rest.includes("`")) return null;
  return rest.trim().split(/\s+/)[0] ?? "";
}

/**
 * A line of a table, its cells, or null for the line under the header.
 * As in GitHub's tables, a backslash keeps the character after it in the
 * cell, and \| is a | of the cell's text.
 */
export function tableRow(line: string): string[] | null {
  const s = line.trim().replace(/^\|/, "");
  const cells: string[] = [];
  let cell = "";
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "\\" && i + 1 < s.length) {
      i++;
      if (s[i] !== "|") cell += "\\";
      cell += s[i];
    } else if (s[i] === "|") {
      cells.push(cell);
      cell = "";
    } else {
      cell += s[i];
    }
  }
  // A closing | ends the last cell; without one, what is after the last
  // | is a cell.
  if (cell !== "" || cells.length === 0) cells.push(cell);
  const trimmed = cells.map((c) => c.trim());
  const rule = trimmed.every((c) => c !== "" && c.replace(/[-: ]/g, "") === "");
  return rule ? null : trimmed;
}

const IMAGE_LINE = /^!\[([^\]]*)\]\((https?:\/\/[^\s)]+)\)$/;

/** Reads Markdown as block specs, as pasting it in the app does. */
export function markdownSpecs(markdown: string): BlockSpec[] {
  const text = markdown.replace(/\r\n/g, "\n").replace(/^\n+|\n+$/g, "");
  const out: BlockSpec[] = [];
  // The list items open at each depth, for nesting.
  let lists: BlockSpec[] = [];
  let table: string[][] = [];
  const flushTable = () => {
    if (table.length) out.push({ type: "table", rows: table });
    table = [];
  };
  // The fenced block open: its fence, its language, its indent and lines.
  let code: { fence: string; language: string; indent: string; lines: string[] } | null = null;
  const closeCode = () => {
    const body = code!.lines.join("\n");
    if (code!.fence === "$$") out.push({ type: "math_equation", formula: body });
    else if (code!.language === "mermaid") out.push({ type: "diagram", source: body });
    else out.push({ type: "code", text: body, language: code!.language });
    code = null;
  };
  // The alert being read: its lines go in it.
  let callout: BlockSpec | null = null;
  for (const line of text ? text.split("\n") : []) {
    const trimmed = line.replace(/^[ \t]+/, "");
    if (code) {
      const t = trimmed.trim();
      if (code.fence === "$$" ? t === "$$" : t.startsWith(code.fence) && t.replace(/[` ]/g, "") === "") {
        closeCode();
      } else {
        code.lines.push(line.startsWith(code.indent) ? line.slice(code.indent.length) : line);
      }
      continue;
    }
    const tone = alertTone(trimmed);
    if (tone) {
      flushTable();
      lists = [];
      callout = { type: "callout", tone, text: "" };
      out.push(callout);
      continue;
    }
    if (callout && trimmed.startsWith(">")) {
      const rest = trimmed.slice(1).replace(/^ /, "");
      callout.text = callout.text ? `${callout.text}\n${rest}` : rest;
      continue;
    }
    callout = null;
    if (line.trim() === "") {
      // A blank line only separates blocks; a list goes on across it.
      flushTable();
      continue;
    }
    const language = codeFence(trimmed);
    if (language !== null || trimmed.trim() === "$$") {
      flushTable();
      lists = [];
      const fence = language !== null ? trimmed.match(/^`+/)![0] : "$$";
      code = { fence, language: language ?? "", indent: line.slice(0, line.length - trimmed.length), lines: [] };
      continue;
    }
    if (trimmed.startsWith("|")) {
      lists = [];
      const row = tableRow(trimmed);
      if (row) table.push(row);
      continue;
    }
    flushTable();
    const lead = line.slice(0, line.length - trimmed.length);
    const indent = (lead.match(/ /g)?.length ?? 0) + 2 * (lead.match(/\t/g)?.length ?? 0);
    let spec: BlockSpec;
    const heading = trimmed.match(/^(#{1,6}) (.*)$/);
    const bullet = /^[-*+] /.test(trimmed);
    const numbered = trimmed.match(/^\d{1,3}[.)] (.*)$/);
    if (heading) {
      spec = { type: "heading", level: heading[1].length, text: heading[2] };
    } else if (bullet && /^. \[[ xX]\] /.test(trimmed)) {
      spec = { type: "todo_list", checked: trimmed[3] !== " ", text: trimmed.slice(6) };
    } else if (bullet) {
      spec = { type: "bulleted_list", text: trimmed.slice(2) };
    } else if (trimmed.startsWith("> ")) {
      spec = { type: "quote", text: trimmed.slice(2) };
    } else if (isRule(trimmed)) {
      lists = [];
      out.push({ type: "divider" });
      continue;
    } else if (IMAGE_LINE.test(trimmed)) {
      const [, alt, url] = trimmed.match(IMAGE_LINE)!;
      lists = [];
      out.push({ type: "image", image: { url, alt } });
      continue;
    } else if (numbered) {
      spec = { type: "numbered_list", text: numbered[1] };
    } else {
      spec = { type: "paragraph", text: trimmed };
    }
    if (!["bulleted_list", "numbered_list", "todo_list"].includes(spec.type)) {
      lists = [];
      out.push(spec);
      continue;
    }
    // Two spaces, or a tab, a level: under the item open one level up.
    const depth = Math.min(Math.floor(indent / 2), lists.length);
    lists = [...lists.slice(0, depth), spec];
    if (depth === 0) {
      out.push(spec);
    } else {
      const parent = lists[depth - 1];
      (parent.children ??= []).push(spec);
    }
  }
  flushTable();
  // A fence left open runs to the end.
  if (code) closeCode();
  return out;
}
