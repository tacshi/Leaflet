// Agent page-edit tools (protocol §2) applied to an in-memory page, producing
// the ops of protocol §3. Mirrors internal/agents/tools.go on the client.

import {
  type BlockSpec,
  clone,
  collectIds,
  emptyContent,
  LIST_TYPES,
  type NodeJSON,
  type PageContent,
  pageViews,
  type Position,
  randomId,
  SPEC_TYPES,
  SpecError,
  specToNode,
  walk,
} from "./blocks.ts";
import type { ToolDef } from "./openai.ts";

export type Op =
  | { op: "insert"; position: Position; nodes: NodeJSON[] }
  | { op: "replace"; id: string; node: NodeJSON; previous: NodeJSON }
  | { op: "move"; id: string; position: Position; from: Position }
  | { op: "delete"; ids: string[]; previous: { node: NodeJSON; position: Position }[] }
  | {
    op: "create_page";
    page: { id: string; space_id: string; parent_id: string; title: string; sort_key: string };
    content: PageContent;
  };

export interface PageSummary {
  id: string;
  title: string;
  parent_id: string | null;
}

export interface ToolHooks {
  /** read_page for the current page, given its current content. */
  readPage(content: PageContent): Promise<unknown>;
  createSubpage(title: string, content: PageContent): Promise<Extract<Op, { op: "create_page" }>["page"]>;
  listPages(): Promise<PageSummary[]>;
  readOtherPage(pageId: string): Promise<unknown>;
}

export interface ToolOutcome {
  result: unknown;
  ops: Op[];
  summary?: string;
  blockIds: string[];
}

export class ToolError extends Error {}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

const positionSchema = {
  type: "object",
  description:
    "Where the blocks go. Exactly one key: after/before a block id, parent (a list block id; appends as its last child), or at start/end of the page.",
  properties: {
    after: { type: "string", description: "Block id to insert after" },
    before: { type: "string", description: "Block id to insert before" },
    parent: { type: "string", description: "Id of a list block; the blocks become its last children" },
    at: { type: "string", enum: ["start", "end"] },
  },
};

const blockSpecSchema = {
  type: "object",
  properties: {
    type: { type: "string", enum: [...SPEC_TYPES] },
    text: {
      type: "string",
      description:
        "Text with inline markdown: **bold**, *italic*, `code`, ~~strike~~, [link](https://…). For text block types. For code, the code itself, verbatim, with its line breaks and no fence.",
    },
    language: { type: "string", description: "For code: the language, such as go, python or typescript" },
    tone: { type: "string", enum: ["info", "tip", "warning", "danger"], description: "For callout: its tone" },
    collapsed: { type: "boolean", description: "For toggle_list: whether it starts folded, its children hidden" },
    formula: { type: "string", description: "For math_equation: the formula in TeX, without $ delimiters" },
    source: {
      type: "string",
      description: "For diagram: Mermaid source (flowchart, sequenceDiagram, classDiagram, gantt, pie, mindmap, …)",
    },
    video: {
      type: "object",
      description: "For video: a YouTube or Vimeo video, or a video file, that plays in the page",
      properties: { url: { type: "string" }, title: { type: "string" } },
      required: ["url"],
    },
    link: {
      type: "object",
      description: "For link_preview: a card for a web page, with its title and picture",
      properties: { url: { type: "string" } },
      required: ["url"],
    },
    level: { type: "integer", minimum: 1, maximum: 6, description: "Heading level" },
    checked: { type: "boolean", description: "For todo_list" },
    due: { type: "string", description: "For todo_list: the day it is due, YYYY-MM-DD" },
    children: {
      type: "array",
      description: "Nested blocks (BlockSpec objects). Only list blocks take children; a toggle_list takes any.",
      items: { type: "object" },
    },
    rows: {
      type: "array",
      description: "For table: rows of cell text; the first row is the header.",
      items: { type: "array", items: { type: "string" } },
    },
    chart: {
      type: "object",
      description: "For chart: rendered natively. Each series has one value per label.",
      properties: {
        chart_type: { type: "string", enum: ["bar", "line", "pie"] },
        title: { type: "string" },
        labels: { type: "array", items: { type: "string" } },
        series: {
          type: "array",
          items: {
            type: "object",
            properties: {
              name: { type: "string" },
              values: { type: "array", items: { type: "number" } },
            },
            required: ["name", "values"],
          },
        },
        caption: { type: "string" },
      },
      required: ["chart_type", "labels", "series"],
    },
    embed: {
      type: "object",
      description:
        "For embed: a self-contained interactive HTML document (inline CSS/JS, no external requests), shown sandboxed.",
      properties: {
        title: { type: "string" },
        html: { type: "string" },
        height: { type: "integer", minimum: 120, maximum: 1200 },
      },
      required: ["html"],
    },
    image: {
      type: "object",
      properties: { url: { type: "string" }, alt: { type: "string" } },
      required: ["url"],
    },
  },
  required: ["type"],
};

function tool(
  name: string,
  description: string,
  properties: Record<string, unknown> = {},
  required: string[] = [],
): ToolDef {
  return {
    type: "function",
    function: {
      name,
      description,
      parameters: { type: "object", properties, required },
    },
  };
}

export function toolDefinitions(opts: { canEdit: boolean; crossPage: boolean }): ToolDef[] {
  const defs: ToolDef[] = [
    tool(
      "read_page",
      "Read the whole current page: every block with its id, its comments, and its subpages.",
    ),
  ];
  if (opts.canEdit) {
    defs.push(
      tool(
        "insert_blocks",
        "Insert new blocks at a position. Returns the ids of the inserted blocks.",
        {
          position: positionSchema,
          blocks: { type: "array", items: blockSpecSchema, minItems: 1 },
        },
        ["position", "blocks"],
      ),
      tool(
        "replace_block",
        "Replace one block's content and type. The block keeps its id, so comments stay attached.",
        { id: { type: "string" }, block: blockSpecSchema },
        ["id", "block"],
      ),
      tool(
        "move_block",
        "Move a block (with its nested blocks) to a new position.",
        { id: { type: "string" }, position: positionSchema },
        ["id", "position"],
      ),
      tool(
        "delete_blocks",
        "Delete blocks (and their nested blocks).",
        { ids: { type: "array", items: { type: "string" }, minItems: 1 } },
        ["ids"],
      ),
      tool(
        "create_subpage",
        "Create a new subpage under the current page, optionally with content.",
        {
          title: { type: "string" },
          blocks: { type: "array", items: blockSpecSchema },
        },
        ["title"],
      ),
    );
  }
  if (opts.crossPage) {
    defs.push(
      tool("list_pages", "List the user's other pages (id, title, parent)."),
      tool(
        "read_other_page",
        "Read another page by id. Read-only.",
        { page_id: { type: "string" } },
        ["page_id"],
      ),
    );
  }
  return defs;
}

// ---------------------------------------------------------------------------
// Edit session
// ---------------------------------------------------------------------------

interface Located {
  node: NodeJSON;
  parent: NodeJSON;
  index: number;
}

/** Addressable blocks: everything except the cells inside tables. */
export function indexBlocks(root: NodeJSON): Map<string, Located> {
  const map = new Map<string, Located>();
  const visit = (parent: NodeJSON) => {
    (parent.children ?? []).forEach((node, index) => {
      map.set(node.id, { node, parent, index });
      if (node.type !== "table") visit(node);
    });
  };
  visit(root);
  return map;
}

function anchorOf(position: Position): string | null {
  if ("after" in position) return position.after;
  if ("before" in position) return position.before;
  if ("parent" in position) return position.parent;
  return null;
}

/** Like the session's resolve, but returns null instead of throwing. */
function resolveLenient(
  root: NodeJSON,
  position: Position,
  idx: Map<string, Located>,
): { parent: NodeJSON; index: number } | null {
  root.children ??= [];
  if ("at" in position) return { parent: root, index: position.at === "start" ? 0 : root.children.length };
  const at = idx.get(anchorOf(position)!);
  if (!at) return null;
  if ("after" in position) return { parent: at.parent, index: at.index + 1 };
  if ("before" in position) return { parent: at.parent, index: at.index };
  if (!LIST_TYPES.has(at.node.type)) return null;
  at.node.children ??= [];
  return { parent: at.node, index: at.node.children.length };
}

/**
 * A replace that left the block's children as they were: its node has the
 * children its previous had (replace_block without children). Children
 * given anew get new ids, so they never match.
 */
function keptChildren(op: Extract<Op, { op: "replace" }>): boolean {
  const ids = (n: NodeJSON) => (n.children ?? []).map((c) => c.id).join("\n");
  return op.previous.type !== "table" && ids(op.node) === ids(op.previous);
}

/** Whether node may take the children current has (replace's own check). */
function canHoldChildren(node: NodeJSON, current: NodeJSON): boolean {
  return current.type !== "table" && (LIST_TYPES.has(node.type) || node.type === current.type);
}

/** JSON with each object's keys in order, as jsonb does not keep them. */
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value && typeof value === "object") {
    const entries = Object.entries(value).filter(([, v]) => v !== undefined).sort(([a], [b]) => (a < b ? -1 : 1));
    return `{${entries.map(([k, v]) => `${JSON.stringify(k)}:${canonical(v)}`).join(",")}}`;
  }
  return JSON.stringify(value ?? null);
}

/**
 * Whether replaying ops on content (applyOps) takes away a change made
 * since the agent read the page: a block that a replace or a delete finds
 * other than the agent read it. A replace that keeps the block's children
 * looks at the block alone, as it keeps the children found.
 */
export function overwrites(content: PageContent, ops: Op[]): boolean {
  const idx = indexBlocks(content.document);
  const seen = new Set<string>();
  // The first op on a block has it as the agent read it.
  const changed = (read: NodeJSON, own: boolean) => {
    if (seen.has(read.id)) return false;
    seen.add(read.id);
    const at = idx.get(read.id);
    if (!at) return false;
    const strip = (n: NodeJSON) => ({ ...n, children: undefined });
    return own ? canonical(strip(at.node)) !== canonical(strip(read)) : canonical(at.node) !== canonical(read);
  };
  let found = false;
  for (const op of ops) {
    if (op.op === "insert") op.nodes.forEach((n) => walk(n, (x) => seen.add(x.id)));
    if (op.op === "replace" && changed(op.previous, keptChildren(op))) found = true;
    if (op.op === "delete" && op.previous.some((p) => changed(p.node, false))) found = true;
  }
  return found;
}

/**
 * Replays ops on a (possibly newer) version of a page. Ops whose blocks are
 * gone are skipped; inserts whose anchor is gone land at the end.
 */
export function applyOps(content: PageContent, ops: Op[], now = Date.now()): PageContent {
  const out = clone(content);
  const root = out.document;
  root.children ??= [];
  const touch = (node: NodeJSON) =>
    walk(node, (n) => {
      out.meta.blocks[n.id] = now;
      delete out.meta.deleted[n.id];
    });
  for (const op of ops) {
    const idx = indexBlocks(root);
    switch (op.op) {
      case "insert": {
        const existing = new Set(collectIds(root));
        const nodes = op.nodes.filter((n) => !existing.has(n.id)).map((n) => clone(n));
        const target = resolveLenient(root, op.position, idx) ?? { parent: root, index: root.children.length };
        target.parent.children ??= [];
        target.parent.children.splice(target.index, 0, ...nodes);
        nodes.forEach(touch);
        break;
      }
      case "replace": {
        const at = idx.get(op.id);
        if (!at) break;
        const node = clone(op.node);
        // A replace that kept the block's children carries them as the
        // agent read them: keep the ones the block has here instead, so
        // a child edited or added since is not undone.
        if (keptChildren(op) && canHoldChildren(node, at.node)) {
          if (at.node.children?.length) node.children = at.node.children;
          else delete node.children;
          at.parent.children![at.index] = node;
          out.meta.blocks[node.id] = now;
          delete out.meta.deleted[node.id];
          break;
        }
        at.parent.children![at.index] = node;
        touch(node);
        break;
      }
      case "move": {
        const at = idx.get(op.id);
        const anchor = anchorOf(op.position);
        if (!at || (anchor && collectIds(at.node).includes(anchor))) break;
        at.parent.children!.splice(at.index, 1);
        const target = resolveLenient(root, op.position, indexBlocks(root));
        if (!target) {
          at.parent.children!.splice(at.index, 0, at.node);
          break;
        }
        target.parent.children ??= [];
        target.parent.children.splice(target.index, 0, at.node);
        out.meta.blocks[op.id] = now;
        break;
      }
      case "delete": {
        for (const id of op.ids) {
          const at = indexBlocks(root).get(id);
          if (!at) continue;
          at.parent.children!.splice(at.index, 1);
          walk(at.node, (n) => {
            delete out.meta.blocks[n.id];
            out.meta.deleted[n.id] = now;
          });
        }
        break;
      }
      case "create_page":
        break;
    }
  }
  return out;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

export function normalizePosition(raw: unknown): Position {
  if (raw === "start" || raw === "end") return { at: raw };
  if (!isObject(raw)) throw new ToolError('position must be an object such as {"after": "<block id>"}');
  const keys = ["after", "before", "parent", "at"].filter((k) => raw[k] !== undefined && raw[k] !== null);
  if (keys.length !== 1) {
    throw new ToolError("position needs exactly one of: after, before, parent, at");
  }
  const key = keys[0];
  const value = raw[key];
  if (key === "at") {
    if (value !== "start" && value !== "end") throw new ToolError('position.at must be "start" or "end"');
    return { at: value };
  }
  if (typeof value !== "string" || !value) throw new ToolError(`position.${key} must be a block id`);
  return { [key]: value } as Position;
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

function quoteOf(node: NodeJSON): string {
  const title = node.data?.title;
  if (typeof title === "string" && title) return ` “${title}”`;
  const delta = node.data?.delta;
  if (Array.isArray(delta)) {
    const text = delta.map((d) => (d as { insert?: string }).insert ?? "").join("").trim();
    if (text) return ` “${text.length > 40 ? text.slice(0, 39) + "…" : text}”`;
  }
  return "";
}

function insertSummary(nodes: NodeJSON[]): string {
  if (nodes.length === 1) {
    const n = nodes[0];
    switch (n.type) {
      case "chart":
        return `Inserted chart${quoteOf(n)}`;
      case "table":
        return "Inserted table";
      case "embed":
        return `Inserted interactive embed${quoteOf(n)}`;
      case "heading":
        return `Added section${quoteOf(n)}`;
      case "image":
        return "Inserted image";
      case "code":
        return "Inserted code block";
      case "callout":
        return `Added callout${quoteOf(n)}`;
      case "toggle_list":
        return `Added toggle${quoteOf(n)}`;
      case "video":
        return "Inserted video";
      case "link_preview":
        return "Inserted link card";
      case "math_equation":
        return "Inserted formula";
      case "diagram":
        return "Inserted diagram";
      default:
        return `Added ${n.type.replace("_", " ")}${quoteOf(n)}`;
    }
  }
  const heading = nodes.find((n) => n.type === "heading");
  return heading ? `Added section${quoteOf(heading)}` : `Added ${plural(nodes.length, "block")}`;
}

export interface SessionOptions {
  canEdit: boolean;
  crossPage: boolean;
  /** What the person asked, whose URLs a block may load (FETCHED_URL_TYPES). */
  request?: string;
  /** Agent name shown on charts it inserts. */
  author?: string;
  now?: () => number;
}

/**
 * The blocks the app loads by itself, as it shows them: an image, a video,
 * a link card. An agent that has read another of the user's pages could
 * write what it read into one, such as https://example.com/x.png?notes=…,
 * and the app would send it to that server without anyone clicking. So
 * once a run reads another page, or lists them, a block it writes may load
 * only a URL that was on this page as the run began, or in what the person
 * asked. A URL only another page has does not count: someone who can edit
 * that page could put many there, and which ones the agent picked would
 * spell out what it read. Links in text are not loaded, so they stay free.
 */
const FETCHED_URL_TYPES = new Set(["image", "video", "link_preview"]);

/** Finds the URLs in text. */
const URL_RE = /https?:\/\/[^\s<>"'()\[\]]+/g;

export class PageEditSession {
  content: PageContent;
  readonly before: PageContent;
  readonly ops: Op[] = [];
  private readonly now: () => number;
  /** The URLs a block may load once the agent has read beyond this page, null before (FETCHED_URL_TYPES). */
  private allowed: Set<string> | null = null;

  constructor(content: PageContent, private hooks: ToolHooks, private opts: SessionOptions) {
    this.content = clone(content);
    this.before = clone(content);
    this.now = opts.now ?? Date.now;
  }

  get changed(): boolean {
    return this.ops.some((op) => op.op !== "create_page");
  }

  /** Runs one tool call. Never throws: failures come back as { error }. */
  async call(name: string, rawArgs: string | Record<string, unknown>): Promise<ToolOutcome> {
    let args: Record<string, unknown>;
    try {
      args = typeof rawArgs === "string" ? (rawArgs.trim() ? JSON.parse(rawArgs) : {}) : rawArgs;
      if (!isObject(args)) throw new Error();
    } catch {
      return { result: { error: "Arguments must be a JSON object" }, ops: [], blockIds: [] };
    }
    try {
      return await this.dispatch(name, args);
    } catch (e) {
      if (e instanceof ToolError || e instanceof SpecError) {
        return { result: { error: e.message }, ops: [], blockIds: [] };
      }
      throw e;
    }
  }

  private async dispatch(name: string, args: Record<string, unknown>): Promise<ToolOutcome> {
    const editing = new Set(["insert_blocks", "replace_block", "move_block", "delete_blocks", "create_subpage"]);
    if (editing.has(name) && !this.opts.canEdit) {
      throw new ToolError("You can read this page but not edit it: the person asking has view access only");
    }
    if ((name === "list_pages" || name === "read_other_page") && !this.opts.crossPage) {
      throw new ToolError("Reading other pages is turned off for this user");
    }
    switch (name) {
      case "read_page":
        return { result: await this.hooks.readPage(this.content), ops: [], blockIds: [] };
      case "insert_blocks":
        return this.insert(normalizePosition(args.position), args.blocks);
      case "replace_block":
        return this.replace(String(args.id ?? ""), args.block);
      case "move_block":
        return this.move(String(args.id ?? ""), normalizePosition(args.position));
      case "delete_blocks":
        return this.remove(args.ids);
      case "create_subpage":
        return await this.createSubpage(args.title, args.blocks);
      case "list_pages":
        // The titles it lists are read too.
        this.arm();
        return { result: { pages: await this.hooks.listPages() }, ops: [], blockIds: [] };
      case "read_other_page": {
        const pageId = String(args.page_id ?? "");
        if (!pageId) throw new ToolError("page_id is required");
        const page = await this.hooks.readOtherPage(pageId);
        this.arm();
        return { result: page, ops: [], blockIds: [] };
      }
      default:
        throw new ToolError(`Unknown tool "${name}"`);
    }
  }

  /**
   * Starts checking URLs, as the agent first reads beyond this page: it may
   * use those on this page as the run found it, and those in the request.
   */
  private arm(): void {
    if (this.allowed) return;
    this.allowed = new Set();
    this.allow(pageViews(this.before));
    this.allow(this.opts.request ?? "");
  }

  /** Adds the URLs in v to allowed. */
  private allow(v: unknown): void {
    if (typeof v === "string") {
      if (v.startsWith("http://") || v.startsWith("https://")) this.allowed!.add(v);
      for (const u of v.match(URL_RE) ?? []) this.allowed!.add(u);
    } else if (Array.isArray(v)) {
      for (const e of v) this.allow(e);
    } else if (isObject(v)) {
      for (const e of Object.values(v)) this.allow(e);
    }
  }

  /** Refuses blocks that would load a URL not allowed, once the agent has read beyond this page. */
  private checkUrls(nodes: NodeJSON[]): void {
    const allowed = this.allowed;
    if (!allowed) return;
    const bad: string[] = [];
    for (const n of nodes) {
      walk(n, (k) => {
        const u = String(k.data?.url ?? "");
        if (FETCHED_URL_TYPES.has(k.type) && !allowed.has(u)) bad.push(u);
      });
    }
    if (bad.length) {
      throw new ToolError(
        `After reading another page, an image, video or link card may only use a URL that was on this page before you began or is in the user's request, which ${
          bad[0]
        } is not: the app loads it by itself. Link to it in text instead.`,
      );
    }
  }

  // --- indexing ------------------------------------------------------------

  private get root(): NodeJSON {
    this.content.document.children ??= [];
    return this.content.document;
  }

  private index(): Map<string, Located> {
    return indexBlocks(this.root);
  }

  private locate(id: string, idx = this.index()): Located {
    const found = idx.get(id);
    if (!found) throw new ToolError(`No block with id "${id}" on this page. Call read_page for current ids.`);
    return found;
  }

  private idFactory(): () => string {
    const taken = new Set<string>(collectIds(this.root));
    for (const id of Object.keys(this.content.meta.deleted)) taken.add(id);
    return () => {
      let id = randomId();
      while (taken.has(id)) id = randomId();
      taken.add(id);
      return id;
    };
  }

  private resolve(position: Position, idx = this.index()): { parent: NodeJSON; index: number } {
    if ("after" in position) {
      const at = this.locate(position.after, idx);
      return { parent: at.parent, index: at.index + 1 };
    }
    if ("before" in position) {
      const at = this.locate(position.before, idx);
      return { parent: at.parent, index: at.index };
    }
    if ("parent" in position) {
      const at = this.locate(position.parent, idx);
      if (!LIST_TYPES.has(at.node.type)) {
        throw new ToolError(`Block "${position.parent}" is a ${at.node.type}; only list blocks take children`);
      }
      at.node.children ??= [];
      return { parent: at.node, index: at.node.children.length };
    }
    return { parent: this.root, index: position.at === "start" ? 0 : this.root.children!.length };
  }

  /** A position that puts the block back where it is now. */
  private positionOf(at: Located): Position {
    const siblings = at.parent.children ?? [];
    if (at.index > 0) return { after: siblings[at.index - 1].id };
    if (at.index + 1 < siblings.length) return { before: siblings[at.index + 1].id };
    if (at.parent === this.root) return { at: "start" };
    return { parent: at.parent.id };
  }

  private touch(node: NodeJSON): void {
    const t = this.now();
    walk(node, (n) => {
      this.content.meta.blocks[n.id] = t;
      delete this.content.meta.deleted[n.id];
    });
  }

  private tombstone(node: NodeJSON): void {
    const t = this.now();
    walk(node, (n) => {
      delete this.content.meta.blocks[n.id];
      this.content.meta.deleted[n.id] = t;
    });
  }

  private specs(raw: unknown, field: string): BlockSpec[] {
    if (!Array.isArray(raw) || raw.length === 0) throw new ToolError(`${field} must be a non-empty array of blocks`);
    if (raw.length > 200) throw new ToolError(`At most 200 blocks per call`);
    return raw as BlockSpec[];
  }

  private record(op: Op): Op {
    this.ops.push(op);
    return op;
  }

  // --- tools ---------------------------------------------------------------

  insert(position: Position, rawBlocks: unknown): ToolOutcome {
    const specs = this.specs(rawBlocks, "blocks");
    // One set of options for all, so the limit on blocks counts them all.
    const opts = { newId: this.idFactory(), author: this.opts.author };
    const nodes = specs.map((spec) => specToNode(spec, opts));
    this.checkUrls(nodes);
    const target = this.resolve(position);
    target.parent.children ??= [];
    target.parent.children.splice(target.index, 0, ...nodes);
    nodes.forEach((n) => this.touch(n));
    const op = this.record({ op: "insert", position, nodes: clone(nodes) });
    const ids = nodes.map((n) => n.id);
    return { result: { inserted_ids: ids }, ops: [op], summary: insertSummary(nodes), blockIds: ids };
  }

  replace(id: string, rawSpec: unknown): ToolOutcome {
    if (!id) throw new ToolError("id is required");
    if (!isObject(rawSpec)) throw new ToolError("block must be a BlockSpec object");
    const spec = rawSpec as unknown as BlockSpec;
    const at = this.locate(id);
    const previous = clone(at.node);
    const node = specToNode(spec, { newId: this.idFactory(), author: this.opts.author }, id);
    this.checkUrls([node]);
    // Children left out, or none given, keep the ones the block had.
    const keepsChildren = !spec.children?.length && (previous.children?.length ?? 0) > 0 &&
      previous.type !== "table";
    if (keepsChildren) {
      if (!LIST_TYPES.has(node.type) && node.type !== previous.type) {
        throw new ToolError(
          `Block "${id}" has nested blocks; a ${node.type} cannot hold them. Move or delete them first, or keep a list type.`,
        );
      }
      node.children = at.node.children;
    }
    at.parent.children![at.index] = node;
    const kept = new Set(collectIds(node));
    for (const oldId of collectIds(previous)) {
      if (!kept.has(oldId)) {
        delete this.content.meta.blocks[oldId];
        this.content.meta.deleted[oldId] = this.now();
      }
    }
    if (keepsChildren) {
      this.content.meta.blocks[id] = this.now();
    } else {
      this.touch(node);
    }
    const op = this.record({ op: "replace", id, node: clone(node), previous });
    const summary = node.type === previous.type
      ? `Rewrote ${node.type.replace("_", " ")}${quoteOf(node)}`
      : `Changed ${previous.type.replace("_", " ")} to ${node.type.replace("_", " ")}`;
    return { result: { id }, ops: [op], summary, blockIds: [id] };
  }

  move(id: string, position: Position): ToolOutcome {
    if (!id) throw new ToolError("id is required");
    const idx = this.index();
    const at = this.locate(id, idx);
    const anchor = "after" in position
      ? position.after
      : "before" in position
      ? position.before
      : "parent" in position
      ? position.parent
      : null;
    if (anchor) {
      if (anchor === id) throw new ToolError("A block cannot be moved relative to itself");
      if (collectIds(at.node).includes(anchor)) {
        throw new ToolError("A block cannot be moved inside itself");
      }
      this.locate(anchor, idx);
    }
    const from = this.positionOf(at);
    at.parent.children!.splice(at.index, 1);
    const target = this.resolve(position);
    target.parent.children ??= [];
    target.parent.children.splice(target.index, 0, at.node);
    this.content.meta.blocks[id] = this.now();
    delete this.content.meta.deleted[id];
    const op = this.record({ op: "move", id, position, from });
    return {
      result: { id },
      ops: [op],
      summary: `Moved ${at.node.type.replace("_", " ")}${quoteOf(at.node)}`,
      blockIds: [id],
    };
  }

  remove(rawIds: unknown): ToolOutcome {
    if (!Array.isArray(rawIds) || rawIds.length === 0) throw new ToolError("ids must be a non-empty array");
    const ids = [...new Set(rawIds.map(String))];
    const idx = this.index();
    for (const id of ids) this.locate(id, idx);
    // Skip ids nested inside another id being deleted.
    const covered = new Set<string>();
    for (const id of ids) {
      for (const inner of collectIds(idx.get(id)!.node).slice(1)) covered.add(inner);
    }
    const targets = ids.filter((id) => !covered.has(id));
    const previous: { node: NodeJSON; position: Position }[] = [];
    for (const id of targets) {
      const at = this.locate(id);
      previous.push({ node: clone(at.node), position: this.positionOf(at) });
      at.parent.children!.splice(at.index, 1);
      this.tombstone(at.node);
    }
    const op = this.record({ op: "delete", ids: targets, previous });
    return {
      result: { deleted_ids: targets },
      ops: [op],
      summary: `Deleted ${plural(targets.length, "block")}`,
      blockIds: targets,
    };
  }

  async createSubpage(rawTitle: unknown, rawBlocks: unknown): Promise<ToolOutcome> {
    const title = String(rawTitle ?? "").trim();
    if (!title) throw new ToolError("title is required");
    const content = emptyContent();
    if (rawBlocks !== undefined && rawBlocks !== null) {
      const specs = Array.isArray(rawBlocks) && rawBlocks.length === 0 ? [] : this.specs(rawBlocks, "blocks");
      const taken = new Set<string>();
      const newId = () => {
        let id = randomId();
        while (taken.has(id)) id = randomId();
        taken.add(id);
        return id;
      };
      const t = this.now();
      const opts = { newId, author: this.opts.author };
      content.document.children = specs.map((spec) => specToNode(spec, opts));
      this.checkUrls(content.document.children);
      walk(content.document, (n) => {
        if (n !== content.document) content.meta.blocks[n.id] = t;
      });
    }
    const page = await this.hooks.createSubpage(title, content);
    const op = this.record({ op: "create_page", page, content: clone(content) });
    return { result: { page_id: page.id }, ops: [op], summary: `Created subpage “${title}”`, blockIds: [] };
  }
}
