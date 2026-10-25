// Page content helpers (protocol §1–§2): node JSON with stable IDs, inline
// markdown ⇄ delta, BlockSpec → node JSON, and BlockView for read_page.
// Mirrors internal/agents/ and internal/doc/ on the client.

export type Attrs = Record<string, unknown>;

export interface DeltaOp {
  insert: string;
  attributes?: Attrs;
}

export interface NodeJSON {
  id: string;
  type: string;
  data?: Record<string, unknown>;
  children?: NodeJSON[];
}

export interface PageContent {
  document: NodeJSON;
  meta: {
    v: number;
    blocks: Record<string, number>;
    deleted: Record<string, number>;
  };
}

export interface ChartSpec {
  chart_type: "bar" | "line" | "pie";
  title?: string;
  labels: string[];
  series: { name: string; values: number[] }[];
  caption?: string;
}

export interface EmbedSpec {
  title?: string;
  html: string;
  height?: number;
}

export interface BlockSpec {
  type: string;
  text?: string;
  level?: number;
  checked?: boolean;
  children?: BlockSpec[];
  rows?: string[][];
  chart?: ChartSpec;
  embed?: EmbedSpec;
  image?: { url: string; alt?: string };
}

export type Position =
  | { after: string }
  | { before: string }
  | { parent: string }
  | { at: "start" | "end" };

export interface BlockView {
  id: string;
  type: string;
  text: string;
  depth: number;
  parent_id?: string;
  level?: number;
  checked?: boolean;
  rows?: string[][];
  chart?: Record<string, unknown>;
  embed?: { title: string; height: number; html_length: number };
  recording?: Record<string, unknown>;
}

export const TEXT_TYPES = new Set([
  "paragraph",
  "heading",
  "bulleted_list",
  "numbered_list",
  "todo_list",
  "quote",
]);

/** Block types that may hold nested blocks. */
export const LIST_TYPES = new Set(["bulleted_list", "numbered_list", "todo_list"]);

/** Block types an agent may create. */
export const SPEC_TYPES = new Set([
  ...TEXT_TYPES,
  "divider",
  "image",
  "table",
  "chart",
  "embed",
]);

// ---------------------------------------------------------------------------
// IDs and documents
// ---------------------------------------------------------------------------

const ID_ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

export function randomId(length = 6): string {
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  let id = "";
  for (const b of bytes) id += ID_ALPHABET[b % ID_ALPHABET.length];
  return id;
}

export function emptyContent(): PageContent {
  return {
    document: { id: "root", type: "page", children: [] },
    meta: { v: 1, blocks: {}, deleted: {} },
  };
}

/** Accepts whatever is stored and returns a well-formed content object. */
export function normalizeContent(raw: unknown): PageContent {
  const content = emptyContent();
  if (!raw || typeof raw !== "object") return content;
  const r = raw as Partial<PageContent> & { document?: NodeJSON };
  if (r.document && typeof r.document === "object") {
    content.document = r.document;
  }
  content.document.id ||= "root";
  content.document.type ||= "page";
  content.document.children ??= [];
  if (r.meta && typeof r.meta === "object") {
    content.meta = {
      v: r.meta.v ?? 1,
      blocks: { ...(r.meta.blocks ?? {}) },
      deleted: { ...(r.meta.deleted ?? {}) },
    };
  }
  const seen = new Set<string>();
  walk(content.document, (node) => {
    if (!node.id || seen.has(node.id)) node.id = randomId();
    seen.add(node.id);
  });
  return content;
}

export function walk(
  node: NodeJSON,
  visit: (node: NodeJSON, parent: NodeJSON | null, depth: number) => void,
  parent: NodeJSON | null = null,
  depth = -1,
): void {
  visit(node, parent, depth);
  for (const child of node.children ?? []) walk(child, visit, node, depth + 1);
}

export function collectIds(node: NodeJSON, into: string[] = []): string[] {
  walk(node, (n) => into.push(n.id));
  return into;
}

export function clone<T>(value: T): T {
  return structuredClone(value);
}

export function deltaOf(node: NodeJSON): DeltaOp[] {
  const delta = node.data?.delta;
  return Array.isArray(delta) ? (delta as DeltaOp[]) : [];
}

// ---------------------------------------------------------------------------
// Inline markdown ⇄ delta
// ---------------------------------------------------------------------------

function sameAttrs(a?: Attrs, b?: Attrs): boolean {
  return JSON.stringify(a ?? {}) === JSON.stringify(b ?? {});
}

function pushText(out: DeltaOp[], text: string, attrs: Attrs): void {
  if (!text) return;
  const attributes = Object.keys(attrs).length ? { ...attrs } : undefined;
  const last = out[out.length - 1];
  if (last && sameAttrs(last.attributes, attributes)) {
    last.insert += text;
    return;
  }
  out.push(attributes ? { insert: text, attributes } : { insert: text });
}

function isWordChar(ch: string | undefined): boolean {
  return !!ch && /[\p{L}\p{N}]/u.test(ch);
}

function findClosing(src: string, marker: string, from: number): number {
  let i = from;
  while (i < src.length) {
    if (src[i] === "\\") {
      i += 2;
      continue;
    }
    if (src[i] === "`") {
      const end = src.indexOf("`", i + 1);
      if (end > i) {
        i = end + 1;
        continue;
      }
    }
    if (src.startsWith(marker, i)) {
      // A single * or _ must not be half of a ** or __.
      if (marker.length === 1 && src[i + 1] === marker) {
        i += 2;
        continue;
      }
      return i;
    }
    i++;
  }
  return -1;
}

const LINK = /^\[((?:\\.|[^\]\\])*)\]\(([^)\s]+)\)/;

function parseMention(href: string): Attrs | null {
  const m = href.match(/^mention:(user|agent):(.+)$/);
  return m ? { kind: m[1], id: m[2] } : null;
}

// Parses **bold**, *italic* or _italic_, `code`, ~~strike~~ and [text](url).
export function markdownToDelta(src: string, attrs: Attrs = {}): DeltaOp[] {
  const out: DeltaOp[] = [];
  let buf = "";
  const flush = () => {
    pushText(out, buf, attrs);
    buf = "";
  };
  const nest = (inner: string, extra: Attrs) => {
    flush();
    for (const op of markdownToDelta(inner, { ...attrs, ...extra })) {
      pushText(out, op.insert, op.attributes ?? {});
    }
  };

  let i = 0;
  while (i < src.length) {
    const ch = src[i];
    if (ch === "\\" && i + 1 < src.length && /[\\`*_~\[\]()@]/.test(src[i + 1])) {
      buf += src[i + 1];
      i += 2;
      continue;
    }
    if (ch === "`") {
      const end = src.indexOf("`", i + 1);
      if (end > i + 1) {
        flush();
        pushText(out, src.slice(i + 1, end), { ...attrs, code: true });
        i = end + 1;
        continue;
      }
    }
    if (src.startsWith("**", i) || src.startsWith("__", i)) {
      const marker = src.slice(i, i + 2);
      const end = findClosing(src, marker, i + 2);
      if (end > i + 2) {
        nest(src.slice(i + 2, end), { bold: true });
        i = end + 2;
        continue;
      }
    }
    if (src.startsWith("~~", i)) {
      const end = findClosing(src, "~~", i + 2);
      if (end > i + 2) {
        nest(src.slice(i + 2, end), { strikethrough: true });
        i = end + 2;
        continue;
      }
    }
    if (ch === "*" || (ch === "_" && !isWordChar(src[i - 1]))) {
      const end = findClosing(src, ch, i + 1);
      if (
        end > i + 1 && src[i + 1] !== " " &&
        (ch === "*" || !isWordChar(src[end + 1]))
      ) {
        nest(src.slice(i + 1, end), { italic: true });
        i = end + 1;
        continue;
      }
    }
    if (ch === "[") {
      const m = src.slice(i).match(LINK);
      if (m) {
        const mention = parseMention(m[2]);
        if (mention) {
          const name = m[1].replace(/\\(.)/g, "$1").replace(/^@/, "");
          flush();
          pushText(out, `@${name}`, { ...attrs, mention: { ...mention, name } });
        } else {
          nest(m[1], { href: m[2] });
        }
        i += m[0].length;
        continue;
      }
    }
    buf += ch;
    i++;
  }
  flush();
  return out;
}

function escapeMarkdown(text: string): string {
  return text
    .replace(/([\\`*~\[\]])/g, "\\$1")
    .replace(/(^|[^\p{L}\p{N}])_/gu, "$1\\_")
    .replace(/_(?=[^\p{L}\p{N}]|$)/gu, "\\_");
}

/** The inverse of markdownToDelta, for read_page. */
export function deltaToMarkdown(delta: DeltaOp[]): string {
  let out = "";
  for (const op of delta) {
    if (typeof op.insert !== "string") continue;
    const a = op.attributes ?? {};
    const mention = a.mention as { kind?: string; id?: string; name?: string } | undefined;
    if (mention?.id) {
      const name = (mention.name ?? op.insert.replace(/^@/, "")).replace(/([\]\\])/g, "\\$1");
      out += `[@${name}](mention:${mention.kind === "agent" ? "agent" : "user"}:${mention.id})`;
      continue;
    }
    const lead = op.insert.match(/^\s*/)![0];
    const trail = op.insert.slice(lead.length).match(/\s*$/)![0];
    const core = op.insert.slice(lead.length, op.insert.length - trail.length);
    if (!core) {
      out += op.insert;
      continue;
    }
    let text = a.code ? "`" + core.replace(/`/g, "'") + "`" : escapeMarkdown(core);
    if (a.strikethrough) text = `~~${text}~~`;
    if (a.italic) text = `*${text}*`;
    if (a.bold) text = `**${text}**`;
    if (typeof a.href === "string" && a.href) text = `[${text}](${a.href})`;
    out += lead + text + trail;
  }
  return out;
}

export function deltaToPlain(delta: DeltaOp[]): string {
  return delta.map((op) => (typeof op.insert === "string" ? op.insert : "")).join("");
}

// ---------------------------------------------------------------------------
// BlockSpec → node JSON
// ---------------------------------------------------------------------------

export class SpecError extends Error {}

export type IdFactory = () => string;

function textNode(id: string, type: string, text: string, extra: Record<string, unknown> = {}): NodeJSON {
  return { id, type, data: { ...extra, delta: markdownToDelta(text ?? "") } };
}

function clampInt(value: unknown, min: number, max: number, fallback: number): number {
  const n = typeof value === "number" ? Math.round(value) : Number.NaN;
  return Number.isFinite(n) ? Math.min(max, Math.max(min, n)) : fallback;
}

function validateChart(chart: unknown): ChartSpec {
  if (!chart || typeof chart !== "object") {
    throw new SpecError("A chart block needs a `chart` object");
  }
  const c = chart as Partial<ChartSpec>;
  const chartType = c.chart_type ?? "bar";
  if (!["bar", "line", "pie"].includes(chartType)) {
    throw new SpecError("chart.chart_type must be bar, line or pie");
  }
  if (!Array.isArray(c.labels) || c.labels.length === 0) {
    throw new SpecError("chart.labels must be a non-empty array of strings");
  }
  if (!Array.isArray(c.series) || c.series.length === 0) {
    throw new SpecError("chart.series must be a non-empty array");
  }
  const labels = c.labels.map((l) => String(l));
  const series = c.series.map((s, i) => {
    const values = Array.isArray(s?.values) ? s.values.map(Number) : [];
    if (values.length !== labels.length || values.some((v) => !Number.isFinite(v))) {
      throw new SpecError(
        `chart.series[${i}].values must hold ${labels.length} numbers, one per label`,
      );
    }
    return { name: String(s?.name ?? `Series ${i + 1}`), values };
  });
  if (chartType === "pie" && series.length > 1) {
    throw new SpecError("A pie chart takes exactly one series");
  }
  return {
    chart_type: chartType,
    title: c.title ? String(c.title) : "",
    labels,
    series,
    caption: c.caption ? String(c.caption) : "",
  };
}

export interface SpecOptions {
  newId: IdFactory;
  /** Shown on chart blocks ("Inserted by …"). */
  author?: string;
}

/** Whether a line is a Markdown horizontal rule: three or more of -, * or _. */
export function isRule(line: string): boolean {
  return /^([-*_])(\s*\1){2,}$/.test(line.trim());
}

export function specToNode(spec: BlockSpec, opts: SpecOptions, id = opts.newId()): NodeJSON {
  if (!spec || typeof spec !== "object") throw new SpecError("Each block must be an object");
  let type = String(spec.type ?? "paragraph");
  // A Markdown rule written as text is the divider it stands for.
  if (type === "paragraph" && isRule(spec.text ?? "")) type = "divider";
  if (!SPEC_TYPES.has(type)) {
    throw new SpecError(
      `Unknown block type "${type}". Use one of: ${[...SPEC_TYPES].join(", ")}`,
    );
  }
  if (spec.children?.length && !LIST_TYPES.has(type)) {
    throw new SpecError(`Only list blocks take children, not "${type}"`);
  }

  let node: NodeJSON;
  switch (type) {
    case "heading":
      node = textNode(id, type, spec.text ?? "", { level: clampInt(spec.level, 1, 6, 2) });
      break;
    case "todo_list":
      node = textNode(id, type, spec.text ?? "", { checked: spec.checked === true });
      break;
    case "paragraph":
    case "bulleted_list":
    case "numbered_list":
    case "quote":
      node = textNode(id, type, spec.text ?? "");
      break;
    case "divider":
      node = { id, type };
      break;
    case "image": {
      const url = spec.image?.url ?? "";
      if (!/^https?:\/\//i.test(url)) throw new SpecError("image.url must be an http(s) URL");
      node = { id, type, data: { url, align: "center" } };
      break;
    }
    case "table":
      node = tableNode(id, spec.rows, opts.newId);
      break;
    case "chart":
      node = {
        id,
        type,
        data: { ...validateChart(spec.chart), author: opts.author ?? "" },
      };
      break;
    case "embed": {
      const html = spec.embed?.html;
      if (typeof html !== "string" || !html.trim()) {
        throw new SpecError("An embed block needs `embed.html`");
      }
      if (html.length > 200_000) throw new SpecError("embed.html must be under 200 KB");
      node = {
        id,
        type,
        data: {
          title: String(spec.embed?.title ?? ""),
          html,
          height: clampInt(spec.embed?.height, 120, 1200, 360),
        },
      };
      break;
    }
    default:
      throw new SpecError(`Unsupported block type "${type}"`);
  }

  if (spec.children?.length) {
    node.children = spec.children.map((child) => specToNode(child, opts));
  }
  return node;
}

/** appflowy_editor's TableNode: col-major `table/cell` children. */
export function tableNode(id: string, rows: unknown, newId: IdFactory): NodeJSON {
  if (!Array.isArray(rows) || rows.length === 0 || !rows.every(Array.isArray)) {
    throw new SpecError("A table block needs `rows`: an array of rows, each an array of cell strings");
  }
  const rowsLen = rows.length;
  const colsLen = Math.max(...rows.map((r) => (r as unknown[]).length));
  if (colsLen === 0) throw new SpecError("Table rows must have at least one cell");
  if (rowsLen * colsLen > 400) throw new SpecError("Tables are limited to 400 cells");
  const children: NodeJSON[] = [];
  for (let col = 0; col < colsLen; col++) {
    for (let row = 0; row < rowsLen; row++) {
      const value = (rows[row] as unknown[])[col];
      children.push({
        id: newId(),
        type: "table/cell",
        data: { colPosition: col, rowPosition: row },
        children: [textNode(newId(), "paragraph", value == null ? "" : String(value))],
      });
    }
  }
  return {
    id,
    type: "table",
    data: {
      colsLen,
      rowsLen,
      colDefaultWidth: 160,
      rowDefaultHeight: 40,
      colMinimumWidth: 40,
    },
    children,
  };
}

export function tableRows(node: NodeJSON): string[][] {
  const colsLen = Number(node.data?.colsLen ?? 0);
  const rowsLen = Number(node.data?.rowsLen ?? 0);
  const rows: string[][] = Array.from({ length: rowsLen }, () => Array(colsLen).fill(""));
  for (const cell of node.children ?? []) {
    const col = Number(cell.data?.colPosition);
    const row = Number(cell.data?.rowPosition);
    if (row >= 0 && row < rowsLen && col >= 0 && col < colsLen) {
      rows[row][col] = (cell.children ?? []).map((p) => deltaToMarkdown(deltaOf(p))).join(" ");
    }
  }
  return rows;
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

export interface RecordingInfo {
  id: string;
  title?: string;
  duration_ms?: number;
  status?: string;
  transcript?: { start_ms?: number; text?: string; speaker?: string }[];
}

function clock(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
}

export function nodeView(
  node: NodeJSON,
  depth: number,
  parentId: string | undefined,
  recordings: Map<string, RecordingInfo>,
): BlockView {
  const view: BlockView = { id: node.id, type: node.type, text: "", depth };
  if (parentId) view.parent_id = parentId;
  const data = node.data ?? {};
  if (TEXT_TYPES.has(node.type)) view.text = deltaToMarkdown(deltaOf(node));
  switch (node.type) {
    case "heading":
      view.level = Number(data.level ?? 1);
      break;
    case "todo_list":
      view.checked = data.checked === true;
      break;
    case "table":
      view.rows = tableRows(node);
      break;
    case "chart":
      view.text = String(data.title ?? "");
      view.chart = {
        chart_type: data.chart_type,
        title: data.title,
        labels: data.labels,
        series: data.series,
      };
      break;
    case "embed":
      view.text = String(data.title ?? "");
      view.embed = {
        title: String(data.title ?? ""),
        height: Number(data.height ?? 360),
        html_length: String(data.html ?? "").length,
      };
      break;
    case "image":
      view.text = String(data.url ?? "");
      break;
    case "recording": {
      const rid = String(data.recording_id ?? "");
      const info = recordings.get(rid);
      view.text = String(data.title ?? info?.title ?? "Recording");
      view.recording = {
        recording_id: rid,
        duration_ms: data.duration_ms ?? info?.duration_ms,
        status: info?.status,
        transcript: (info?.transcript ?? [])
          .map((s) => `${clock(s.start_ms ?? 0)}${s.speaker ? ` ${s.speaker}:` : ""} ${s.text ?? ""}`.trim())
          .join("\n"),
      };
      break;
    }
    case "subpage":
      view.text = String(data.title ?? data.page_id ?? "");
      break;
  }
  return view;
}

/** Every addressable block, in reading order. Table cells are folded into their table. */
export function pageViews(
  content: PageContent,
  recordings: Map<string, RecordingInfo> = new Map(),
): BlockView[] {
  const views: BlockView[] = [];
  const visit = (nodes: NodeJSON[], depth: number, parentId?: string) => {
    for (const node of nodes) {
      views.push(nodeView(node, depth, parentId, recordings));
      if (node.type !== "table" && node.children?.length) {
        visit(node.children, depth + 1, node.id);
      }
    }
  };
  visit(content.document.children ?? [], 0);
  return views;
}

export function plainText(content: PageContent): string {
  const lines: string[] = [];
  walk(content.document, (node) => {
    if (TEXT_TYPES.has(node.type)) {
      const text = deltaToPlain(deltaOf(node));
      if (text) lines.push(text);
    } else if (node.type === "chart" || node.type === "embed") {
      const title = String(node.data?.title ?? "");
      if (title) lines.push(title);
    }
  });
  return lines.join("\n");
}
