// Agent page-edit tools (protocol §2) applied to an in-memory page, producing
// the ops of protocol §3. Mirrors internal/agents/tools.go on the client.

import {
  type BlockSpec,
  type BlockView,
  clone,
  collectIds,
  deltaOf,
  emptyContent,
  fileWithNotes,
  LIST_TYPES,
  type NodeJSON,
  type PageContent,
  pageViews,
  type Position,
  randomId,
  type RecordingInfo,
  SPACES,
  SPEC_TYPES,
  SpecError,
  type SpecOptions,
  specToNode,
  storableValue,
  TEXT_TYPES,
  transcriptText,
  trimSpace,
  videoOf,
  type VideoRef,
  walk,
} from "./blocks.ts";
import { type Captions, CaptionsError, hasCaptions, passages } from "./captions.ts";
import { markdownSpecs } from "./lines.ts";
import type { ToolDef } from "./openai.ts";
import type { WebPage } from "./page.ts";
import type { Search } from "./search.ts";
import type { WorkspaceHooks, WorkspacePage } from "./workspace.ts";

export type Op =
  | { op: "insert"; position: Position; nodes: NodeJSON[] }
  | { op: "replace"; id: string; node: NodeJSON; previous: NodeJSON }
  | { op: "move"; id: string; position: Position; from: Position }
  | { op: "delete"; ids: string[]; previous: { node: NodeJSON; position: Position }[] }
  | {
    op: "create_page";
    /** Inside parent_id, or at the top level when it is null: in folder_id when it has one. */
    page: {
      id: string;
      space_id: string;
      parent_id: string | null;
      folder_id?: string | null;
      title: string;
      sort_key: string;
    };
    content: PageContent;
  }
  | { op: "move_page"; page_id: string; title: string; place: PagePlace; previous: PagePlace }
  | { op: "rename_page"; page_id: string; title: string; previous: string }
  | { op: "set_page_tags"; page_id: string; title: string; tags: string[]; previous: string[] }
  | { op: "create_folder"; folder: { id: string; space_id: string; name: string; sort_key: string } };

/**
 * Where a page is among the person's pages: inside parent_id, or at the top
 * level, in folder_id when it has one, at sort_key among the pages there;
 * card is where its card is in parent_id, for Undo to put it back there,
 * none for the end (Go's PagePlace).
 */
export interface PagePlace {
  parent_id?: string;
  folder_id?: string;
  sort_key?: string;
  card?: Position;
}

/** The ops that change the pages around a page's blocks rather than its blocks (Go's IsPageOp). */
export const PAGE_OPS = new Set([
  "create_page",
  "delete_page",
  "move_page",
  "rename_page",
  "set_page_tags",
  "create_folder",
  "delete_folder",
]);

export interface PageSummary {
  id: string;
  title: string;
  parent_id: string | null;
}

export interface ToolHooks {
  /** read_page for the current page, given its current content. */
  readPage(content: PageContent): Promise<unknown>;
  createSubpage(title: string, content: PageContent): Promise<Extract<Op, { op: "create_page" }>["page"]>;
  /**
   * Makes a page of the person's own at the top level, in their folder of
   * id folderId when there is one; refuses one they have not.
   */
  createPage(
    title: string,
    content: PageContent,
    folderId: string | null,
  ): Promise<Extract<Op, { op: "create_page" }>["page"]>;
  listPages(): Promise<PageSummary[]>;
  readOtherPage(pageId: string): Promise<unknown>;
  /**
   * Whether page pageId, one the run read, is the person's alone, as the
   * page the run is on is counted for ownPages: no one else has it, no
   * guest commented on it, and it has no public link. None counts every
   * page so.
   */
  pageAlone?(pageId: string): Promise<boolean>;
  /**
   * Whether the page the run is on has a public link now, read again
   * before each call that reads beyond it, and once the run has read
   * beyond it, before each call that writes to it: one started since the
   * run began has visitors read what the run writes there. None counts it
   * as it was.
   */
  linkedNow?(): Promise<boolean>;
  listFolders(): Promise<{ id: string; name: string }[]>;
  /** Searches the web through the run's provider; none when the run has no web, or its provider no search. */
  searchWeb?(query: string): Promise<Search>;
  /** Reads a page of the web; none when the run has no web. */
  readWebPage?(url: string): Promise<WebPage>;
  /** Reads a video's captions from its site; none reads none. */
  readCaptions?(video: VideoRef): Promise<Captions>;
}

/** An edit as the person sees it: what it did, and the blocks it touched. */
export interface Edit {
  summary: string;
  blockIds: string[];
}

export interface ToolOutcome {
  result: unknown;
  ops: Op[];
  /** An edit for each op, as the app makes one for each of its own. */
  edits: Edit[];
  /** The edits' summaries in one. */
  summary?: string;
  /** The blocks all its edits touched. */
  blockIds: string[];
}

/** A call's outcome of these ops, each with its edit. */
function outcome(result: unknown, ops: Op[], edits: Edit[]): ToolOutcome {
  return {
    result,
    ops,
    edits,
    summary: edits.map((e) => e.summary).join(". ") || undefined,
    blockIds: edits.flatMap((e) => e.blockIds),
  };
}

/** A call that changed nothing. */
const unchanged = (result: unknown): ToolOutcome => outcome(result, [], []);

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

/** Where write_markdown writes: a position, or an empty block to write in place of. */
const writeSchema = {
  type: "object",
  description:
    "Where to write. Exactly one key: replace (an empty block to write in place of, as the empty line the person asked from), after/before a block id, parent (a list block id), or at start/end of the page.",
  properties: {
    replace: { type: "string", description: "Id of an empty block to write in place of" },
    ...positionSchema.properties,
  },
};

const blockSpecSchema = {
  type: "object",
  properties: {
    type: { type: "string", enum: [...SPEC_TYPES, "file"] },
    text: {
      type: "string",
      description:
        'Text with inline markdown: **bold**, *italic*, `code`, ~~strike~~, [link](https://…), and text color and highlight as <span style="color:#ef4444;background:#fde68a">text</span>, either alone, in hex, not nested. Keep the spans read_page shows when rewriting text. For text block types. For code, the code itself, verbatim, with its line breaks and no fence.',
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
      description:
        "For video: a video on YouTube, Vimeo, Bilibili, Dailymotion or Niconico, or a video file, that plays in the page",
      properties: {
        url: { type: "string" },
        title: { type: "string" },
        notes: {
          type: "string",
          description:
            "Plain-text notes shown under the video, a line each, no Markdown: a line that starts with a time, as 0:00 or 1:02:03, plays the video from there when clicked. For its summary, chapters or key moments",
        },
      },
      required: ["url"],
    },
    file: {
      type: "object",
      description: "For replace_block on a file block, keeping the file itself: its notes. No tool makes a new file",
      properties: {
        notes: {
          type: "string",
          description:
            "Plain-text notes shown under the file, a line each, no Markdown: for an audio or video file, a line that starts with a time, as 0:00 or 1:02:03, plays it from there when clicked",
        },
      },
    },
    link: {
      type: "object",
      description: "For link_preview: a card for a web page, with its title and picture",
      properties: { url: { type: "string" } },
      required: ["url"],
    },
    level: { type: "integer", minimum: 1, maximum: 6, description: "Heading level" },
    checked: { type: "boolean", description: "For todo_list" },
    due: {
      type: "string",
      description:
        "For todo_list: the day it is due, YYYY-MM-DD, or with a time of day, YYYY-MM-DDTHH:MM, in the user's own time",
    },
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

export function tool(
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

/**
 * The longest embed source agents read, in characters (Go's maxViewHTML):
 * a few longer would be more than a model reads at once, and the page
 * could not be read at all.
 */
export const MAX_VIEW_HTML = 30_000;

/**
 * The blocks as agents read them, as the app's agents do (Go's
 * ToolContext.Views): pageViews, without an embed's source longer than
 * MAX_VIEW_HTML, whose length is given instead, and without what sites
 * wrote of the blocks (withoutSiteInfo).
 */
export function agentViews(content: PageContent, recordings?: Map<string, RecordingInfo>): BlockView[] {
  const views = pageViews(content, recordings);
  for (const v of views) {
    const html = v.embed?.html;
    if (typeof html === "string") {
      const length = [...html].length;
      if (length > MAX_VIEW_HTML) {
        delete v.embed!.html;
        v.embed!.html_length = length;
      }
    }
    withoutSiteInfo(v);
  }
  return views;
}

/**
 * A block's view without what a site wrote of it, which the apps read from
 * the site to show the block (Go's withoutSiteInfo): a link card's title
 * and description, its page's of itself, and a video's title, its
 * uploader's, which the apps give a video that has none. That is the web's
 * writing, read with none of the web's guard (checkUrls). A video's title
 * is the site's but for one an agent wrote, and the block keeps no word of
 * which, nor did pages kept before, so none is read. A run that would know
 * what a page says reads it with the web, and a video's title comes with
 * its transcript, read as the web is (readTranscript), each keeping the
 * run to the URLs it may write after. Nor are the URLs in that text the
 * person's, to write as the page's (given).
 */
function withoutSiteInfo(v: BlockView): void {
  if (v.type === "link_preview" && v.link) {
    delete v.link.title;
    delete v.link.description;
  }
  if (v.type === "video" && v.video) delete v.video.title;
}

/** The most pages list_pages lists (Go's maxListedPages): more would be more than a model reads at once. */
export const MAX_LISTED_PAGES = 500;

/** The longest tool result a model reads, in UTF-16 units (Go's toolResultLimit): longer ones are cut. */
export const TOOL_RESULT_LIMIT = 60_000;

/**
 * A tool's result as the model reads it, cut off past TOOL_RESULT_LIMIT
 * (Go's cutResult): a cut inside a surrogate pair leaves U+FFFD, as half a
 * pair is JSON a provider may refuse, which would fail the run every time
 * it read the page.
 */
export function cutResult(text: string): string {
  return text.length > TOOL_RESULT_LIMIT ? `${text.slice(0, TOOL_RESULT_LIMIT).toWellFormed()}…(truncated)` : text;
}

/**
 * How much of a tool's result, by jsonCost, the pages list_pages lists take
 * at most, with what is listed with them, with room for the keys around
 * them (Go's listBudget): past it, the rest are left for a search to find,
 * rather than cut off with the result.
 */
export const LIST_BUDGET = TOOL_RESULT_LIMIT - 2_000;

/**
 * At least how many UTF-16 units v takes as JSON, as either side writes it
 * (Go's jsonCost): six for a character one of them escapes (\u003c, as Go
 * writes <), one or two for the rest.
 */
export function jsonCost(v: unknown): number {
  if (typeof v === "string") {
    let n = 2;
    for (const ch of v) {
      const c = ch.codePointAt(0)!;
      if (
        c < 0x20 || '"\\<>&'.includes(ch) || c === 0x2028 || c === 0x2029 || c === 0xfffd || (c >= 0xd800 && c < 0xe000)
      ) {
        n += 6;
      } else n += c > 0xffff ? 2 : 1;
    }
    return n;
  }
  if (typeof v === "boolean") return v ? 4 : 5;
  if (typeof v === "number") return String(v).length;
  if (Array.isArray(v)) return 2 + Math.max(v.length - 1, 0) + v.reduce((n: number, e) => n + jsonCost(e), 0);
  if (v && typeof v === "object") {
    const entries = Object.entries(v);
    return 2 + Math.max(entries.length - 1, 0) + entries.reduce((n, [k, e]) => n + jsonCost(k) + 1 + jsonCost(e), 0);
  }
  return 4;
}

/**
 * How many of items list_pages lists (Go's listed): at most MAX_LISTED_PAGES,
 * and as many as LIST_BUDGET takes past spent, what is listed with them.
 */
export function listed(items: unknown[], spent: number): number {
  const n = Math.min(items.length, MAX_LISTED_PAGES);
  for (let i = 0; i < n; i++) {
    spent += jsonCost(items[i]) + 1;
    if (spent > LIST_BUDGET) return i;
  }
  return n;
}

/** Refuses a page of the person's own on a page someone else writes or reads (Go's errNoOwnPages). */
export const NO_OWN_PAGES =
  "Someone else writes or reads this page: a page of the person's own made from it could take what they wrote into the person's own pages. Make a subpage of this page with create_subpage instead";

/**
 * Refuses a page of the person's own, a subpage of this page among them,
 * after the run read another page someone else writes or reads with
 * read_other_page (Go's errReadOthers).
 */
export const READ_OTHERS =
  "This run read a page someone else writes or reads: a page of the person's own made now could take what they wrote into the person's own pages";

/** Refuses reading beyond a page whose public link started while the run went on (linkedNow). */
export const NOW_LINKED =
  "This page has a public link now: its visitors read what you write here, so this run reads no other page. Work from this page";

/** The longest page title the pages table takes, in characters, which it counts as code points (char_length). */
export const MAX_TITLE = 2000;

/** title cut to MAX_TITLE characters, as the app's store keeps it, so a long one the model gives is saved. */
export function clampTitle(title: string): string {
  const chars = [...title];
  return chars.length > MAX_TITLE ? chars.slice(0, MAX_TITLE).join("") : title;
}

/** The most blocks one run writes (Go's maxRunBlocks): a model that goes on and on, as one repeating itself, stops there. */
export const MAX_RUN_BLOCKS = 5000;

/** The most tool calls of one answer that run (Go's maxRoundCalls): those past it are refused, for the next answer. */
export const MAX_ROUND_CALLS = 50;

/**
 * The most links picked from pages it read a run reads (Go's
 * maxFollowedLinks): the pages the person gave it or a search found are
 * not counted.
 */
const MAX_FOLLOWED_LINKS = 10;

/** What the model is told of a call past MAX_ROUND_CALLS. */
export const TOO_MANY_CALLS =
  `This call was not made: one answer makes at most ${MAX_ROUND_CALLS} calls. Make it again in your next.`;

/**
 * The page's tools. ownPages, unless false, offers create_page: none on a page
 * someone else writes or reads, whose writing a page of the person's own
 * would take into their pages, where their workspace agent reads it as
 * theirs.
 */
export function toolDefinitions(opts: { canEdit: boolean; crossPage: boolean; ownPages?: boolean }): ToolDef[] {
  const defs: ToolDef[] = [
    tool(
      "read_page",
      `Read the whole current page: every block with its id, its comments, and its subpages. An embed's html longer than ${MAX_VIEW_HTML} characters is left out, its html_length given instead: to change such an embed, write its html whole.`,
    ),
    tool(
      "read_video_transcript",
      "Read what is said in a video block of this page, from its site's captions: lines of [m:ss] text, each the time it starts. Only YouTube's and Vimeo's videos have them, and not every one. Read it to summarize a video, write its notes, or cite a moment of it.",
      { id: { type: "string", description: "The video block's id." } },
      ["id"],
    ),
  ];
  if (opts.canEdit) {
    defs.push(
      tool(
        "write_markdown",
        "Write into the page in Markdown: the way to write prose, as a story, a draft, notes or a summary. Each line is a block: a paragraph, # to ### headings, - or 1. list items (indent two spaces to nest), - [ ] to-dos, > quotes, ``` fenced code, | tables, --- dividers; blank lines only separate them. Returns the ids of the blocks written.",
        { position: writeSchema, markdown: { type: "string" } },
        ["position", "markdown"],
      ),
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
        "Create a subpage inside this page, for content that is part of it, optionally with content: a link to it goes at the end of this page.",
        pageSchema(),
        ["title"],
      ),
      tool(
        "create_page",
        'Create a page of its own, outside this one, optionally with content: at the top level of the person\'s pages, or in one of their folders. Make "a new page" with this, unless the person asks for one inside or under this page.',
        pageSchema({
          folder_id: {
            type: "string",
            description:
              "The folder to put it in, its id from list_folders where that is offered; none for the top level.",
          },
        }),
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
      tool("list_folders", "List the person's folders (names and ids), to make a page in one with create_page."),
    );
  }
  return opts.ownPages === false ? defs.filter((d) => d.function.name !== "create_page") : defs;
}

/** A new page's schema: its title, where it goes, and its content in Markdown or as blocks, or neither for an empty page. */
export function pageSchema(place: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    title: { type: "string" },
    ...place,
    markdown: {
      type: "string",
      description:
        "The page's content in Markdown, as write_markdown writes it: the way to write text. Not with blocks.",
    },
    blocks: { type: "array", items: blockSpecSchema },
  };
}

/**
 * The web's tools offered to a model (Go's WebToolDefinitions): a search,
 * when its provider searches, and reading a page.
 */
export function webToolDefinitions(search: boolean): ToolDef[] {
  const defs: ToolDef[] = [];
  if (search) {
    defs.push(tool(
      "web_search",
      "Search the web, for what is recent or what you do not know: this week's news, a price, a page's address. Returns the results' links and what they say; read one whole with read_web_page.",
      { query: { type: "string", description: "What to search for, as you would type it into a search engine." } },
      ["query"],
    ));
  }
  defs.push(tool(
    "read_web_page",
    "Read a web page: its title, its text, and the links in it. Once you have read the web in this run, read only a page the person gave, one on this page, or one a search found or a page you read links to.",
    { url: { type: "string" } },
    ["url"],
  ));
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

/**
 * A page's addressable blocks, as indexBlocks has them, kept up as the
 * page changes rather than found again for every edit: each block is
 * looked up by id, with the block it is in, and its place among its
 * siblings found as it is asked for.
 */
class BlockIndex {
  private readonly parents = new Map<string, { node: NodeJSON; parent: NodeJSON }>();

  constructor(private readonly root: NodeJSON) {
    this.add(root.children ?? [], root);
  }

  /**
   * Whether the blocks in parent are addressable: in the page, or in an
   * addressable block but a table. Those put in a table's cell, or in a
   * block in one, are no more than the ones there already.
   */
  private holds(parent: NodeJSON): boolean {
    return parent === this.root || (parent.type !== "table" && this.parents.get(parent.id)?.node === parent);
  }

  /** Puts nodes, and the blocks in them, in the index, as children of parent, where it holds them. */
  add(nodes: NodeJSON[], parent: NodeJSON): void {
    if (!this.holds(parent)) return;
    // In reading order, so the last of blocks with one id wins, as in
    // indexBlocks.
    const stack = nodes.map((node) => ({ node, parent })).reverse();
    while (stack.length) {
      const at = stack.pop()!;
      this.parents.set(at.node.id, at);
      if (at.node.type === "table") continue;
      const kids = at.node.children ?? [];
      for (let i = kids.length - 1; i >= 0; i--) stack.push({ node: kids[i], parent: at.node });
    }
  }

  /** Takes node and the blocks in it out of the index. */
  remove(node: NodeJSON): void {
    walk(node, (n) => {
      if (this.parents.get(n.id)?.node === n) this.parents.delete(n.id);
    });
  }

  /** Has node in parent now, where it holds it. */
  moved(node: NodeJSON, parent: NodeJSON): void {
    if (this.holds(parent)) this.parents.set(node.id, { node, parent });
  }

  get(id: string): Located | undefined {
    const at = this.parents.get(id);
    if (!at) return undefined;
    const index = at.parent.children?.indexOf(at.node) ?? -1;
    return index < 0 ? undefined : { ...at, index };
  }
}

function anchorOf(position: Position): string | null {
  if ("after" in position) return position.after;
  if ("before" in position) return position.before;
  if ("parent" in position) return position.parent;
  return null;
}

/**
 * Where a block is, as doc.Content.Locate finds it: a table's cell, or a
 * block in one, too, which the index leaves out.
 */
function locate(root: NodeJSON, idx: BlockIndex, id: string): Located | undefined {
  const at = idx.get(id);
  if (at) return at;
  let found: Located | undefined;
  walk(root, (node) => {
    const index = found ? -1 : (node.children ?? []).findIndex((k) => k.id === id);
    if (index >= 0) found = { node: node.children![index], parent: node, index };
  });
  return found;
}

/** Whether a block is a table or one of its cells (doc's inTable). */
function inTable(node: NodeJSON): boolean {
  return node.type === "table" || node.type === "table/cell";
}

/** Where a position puts blocks, or null for an anchor that is gone (doc.Content.Insert). */
function place(root: NodeJSON, position: Position, idx: BlockIndex): { parent: NodeJSON; index: number } | null {
  root.children ??= [];
  const anchor = anchorOf(position);
  if (!anchor) return { parent: root, index: "at" in position && position.at === "start" ? 0 : root.children.length };
  const at = locate(root, idx, anchor);
  if (!at) return null;
  if ("after" in position) return { parent: at.parent, index: at.index + 1 };
  if ("before" in position) return { parent: at.parent, index: at.index };
  at.node.children ??= [];
  return { parent: at.node, index: at.node.children.length };
}

/**
 * Where an insert puts its blocks on a page that may have changed since
 * (doc.Content.Apply): an anchor gone since, as a neighbour deleted after
 * the op, leaves them at the end of the page rather than not at all, and
 * a parent that is no longer a list has them after it. A table holds only
 * its cells: other blocks put in it or next to one of its cells go after
 * the table.
 */
function insertPosition(root: NodeJSON, position: Position, nodes: NodeJSON[], idx: BlockIndex): Position {
  const anchor = anchorOf(position);
  if (!anchor) return position;
  const at = locate(root, idx, anchor);
  const cells = nodes.every((n) => n.type === "table/cell");
  if (!at) return { at: "end" };
  if ("parent" in position && at.node.type === "table" && !cells) return { after: anchor };
  if ("parent" in position && !LIST_TYPES.has(at.node.type) && !inTable(at.node)) return { after: anchor };
  if (!("parent" in position) && at.parent.type === "table" && !cells) return { after: at.parent.id };
  return position;
}

/**
 * Whether a move may go where it says (doc.Content.Move): next to or into
 * a block that is not itself or in it, only into a list, and neither into
 * a table nor out of one, whose cells keep their blocks.
 */
function canMove(root: NodeJSON, from: Located, position: Position, idx: BlockIndex): boolean {
  const anchor = anchorOf(position);
  if (!anchor) return !inTable(from.parent);
  const at = locate(root, idx, anchor);
  if (!at || collectIds(from.node).includes(anchor)) return false;
  const to = "parent" in position ? at.node : at.parent;
  if ("parent" in position && !LIST_TYPES.has(to.type) && !inTable(to)) return false;
  return !((inTable(to) || inTable(from.parent)) && to !== from.parent);
}

/** node without the blocks in it the page has already (have), or null when node is one (doc's withoutPresent). */
function withoutPresent(node: NodeJSON, have: Set<string>): NodeJSON | null {
  if (node.id && have.has(node.id)) return null;
  if (node.children) {
    node.children = node.children.map((k) => withoutPresent(k, have)).filter((k): k is NodeJSON => k !== null);
  }
  return node;
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

/** node without the cells only a table holds, when it is none (doc.DropCells). */
function dropCells(node: NodeJSON): void {
  if (node.type === "table" || !node.children) return;
  node.children = node.children.filter((k) => k.type !== "table/cell");
  if (!node.children.length) delete node.children;
}

/**
 * Whether node may take the children current has (doc's canHoldChildren):
 * a list takes any, another block those of a block of its own type, as
 * one from another editor may hold.
 */
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
    const entry = idx.get(read.id);
    if (!entry) return false;
    const at = asRead(entry.node, read);
    const strip = (n: NodeJSON) => ({ ...n, children: undefined });
    return own ? canonical(strip(at)) !== canonical(strip(read)) : canonical(at) !== canonical(read);
  };
  let found = false;
  for (const op of ops) {
    if (op.op === "insert") op.nodes.forEach((n) => walk(n, (x) => seen.add(x.id)));
    if (op.op === "replace" && changed(op.previous, keptChildren(op))) found = true;
    if (op.op === "delete" && op.previous.some((p) => changed(p.node, false))) found = true;
  }
  return found;
}

/** What the app looks up for a link card that lacks its title (web.LinkInfo). */
const LINK_INFO = ["title", "description", "image", "site"];

/**
 * A block as found, with the info the app looks up for a link card that
 * lacked it as read put back as read: filling a card in is no change of
 * the page's.
 */
function asRead(at: NodeJSON, read: NodeJSON): NodeJSON {
  const str = (n: NodeJSON, k: string) => (typeof n.data?.[k] === "string" ? n.data[k] as string : "");
  const link = str(read, "url");
  if (!link || read.type !== "link_preview" || str(read, "title") || at.type !== read.type || str(at, "url") !== link) {
    return at;
  }
  const data = { ...at.data };
  for (const k of LINK_INFO) {
    if (read.data && k in read.data) data[k] = read.data[k];
    else delete data[k];
  }
  return { ...at, data };
}

/**
 * The ops of a run still to replay on latest, a version of the page saved
 * since the run read it as before. An op on a block latest changed since,
 * by its edit time in meta, is left out: the app that had the page open
 * already applied it, as the ops streamed, and saved it, and what a person
 * then wrote over it, or a device wrote meanwhile, is newer than the
 * agent's edit; replayed, the edit would undo it. The ops on blocks latest
 * has as the run read them go, as on a page no one had open.
 */
export function unseenOps(before: PageContent, latest: PageContent, ops: Op[]): Op[] {
  const at = (c: PageContent, id: string) => c.meta.blocks[id] ?? c.meta.deleted[id];
  const touched = (id: string) => at(latest, id) !== at(before, id);
  const out: Op[] = [];
  for (const op of ops) {
    switch (op.op) {
      case "insert":
        if (!op.nodes.some((n) => touched(n.id))) out.push(op);
        break;
      case "replace":
      case "move":
        if (!touched(op.id)) out.push(op);
        break;
      case "delete": {
        const ids = op.ids.filter((id) => !touched(id));
        if (ids.length > 0) {
          out.push({ ...op, ids, previous: op.previous.filter((p) => ids.includes(p.node.id)) });
        }
        break;
      }
      default:
        out.push(op);
    }
  }
  return out;
}

/**
 * Replays ops on a (possibly newer) version of a page. Ops whose blocks are
 * gone are skipped; inserts whose anchor is gone land at the end. The page
 * is copied once, and its blocks indexed once, for all the ops.
 */
export function applyOps(content: PageContent, ops: Op[], now = Date.now()): PageContent {
  const out = clone(content);
  const root = out.document;
  root.children ??= [];
  const idx = new BlockIndex(root);
  // Every block's id, table cells' too: an insert skips a block the page
  // has already.
  const ids = new Set(collectIds(root));
  const touch = (node: NodeJSON) =>
    walk(node, (n) => {
      out.meta.blocks[n.id] = now;
      delete out.meta.deleted[n.id];
    });
  const forget = (node: NodeJSON) => {
    idx.remove(node);
    walk(node, (n) => ids.delete(n.id));
  };
  const learn = (node: NodeJSON, parent: NodeJSON) => {
    idx.add([node], parent);
    walk(node, (n) => ids.add(n.id));
  };
  for (const op of ops) {
    switch (op.op) {
      case "insert": {
        // Blocks the page has already, as when an undo run puts back what
        // an undo or a merge brought back first, stay as they are.
        const nodes = op.nodes.map((n) => withoutPresent(clone(n), ids)).filter((n): n is NodeJSON => n !== null);
        if (nodes.length === 0) break;
        const target = place(root, insertPosition(root, op.position, nodes, idx), idx)!;
        target.parent.children ??= [];
        target.parent.children.splice(target.index, 0, ...nodes);
        for (const n of nodes) {
          learn(n, target.parent);
          touch(n);
        }
        break;
      }
      case "replace": {
        const at = idx.get(op.id);
        if (!at) break;
        const node = clone(op.node);
        dropCells(node);
        forget(at.node);
        // A replace that kept the block's children carries them as the
        // agent read them: keep the ones the block has here instead, so
        // a child edited or added since is not undone.
        if (keptChildren(op) && canHoldChildren(node, at.node)) {
          if (at.node.children?.length) node.children = at.node.children;
          else delete node.children;
          at.parent.children![at.index] = node;
          learn(node, at.parent);
          out.meta.blocks[node.id] = now;
          delete out.meta.deleted[node.id];
          break;
        }
        at.parent.children![at.index] = node;
        learn(node, at.parent);
        touch(node);
        break;
      }
      case "move": {
        const at = locate(root, idx, op.id);
        if (!at || !canMove(root, at, op.position, idx)) break;
        at.parent.children!.splice(at.index, 1);
        const target = place(root, op.position, idx)!;
        target.parent.children ??= [];
        target.parent.children.splice(target.index, 0, at.node);
        idx.moved(at.node, target.parent);
        out.meta.blocks[op.id] = now;
        break;
      }
      case "delete": {
        for (const id of op.ids) {
          const at = idx.get(id);
          if (!at) continue;
          at.parent.children!.splice(at.index, 1);
          forget(at.node);
          walk(at.node, (n) => {
            delete out.meta.blocks[n.id];
            out.meta.deleted[n.id] = now;
          });
        }
        break;
      }
      default:
        // A page op changes the pages around this one (PAGE_OPS).
        break;
    }
  }
  return out;
}

/**
 * Where each block a delete takes out was, so its undo puts it back
 * (doc.Content.DeleteOp, whose Invert puts them back in this order): in
 * document order, each after its previous sibling, which is either still
 * there or put back just before it; a first child goes before the next
 * sibling that stays. A block inside another deleted one comes back with
 * it, so it isn't recorded.
 */
export function deletedPositions(root: NodeJSON, gone: Set<string>): { node: NodeJSON; position: Position }[] {
  const out: { node: NodeJSON; position: Position }[] = [];
  const visit = (nodes: NodeJSON[], parent: NodeJSON) => {
    nodes.forEach((node, i) => {
      if (!gone.has(node.id)) {
        if (node.type !== "table") visit(node.children ?? [], node);
        return;
      }
      const stays = nodes.find((k) => !gone.has(k.id));
      const position: Position = i > 0
        ? { after: nodes[i - 1].id }
        : stays
        ? { before: stays.id }
        : parent === root
        ? { at: "start" }
        : { parent: parent.id };
      out.push({ node: clone(node), position });
    });
  };
  visit(root.children ?? [], root);
  return out;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

/**
 * The keys of a position a model gave a value. Models that write every
 * key of a schema give the others null or "", and at, which takes only
 * start or end, one of those: at counts only alone.
 */
function givenKeys(raw: Record<string, unknown>, keys: string[]): string[] {
  const set = keys.filter((k) => raw[k] !== undefined && raw[k] !== null && raw[k] !== "");
  return set.length > 1 ? set.filter((k) => k !== "at") : set;
}

export function normalizePosition(raw: unknown): Position {
  if (raw === "start" || raw === "end") return { at: raw };
  if (!isObject(raw)) throw new ToolError('position must be an object such as {"after": "<block id>"}');
  // replace, which only write_markdown takes, counts as a key here too, as
  // the app counts it: with another, the position has two.
  const keys = givenKeys(raw, ["after", "before", "parent", "at", "replace"]);
  if (keys.length !== 1) {
    throw new ToolError("position needs exactly one of: after, before, parent, at");
  }
  const key = keys[0];
  if (key === "replace") throw new ToolError('Unknown position key "replace"');
  const value = raw[key];
  if (key === "at") {
    if (value !== "start" && value !== "end") throw new ToolError('position.at must be "start" or "end"');
    return { at: value };
  }
  if (typeof value !== "string" || !value) throw new ToolError(`position.${key} must be a block id`);
  return { [key]: value } as Position;
}

/**
 * A call's argument of text, as the app's agents take one (agents.str):
 * refused when it is of another kind, or only spaces.
 */
function textArg(args: Record<string, unknown>, key: string): string {
  const value = typeof args[key] === "string" ? args[key] as string : "";
  if (!trimSpace(value)) throw new ToolError(`${key} is required`);
  return value;
}

/** A page as list_pages lists it (Go's pageView). */
function pageView(p: WorkspacePage): Record<string, unknown> {
  return {
    id: p.id,
    title: p.title,
    ...(p.tags.length ? { tags: p.tags } : {}),
    ...(p.parent_id ? { parent_id: p.parent_id } : {}),
    ...(p.folder_id ? { folder_id: p.folder_id } : {}),
    ...(p.shared ? { shared: true } : {}),
    ...(p.people ? { shared_with_people: true } : {}),
  };
}

/** A page's title as it keeps it: "" for one untitled (Go's WorkspacePage.title). */
function keptTitle(p: WorkspacePage): string {
  return p.untitled ? "" : p.title;
}

/** Refuses a page someone else reads or writes (Go's changeable). */
function changeable(p: WorkspacePage): void {
  if (p.shared) {
    throw new ToolError(
      `“${p.title}” is shared with others: the agent here leaves it as it is, and puts nothing in it. To change it, ask the agent on that page`,
    );
  }
}

/** The text args has at key: "" when it has none, refused when it has something else (Go's optionalString). */
function optionalText(args: Record<string, unknown>, key: string): string {
  const value = args[key];
  if (value === undefined || value === null) return "";
  if (typeof value !== "string") throw new ToolError(`${key} must be a string`);
  return value;
}

/** What a kind of block is called in an edit's summary. */
const KINDS: Record<string, string> = {
  chart: "chart",
  table: "table",
  embed: "interactive embed",
  heading: "heading",
  image: "image",
  video: "video",
  code: "code block",
  callout: "callout",
  toggle_list: "toggle",
  link_preview: "link card",
  math_equation: "formula",
  diagram: "diagram",
  subpage: "subpage link",
};

/**
 * An edit's summary, which the person sees in the agent's edits, as the
 * app words it (agents.summaryFor): "Inserted 3 blocks", "Wrote a
 * heading", "Inserted a chart · Sales".
 */
function summaryFor(verb: string, nodes: NodeJSON[]): string {
  if (nodes.length > 1) return `${verb} ${nodes.length} blocks`;
  const n = nodes[0];
  const kind = KINDS[n.type] ?? "";
  const title = ["chart", "embed", "video"].includes(n.type) && typeof n.data?.title === "string" ? n.data.title : "";
  let what = kind.startsWith("i") ? `an ${kind}` : kind ? `a ${kind}` : "a block";
  if (title) what += ` · ${title}`;
  return `${verb} ${what}`;
}

export interface SessionOptions {
  canEdit: boolean;
  crossPage: boolean;
  /** False on a page someone else writes or reads, where create_page is refused (toolDefinitions). */
  ownPages?: boolean;
  /** What the person asked, whose URLs a block may load (checkUrls). */
  request?: string;
  /** Agent name shown on charts it inserts. */
  author?: string;
  now?: () => number;
  /** The workspace agent's, whose tools are of the person's pages rather than of one: none on a page. */
  workspace?: WorkspaceHooks;
  /**
   * Earlier turns of the conversation that read beyond the page are among
   * what the model reads: the run holds what they read, as one that read
   * it itself (linkedNow).
   */
  heldPages?: boolean;
}

// The URL guard (checkUrls): the app loads an image, a video or a link
// card by itself, as it shows them. An agent that has read another of the
// user's pages could write what it read into one, such as
// https://example.com/x.png?notes=…, and the app would send it to that
// server without anyone clicking. So once a run reads another page, or
// lists them, what it writes may hold only a URL that was on this page as
// the run began, or in what the person asked. A URL only another page has
// does not count: someone who can edit that page could put many there, and
// which ones the agent picked would spell out what it read. Not in text
// either: a link a run writes in text would be on the page for the next
// run, which takes the page's URLs as the person's, to load in a block
// (internal/agents checkURLs). A page it read it names with a link to that
// page, which loads nothing.
//
// The web is written by anyone, to steer the agents that read it: a page
// could have one write what is on this page into such a URL, or read the
// page at one. So once a run reads the web, a video's captions among it,
// what it writes, and the pages it reads, may hold only a URL the person
// gave it, or one it found there: a search's result, or a link on a page
// it read. Not one it made up. A block the app loads by itself keeps to
// fewer: a search's result, or a page the run read but did not pick from
// another it read, not a link on one, read or not, which its writer could
// have put there by the hundred for the run to pick from. Reading such a
// link tells its site the same, with no block at all, so a run reads a few
// of them and no more (MAX_FOLLOWED_LINKS). And once it reads the person's
// other pages, it does not use the web at all (WEB_CLOSED), as what it asked
// for there could carry what it read, and what it writes keeps to the URLs
// the person gave it, as above.

/**
 * The URLs in v, as allow takes them, their schemes in lower case
 * (canonUrl): whole strings that are one, and those in text.
 */
function urlsIn(v: unknown, out = new Set<string>()): Set<string> {
  if (typeof v === "string") {
    const whole = canonUrl(v);
    if (whole.startsWith("http://") || whole.startsWith("https://")) out.add(whole);
    for (const u of v.match(URL_RE) ?? []) out.add(canonUrl(u));
  } else if (Array.isArray(v)) {
    for (const e of v) urlsIn(e, out);
  } else if (isObject(v)) {
    for (const e of Object.values(v)) urlsIn(e, out);
  }
  return out;
}

/**
 * Finds the URLs in text, each to the first space as the client has one
 * (SPACES), as its urlRe: their scheme in any case, as the app takes a
 * link's (webURL), and in ASCII's letters alone, as a scheme is.
 */
export const URL_RE = new RegExp(`[hH][tT][tT][pP][sS]?://[^${SPACES}<>"'()\\[\\]]+`, "gu");

/**
 * u with a web link's scheme in lower case, as a URL parser reads it (Go's
 * canonURL): the guard keeps and compares URLs so, as two that only their
 * scheme's case sets apart are one link to the app.
 */
function canonUrl(u: string): string {
  for (const scheme of ["https://", "http://"]) {
    if (u.length >= scheme.length && asciiFold(u.slice(0, scheme.length)) === scheme) {
      return scheme + u.slice(scheme.length);
    }
  }
  return u;
}

/** s with its ASCII letters in lower case, and nothing else changed. */
function asciiFold(s: string): string {
  return s.replace(/[A-Z]/g, (c) => c.toLowerCase());
}

/** The blocks whose url the app loads by itself, as it shows them (Go's loads). */
const LOADS = new Set(["image", "link_preview", "video"]);

/**
 * The tools that change the page, which a viewer may not call. Text on the
 * page, in its comments or in what a tool returns may be written to steer
 * the agent, and only the prompt has it take that as data; the tools that
 * delete or rewrite blocks are not refused for it. A run is asked for by
 * someone who may edit the page, in their own words (a comment's run by
 * their own comment), and the run saves the page as it was first
 * (page_history, agent_run), its edits one undo step in the app: what such
 * text has the agent take away comes back with Undo. Reading the user's
 * other pages, which could carry them off, is what is refused where others
 * write (crossPage in the agent function), as in internal/agents/tools.go.
 */
const EDITING_TOOLS = new Set([
  "write_markdown",
  "insert_blocks",
  "replace_block",
  "move_block",
  "delete_blocks",
  "create_subpage",
  "create_page",
]);

/** A web tool's answer once the run has read the person's other pages, or follows turns that did (Go's errWebClosed). */
const WEB_CLOSED =
  "This run has read the user's other pages, or follows turns of this conversation that did, so it cannot use the web: what it asked for there could carry what it read. Work from what you have.";

/** A web address in a search's query: its scheme, its www. or a path after its host (Go's addressRe). */
const ADDRESS_RE = /:\/\/|\bwww\.|\b[a-z0-9-]+(\.[a-z0-9-]+)+\//i;

export class PageEditSession {
  content: PageContent;
  readonly before: PageContent;
  readonly ops: Op[] = [];
  private readonly now: () => number;
  /** The URLs the person gave the run: on this page as it began, and in their request (checkUrls). */
  private allowed: Set<string> | null = null;
  /** A run that has read beyond this page, or follows turns that may have; readPages, one that has itself. */
  private armed = false;
  private readPages = false;
  /**
   * A run that read another page someone else writes or reads (pageAlone):
   * what they wrote there would go into a page of the person's own made
   * after it, where their workspace agent reads it as theirs, so it makes
   * none, as on such a page itself (ownPages).
   */
  private readOthers = false;
  /** A run whose page has had a public link since it began (linkedNow): it reads beyond the page no more. */
  private linked = false;
  /**
   * A run that had read beyond its page as its page gained a public link:
   * it ends there, keeping what it did, as the app ends its runs on a page
   * whose link starts, rather than write where the link's visitors read.
   */
  private stoppedByLink = false;
  /**
   * The URLs the run came on in the web: those its searches found, and
   * those on the pages it read. Null before it reads the web, after which
   * what it writes, and the pages it reads, keep to them and those the
   * person gave it (checkUrls, readWeb). sources are those of them the run
   * came on itself, its searches' results and the pages it read that it
   * picked from none it read, not the links on those: the only ones a block
   * the app loads by itself may hold (LOADS).
   */
  private found: Set<string> | null = null;
  private sources = new Set<string>();
  /** The page's blocks, kept up as the calls change them. */
  private readonly idx: BlockIndex;
  /** Every id the page has or had, which a new block may not take. */
  private readonly taken: Set<string>;
  /** The links the run has read that it picked from pages it read, toward MAX_FOLLOWED_LINKS. */
  private followed = 0;
  /** The blocks the run's calls have written, toward MAX_RUN_BLOCKS. */
  private written = 0;

  constructor(content: PageContent, private hooks: ToolHooks, private opts: SessionOptions) {
    this.content = clone(content);
    this.before = clone(content);
    this.now = opts.now ?? Date.now;
    this.content.document.children ??= [];
    this.idx = new BlockIndex(this.root);
    this.taken = new Set([...collectIds(this.root), ...Object.keys(this.content.meta.deleted)]);
  }

  get changed(): boolean {
    return this.ops.some((op) => !PAGE_OPS.has(op.op));
  }

  /** A run that read nothing beyond its page. */
  /** A run whose page gained a public link after it read beyond the page: it ends (stoppedByLink). */
  get stopped(): boolean {
    return this.stoppedByLink;
  }

  /** A run that read a page someone else writes or reads, or follows turns that did (readOthers). */
  get readOtherPeoples(): boolean {
    return this.readOthers;
  }

  /**
   * Makes no page of the person's own from the start, as after reading a
   * page someone else writes or reads: an earlier turn of the conversation
   * did, and what it read is before this one.
   */
  keepFromOwnPages(): void {
    this.readOthers = true;
  }

  get keptToPage(): boolean {
    return !this.readPages;
  }

  /**
   * Whether the run's URL guard is on: it has read beyond its page or the
   * web, or follows turns that did (Go's Guarded). What it says may then
   * carry what it read, its reply too.
   */
  get guarded(): boolean {
    return this.armed || this.found !== null;
  }

  /**
   * Runs one tool call. A call refused comes back as { error }; anything
   * else it throws, the session left as it was before the call.
   */
  async call(name: string, rawArgs: string | Record<string, unknown>): Promise<ToolOutcome> {
    let args: Record<string, unknown>;
    try {
      args = typeof rawArgs === "string" ? (rawArgs.trim() ? JSON.parse(rawArgs) : {}) : rawArgs;
      if (!isObject(args)) throw new Error();
    } catch {
      return unchanged({ error: "Arguments must be a JSON object" });
    }
    args = storableValue(args) as Record<string, unknown>;
    // A call that fails part way, such as write_markdown whose last block
    // is refused, leaves the page as it was: the model is told it failed,
    // so nothing it did may stay. Each tool makes all its blocks and finds
    // where they go before it changes the page, which nothing after can
    // fail, so the page is not copied for each call to go back to.
    try {
      return await this.dispatch(name, args);
    } catch (e) {
      if (e instanceof ToolError || e instanceof SpecError) return unchanged({ error: e.message });
      // Anything else fails the run, whose edits so far may still be
      // saved: those are the ones streamed, without this call's.
      throw e;
    }
  }

  private async dispatch(name: string, args: Record<string, unknown>): Promise<ToolOutcome> {
    if (this.opts.workspace) {
      // What the person's pages could not be read or changed for, as the
      // database failing or slow, the model is told, as Go tells it: the
      // run goes on, its changes so far kept.
      try {
        return await this.workspace(this.opts.workspace, name, args);
      } catch (e) {
        if (e instanceof ToolError || e instanceof SpecError) throw e;
        throw new ToolError(`The call failed: ${e instanceof Error ? e.message : String(e)}`);
      }
    }
    if (EDITING_TOOLS.has(name) && !this.opts.canEdit) {
      throw new ToolError("You can read this page but not edit it: the person asking has view access only");
    }
    const cross = name === "list_pages" || name === "read_other_page" || name === "list_folders";
    if (cross && !this.opts.crossPage) throw new ToolError("Reading other pages is turned off for this user");
    // A public link started on the page since the run began has its
    // visitors read what the run writes there: the run keeps to its page,
    // and one that read beyond it ends, as the app's runs on a page do as
    // its link starts. The page's lease is read again for each call that
    // could take what is beyond the page into it, until it has one.
    const beyond = this.readPages || !!this.opts.heldPages;
    if (cross || (beyond && EDITING_TOOLS.has(name))) {
      if (!this.linked && this.hooks.linkedNow && await this.hooks.linkedNow()) this.linked = true;
      if (this.linked) {
        if (beyond) this.stoppedByLink = true;
        throw new ToolError(NOW_LINKED);
      }
    }
    switch (name) {
      case "read_page":
        return unchanged(await this.hooks.readPage(this.content));
      case "write_markdown":
        return this.write(args.position, args.markdown);
      case "insert_blocks":
        return this.insert(normalizePosition(args.position), args.blocks);
      case "replace_block":
        return this.replace(textArg(args, "id"), args.block);
      case "move_block":
        return this.move(textArg(args, "id"), normalizePosition(args.position));
      case "delete_blocks":
        return this.remove(args.ids);
      case "create_subpage":
        return await this.createSubpage(args);
      case "create_page":
        return await this.createPage(args);
      case "list_folders":
        // Only the person names their folders, and no page's words are in
        // them: a run that lists them has read nothing beyond this page, so
        // the web's links stay the run's to write. But the names are the
        // person's to keep: the run's one read of the web that may go to
        // any page, whose link could spell them out, is spent, as by a
        // first read (saw), and it reads and writes only the URLs it may
        // then.
        this.saw();
        return unchanged({ folders: await this.hooks.listFolders() });
      case "list_pages": {
        // The titles it lists are read too.
        this.arm();
        this.readPages = true;
        // As many as a model reads at once, each counted as Go counts it:
        // with no parent as null, the more of null and Go's "".
        const pages = await this.hooks.listPages();
        return unchanged({ pages: pages.slice(0, listed(pages, 0)) });
      }
      case "read_other_page": {
        const pageId = textArg(args, "page_id");
        const page = await this.hooks.readOtherPage(pageId);
        this.arm();
        this.readPages = true;
        // One someone else writes or reads keeps the run from making pages
        // of the person's own.
        if (!this.readOthers && this.hooks.pageAlone && !(await this.hooks.pageAlone(pageId))) {
          this.readOthers = true;
        }
        return unchanged(page);
      }
      case "web_search":
        return unchanged(await this.searchWeb(args));
      case "read_web_page":
        return unchanged(await this.readWeb(args));
      case "read_video_transcript":
        return unchanged(await this.readTranscript(args));
      default:
        throw new ToolError(`Unknown tool "${name}"`);
    }
  }

  /**
   * Starts checking URLs, as the agent first reads beyond this page: it may
   * use those on this page as the run found it, and those in the request.
   */
  private arm(): void {
    this.armed = true;
  }

  /**
   * The URLs the person gave the run: on this page as the run found it,
   * but what sites wrote of its blocks (withoutSiteInfo), and in the
   * request.
   */
  private given(): Set<string> {
    if (!this.allowed) {
      const views = pageViews(this.before);
      views.forEach(withoutSiteInfo);
      this.allowed = urlsIn(views);
      urlsIn(this.opts.request ?? "", this.allowed);
    }
    return this.allowed;
  }

  /** Whether the run may write or read u: one the person gave it, or one it found on the web while it has not read beyond this page (Go's given). */
  private may(u: string): boolean {
    u = canonUrl(u);
    return this.given().has(u) || (!this.armed && !!this.found?.has(u));
  }

  /**
   * Whether a block the app loads by itself may hold u: one the person gave
   * the run, or one of the web's it came on itself (sources) while it has
   * not read beyond this page (Go's mayLoad).
   */
  private mayLoad(u: string): boolean {
    u = canonUrl(u);
    return this.given().has(u) || (!this.armed && this.sources.has(u));
  }

  /** Starts keeping the URLs the run finds on the web, as it first reads it (Go's saw). */
  private saw(): Set<string> {
    return this.found ??= new Set();
  }

  /** Keeps a URL the run came on in the web, a source of its own or only a link on one (Go's find). */
  private find(u: string, source: boolean): void {
    u = canonUrl(u);
    this.saw().add(u);
    if (source) this.sources.add(u);
  }

  /**
   * Searches the web through the run's provider (Go's searchWeb). The
   * results' links are found, but not those its answer names, which the
   * model that searched wrote and the query could have asked it for, nor
   * those the query names itself. Once the run has read the web, a query
   * may name no address: a search may read the page at one.
   */
  private async searchWeb(args: Record<string, unknown>): Promise<unknown> {
    if (!this.hooks.searchWeb) throw new ToolError("Searching the web is off.");
    if (this.armed) throw new ToolError(WEB_CLOSED);
    const query = trimSpace(textArg(args, "query"));
    if (this.found && ADDRESS_RE.test(query)) {
      throw new ToolError("Search for words, not a web address: read a page you found with read_web_page.");
    }
    let search: Search;
    try {
      search = await this.hooks.searchWeb(query);
    } catch (e) {
      throw new ToolError(`The search failed: ${e instanceof Error ? e.message : String(e)}`);
    }
    this.saw();
    for (const s of search.sources) {
      const bare = s.url.split("://")[1];
      if (bare === undefined || !query.includes(bare)) this.find(s.url, true);
    }
    return { query, answer: search.answer, results: search.sources };
  }

  /** Reads a web page for the run: any the run names until it has read the web, then only one it may (Go's readWeb). */
  private async readWeb(args: Record<string, unknown>): Promise<unknown> {
    if (!this.hooks.readWebPage) throw new ToolError("Reading the web is off.");
    if (this.armed) throw new ToolError(WEB_CLOSED);
    const url = trimSpace(textArg(args, "url"));
    if (this.found && !this.may(url) && !this.may(url.split("#")[0])) {
      throw new ToolError(
        `After reading the web, read only a page the user gave, one on this page, or one a search found or a page you read links to, which ${url} is not.`,
      );
    }
    // The page is a source of the run's own (LOADS) only when its link was
    // not one picked from a page the run read: the run's first page of the
    // web, one the person gave, or a search's result (Go's readWeb). A link
    // a page had, read, stays a link: reading it does not make it one a
    // block may load.
    const source = !this.found || this.mayLoad(url) || this.mayLoad(url.split("#")[0]);
    if (!source) {
      if (this.followed >= MAX_FOLLOWED_LINKS) {
        throw new ToolError(
          `This run has followed ${MAX_FOLLOWED_LINKS} links from the pages it read, the most one run follows. Work from what you have, or search for what is missing.`,
        );
      }
      // Counted though it fails: its site heard the request all the same.
      this.followed++;
    }
    let page: WebPage;
    try {
      page = await this.hooks.readWebPage(url);
    } catch (e) {
      throw new ToolError(`Could not read ${url}: ${e instanceof Error ? e.message : String(e)}`);
    }
    this.find(page.url, source);
    for (const u of page.links) this.find(u, false);
    return { url: page.url, title: page.title, text: page.text, ...(page.truncated ? { truncated: true } : {}) };
  }

  /**
   * Reads what is said in a video block of the page, from its site's
   * captions, as lines of the times they start and their words (Go's
   * readTranscript). Once the run has read the person's other pages, only a
   * video the page had as it began, or the request names, may be read. The
   * captions, whoever put the video up wrote, are the web's: the run that
   * reads them keeps to the URLs it may write after reading the web
   * (checkUrls).
   */
  private async readTranscript(args: Record<string, unknown>): Promise<unknown> {
    const id = textArg(args, "id");
    const node = this.idx.get(id)?.node;
    if (!node) throw new ToolError(`No block with id "${id}" on this page. Call read_page for current ids.`);
    if (node.type === "recording") {
      throw new ToolError(`Block ${id} is a recording: its transcript is in its view, from read_page.`);
    }
    if (node.type !== "video") throw new ToolError(`Block ${id} is a ${node.type}, not a video.`);
    const link = typeof node.data?.url === "string" ? node.data.url : "";
    const video = videoOf(link);
    if (!video) throw new ToolError(`Block ${id} has no video Leaflet plays.`);
    if (!hasCaptions(video)) {
      throw new ToolError(
        `Leaflet can read what is said only in YouTube's and Vimeo's videos, whose sites give their captions; this one is a ${video.provider} video. Work from what the user tells you of it.`,
      );
    }
    if (!this.hooks.readCaptions) throw new ToolError("Reading videos' transcripts is off.");
    if (this.armed && !this.may(trimSpace(link))) throw new ToolError(WEB_CLOSED);
    const site = video.provider === "vimeo" ? "Vimeo" : "YouTube";
    let captions: Captions;
    try {
      captions = await this.hooks.readCaptions(video);
    } catch (e) {
      if (e instanceof CaptionsError && e.kind === "none") {
        throw new ToolError(
          "The video has no captions to read: its creator gave none, and its site made none. Work from what the user tells you of it.",
        );
      }
      if (e instanceof CaptionsError && e.kind === "refused") {
        throw new ToolError(
          `${site} refused to give the video's captions to Leaflet just now. Tell the user; trying again later may work.`,
        );
      }
      if (e instanceof CaptionsError) {
        throw new ToolError(`${site} won't show this video here (${e.message}), so its captions can't be read.`);
      }
      throw new ToolError(`Could not read the video's captions: ${e instanceof Error ? e.message : String(e)}`);
    }
    // Read with the web or without it.
    this.saw();
    const segments = passages(captions.lines).map((l) => ({ start_ms: l.start_ms, text: l.text }));
    const title = typeof node.data?.title === "string" ? node.data.title : "";
    return {
      id,
      language: captions.language,
      auto_generated: captions.auto,
      transcript: transcriptText(segments),
      ...(title ? { title } : {}),
    };
  }

  /**
   * Keeps what the agent writes to the URLs the person gave from the start,
   * as after reading another page: the conversation's earlier turns may
   * hold what a run read on one.
   */
  keepToGivenUrls(): void {
    this.arm();
  }

  /**
   * Keeps what the agent writes to the URLs it may write after reading the
   * web from the start, with none found yet, as a run that read it does:
   * the conversation's earlier turns may hold what a run read there.
   */
  keepToFoundUrls(): void {
    this.saw();
  }

  /**
   * Refuses blocks that hold a URL the run may not write, in a block the
   * app loads or in text, once the agent has read beyond this page or the
   * web (checkUrls).
   */
  private checkUrls(nodes: NodeJSON[], ...more: unknown[]): void {
    if (!this.armed && !this.found) return;
    // Every URL, as text holds them, and those the app loads by itself.
    const urls = urlsIn(more);
    const loaded = new Set<string>();
    for (const n of nodes) {
      walk(n, (k) => {
        urlsIn(k.data, urls);
        if (LOADS.has(k.type)) urlsIn(k.data?.url, loaded);
      });
    }
    // The first of those refused, in order.
    const first = (set: Set<string>, may: (u: string) => boolean) =>
      [...set].filter((u) => !may(u)).sort((a, b) => (a < b ? -1 : a > b ? 1 : 0))[0];
    const bad = first(urls, (u) => this.may(u));
    if (bad === undefined) {
      const load = first(loaded, (u) => this.mayLoad(u));
      if (load === undefined) return;
      throw new ToolError(
        `After reading the web, a picture, a video or a link card may only hold a URL a search found, of a page you read, that was on this page before you began, or that is in the user's request, which ${load} is not: a page's links are its writer's choice, and the one picked could carry what is on this page, as the app loads it by itself. Link to it in text instead.`,
      );
    }
    if (this.armed) {
      throw new ToolError(
        `After reading another page, what you write may only hold a URL that was on this page before you began or is in the user's request, which ${bad} is not: a link written here could carry what you read, and a later edit could have the app load it. Name the page you read it on with a link to that page instead.`,
      );
    }
    throw new ToolError(
      `After reading the web, what you write may only hold a URL you found there, that was on this page before you began, or that is in the user's request, which ${bad} is not: one made up could carry what is on this page to whoever runs its site, as the app loads it.`,
    );
  }

  // --- indexing ------------------------------------------------------------

  private get root(): NodeJSON {
    this.content.document.children ??= [];
    return this.content.document;
  }

  private locate(id: string): Located {
    const found = this.idx.get(id);
    if (!found) throw new ToolError(`No block with id "${id}" on this page. Call read_page for current ids.`);
    return found;
  }

  /**
   * Options for a call's blocks: ids no block has had, one count for all
   * toward MAX_SPEC_NODES, and whether they were written in Markdown.
   */
  private specOptions(markdown = false): SpecOptions {
    const newId = () => {
      let id = randomId();
      while (this.taken.has(id)) id = randomId();
      this.taken.add(id);
      return id;
    };
    return { newId, author: this.opts.author, markdown };
  }

  /** Where a position puts a block, refused when it names no block here or one that takes none. */
  private resolve(position: Position): { parent: NodeJSON; index: number } {
    if ("after" in position) {
      const at = this.locate(position.after);
      return { parent: at.parent, index: at.index + 1 };
    }
    if ("before" in position) {
      const at = this.locate(position.before);
      return { parent: at.parent, index: at.index };
    }
    if ("parent" in position) {
      const at = this.locate(position.parent);
      if (!LIST_TYPES.has(at.node.type)) {
        throw new ToolError(`Block "${position.parent}" is a ${at.node.type}; only list blocks take children`);
      }
      return { parent: at.node, index: at.node.children?.length ?? 0 };
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

  /** A call's blocks, which specToNode counts toward MAX_SPEC_NODES all together, as doc.SpecsToNodes does. */
  private specs(raw: unknown, field: string): BlockSpec[] {
    if (!Array.isArray(raw) || raw.length === 0) throw new ToolError(`${field} must be a non-empty array of blocks`);
    return raw as BlockSpec[];
  }

  /** Refuses blocks that would take the run past MAX_RUN_BLOCKS. */
  private room(nodes: NodeJSON[]): number {
    let n = 0;
    for (const node of nodes) walk(node, () => n++);
    if (this.written + n > MAX_RUN_BLOCKS) {
      throw new ToolError(
        `This run has written ${this.written} blocks, and one run writes at most ${MAX_RUN_BLOCKS}. Stop here, and say in your reply what is left to do.`,
      );
    }
    return n;
  }

  private record(op: Op): Op {
    this.ops.push(op);
    return op;
  }

  // --- changing the page ---------------------------------------------------
  //
  // Each tool makes its blocks and checks them, then puts them in with
  // these, which cannot fail.

  /** Puts nodes at a place resolve found, and records the insert. */
  private put(target: { parent: NodeJSON; index: number }, position: Position, nodes: NodeJSON[]): Op {
    target.parent.children ??= [];
    target.parent.children.splice(target.index, 0, ...nodes);
    for (const n of nodes) {
      this.idx.add([n], target.parent);
      this.touch(n);
    }
    return this.record({ op: "insert", position, nodes: clone(nodes) });
  }

  /** Puts node in the place of the block at, its children kept or not, and records the replace. */
  private swap(at: Located, node: NodeJSON, keepsChildren: boolean): Op {
    const previous = clone(at.node);
    if (keepsChildren) node.children = at.node.children;
    this.idx.remove(at.node);
    at.parent.children![at.index] = node;
    this.idx.add([node], at.parent);
    const kept = new Set(collectIds(node));
    for (const oldId of collectIds(previous)) {
      if (!kept.has(oldId)) {
        delete this.content.meta.blocks[oldId];
        this.content.meta.deleted[oldId] = this.now();
      }
    }
    if (keepsChildren) {
      this.content.meta.blocks[node.id] = this.now();
    } else {
      this.touch(node);
    }
    return this.record({ op: "replace", id: node.id, node: clone(node), previous });
  }

  /**
   * Whether a replace keeps the block's children: children left out, or
   * none given, keep the ones it had, which only a list block may hold
   * (protocol §2), as the app has it: rewriting a block of another type
   * that holds some, as one from another editor may, is refused even in
   * its own type.
   */
  private keepsChildren(at: Located, spec: BlockSpec, node: NodeJSON): boolean {
    const keeps = !spec.children?.length && (at.node.children?.length ?? 0) > 0 && at.node.type !== "table";
    if (keeps && !LIST_TYPES.has(node.type)) {
      throw new ToolError(
        `Block "${at.node.id}" has nested blocks; a ${node.type} cannot hold them. Move or delete them first, or keep a list type.`,
      );
    }
    return keeps;
  }

  // --- tools ---------------------------------------------------------------

  /**
   * Writes Markdown as blocks: in place of an empty block, the first
   * taking its id and the rest going after it, or at a position.
   */
  write(rawPosition: unknown, markdown: unknown): ToolOutcome {
    const specs = markdownSpecs(typeof markdown === "string" ? markdown : "");
    if (!specs.length) throw new ToolError("markdown is empty");
    if (!isObject(rawPosition) || !givenKeys(rawPosition, ["replace"]).length) {
      return this.insert(normalizePosition(rawPosition), specs, true);
    }
    // An empty block to write in place of says where, whatever else the
    // position has.
    const id = String(rawPosition.replace);
    const at = this.locate(id);
    // Empty as the app's own agents take it: no text at all, so a block
    // the person typed even a space in is not written over.
    const empty = TEXT_TYPES.has(at.node.type) && !at.node.children?.length &&
      deltaOf(at.node).every((op) => op.insert === "");
    if (!empty) throw new ToolError("position.replace takes an empty block only; use replace_block to rewrite one");
    // Every block first, counted together, as the app counts a call's.
    const opts = this.specOptions(true);
    const first = specToNode(specs[0], opts, id);
    const rest = specs.slice(1).map((spec) => specToNode(spec, opts));
    this.checkUrls([first, ...rest]);
    const count = this.room([first, ...rest]);
    const ops = [this.swap(at, first, false)];
    const edits = [{ summary: summaryFor("Wrote", [first]), blockIds: [id] }];
    if (rest.length) {
      const position = { after: id };
      ops.push(this.put(this.resolve(position), position, rest));
      edits.push({ summary: summaryFor("Wrote", rest), blockIds: rest.map((n) => n.id) });
    }
    this.written += count;
    return outcome({ inserted_ids: [id, ...rest.map((n) => n.id)] }, ops, edits);
  }

  /** Inserts blocks, given as specs or, for write_markdown, read from Markdown. */
  insert(position: Position, rawBlocks: unknown, markdown = false): ToolOutcome {
    const specs = this.specs(rawBlocks, "blocks");
    const verb = markdown ? "Wrote" : "Inserted";
    // One set of options for all, so the limit on blocks counts them all.
    const opts = this.specOptions(markdown);
    const nodes = specs.map((spec) => specToNode(spec, opts));
    this.checkUrls(nodes);
    const count = this.room(nodes);
    const op = this.put(this.resolve(position), position, nodes);
    this.written += count;
    const ids = nodes.map((n) => n.id);
    return outcome({ inserted_ids: ids }, [op], [{ summary: summaryFor(verb, nodes), blockIds: ids }]);
  }

  replace(id: string, rawSpec: unknown): ToolOutcome {
    if (!id) throw new ToolError("id is required");
    if (!isObject(rawSpec)) throw new ToolError("block must be a BlockSpec object");
    const spec = rawSpec as unknown as BlockSpec;
    const at = this.locate(id);
    const file = spec.type === "file" && at.node.type === "file";
    const node = file ? fileWithNotes(at.node, spec) : specToNode(spec, this.specOptions(), id);
    this.checkUrls([node]);
    const op = this.swap(at, node, this.keepsChildren(at, spec, node));
    return outcome({ id }, [op], [{ summary: summaryFor("Rewrote", [node]), blockIds: [id] }]);
  }

  move(id: string, position: Position): ToolOutcome {
    if (!id) throw new ToolError("id is required");
    const at = this.locate(id);
    const anchor = anchorOf(position);
    if (anchor) {
      if (anchor === id) throw new ToolError("A block cannot be moved relative to itself");
      if (collectIds(at.node).includes(anchor)) {
        throw new ToolError("A block cannot be moved inside itself");
      }
    }
    // Where it goes, found before it leaves where it is: a position the
    // page cannot take leaves it there.
    this.resolve(position);
    const from = this.positionOf(at);
    at.parent.children!.splice(at.index, 1);
    const target = this.resolve(position);
    target.parent.children ??= [];
    target.parent.children.splice(target.index, 0, at.node);
    this.idx.moved(at.node, target.parent);
    this.content.meta.blocks[id] = this.now();
    delete this.content.meta.deleted[id];
    const op = this.record({ op: "move", id, position, from });
    return outcome({ id }, [op], [{ summary: "Moved a block", blockIds: [id] }]);
  }

  remove(rawIds: unknown): ToolOutcome {
    if (!Array.isArray(rawIds) || rawIds.length === 0) throw new ToolError("ids must be a non-empty array");
    // An ID is text: another value names no block.
    const ids = [...new Set(rawIds.map((id) => typeof id === "string" ? id : ""))];
    const found = ids.map((id) => this.locate(id));
    // Skip ids nested inside another id being deleted.
    const covered = new Set<string>();
    for (const at of found) {
      for (const inner of collectIds(at.node).slice(1)) covered.add(inner);
    }
    const targets = ids.filter((id) => !covered.has(id));
    const previous = deletedPositions(this.root, new Set(targets));
    for (const id of targets) {
      const at = this.locate(id);
      at.parent.children!.splice(at.index, 1);
      this.idx.remove(at.node);
      this.tombstone(at.node);
    }
    const op = this.record({ op: "delete", ids: targets, previous });
    const summary = targets.length > 1 ? `Deleted ${targets.length} blocks` : "Deleted a block";
    return outcome({ deleted_ids: targets }, [op], [{ summary, blockIds: targets }]);
  }

  /**
   * The title and the content a call gives a new page: as blocks, or in
   * Markdown as write_markdown takes it, or neither for an empty page (Go's
   * pageContent).
   */
  private pageContent(args: Record<string, unknown>): { title: string; content: PageContent } {
    // A title that is no text is refused, as the app refuses it.
    if (args.title !== undefined && args.title !== null && typeof args.title !== "string") {
      throw new ToolError("title must be a string");
    }
    const title = clampTitle(trimSpace(String(args.title ?? "")));
    if (!title) throw new ToolError("title is required");
    const markdown = optionalText(args, "markdown");
    const taken = new Set<string>();
    const newId = () => {
      let id = randomId();
      while (taken.has(id)) id = randomId();
      taken.add(id);
      return id;
    };
    const opts: SpecOptions = { newId, author: this.opts.author };
    const content = emptyContent();
    // No blocks, or none listed, is no content, as the app takes it. Either
    // way the blocks share one set of options, which counts them all toward
    // MAX_SPEC_NODES, as a call's are counted (doc.CallNodes).
    const blocks = args.blocks;
    if (blocks !== undefined && blocks !== null && !(Array.isArray(blocks) && blocks.length === 0)) {
      if (markdown !== "") throw new ToolError("Give the page markdown or blocks, not both");
      content.document.children = this.specs(blocks, "blocks").map((spec) => specToNode(spec, opts));
    } else {
      opts.markdown = true;
      content.document.children = markdownSpecs(markdown).map((spec) => specToNode(spec, opts));
    }
    const t = this.now();
    walk(content.document, (n) => {
      if (n !== content.document) content.meta.blocks[n.id] = t;
    });
    return { title, content };
  }

  async createSubpage(args: Record<string, unknown>): Promise<ToolOutcome> {
    // A subpage of a page that is the person's alone is a page of their
    // own too.
    if (this.readOthers) throw new ToolError(READ_OTHERS);
    const { title, content } = this.pageContent(args);
    // Its title shows on this page, in its card, as the next run reads it.
    this.checkUrls(content.document.children ?? [], title);
    const count = this.room(content.document.children ?? []);
    const page = await this.hooks.createSubpage(title, content);
    this.written += count;
    // Its card goes at the end of this page, as the app puts one there, so
    // the page shows its subpage and Undo takes both away.
    const position: Position = { at: "end" };
    const card: NodeJSON = { id: this.specOptions().newId(), type: "subpage", data: { page_id: page.id } };
    const insert = this.put(this.resolve(position), position, [card]);
    const op = this.record({ op: "create_page", page, content: clone(content) });
    return outcome({ page_id: page.id }, [insert, op], [
      { summary: summaryFor("Inserted", [card]), blockIds: [card.id] },
      { summary: `Created subpage “${title}”`, blockIds: [] },
    ]);
  }

  async createPage(args: Record<string, unknown>): Promise<ToolOutcome> {
    if (this.opts.ownPages === false) throw new ToolError(NO_OWN_PAGES);
    if (this.readOthers) throw new ToolError(READ_OTHERS);
    const { title, content } = this.pageContent(args);
    const folderId = optionalText(args, "folder_id");
    // Its title shows in the sidebar, where the next run that lists the
    // person's pages reads it.
    this.checkUrls(content.document.children ?? [], title);
    const count = this.room(content.document.children ?? []);
    const page = await this.hooks.createPage(title, content, folderId || null);
    this.written += count;
    const op = this.record({ op: "create_page", page, content: clone(content) });
    return outcome({ page_id: page.id }, [op], [{ summary: `Created page “${title}”`, blockIds: [] }]);
  }

  // --- the workspace agent's tools ------------------------------------------

  /** Runs a workspace agent's tool (Go's Executor.workspace). */
  private async workspace(w: WorkspaceHooks, name: string, args: Record<string, unknown>): Promise<ToolOutcome> {
    if ((name === "list_pages" || name === "search_pages" || name === "read_page") && !w.reading) {
      throw new ToolError("Reading the person's pages is off");
    }
    switch (name) {
      case "list_pages": {
        // The titles it lists are read too.
        this.arm();
        this.readPages = true;
        const all = await w.listPages();
        const folders = await w.listFolders();
        const views = all.slice(0, MAX_LISTED_PAGES).map(pageView);
        // More would be more than a model reads at once: it finds the rest.
        // The folders and the count go first, so a result cut off keeps them.
        const pages = views.slice(0, listed(views, jsonCost(folders)));
        const more = all.length - pages.length;
        return unchanged({ folders, ...(more > 0 ? { more_pages: more } : {}), pages });
      }
      case "search_pages": {
        const query = trimSpace(textArg(args, "query"));
        this.arm();
        this.readPages = true;
        const results = await w.searchPages(query);
        // What people wrote in a page shared with them is not read: it is
        // found, but none of its text shown; nor of one the run does not
        // know, as one made since it began.
        for (const r of results as Record<string, unknown>[]) {
          const page = typeof r.page_id === "string" && r.page_id ? await w.page(r.page_id) : null;
          if (!page || page.people) delete r.snippet;
        }
        return unchanged({ results });
      }
      case "read_page": {
        const page = await this.workspacePage(w, args, "page_id");
        if (page.people) {
          throw new ToolError(
            `“${page.title}” is shared with other people, who write in it too: the agent here reads none of it. To work with it, ask the agent on that page`,
          );
        }
        this.arm();
        this.readPages = true;
        return unchanged(await w.readPage(page.id));
      }
      case "list_folders":
        // Only the person names their folders: as on a page, listing them
        // reads nothing someone else wrote, and spends the run's one read
        // of the web that may go to any page, whose link could spell them
        // out (saw).
        this.saw();
        return unchanged({ folders: await w.listFolders() });
      case "create_page":
        return await this.workspaceCreate(w, args);
      case "move_page":
        return await this.workspaceMove(w, args);
      case "rename_page":
        return await this.workspaceRename(w, args);
      case "set_page_tags":
        return await this.workspaceTags(w, args);
      case "create_folder":
        return await this.workspaceFolder(w, args);
      case "web_search":
        return unchanged(await this.searchWeb(args));
      case "read_web_page":
        return unchanged(await this.readWeb(args));
      default:
        throw new ToolError(`Unknown tool "${name}"`);
    }
  }

  /** The page args names at key, one of the person's. */
  private async workspacePage(w: WorkspaceHooks, args: Record<string, unknown>, key: string): Promise<WorkspacePage> {
    const id = textArg(args, key);
    const page = await w.page(id);
    if (!page) {
      throw new ToolError(`No page "${id}" of the person's. Call list_pages or search_pages for the pages' ids`);
    }
    return page;
  }

  /** Where args puts a page: inside a page of the person's that no one else shares, or at the top level, in a folder or none. */
  private async place(
    w: WorkspaceHooks,
    args: Record<string, unknown>,
  ): Promise<{ parent: WorkspacePage | null; folder: string }> {
    const parentId = optionalText(args, "parent_id");
    const folder = optionalText(args, "folder_id");
    if (!parentId) return { parent: null, folder };
    if (folder) throw new ToolError("Give parent_id or folder_id, not both: a page inside another is in no folder");
    const parent = await this.workspacePage(w, args, "parent_id");
    changeable(parent);
    return { parent, folder: "" };
  }

  private async workspaceCreate(w: WorkspaceHooks, args: Record<string, unknown>): Promise<ToolOutcome> {
    const { title, content } = this.pageContent(args);
    // A page made inside another changes it, with its card.
    if (optionalText(args, "parent_id")) await w.changing?.();
    const { parent, folder } = await this.place(w, args);
    this.checkUrls(content.document.children ?? [], title);
    const count = this.room(content.document.children ?? []);
    const made = await w.createPage(title, content, parent?.id ?? "", folder);
    this.written += count;
    const op = this.record({
      op: "create_page",
      page: { ...made, parent_id: parent?.id ?? null, folder_id: folder || null, title },
      content: clone(content),
    });
    const summary = parent ? `Created subpage “${title}”` : `Created page “${title}”`;
    return outcome({ page_id: made.id }, [op], [{ summary, blockIds: [] }]);
  }

  private async workspaceMove(w: WorkspaceHooks, args: Record<string, unknown>): Promise<ToolOutcome> {
    await w.changing?.();
    const page = await this.workspacePage(w, args, "page_id");
    changeable(page);
    const { parent, folder } = await this.place(w, args);
    if (parent?.id === page.id) throw new ToolError("A page cannot move inside itself");
    const { from, to } = await w.movePage(page.id, parent?.id ?? "", folder);
    if ((from.parent_id ?? "") === (to.parent_id ?? "") && (from.folder_id ?? "") === (to.folder_id ?? "")) {
      return unchanged({ page_id: page.id });
    }
    // An untitled page is named "", as the app says it in the person's language.
    const op = this.record({ op: "move_page", page_id: page.id, title: keptTitle(page), place: to, previous: from });
    return outcome({ page_id: page.id }, [op], [{ summary: `Moved “${page.title}”`, blockIds: [] }]);
  }

  private async workspaceRename(w: WorkspaceHooks, args: Record<string, unknown>): Promise<ToolOutcome> {
    await w.changing?.();
    const page = await this.workspacePage(w, args, "page_id");
    changeable(page);
    // Without the spaces around it, as a new page's, and no longer than the
    // pages table takes.
    const title = clampTitle(trimSpace(textArg(args, "title")));
    this.checkUrls([], title);
    const had = keptTitle(page);
    if (title === had) return unchanged({ page_id: page.id });
    await w.renamePage(page.id, title);
    const op = this.record({ op: "rename_page", page_id: page.id, title, previous: had });
    return outcome({ page_id: page.id }, [op], [{ summary: `Renamed “${page.title}” to “${title}”`, blockIds: [] }]);
  }

  private async workspaceTags(w: WorkspaceHooks, args: Record<string, unknown>): Promise<ToolOutcome> {
    await w.changing?.();
    const page = await this.workspacePage(w, args, "page_id");
    changeable(page);
    const raw = args.tags;
    if (!Array.isArray(raw) || raw.some((t) => typeof t !== "string")) {
      throw new ToolError("tags must be a list of words");
    }
    const tags = raw as string[];
    this.checkUrls([], tags);
    const kept = await w.setTags(page.id, tags);
    if (kept.length === page.tags.length && kept.every((t, i) => t === page.tags[i])) {
      return unchanged({ page_id: page.id, tags: kept });
    }
    const op = this.record({
      op: "set_page_tags",
      page_id: page.id,
      title: keptTitle(page),
      tags: kept,
      previous: page.tags,
    });
    return outcome({ page_id: page.id, tags: kept }, [op], [{ summary: `Tagged “${page.title}”`, blockIds: [] }]);
  }

  private async workspaceFolder(w: WorkspaceHooks, args: Record<string, unknown>): Promise<ToolOutcome> {
    const name = trimSpace(textArg(args, "name"));
    this.checkUrls([], name);
    const folder = await w.createFolder(name);
    const op = this.record({ op: "create_folder", folder });
    return outcome({ folder_id: folder.id }, [op], [{ summary: `Created folder “${name}”`, blockIds: [] }]);
  }
}
