/**
 * The workspace agent: a chat of the person's own, not of a page, whose
 * agent finds, reads, makes and files their pages, and changes no page's
 * blocks, which the agent on the page does (docs/protocol.md §2), as
 * internal/agents/workspace.go has it.
 */
import { fields, type PageContent, trimSpace } from "./blocks.ts";
import type { ToolDef } from "./openai.ts";
import { MAX_LISTED_PAGES, type PagePlace, pageSchema, tool } from "./tools.ts";
import { goLower } from "./url.ts";

/** A page as the workspace agent sees it (Go's WorkspacePage). */
export interface WorkspacePage {
  id: string;
  title: string;
  parent_id?: string;
  folder_id?: string;
  tags: string[];
  /**
   * A page someone else reads or writes: shared with others, with a
   * guest's comment or a public link. The agent changes nothing of it, nor
   * puts a page in it: what someone else wrote there could have it copy
   * the person's other pages in.
   */
  shared?: boolean;
  /**
   * A page shared with other people, who write in it too: the agent reads
   * none of it either, as what they wrote could steer what it does to the
   * person's other pages (Go's People).
   */
  people?: boolean;
  /** A page of no title, whose title is "Untitled": renamed, it had "", for Undo to give back. */
  untitled?: boolean;
}

/** A folder as the agent sees it. */
export interface WorkspaceFolder {
  id: string;
  space_id: string;
  name: string;
  sort_key: string;
}

/**
 * What the workspace agent's tools need: the person's pages, those of
 * their own space but templates, and the changes to them (Go's Workspace).
 */
export interface WorkspaceHooks {
  /** Lets the agent read the pages, as allow_cross_page does: without it, it reads none, but makes and files them. */
  reading: boolean;
  listPages(): Promise<WorkspacePage[]>;
  /** One of the person's pages, null for one they have not. */
  page(id: string): Promise<WorkspacePage | null>;
  readPage(id: string): Promise<unknown>;
  searchPages(query: string): Promise<unknown[]>;
  listFolders(): Promise<{ id: string; name: string }[]>;
  /** Makes a page, in folderId at the top level or inside parentId, linked at its end, and returns its id. */
  createPage(title: string, content: PageContent, parentId: string, folderId: string): Promise<string>;
  /** Moves a page, returning where it was and where it went. */
  movePage(id: string, parentId: string, folderId: string): Promise<{ from: PagePlace; to: PagePlace }>;
  renamePage(id: string, title: string): Promise<void>;
  /** Sets a page's tags, returning them as the page keeps them (normalizeTags). */
  setTags(id: string, tags: string[]): Promise<string[]>;
  createFolder(name: string): Promise<WorkspaceFolder>;
}

/** The workspace agent's tools: with reading, those that read the person's pages too (Go's WorkspaceToolDefinitions). */
export function workspaceToolDefinitions(reading: boolean): ToolDef[] {
  const pageId = { type: "string", description: "The page's id, from list_pages or search_pages, or the open page's." };
  const out: ToolDef[] = [];
  if (reading) {
    out.push(
      tool(
        "list_pages",
        `List the person's pages, at most ${MAX_LISTED_PAGES} (search_pages finds the others): their ids, titles and tags, the page or folder each is in, and whether others share it; and their folders.`,
      ),
      tool(
        "search_pages",
        "Find the person's pages by title, tag or text. Words starting with # (as #work) keep to the pages with those tags.",
        { query: { type: "string" } },
        ["query"],
      ),
      tool(
        "read_page",
        "Read one of the person's pages: every block with its id, its tags and its subpages. Read-only.",
        { page_id: pageId },
        ["page_id"],
      ),
    );
  }
  out.push(
    tool("list_folders", "List the person's folders (names and ids)."),
    tool(
      "create_page",
      "Create a page, optionally with content: at the top level of the person's pages, in one of their folders, or inside one of their pages, at whose end a link to it goes.",
      pageSchema({
        parent_id: { type: "string", description: "The page to put it inside; none for the top level." },
        folder_id: { type: "string", description: "The folder to put it in, at the top level; not with parent_id." },
      }),
      ["title"],
    ),
    tool(
      "move_page",
      "Move a page, with its subpages: inside another page, at whose end a link to it goes; into a folder; or with neither to the top level outside folders.",
      {
        page_id: pageId,
        parent_id: { type: "string", description: "The page to put it inside." },
        folder_id: { type: "string", description: "The folder to put it in, at the top level; not with parent_id." },
      },
      ["page_id"],
    ),
    tool("rename_page", "Change a page's title.", { page_id: pageId, title: { type: "string" } }, ["page_id", "title"]),
    tool(
      "set_page_tags",
      "Set a page's tags, replacing the ones it has: an empty list takes them all off. Tags are short words or phrases, without #; reuse the person's where they fit.",
      { page_id: pageId, tags: { type: "array", items: { type: "string" } } },
      ["page_id", "tags"],
    ),
    tool("create_folder", "Create a folder for top-level pages, last among the person's folders.", {
      name: { type: "string" },
    }, ["name"]),
  );
  return out;
}

/** The most tags a page has, and the longest one, in characters, as the pages table checks (Go's maxTags, maxTagLen). */
export const MAX_TAGS = 50;
export const MAX_TAG_LEN = 64;

/**
 * Tags tidied as people type them (store.NormalizeTags): each trimmed,
 * without a leading #, its spaces one, cut to MAX_TAG_LEN; the blank ones
 * and those repeated, in any case, left out, the first spelling kept, in
 * order.
 */
export function normalizeTags(tags: string[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of tags) {
    let t = fields(trimSpace(raw).replace(/^#+/, "")).join(" ");
    const chars = [...t];
    if (chars.length > MAX_TAG_LEN) t = trimSpace(chars.slice(0, MAX_TAG_LEN).join(""));
    const k = goLower(t);
    if (!t || seen.has(k) || out.length === MAX_TAGS) continue;
    seen.add(k);
    out.push(t);
  }
  return out;
}

/** A search's tag filters, the words starting with #, without it, and the rest of its words (store.SplitTagQuery). */
export function splitTagQuery(query: string): { tags: string[]; rest: string } {
  const tags: string[] = [];
  const words: string[] = [];
  for (const w of fields(query)) {
    if (w.startsWith("#")) {
      const t = w.replace(/^#+/, "");
      if (t) tags.push(t);
      continue;
    }
    words.push(w);
  }
  return { tags, rest: words.join(" ") };
}
