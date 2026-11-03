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
  isPageContent,
  normalizeContent,
  type PageContent,
  plainText,
  type RecordingInfo,
  trimSpace,
} from "../_shared/blocks.ts";
import {
  agentViews,
  applyOps,
  MAX_ROUND_CALLS,
  type Op,
  overwrites,
  PageEditSession,
  type PageSummary,
  TOO_MANY_CALLS,
  toolDefinitions,
  ToolError,
  type ToolOutcome,
  unseenOps,
} from "../_shared/tools.ts";
import { pageFits, type Surface, systemPrompt, userMessage } from "../_shared/prompt.ts";
import { isTimeout } from "../_shared/net.ts";
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
}

interface Job {
  id: string;
  updated_at: string;
  page_id: string;
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

async function runJob(
  uc: SupabaseClient,
  sc: SupabaseClient,
  user: User,
  job: Job,
  emit: Emit,
  deadline: number,
  deps: AgentDeps,
): Promise<void> {
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
      .select("id, space_id, parent_id, title, is_template")
      .eq("id", job.page_id)
      .is("deleted_at", null)
      .maybeSingle();
    if (pageError) throw new Error(pageError.message);
    if (!page) throw new Error("This page was deleted or is not shared with you");
    const { data: editable } = await uc.rpc("can_edit_page", { p_page_id: page.id });
    const canEdit = editable === true;

    // --- provider -------------------------------------------------------
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
    const agentName = provider.name || PRESET_NAMES[provider.preset] || "Agent";

    // --- context --------------------------------------------------------
    const { data: contentRow, error: contentError } = await uc
      .from("page_content")
      .select("content, revision")
      .eq("page_id", page.id)
      .maybeSingle();
    if (contentError) throw new Error(contentError.message);
    if (contentRow && !isPageContent(contentRow.content)) throw new Error(UNREADABLE);
    const content = normalizeContent(contentRow?.content);

    const { data: profile } = await uc
      .from("profiles")
      .select("display_name")
      .eq("id", user.id)
      .maybeSingle();
    const userName = profile?.display_name || user.email?.split("@")[0] || "the user";

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
    // visitors read.
    let crossPage = job.request?.allow_cross_page === true &&
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

    // --- tools ----------------------------------------------------------
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
        // them, not the database's collation.
        const siblings = await rows<{ sort_key: string }>(
          uc.from("pages").select("sort_key").eq("parent_id", page.id),
        );
        const sortKey = newKeyAfter(lastKey(siblings.map((s) => s.sort_key)));
        const id = crypto.randomUUID();
        const { error: insertError } = await uc.from("pages").insert({
          id,
          space_id: page.space_id,
          parent_id: page.id,
          title,
          sort_key: sortKey,
          // A subpage of a template is part of it.
          is_template: page.is_template,
        });
        if (insertError) throw new ToolError(`Could not create the subpage: ${insertError.message}`);
        const { error: subContentError } = await uc.from("page_content").insert({
          page_id: id,
          content: subContent,
          plain_text: plainText(subContent),
          updated_by: user.id,
        });
        if (subContentError) {
          // A page without its content would stay, empty, with no op to
          // undo it by: it goes too. Only its space's owner may delete a
          // page, so the service role deletes the one the run just made.
          const { error: removeError } = await sc.from("pages").delete().eq("id", id);
          if (removeError) console.error("subpage without its content:", removeError.message);
          throw new ToolError(`Could not save the subpage: ${subContentError.message}`);
        }
        subpages.push({ id, title });
        return { id, space_id: page.space_id, parent_id: page.id, title, sort_key: sortKey };
      },
      listPages: async (): Promise<PageSummary[]> =>
        (await rows<PageSummary>(
          uc.from("pages").select("id, title, parent_id").eq("space_id", page.space_id).is("deleted_at", null)
            .eq("is_template", false).neq("id", page.id).limit(500),
        )).map((p) => ({ ...p, title: displayTitle(p.title) })),
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
      const runOps = run.ops.filter((op: Op) => op.op !== "create_page");
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
    const promptContext = {
      agentName,
      surface: job.surface,
      canEdit,
      crossPage,
      toolsEnabled,
      userName,
      today: new Date().toISOString().slice(0, 10),
    };
    const messages: ChatMessage[] = [{ role: "system", content: systemPrompt(promptContext) }];

    if (job.conversation_id) {
      const history = await rows<{ request: Job["request"]; result: { reply?: string } | null }>(
        uc.from("ai_jobs")
          .select("request, result")
          .eq("conversation_id", job.conversation_id)
          .eq("status", "done")
          .neq("id", job.id)
          .order("created_at", { ascending: false })
          .limit(10),
      );
      for (const turn of history.reverse()) {
        if (turn.request?.instruction) messages.push({ role: "user", content: turn.request.instruction });
        if (turn.result?.reply) messages.push({ role: "assistant", content: turn.result.reply });
      }
      // An earlier turn may hold what a run read on another page: this one
      // keeps to the URLs the person gave, as after reading one.
      if (history.length > 0 && crossPage) session.keepToGivenUrls();
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
    const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
    let tools: ToolDef[] = toolsEnabled ? toolDefinitions({ canEdit, crossPage }) : [];
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
            conn,
            { model, messages, tools },
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
        if (e instanceof ProviderError && e.rejectedTools && tools.length && round === 0) {
          // The model has no tool calling: answer chat-only.
          tools = [];
          toolsEnabled = false;
          messages[0] = { role: "system", content: systemPrompt({ ...promptContext, toolsEnabled }) };
          messages[requestAt] = request();
          continue;
        }
        throw e;
      }
      const message = result.message;
      messages.push(message);
      if (message.tool_calls?.length && tools.length) {
        for (const [i, call] of message.tool_calls.entries()) {
          // The calls stop at the deadline, as the model's do, keeping
          // time to save what they did.
          if (Date.now() >= deadline) {
            outOfTime = true;
            break rounds;
          }
          // Calls past the most one answer makes are refused, for the
          // model to make in its next (Go's maxRoundCalls).
          const outcome: ToolOutcome = i < MAX_ROUND_CALLS
            ? await session.call(call.function.name, call.function.arguments)
            : { result: { error: TOO_MANY_CALLS }, ops: [], edits: [], blockIds: [] };
          const tool = call.function.name;
          outcome.ops.forEach((op, k) => emit({ type: "op", op, summary: outcome.edits[k]?.summary ?? "", tool }));
          for (const e of outcome.edits) edits.push({ summary: e.summary, block_ids: e.blockIds, tool });
          messages.push({
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
    if (outOfTime && !session.changed) {
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
      .update({ status: "done", result: { reply, ops: session.ops, edits }, error: null })
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

    emit({ type: "done", reply, edits });
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
    const result = savedEdits ? { reply: "", ops: session!.ops, edits } : undefined;
    await uc.from("ai_jobs").update({ status: "failed", error: message, ...(result ? { result } : {}) }).eq(
      "id",
      job.id,
    );
    emit({ type: "error", message });
  }
}
