// Agent worker (protocol §4): runs one ai_jobs row against the user's cloud
// model provider, applying validated page-edit tool calls and streaming them
// back as NDJSON. index.ts serves it; tests give it a project of their own.

import type { SupabaseClient, User } from "jsr:@supabase/supabase-js@2.118.0";
import { corsHeaders, errorResponse, HttpError, preflight, readJson } from "../_shared/cors.ts";
import { type Clients, loadProvider, providerSecret, requireTokenUntil, requireUser } from "../_shared/supabase.ts";
import {
  type chatCompletion,
  type ChatMessage,
  type ChatResult,
  type Connection,
  LOCAL_PRESETS,
  normalizeBaseUrl,
  PRESET_NAMES,
  ProviderError,
  retryWait,
  type ToolDef,
} from "../_shared/openai.ts";
import {
  type BlockView,
  emptyContent,
  isPageContent,
  type NodeJSON,
  normalizeContent,
  type PageContent,
  plainText,
  randomId,
  type RecordingInfo,
  trimSpace,
  type VideoRef,
  walk,
} from "../_shared/blocks.ts";
import {
  agentViews,
  applyOps,
  MAX_LISTED_PAGES,
  MAX_ROUND_CALLS,
  type Op,
  overwrites,
  PAGE_OPS,
  PageEditSession,
  type PageSummary,
  TOO_MANY_CALLS,
  toolDefinitions,
  ToolError,
  type ToolHooks,
  type ToolOutcome,
  unseenOps,
  webToolDefinitions,
} from "../_shared/tools.ts";
import {
  pageFits,
  type Surface,
  systemPrompt,
  userMessage,
  workspaceMessage,
  workspacePrompt,
} from "../_shared/prompt.ts";
import {
  normalizeTags,
  splitTagQuery,
  type WorkspaceHooks,
  type WorkspacePage,
  workspaceToolDefinitions,
} from "../_shared/workspace.ts";
import { isTimeout } from "../_shared/net.ts";
import { readPage } from "../_shared/page.ts";
import { canSearch, searchWeb } from "../_shared/search.ts";
import { readCaptions } from "../_shared/captions.ts";
import { SAVE_MS } from "../_shared/deadline.ts";
import { lastKey, newKeyAfter } from "../_shared/sortkey.ts";

const MAX_ROUNDS = 12;
const UNREADABLE = "The page's content cannot be read, so the agent left it as it is.";
const TOOL_RESULT_LIMIT = 60_000;
const HEARTBEAT_MS = 15_000;
// A call with less time than this left is not started.
const MIN_CALL_MS = 5_000;

export interface AgentDeps {
  clients: Clients;
  /** Asks the model: chatCompletion, or a test's. */
  chat: typeof chatCompletion;
  /**
   * When a run stops calling the model, from its start, keeping time to
   * save what it did: an Edge Function is stopped at its wall-clock limit
   * (150 s on the free plan, 400 s on paid plans).
   */
  deadlineMs: number;
  /** One model call; a call never runs past the deadline either. */
  callTimeoutMs: number;
  /** Keeps the run going, and saving, after the client disconnects (EdgeRuntime.waitUntil). */
  waitUntil?(work: Promise<unknown>): void;
  /** Waits ms before asking a busy model again; tests wait less. */
  sleep?(ms: number): Promise<void>;
  /** Searches the web through the provider, and reads a page of it: searchWeb and readPage, or a test's. */
  searchWeb?: typeof searchWeb;
  readPage?: typeof readPage;
  /** Reads a video's captions from its site: readCaptions, or a test's. */
  readCaptions?: typeof readCaptions;
}

interface Job {
  id: string;
  updated_at: string;
  /** None for a workspace job, of the person's pages rather than of one. */
  page_id: string | null;
  user_id: string;
  surface: Surface;
  focus_block_ids: string[] | null;
  comment_id: string | null;
  conversation_id: string | null;
  provider_id: string | null;
  model: string | null;
  status: string;
  request: {
    instruction?: string;
    selection_text?: string;
    allow_cross_page?: boolean;
    allow_web?: boolean;
    /** A workspace job's: the page open as the person asked, and those the app has a public link open to. */
    open_page_id?: string;
    public_page_ids?: string[];
  } | null;
}

interface CommentRow {
  id: string;
  block_id: string;
  parent_comment_id: string | null;
  author_id: string | null;
  author_kind: "user" | "agent" | "guest";
  agent_name: string | null;
  body: string;
  resolved: boolean;
  created_at: string;
  deleted_at: string | null;
}

type Emit = (event: Record<string, unknown>) => void;

export async function handle(req: Request, deps: AgentDeps): Promise<Response> {
  const started = Date.now();
  // A job still running this long after it was claimed was stopped by the
  // platform before it could finish: it may be claimed again.
  const staleMs = deps.deadlineMs + 60_000;
  const pre = preflight(req);
  if (pre) return pre;
  try {
    const { job_id } = await readJson<{ job_id?: string }>(req);
    if (!job_id) throw new HttpError(400, "job_id is required");
    const uc = deps.clients.user(req);
    const user = await requireUser(req, uc);
    // The run saves as the caller up to its end, its failure too.
    requireTokenUntil(req, started + deps.deadlineMs + SAVE_MS);

    const { data: job, error } = await uc
      .from("ai_jobs")
      .select("*")
      .eq("id", job_id)
      .maybeSingle();
    if (error) throw new Error(error.message);
    if (!job) throw new HttpError(404, "Job not found");
    const staleBefore = new Date(started - staleMs).toISOString();
    const stale = job.status === "running" && Date.parse(job.updated_at) < started - staleMs;
    if (job.status !== "queued" && !stale) throw new HttpError(409, `Job is already ${job.status}`);

    // Runs the platform stopped before they finished stay running: fail
    // the caller's others, and claim this one again if it is one.
    const { error: sweepError } = await uc
      .from("ai_jobs")
      .update({ status: "failed", error: "The run stopped before it finished" })
      .eq("user_id", user.id)
      .eq("status", "running")
      .lt("updated_at", staleBefore)
      .neq("id", job_id);
    if (sweepError) console.error("ai_jobs sweep:", sweepError.message);

    // Claim the job so a retry cannot run it twice. Claiming moves
    // updated_at on, so two retries of a stale job cannot both claim it.
    let claim = uc.from("ai_jobs").update({ status: "running", error: null }).eq("id", job_id);
    claim = stale ? claim.eq("status", "running").lt("updated_at", staleBefore) : claim.eq("status", "queued");
    const { data: claimed, error: claimError } = await claim.select("id");
    if (claimError) throw new Error(claimError.message);
    if (!claimed?.length) throw new HttpError(409, "Job is already running");

    const encoder = new TextEncoder();
    let open = true;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        const emit: Emit = (event) => {
          if (!open) return;
          try {
            controller.enqueue(encoder.encode(JSON.stringify(event) + "\n"));
          } catch {
            open = false;
          }
        };
        const heartbeat = setInterval(() => emit({ type: "status", status: "running" }), HEARTBEAT_MS);
        const work = runJob(uc, deps.clients.service(), user, job as Job, emit, started + deps.deadlineMs, deps)
          .finally(() => {
            clearInterval(heartbeat);
            if (open) {
              open = false;
              try {
                controller.close();
              } catch {
                // already closed by the client
              }
            }
          });
        deps.waitUntil?.(work);
      },
      cancel() {
        open = false;
      },
    });

    return new Response(stream, {
      headers: {
        ...corsHeaders,
        "Content-Type": "application/x-ndjson; charset=utf-8",
        "Cache-Control": "no-cache",
      },
    });
  } catch (e) {
    return errorResponse(e);
  }
}

/**
 * An answer with the model's thinking left out, as the app leaves it out
 * (Go's withoutThinking), in any case: each block of it; all before a
 * closing tag that none opened, as models whose chat template opens the
 * tag write it (DeepSeek-R1's distills); and all after an opening tag the
 * answer ends without closing.
 */
export function withoutThinking(text: string): string {
  let s = text.replace(/<think>[\s\S]*?<\/think>/gi, "");
  const closes = [...s.matchAll(/<\/think>/gi)];
  if (closes.length) {
    const last = closes[closes.length - 1];
    s = s.slice(last.index! + last[0].length);
  }
  const open = s.search(/<think>/i);
  if (open >= 0) s = s.slice(0, open);
  return s.trim();
}

function truncate(text: string, limit: number): string {
  return text.length > limit ? `${text.slice(0, limit)}…(truncated)` : text;
}

/** A page's title as the app shows it: "Untitled" for a blank one (store.TitleOr). */
function displayTitle(title: string | null | undefined): string {
  return trimSpace(title ?? "") ? title! : "Untitled";
}

async function rows<T>(query: PromiseLike<{ data: T[] | null; error: { message: string } | null }>): Promise<T[]> {
  const { data, error } = await query;
  if (error) throw new Error(error.message);
  return data ?? [];
}

/** The rows allRows asks for at a time: max_rows (supabase/config.toml), the most the API answers a read with. */
const PAGE_ROWS = 1000;

/**
 * Every row a query finds. The API answers a read with max_rows at most,
 * however many it asks for, and says nothing of the rest: they are asked
 * for a page at a time, until one comes empty, so a project that answers
 * with fewer still gives them all. query makes the read again each time,
 * in an order that tells every row apart.
 */
async function allRows<T>(
  query: () => {
    range(from: number, to: number): PromiseLike<{ data: T[] | null; error: { message: string } | null }>;
  },
): Promise<T[]> {
  const out: T[] = [];
  for (;;) {
    const page = await rows<T>(query().range(out.length, out.length + PAGE_ROWS - 1));
    if (!page.length) return out;
    out.push(...page);
  }
}

/** By sort key, then id, as the app's store orders them in its SQLite. */
function bySortKey(a: { id: string; sort_key: string }, b: { id: string; sort_key: string }): number {
  return a.sort_key < b.sort_key ? -1 : a.sort_key > b.sort_key ? 1 : a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/** The folders of a space, not deleted, in their order, as list_folders lists them (Go's spaceFolders). */
async function foldersOf(uc: SupabaseClient, space: string): Promise<{ id: string; name: string }[]> {
  return (await allRows<{ id: string; name: string; sort_key: string }>(() =>
    uc.from("folders").select("id, name, sort_key").eq("space_id", space).is("deleted_at", null).order("id")
  )).sort(bySortKey).map(({ id, name }) => ({ id, name }));
}

/** Whether id is a folder of space, not deleted: one a page can be made or put in (Go's spaceFolder). */
async function folderIn(uc: SupabaseClient, space: string, id: string): Promise<boolean> {
  const { data } = await uc.from("folders").select("id").eq("id", id).eq("space_id", space).is("deleted_at", null)
    .maybeSingle();
  return !!data;
}

/** A call naming a folder the person has not (Go's noFolder). */
function noFolder(id: string): ToolError {
  return new ToolError(`No folder "${id}". Call list_folders for the folders' ids.`);
}

/** The recordings on a page, by ID, with their transcripts, for the blocks that show them. */
async function recordingsOf(uc: SupabaseClient, pageId: string): Promise<Map<string, RecordingInfo>> {
  const recordings = new Map<string, RecordingInfo>();
  for (
    const r of await rows<
      { id: string; duration_ms: number; status: string; transcript: RecordingInfo["transcript"] }
    >(
      uc.from("recordings").select("id, duration_ms, status, transcript").eq("page_id", pageId),
    )
  ) {
    recordings.set(r.id, { id: r.id, duration_ms: r.duration_ms, status: r.status, transcript: r.transcript ?? [] });
  }
  return recordings;
}

/** Whether a page's public link counts now: its lease, public_link_until, is still to come. */
function publiclyLinked(until: unknown): boolean {
  return typeof until === "string" && Date.parse(until) > Date.now();
}

/** The provider a job runs on, its model and how to reach it. */
async function providerOf(uc: SupabaseClient, sc: SupabaseClient, job: Job) {
  if (!job.provider_id) throw new Error("Choose a model in Settings › Agent providers first");
  const provider = await loadProvider(uc, job.provider_id);
  if (provider.is_local || LOCAL_PRESETS.has(provider.preset)) {
    throw new Error("Local models run on your device, not on the server");
  }
  const apiKey = await providerSecret(sc, provider.id);
  if (provider.auth_kind !== "none" && !apiKey) {
    throw new Error(`Add an API key for ${provider.name || provider.preset} in Settings › Agent providers`);
  }
  const model = job.model || provider.default_model;
  if (!model) throw new Error(`Choose a model for ${provider.name || provider.preset} in Settings › Agent providers`);
  const conn: Connection = {
    baseUrl: normalizeBaseUrl(provider.base_url, provider.preset),
    apiKey,
    preset: provider.preset,
  };
  return { provider, model, conn, agentName: provider.name || PRESET_NAMES[provider.preset] || "Agent" };
}

/**
 * The earlier turns of a job's conversation that are done, the latest
 * first: each one's request and reply, whether it kept to its page and
 * whether its URL guard was on, not its ops, which may be many.
 */
async function historyOf(uc: SupabaseClient, job: Job) {
  if (!job.conversation_id) return [];
  return await rows<{ instruction: string | null; reply: string | null; kept_to_page: unknown; guarded: unknown }>(
    uc.from("ai_jobs")
      .select(
        "instruction:request->>instruction, reply:result->>reply, kept_to_page:result->kept_to_page, guarded:result->guarded",
      )
      .eq("conversation_id", job.conversation_id)
      .eq("status", "done")
      .neq("id", job.id)
      .order("created_at", { ascending: false })
      .limit(10),
  );
}

/** The person's name as their profile has it, or their address's start. */
async function userNameOf(uc: SupabaseClient, user: User): Promise<string> {
  const { data: profile } = await uc.from("profiles").select("display_name").eq("id", user.id).maybeSingle();
  return profile?.display_name || user.email?.split("@")[0] || "the user";
}

/** A page row a run makes. */
interface PageRow {
  space_id: string;
  parent_id: string | null;
  folder_id?: string | null;
  title: string;
  sort_key: string;
  is_template: boolean;
}

/** Makes a page with its content, or neither, as the caller. */
async function insertPageOf(uc: SupabaseClient, sc: SupabaseClient, user: User, row: PageRow, content: PageContent) {
  const id = crypto.randomUUID();
  const { error: insertError } = await uc.from("pages").insert({ id, ...row });
  if (insertError) throw new ToolError(`Could not create the page: ${insertError.message}`);
  const { error: contentError } = await uc.from("page_content").insert({
    page_id: id,
    content,
    plain_text: plainText(content),
    updated_by: user.id,
  });
  if (contentError) {
    // A page without its content would stay, empty, with no op to undo it
    // by: it goes too. Only its space's owner may delete a page, so the
    // service role deletes the one the run just made.
    const { error: removeError } = await sc.from("pages").delete().eq("id", id);
    if (removeError) console.error("page without its content:", removeError.message);
    throw new ToolError(`Could not save the page: ${contentError.message}`);
  }
  const { is_template: _, ...made } = row;
  return { id, ...made };
}

/**
 * Changes page id's content with edit, which reports whether it changed
 * it, as the app's changes from outside a page do (core.Pages.EditPage):
 * saved over the revision read, read again while someone saves it
 * meanwhile, and kept in no history.
 */
async function editContent(
  uc: SupabaseClient,
  user: User,
  id: string,
  edit: (c: PageContent) => boolean,
): Promise<void> {
  for (let attempt = 0; attempt < 3; attempt++) {
    const { data, error } = await uc.from("page_content").select("content, revision").eq("page_id", id).maybeSingle();
    if (error) throw new Error(error.message);
    if (data && !isPageContent(data.content)) return;
    const content = normalizeContent(data?.content);
    if (!edit(content)) return;
    const values = { content, plain_text: plainText(content), updated_by: user.id };
    if (!data) {
      const { error: insertError } = await uc.from("page_content").insert({ page_id: id, ...values });
      if (!insertError) return;
      if (insertError.code !== "23505") throw new Error(insertError.message);
      continue;
    }
    const { data: saved, error: saveError } = await uc.from("page_content").update(values).eq("page_id", id)
      .eq("revision", data.revision).select("revision");
    if (saveError) throw new Error(saveError.message);
    if (saved?.length) return;
  }
  throw new ToolError("The page kept changing while its subpage's link was put in it");
}

/** Puts a card for subpage id at the end of page parent, unless it has one (core.Pages.LinkSubpage). */
function linkSubpage(uc: SupabaseClient, user: User, parent: string, id: string): Promise<void> {
  return editContent(uc, user, parent, (c) => {
    const kids = c.document.children ??= [];
    let has = false;
    walk(c.document, (n) => has ||= n.type === "subpage" && n.data?.page_id === id);
    if (has) return false;
    const card = { id: randomId(), type: "subpage", data: { page_id: id } };
    kids.push(card);
    c.meta.blocks[card.id] = Date.now();
    return true;
  });
}

/** Takes the cards for subpage id out of page parent, which keeps one block to type in (core.Pages.UnlinkSubpage). */
function unlinkSubpage(uc: SupabaseClient, user: User, parent: string, id: string): Promise<void> {
  return editContent(uc, user, parent, (c) => {
    let gone = false;
    const drop = (nodes: NodeJSON[] | undefined): NodeJSON[] | undefined =>
      nodes?.filter((n) => {
        if (n.type === "subpage" && n.data?.page_id === id) {
          gone = true;
          delete c.meta.blocks[n.id];
          c.meta.deleted[n.id] = Date.now();
          return false;
        }
        n.children = drop(n.children);
        return true;
      });
    c.document.children = drop(c.document.children);
    if (gone && !c.document.children?.length) c.document.children = emptyContent().document.children;
    return gone;
  });
}

/** The page tools of no page, which a workspace run's session is given (it has its own). */
const NO_PAGE: ToolHooks = {
  readPage: () => Promise.resolve({}),
  createSubpage: () => Promise.reject(new ToolError("A workspace run is of no page")),
  createPage: () => Promise.reject(new ToolError("A workspace run is of no page")),
  listPages: () => Promise.resolve([]),
  readOtherPage: () => Promise.reject(new ToolError("A workspace run is of no page")),
  listFolders: () => Promise.resolve([]),
};

/**
 * Runs a workspace job: a chat of the person's own, of no page, whose
 * agent finds, reads, makes and files the pages of their own space, and
 * changes no page's blocks (internal/agents/workspace.go). It changes no
 * page someone else reads or writes, nor puts a page in one: one shared
 * with others, commented on by a guest, or with a public link open, as the
 * app that serves it keeps on the page (public_link_until) or the app that
 * asks says (public_page_ids).
 */
async function runWorkspaceJob(
  uc: SupabaseClient,
  sc: SupabaseClient,
  user: User,
  job: Job,
  emit: Emit,
  deadline: number,
  deps: AgentDeps,
): Promise<void> {
  emit({ type: "status", status: "running" });
  let session: PageEditSession | undefined;
  const edits: { summary: string; block_ids: string[]; tool: string }[] = [];
  try {
    const instruction = String(job.request?.instruction ?? "").trim();
    if (!instruction) throw new Error("The request has no instruction");
    const { provider, model, conn, agentName } = await providerOf(uc, sc, job);
    const userName = await userNameOf(uc, user);
    const { data: space, error: spaceError } = await uc.from("spaces").select("id").eq("owner_id", user.id)
      .maybeSingle();
    if (spaceError) throw new Error(spaceError.message);
    if (!space) throw new Error("You have no space of your own");
    const own = space.id as string;
    const reading = job.request?.allow_cross_page === true;
    const publicIds = new Set(job.request?.public_page_ids ?? []);

    // --- the person's pages ---------------------------------------------
    // Read once a run, all of them, and kept as the run changes them: who
    // else has a page comes from its shares, those of the pages it is
    // inside too, its guests' comments and its public link, which no change
    // of a run's changes, as it changes no page someone else has.
    type Row = {
      id: string;
      title: string;
      parent_id: string | null;
      folder_id: string | null;
      tags: string[] | null;
      sort_key: string;
      public_link_until: string | null;
    };
    let world: Promise<{ rows: Map<string, Row>; shares: Set<string>; guested: Set<string> }> | undefined;
    const rowsNow = () =>
      world ??= Promise.all([
        allRows<Row>(() =>
          uc.from("pages").select("id, title, parent_id, folder_id, tags, sort_key, public_link_until").eq(
            "space_id",
            own,
          )
            .is("deleted_at", null).eq("is_template", false).order("id")
        ),
        // Of the pages of the person's own space alone.
        allRows<{ page_id: string }>(() =>
          uc.from("page_permissions").select("page_id, pages!inner(space_id)").eq("pages.space_id", own)
            .neq("user_id", user.id).order("page_id").order("user_id")
        ),
        allRows<{ page_id: string }>(() =>
          uc.from("comments").select("page_id, pages!inner(space_id)").eq("pages.space_id", own)
            .eq("author_kind", "guest").order("id")
        ),
      ]).then(([list, shares, guested]) => ({
        rows: new Map(list.map((r) => [r.id, r])),
        shares: new Set(shares.map((r) => r.page_id)),
        guested: new Set(guested.map((r) => r.page_id)),
      })).catch((e) => {
        // Read again by the next call.
        world = undefined;
        throw e;
      });
    // A page as the agent sees it, from its row.
    const pageOf = async (id: string): Promise<WorkspacePage | null> => {
      const { rows: byId, shares, guested } = await rowsNow();
      const r = byId.get(id);
      if (!r) return null;
      // Shared with other people, it or a page it is inside.
      const seen = new Set<string>();
      let people = false;
      for (let p: Row | undefined = r; p && !people && !seen.has(p.id); p = byId.get(p.parent_id ?? "")) {
        seen.add(p.id);
        people = shares.has(p.id);
      }
      const shared = people || guested.has(r.id) || publicIds.has(r.id) || publiclyLinked(r.public_link_until);
      return {
        id: r.id,
        title: displayTitle(r.title),
        ...(r.parent_id ? { parent_id: r.parent_id } : {}),
        ...(r.folder_id ? { folder_id: r.folder_id } : {}),
        tags: r.tags ?? [],
        ...(shared ? { shared: true } : {}),
        ...(people ? { people: true } : {}),
        ...(trimSpace(r.title ?? "") ? {} : { untitled: true }),
      };
    };
    // The row of a page the run changes, which a call to page() has read.
    const rowOf = async (id: string) => (await rowsNow()).rows.get(id)!;
    // Last among the pages inside parent, or the folder's at the top level.
    const keyLast = async (parent: string, folder: string, skip = "") => {
      const siblings = [...(await rowsNow()).rows.values()].filter((r) =>
        r.id !== skip && (parent ? r.parent_id === parent : !r.parent_id && (r.folder_id ?? "") === folder)
      );
      return newKeyAfter(lastKey(siblings.map((r) => r.sort_key)));
    };
    // A card that could not be put in a page, or taken out of it, leaves
    // the page made or moved all the same, as the app's moves do
    // (core.Pages.MovePage): the run keeps what it did, to undo.
    const card = (what: Promise<void>) =>
      what.catch((e) => console.error("subpage card:", e instanceof Error ? e.message : String(e)));
    const hooks: WorkspaceHooks = {
      reading,
      listPages: async () =>
        (await Promise.all([...(await rowsNow()).rows.values()].sort(bySortKey).map((r) => pageOf(r.id))))
          .filter((p): p is WorkspacePage => !!p),
      page: pageOf,
      readPage: async (id) => {
        const page = (await pageOf(id))!;
        const { data } = await uc.from("page_content").select("content").eq("page_id", id).maybeSingle();
        if (data && !isPageContent(data.content)) throw new ToolError(UNREADABLE);
        const subpages = [...(await rowsNow()).rows.values()].filter((r) => r.parent_id === id).sort(bySortKey)
          .map((r) => ({ id: r.id, title: displayTitle(r.title) }));
        return {
          page: { id, title: page.title, ...(page.tags.length ? { tags: page.tags } : {}) },
          blocks: agentViews(normalizeContent(data?.content), await recordingsOf(uc, id)),
          subpages,
        };
      },
      searchPages: async (query) => {
        const { tags, rest } = splitTagQuery(query);
        const { data, error } = await uc.rpc("agent_search_pages", { p_text: rest, p_tags: tags, p_limit: 20 });
        if (error) throw new Error(error.message);
        return ((data ?? []) as { page_id: string; title: string; snippet: string | null }[]).map((r) => ({
          page_id: r.page_id,
          title: displayTitle(r.title),
          ...(r.snippet ? { snippet: r.snippet } : {}),
        }));
      },
      listFolders: () => foldersOf(uc, own),
      createPage: async (title, content, parent, folder) => {
        if (folder && !(await folderIn(uc, own, folder))) throw noFolder(folder);
        const row = {
          space_id: own,
          parent_id: parent || null,
          folder_id: parent ? null : folder || null,
          title,
          sort_key: await keyLast(parent, folder),
          is_template: false,
        };
        const made = await insertPageOf(uc, sc, user, row, content);
        (await rowsNow()).rows.set(made.id, { ...row, id: made.id, tags: [], public_link_until: null });
        // As a page made from the sidebar, it shows in the page it is
        // inside.
        if (parent) await card(linkSubpage(uc, user, parent, made.id));
        return made.id;
      },
      movePage: async (id, parent, folder) => {
        if (folder && !(await folderIn(uc, own, folder))) throw noFolder(folder);
        const { rows: byId } = await rowsNow();
        const was = byId.get(id)!;
        for (let p = parent; p; p = byId.get(p)?.parent_id ?? "") {
          if (p === id) throw new ToolError("A page cannot move inside itself or one of its subpages");
        }
        const from = {
          ...(was.parent_id ? { parent_id: was.parent_id } : {}),
          ...(was.folder_id ? { folder_id: was.folder_id } : {}),
          sort_key: was.sort_key,
        };
        if ((was.parent_id ?? "") === parent && (parent || (was.folder_id ?? "") === folder)) return { from, to: from };
        const place = {
          parent_id: parent || null,
          folder_id: parent ? null : folder || null,
          sort_key: await keyLast(parent, folder, id),
        };
        const { error } = await uc.from("pages").update(place).eq("id", id);
        if (error) throw new ToolError(`Could not move the page: ${error.message}`);
        const left = was.parent_id;
        Object.assign(was, place);
        // Its card leaves the page it was in, and goes in the one it is in
        // now, as the sidebar's moves keep them.
        if (left) await card(unlinkSubpage(uc, user, left, id));
        if (parent) await card(linkSubpage(uc, user, parent, id));
        return {
          from,
          to: { ...(parent ? { parent_id: parent } : folder ? { folder_id: folder } : {}), sort_key: place.sort_key },
        };
      },
      renamePage: async (id, title) => {
        const { error } = await uc.from("pages").update({ title }).eq("id", id);
        if (error) throw new ToolError(`Could not rename the page: ${error.message}`);
        (await rowOf(id)).title = title;
      },
      setTags: async (id, tags) => {
        const kept = normalizeTags(tags);
        const { error } = await uc.from("pages").update({ tags: kept }).eq("id", id);
        if (error) throw new ToolError(`Could not tag the page: ${error.message}`);
        (await rowOf(id)).tags = kept;
        return kept;
      },
      createFolder: async (name) => {
        const all = await allRows<{ sort_key: string }>(() =>
          uc.from("folders").select("sort_key").eq("space_id", own).is("deleted_at", null).order("id")
        );
        const folder = {
          id: crypto.randomUUID(),
          space_id: own,
          name,
          sort_key: newKeyAfter(lastKey(all.map((f) => f.sort_key))),
        };
        const { error } = await uc.from("folders").insert(folder);
        if (error) throw new ToolError(`Could not create the folder: ${error.message}`);
        return folder;
      },
    };

    // --- history, the web and the open page -----------------------------
    const history = await historyOf(uc, job);
    // An earlier turn that read the person's pages closes the web to this
    // one, and keeps what it writes to the URLs the person gave. Where it
    // may not read their pages, those turns are left out, as on a page.
    const pagesBefore = history.some((turn) => turn.kept_to_page !== true);
    const web = job.request?.allow_web === true && !pagesBefore;
    // One that read the web keeps this one to the URLs it may write after
    // reading the web from its start, as on a page.
    const webBefore = history.some((turn) => turn.guarded === true);
    const today = new Date().toISOString().slice(0, 10);
    const webSignal = () => AbortSignal.timeout(Math.max(0, Math.min(deps.callTimeoutMs, deadline - Date.now())));
    const open = job.request?.open_page_id ? await pageOf(job.request.open_page_id) : null;
    const openPage = open ? { id: open.id, title: open.title, ...(open.shared ? { shared: true } : {}) } : undefined;

    session = new PageEditSession(emptyContent(), {
      ...NO_PAGE,
      ...(web
        ? {
          readWebPage: (url: string) => (deps.readPage ?? readPage)(url, webSignal()),
          ...(canSearch(conn)
            ? { searchWeb: (query: string) => (deps.searchWeb ?? searchWeb)(conn, model, query, today, webSignal()) }
            : {}),
        }
        : {}),
    }, { canEdit: true, crossPage: false, request: instruction, author: agentName, workspace: hooks });
    if (pagesBefore) session.keepToGivenUrls();
    if (webBefore) session.keepToFoundUrls();

    // --- messages and the loop ------------------------------------------
    let toolsEnabled = !(provider.supports_tools === false && model === provider.default_model);
    const search = web && canSearch(conn);
    const prompt = () => workspacePrompt({ agentName, reading, toolsEnabled, userName, today, web, search });
    const messages: ChatMessage[] = [{ role: "system", content: prompt() }];
    for (const turn of history.reverse()) {
      if (pagesBefore && !reading && turn.kept_to_page !== true) continue;
      if (turn.instruction) messages.push({ role: "user", content: turn.instruction });
      if (turn.reply) messages.push({ role: "assistant", content: turn.reply });
    }
    messages.push({ role: "user", content: workspaceMessage(instruction, openPage) });
    const tools: ToolDef[] = toolsEnabled
      ? [...workspaceToolDefinitions(reading), ...(web ? webToolDefinitions(search) : [])]
      : [];
    const { reply: answered, finished, outOfTime } = await converse(
      {
        conn,
        model,
        messages,
        tools,
        session,
        edits,
        withoutTools: () => {
          toolsEnabled = false;
          messages[0] = { role: "system", content: prompt() };
        },
      },
      emit,
      deadline,
      deps,
    );
    // A run that changed pages did something, out of time too.
    if (outOfTime && !session.ops.length) {
      throw new Error("The model took too long to answer. Try again, or choose a faster model.");
    }
    let reply = answered;
    if (!reply) {
      const done = edits.map((e) => e.summary).join(". ");
      reply = finished
        ? (done ? `${done}.` : "Done.")
        : outOfTime
        ? `I ran out of time and stopped.${done ? ` So far: ${done}.` : ""}`
        : `I stopped after ${MAX_ROUNDS} steps.${done ? ` So far: ${done}.` : ""}`;
    }
    const { error: jobError } = await uc.from("ai_jobs").update({
      status: "done",
      result: { reply, ops: session.ops, edits, kept_to_page: session.keptToPage, guarded: session.guarded },
      error: null,
    }).eq("id", job.id);
    if (jobError) console.error("ai_jobs:", jobError.message);
    emit({ type: "done", reply, edits, kept_to_page: session.keptToPage, guarded: session.guarded });
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e);
    console.error("agent job failed:", message);
    // What it changed stays, its ops with it, for Undo to take it back by.
    const result = session?.ops.length ? { reply: "", ops: session.ops, edits } : undefined;
    await uc.from("ai_jobs").update({ status: "failed", error: message, ...(result ? { result } : {}) }).eq(
      "id",
      job.id,
    );
    emit({ type: "error", message });
  }
}

async function runJob(
  uc: SupabaseClient,
  sc: SupabaseClient,
  user: User,
  job: Job,
  emit: Emit,
  deadline: number,
  deps: AgentDeps,
): Promise<void> {
  if (job.surface === "workspace") return await runWorkspaceJob(uc, sc, user, job, emit, deadline, deps);
  emit({ type: "status", status: "running" });
  // What a run that fails part way did: its edits streamed to the app as
  // they came, so they are saved still, as the app has them.
  let session: PageEditSession | undefined;
  // An edit for each op, naming the call that made it, by which the app
  // says it in its own words (opEdit in internal/agents).
  const edits: { summary: string; block_ids: string[]; tool: string }[] = [];
  let save: (() => Promise<void>) | undefined;
  let saveTried = false;
  let savedEdits = false;
  try {
    const instruction = String(job.request?.instruction ?? "").trim();
    if (!instruction) throw new Error("The request has no instruction");

    // --- page and permissions -------------------------------------------
    const { data: page, error: pageError } = await uc
      .from("pages")
      .select("id, space_id, parent_id, title, is_template, public_link_until")
      .eq("id", job.page_id)
      .is("deleted_at", null)
      .maybeSingle();
    if (pageError) throw new Error(pageError.message);
    if (!page) throw new Error("This page was deleted or is not shared with you");
    const { data: editable } = await uc.rpc("can_edit_page", { p_page_id: page.id });
    const canEdit = editable === true;

    // --- provider -------------------------------------------------------
    const { provider, model, conn, agentName } = await providerOf(uc, sc, job);

    // --- context --------------------------------------------------------
    const { data: contentRow, error: contentError } = await uc
      .from("page_content")
      .select("content, revision")
      .eq("page_id", page.id)
      .maybeSingle();
    if (contentError) throw new Error(contentError.message);
    if (contentRow && !isPageContent(contentRow.content)) throw new Error(UNREADABLE);
    const content = normalizeContent(contentRow?.content);

    const userName = await userNameOf(uc, user);

    const recordings = await recordingsOf(uc, page.id);

    const subpages = (await rows<{ id: string; title: string }>(
      uc.from("pages").select("id, title").eq("parent_id", page.id).is("deleted_at", null).order("sort_key"),
    )).map((p) => ({ id: p.id, title: displayTitle(p.title) }));

    const comments = await rows<CommentRow>(
      uc.from("comments")
        .select(
          "id, block_id, parent_comment_id, author_id, author_kind, agent_name, body, resolved, created_at, deleted_at",
        )
        .eq("page_id", page.id)
        .order("created_at"),
    );
    // A thread whose first comment was deleted stays while it has replies,
    // that comment shown as deleted: the app shows it so too.
    const repliesTo = (root: CommentRow) => comments.filter((c) => c.parent_comment_id === root.id && !c.deleted_at);
    const bodyOf = (c: CommentRow) => c.deleted_at ? "(deleted)" : c.body;
    const authorIds = [...new Set(comments.map((c) => c.author_id).filter((id): id is string => !!id))];
    const names = new Map<string, string>();
    if (authorIds.length) {
      for (
        const p of await rows<{ id: string; display_name: string }>(
          uc.from("profiles").select("id, display_name").in("id", authorIds),
        )
      ) names.set(p.id, p.display_name);
    }
    // An agent's comment is named as the app's runs name it (Hub.thread):
    // the app keeps no one as who asked an agent on the device. A guest is
    // a visitor of the page's public link, posted by its owner.
    const personOf = (c: CommentRow) => (c.author_id && names.get(c.author_id)) || "Someone";
    const authorOf = (c: CommentRow) =>
      c.author_kind === "agent"
        ? `${c.agent_name || "Agent"} (agent)`
        : c.author_kind === "guest"
        ? `${c.agent_name || "Guest"} (guest, through the page's public link)`
        : personOf(c);

    const threadsFor = (list: CommentRow[]) => {
      const roots = list.filter((c) => !c.parent_comment_id && (!c.deleted_at || repliesTo(c).length > 0));
      return roots.map((root) => ({
        block_id: root.block_id,
        resolved: root.resolved,
        thread: [root, ...repliesTo(root)].map((c) => ({
          author: authorOf(c),
          body: bodyOf(c),
        })),
      }));
    };

    let thread: { author: string; body: string }[] | undefined;
    let threadRoot: CommentRow | undefined;
    if (job.surface === "comment" && job.comment_id) {
      const trigger = comments.find((c) => c.id === job.comment_id);
      const rootId = trigger?.parent_comment_id ?? trigger?.id;
      threadRoot = comments.find((c) => c.id === rootId);
      if (threadRoot) {
        thread = [threadRoot, ...repliesTo(threadRoot)]
          .map((c) => ({ author: authorOf(c), body: bodyOf(c) }));
      }
    }

    // Other pages are offered only on a page that is the caller's alone: on
    // a shared page, someone else's writing could have the agent read the
    // caller's other pages into it. A guest's comment is someone else's too,
    // though stored as the owner's (a deleted one still shows its name), and
    // it is on a page with a public link: what the agent reads into it, its
    // visitors read. So is a public link no one commented through yet,
    // which the app that serves it keeps on the page while it does
    // (public_link_until).
    let crossPage = job.request?.allow_cross_page === true && !publiclyLinked(page.public_link_until) &&
      !comments.some((c) => c.author_kind === "guest");
    if (crossPage) {
      const { data: members, error: membersError } = await uc.rpc("page_members", { p_page_id: page.id });
      crossPage = !membersError && Array.isArray(members) && members.length > 0 &&
        members.every((m: { user_id: string }) => m.user_id === user.id);
    }
    // The provider's test found whether its default model calls tools; any
    // other model is offered them, and answers without them if it refuses
    // them (rejectedTools, below).
    let toolsEnabled = !(provider.supports_tools === false && model === provider.default_model);
    // The person lets agents use the web (the app's setting, allow_web);
    // the provider may search it too (canSearch).
    const allowWeb = job.request?.allow_web === true;
    const today = new Date().toISOString().slice(0, 10);
    const webSignal = () => AbortSignal.timeout(Math.max(0, Math.min(deps.callTimeoutMs, deadline - Date.now())));

    // --- history --------------------------------------------------------
    const history = await historyOf(uc, job);
    // An earlier turn may hold what a run read on another page: this one
    // has no web, and keeps to the URLs the person gave, as after reading
    // one (keepToGivenUrls, below). A turn that read nothing beyond its
    // page says so. Where this run may no longer read other pages, as the
    // page is shared now, the turns that read beyond it are left out: what
    // they read would be written where others read it.
    const pagesBefore = history.some((turn) => turn.kept_to_page !== true);
    const web = allowWeb && !pagesBefore;
    // An earlier turn that read the web may hold what it read there: this
    // run keeps to the URLs it may write after reading the web from its
    // start, with none found yet (keepToFoundUrls, below).
    const webBefore = history.some((turn) => turn.guarded === true);

    // --- tools ----------------------------------------------------------
    // The person's own space, where the pages they make of their own go.
    let own: string | undefined;
    const ownSpace = async (): Promise<string> => {
      if (own) return own;
      const { data, error } = await uc.from("spaces").select("id").eq("owner_id", user.id).maybeSingle();
      if (error) throw new Error(error.message);
      if (!data) throw new ToolError("You have no space of your own to make a page in");
      return own = data.id as string;
    };
    const insertPage = (row: PageRow, pageContent: PageContent) => insertPageOf(uc, sc, user, row, pageContent);
    session = new PageEditSession(content, {
      readPage: (current: PageContent) =>
        Promise.resolve({
          page: { id: page.id, title: displayTitle(page.title) },
          blocks: agentViews(current, recordings),
          subpages,
          comments: threadsFor(comments),
        }),
      createSubpage: async (title: string, subContent: PageContent) => {
        // Last among its siblings, by the keys' bytes as the app orders
        // them, not the database's collation: all of them, past max_rows.
        const siblings = await allRows<{ sort_key: string }>(() =>
          uc.from("pages").select("sort_key").eq("parent_id", page.id).order("id")
        );
        const made = await insertPage({
          space_id: page.space_id,
          parent_id: page.id,
          title,
          sort_key: newKeyAfter(lastKey(siblings.map((s) => s.sort_key))),
          // A subpage of a template is part of it.
          is_template: page.is_template,
        }, subContent);
        subpages.push({ id: made.id, title });
        return made;
      },
      // A page of its own is the person's, in their space, wherever the
      // page the run is on is: one someone else shared is theirs.
      createPage: async (title: string, pageContent: PageContent, folderId: string | null) => {
        const space = await ownSpace();
        if (folderId && !(await folderIn(uc, space, folderId))) throw noFolder(folderId);
        // Last among the folder's pages, or the top level's outside folders:
        // all of them, past max_rows.
        const siblings = await allRows<{ sort_key: string }>(() => {
          const top = uc.from("pages").select("sort_key").eq("space_id", space).is("parent_id", null);
          return (folderId ? top.eq("folder_id", folderId) : top.is("folder_id", null)).order("id");
        });
        return await insertPage({
          space_id: space,
          parent_id: null,
          folder_id: folderId,
          title,
          sort_key: newKeyAfter(lastKey(siblings.map((s) => s.sort_key))),
          is_template: false,
        }, pageContent);
      },
      listPages: async (): Promise<PageSummary[]> =>
        (await rows<PageSummary>(
          uc.from("pages").select("id, title, parent_id").eq("space_id", page.space_id).is("deleted_at", null)
            .eq("is_template", false).neq("id", page.id).limit(MAX_LISTED_PAGES),
        )).map((p) => ({ ...p, title: displayTitle(p.title) })),
      listFolders: async () => foldersOf(uc, await ownSpace()),
      // Shaped as read_page's, as the app's (protocol §2): its subpages,
      // and no comments, which are of this page's alone.
      readOtherPage: async (otherId: string) => {
        const { data: other } = await uc
          .from("pages")
          .select("id, title")
          .eq("id", otherId)
          .eq("space_id", page.space_id)
          .is("deleted_at", null)
          .maybeSingle();
        const { data: otherContent } = other
          ? await uc.from("page_content").select("content").eq("page_id", otherId).maybeSingle()
          : { data: null };
        if (!other || !otherContent || !isPageContent(otherContent.content)) {
          throw new ToolError(`No page with id "${otherId}" that you can read`);
        }
        const otherSubpages = await rows<{ id: string; title: string }>(
          uc.from("pages").select("id, title").eq("parent_id", otherId).is("deleted_at", null).order("sort_key"),
        );
        return {
          page: { id: other.id, title: displayTitle(other.title) },
          blocks: agentViews(normalizeContent(otherContent.content), await recordingsOf(uc, otherId)),
          subpages: otherSubpages.map((p) => ({ id: p.id, title: displayTitle(p.title) })),
          comments: [],
        };
      },
      // A video's captions are read from its site, whose link is on the
      // page, with or without the web.
      readCaptions: (video: VideoRef) => (deps.readCaptions ?? readCaptions)(video, webSignal()),
      ...(web
        ? {
          readWebPage: (url: string) => (deps.readPage ?? readPage)(url, webSignal()),
          ...(canSearch(conn)
            ? { searchWeb: (query: string) => (deps.searchWeb ?? searchWeb)(conn, model, query, today, webSignal()) }
            : {}),
        }
        : {}),
    }, { canEdit, crossPage, request: instruction, author: agentName });

    // Saves the run's edits, once: on its end, or on its failure after it
    // made some.
    const run = session;
    save = async () => {
      saveTried = true;
      // The page as it was goes first: edits that could not keep it are
      // not saved, so none is saved that the page cannot go back from.
      const { error: historyError } = await uc.from("page_history").insert({
        page_id: page.id,
        content: run.before,
        reason: "agent_run",
        ai_job_id: job.id,
      });
      if (historyError) throw new Error(`Could not save the page: ${historyError.message}`);

      // Someone may save the page while the agent runs, or between reading
      // and saving here: replay the agent's ops on what they saved, and save
      // only over the revision that was read (protocol §1). The app with
      // the page open saved the ops as they came, and a block changed since
      // the run read it keeps that change (unseenOps). A block they changed
      // that an op still replaces or deletes loses their change, so what
      // they saved is kept in the page's history first, to restore.
      const runOps = run.ops.filter((op: Op) => !PAGE_OPS.has(op.op));
      let saved = false;
      let kept: number | null = null;
      for (let attempt = 0; attempt < 3 && !saved; attempt++) {
        const { data: latest, error: latestError } = await uc
          .from("page_content")
          .select("content, revision")
          .eq("page_id", page.id)
          .maybeSingle();
        if (latestError) throw new Error(`Could not save the page: ${latestError.message}`);
        const replayed = (latest?.revision ?? null) !== (contentRow?.revision ?? null);
        if (latest && replayed && !isPageContent(latest.content)) throw new Error(UNREADABLE);
        const latestContent = normalizeContent(latest?.content);
        const ops = replayed ? unseenOps(run.before, latestContent, runOps) : runOps;
        if (latest && replayed && kept !== latest.revision && overwrites(latestContent, ops)) {
          const { error } = await uc.from("page_history").insert({
            page_id: page.id,
            content: latest.content,
            reason: "conflict",
            ai_job_id: job.id,
          });
          if (error) throw new Error(`Could not save the page: ${error.message}`);
          kept = latest.revision;
        }
        const finalContent = replayed ? applyOps(latestContent, ops) : run.content;
        const values = { content: finalContent, plain_text: plainText(finalContent), updated_by: user.id };
        if (latest) {
          const { data, error } = await uc
            .from("page_content")
            .update(values)
            .eq("page_id", page.id)
            .eq("revision", latest.revision)
            .select("revision");
          if (error) throw new Error(`Could not save the page: ${error.message}`);
          saved = (data?.length ?? 0) > 0;
        } else {
          const { error } = await uc.from("page_content").insert({ page_id: page.id, ...values });
          if (error && error.code !== "23505") throw new Error(`Could not save the page: ${error.message}`);
          saved = !error;
        }
      }
      if (!saved) throw new Error("Could not save the page: it kept changing, or you cannot edit it.");
      savedEdits = true;
    };

    // --- messages -------------------------------------------------------
    if (pagesBefore) session.keepToGivenUrls();
    if (webBefore) session.keepToFoundUrls();
    const promptContext = {
      agentName,
      surface: job.surface,
      canEdit,
      crossPage,
      toolsEnabled,
      userName,
      today,
      web,
      search: web && canSearch(conn),
    };
    const messages: ChatMessage[] = [{ role: "system", content: systemPrompt(promptContext) }];
    for (const turn of history.reverse()) {
      if (pagesBefore && !crossPage && turn.kept_to_page !== true) continue;
      if (turn.instruction) messages.push({ role: "user", content: turn.instruction });
      if (turn.reply) messages.push({ role: "assistant", content: turn.reply });
    }

    const views = agentViews(content, recordings);
    const focusIds = new Set(job.focus_block_ids ?? []);
    if (threadRoot) focusIds.add(threadRoot.block_id);
    const focus: BlockView[] = views.filter((v) => focusIds.has(v.id));
    // A long page is read with read_page, by a model that has tools; one
    // that has none gets it here whatever its length, as the app's runs
    // give it (userMessage in internal/agents).
    const fits = pageFits(views);
    const request = (): ChatMessage => ({
      role: "user",
      content: userMessage({
        instruction,
        pageTitle: page.title,
        focus,
        selectionText: job.request?.selection_text,
        pageViews: fits || !toolsEnabled ? views : undefined,
        thread,
      }),
    });
    const requestAt = messages.push(request()) - 1;

    // --- loop -----------------------------------------------------------
    const tools: ToolDef[] = toolsEnabled
      ? [...toolDefinitions({ canEdit, crossPage }), ...(web ? webToolDefinitions(promptContext.search) : [])]
      : [];
    const { reply: answered, finished, outOfTime } = await converse(
      {
        conn,
        model,
        messages,
        tools,
        session,
        edits,
        withoutTools: () => {
          toolsEnabled = false;
          messages[0] = { role: "system", content: systemPrompt({ ...promptContext, toolsEnabled }) };
          messages[requestAt] = request();
        },
      },
      emit,
      deadline,
      deps,
    );
    let reply = answered;
    // A run that made pages did something, if it changed nothing here.
    if (outOfTime && !session.ops.length) {
      throw new Error("The model took too long to answer. Try again, or choose a faster model.");
    }
    if (!reply) {
      const done = edits.map((e) => e.summary).join(". ");
      reply = finished
        ? (done ? `${done}.` : "Done.")
        : outOfTime
        ? `I ran out of time and stopped.${done ? ` So far: ${done}.` : ""}`
        : `I stopped after ${MAX_ROUNDS} steps.${done ? ` So far: ${done}.` : ""}`;
    }
    // --- save -----------------------------------------------------------
    if (session.changed) await save();

    const { error: jobError } = await uc
      .from("ai_jobs")
      .update({
        status: "done",
        result: { reply, ops: session.ops, edits, kept_to_page: session.keptToPage, guarded: session.guarded },
        error: null,
      })
      .eq("id", job.id);
    if (jobError) console.error("ai_jobs:", jobError.message);

    if (job.surface === "comment" && threadRoot) {
      // The service role posts the reply: comments_guard pins author_id to the
      // caller on client inserts, and here it names the user who asked.
      const { error: replyError } = await sc.from("comments").insert({
        page_id: page.id,
        author_id: user.id,
        block_id: threadRoot.block_id,
        parent_comment_id: threadRoot.id,
        author_kind: "agent",
        agent_name: agentName,
        body: reply,
      });
      if (replyError) console.error("comment reply:", replyError.message);
    }

    emit({ type: "done", reply, edits, kept_to_page: session.keptToPage, guarded: session.guarded });
  } catch (e) {
    let message = e instanceof Error ? e.message : String(e);
    console.error("agent job failed:", message);
    // A run that failed after it edited the page, as when a later call of
    // the model fails, saves its edits: the app has them already, open or
    // as delivered, and undoes them by the job's ops. Edits it could not
    // save either, it says so of.
    if (session?.changed && save && !saveTried) {
      try {
        await save();
      } catch (saveError) {
        const why = saveError instanceof Error ? saveError.message : String(saveError);
        console.error("agent job's edits:", why);
        message = `${message} Its edits were not saved either: ${why}`;
      }
    }
    // The pages it made are kept, its ops with them, for Undo to take
    // them away by.
    const result = savedEdits || (session?.ops.length && !session.changed)
      ? { reply: "", ops: session!.ops, edits }
      : undefined;
    await uc.from("ai_jobs").update({ status: "failed", error: message, ...(result ? { result } : {}) }).eq(
      "id",
      job.id,
    );
    emit({ type: "error", message });
  }
}

/** What a run asks the model with, and keeps what it did in. */
interface Conversation {
  conn: Connection;
  model: string;
  messages: ChatMessage[];
  tools: ToolDef[];
  session: PageEditSession;
  /** An edit for each op, naming the call that made it. */
  edits: { summary: string; block_ids: string[]; tool: string }[];
  /** Words the prompt and the request for a model that turned the tools down: it answers without them. */
  withoutTools(): void;
}

/**
 * Asks the model, round after round, making the calls it asks for, until
 * it answers, runs out of rounds, or of time before deadline: a page's run
 * and a workspace run alike.
 */
async function converse(
  c: Conversation,
  emit: Emit,
  deadline: number,
  deps: AgentDeps,
): Promise<{ reply: string; finished: boolean; outOfTime: boolean }> {
  const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  let reply = "";
  let finished = false;
  let outOfTime = false;
  // One answer of the model. One that failed busy (429) or failing (5xx)
  // is asked once more, after its Retry-After or a while, as the app's
  // runs ask (Go's retryable), when nothing of it came yet: a round's
  // calls change the page only once its answer has all come.
  const answer = async (round: number): Promise<ChatResult> => {
    for (let attempt = 0;; attempt++) {
      let streamed = false;
      try {
        return await deps.chat(
          c.conn,
          { model: c.model, messages: c.messages, tools: c.tools },
          AbortSignal.timeout(Math.min(deps.callTimeoutMs, deadline - Date.now())),
          (delta) => {
            streamed = true;
            emit({ type: "text", delta, round });
          },
        );
      } catch (e) {
        const wait = attempt === 0 && !streamed ? retryWait(e) : null;
        if (wait === null || deadline - Date.now() - wait < MIN_CALL_MS) throw e;
        console.error(`agent model: ${e instanceof Error ? e.message : e}; asking again in ${wait} ms`);
        await sleep(wait);
      }
    }
  };
  rounds: for (let round = 0; round < MAX_ROUNDS; round++) {
    if (deadline - Date.now() < MIN_CALL_MS) {
      outOfTime = true;
      break;
    }
    let result;
    try {
      // The answer streams to the app as it comes, each round's
      // starting again where tool calls came before it.
      result = await answer(round);
    } catch (e) {
      if (isTimeout(e)) {
        outOfTime = true;
        break;
      }
      if (e instanceof ProviderError && e.rejectedTools && c.tools.length && round === 0) {
        // The model has no tool calling: answer chat-only.
        c.tools = [];
        c.withoutTools();
        continue;
      }
      throw e;
    }
    const message = result.message;
    c.messages.push(message);
    if (message.tool_calls?.length && c.tools.length) {
      for (const [i, call] of message.tool_calls.entries()) {
        // The calls stop at the deadline, as the model's do, keeping
        // time to save what they did.
        if (Date.now() >= deadline) {
          outOfTime = true;
          break rounds;
        }
        const tool = call.function.name;
        // A call that takes a while, the app says it does, as searching
        // the web (activity in internal/agents).
        if (
          i < MAX_ROUND_CALLS &&
          (tool === "web_search" || tool === "read_web_page" || tool === "read_video_transcript")
        ) {
          emit({ type: "call", tool, args: webArgs(call.function.arguments) });
        }
        // Calls past the most one answer makes are refused, for the
        // model to make in its next (Go's maxRoundCalls).
        const outcome: ToolOutcome = i < MAX_ROUND_CALLS
          ? await c.session.call(tool, call.function.arguments)
          : { result: { error: TOO_MANY_CALLS }, ops: [], edits: [], blockIds: [] };
        outcome.ops.forEach((op, k) => emit({ type: "op", op, summary: outcome.edits[k]?.summary ?? "", tool }));
        for (const e of outcome.edits) c.edits.push({ summary: e.summary, block_ids: e.blockIds, tool });
        c.messages.push({
          role: "tool",
          tool_call_id: call.id,
          content: truncate(JSON.stringify(outcome.result), TOOL_RESULT_LIMIT),
        });
      }
      continue;
    }
    reply = withoutThinking(message.content ?? "");
    finished = true;
    break;
  }
  return { reply, finished, outOfTime };
}

/** A web call's query or URL, as the app shows what it does: nothing else the model sent. */
function webArgs(raw: string): { query?: string; url?: string } {
  try {
    const args = JSON.parse(raw);
    return {
      ...(typeof args?.query === "string" ? { query: args.query } : {}),
      ...(typeof args?.url === "string" ? { url: args.url } : {}),
    };
  } catch {
    return {};
  }
}
