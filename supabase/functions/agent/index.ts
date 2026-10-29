// Agent worker (protocol §4): runs one ai_jobs row against the user's cloud
// model provider, applying validated page-edit tool calls and streaming them
// back as NDJSON.

import type { SupabaseClient, User } from "jsr:@supabase/supabase-js@2";
import { corsHeaders, errorResponse, HttpError, preflight, readJson } from "../_shared/cors.ts";
import { loadProvider, providerSecret, requireUser, serviceClient, userClient } from "../_shared/supabase.ts";
import {
  chatCompletion,
  type ChatMessage,
  type Connection,
  LOCAL_PRESETS,
  normalizeBaseUrl,
  PRESET_NAMES,
  ProviderError,
  type ToolDef,
} from "../_shared/openai.ts";
import {
  type BlockView,
  normalizeContent,
  type PageContent,
  pageViews,
  plainText,
  type RecordingInfo,
} from "../_shared/blocks.ts";
import { applyOps, type Op, PageEditSession, type PageSummary, toolDefinitions, ToolError } from "../_shared/tools.ts";
import { type Surface, systemPrompt, userMessage } from "../_shared/prompt.ts";
import { isTimeout } from "../_shared/net.ts";

const MAX_ROUNDS = 12;
const INLINE_PAGE_LIMIT = 40_000;
const TOOL_RESULT_LIMIT = 60_000;
const HEARTBEAT_MS = 15_000;

function envMs(name: string, fallback: number, min: number, max: number): number {
  const n = Number(Deno.env.get(name));
  return Number.isFinite(n) && n > 0 ? Math.min(max, Math.max(min, n)) : fallback;
}

// An Edge Function is stopped at its wall-clock limit (150 s on the free
// plan, 400 s on paid plans), so a run stops calling the model at its
// deadline, keeping time to save what it did. On a paid plan, raise
// AGENT_DEADLINE_MS (at most 380 s).
const DEADLINE_MS = envMs("AGENT_DEADLINE_MS", 120_000, 10_000, 380_000);
// One model call; a call never runs past the deadline either.
const CALL_TIMEOUT_MS = envMs("AGENT_CALL_TIMEOUT_MS", 60_000, 5_000, 380_000);
// A call with less time than this left is not started.
const MIN_CALL_MS = 5_000;
// A job still running this long after it was claimed was stopped by the
// platform before it could finish: it may be claimed again.
const STALE_RUNNING_MS = DEADLINE_MS + 60_000;

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

interface PageRow {
  id: string;
  space_id: string;
  parent_id: string | null;
  title: string;
}

interface CommentRow {
  id: string;
  block_id: string;
  parent_comment_id: string | null;
  author_id: string | null;
  author_kind: "user" | "agent";
  agent_name: string | null;
  body: string;
  resolved: boolean;
  created_at: string;
}

type Emit = (event: Record<string, unknown>) => void;

declare const EdgeRuntime: { waitUntil(promise: Promise<unknown>): void } | undefined;

Deno.serve(async (req) => {
  const started = Date.now();
  const pre = preflight(req);
  if (pre) return pre;
  try {
    const { job_id } = await readJson<{ job_id?: string }>(req);
    if (!job_id) throw new HttpError(400, "job_id is required");
    const uc = userClient(req);
    const user = await requireUser(req, uc);

    const { data: job, error } = await uc
      .from("ai_jobs")
      .select("*")
      .eq("id", job_id)
      .maybeSingle();
    if (error) throw new Error(error.message);
    if (!job) throw new HttpError(404, "Job not found");
    const staleBefore = new Date(started - STALE_RUNNING_MS).toISOString();
    const stale = job.status === "running" && Date.parse(job.updated_at) < started - STALE_RUNNING_MS;
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
        const work = runJob(uc, serviceClient(), user, job as Job, emit, started + DEADLINE_MS).finally(() => {
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
        // Keep running (and saving) even if the client disconnects.
        if (typeof EdgeRuntime !== "undefined") EdgeRuntime.waitUntil(work);
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
});

function stripThinking(text: string): string {
  return text.replace(/<think>[\s\S]*?<\/think>/gi, "").trim();
}

function truncate(text: string, limit: number): string {
  return text.length > limit ? `${text.slice(0, limit)}…(truncated)` : text;
}

async function rows<T>(query: PromiseLike<{ data: T[] | null; error: { message: string } | null }>): Promise<T[]> {
  const { data, error } = await query;
  if (error) throw new Error(error.message);
  return data ?? [];
}

async function runJob(
  uc: SupabaseClient,
  sc: SupabaseClient,
  user: User,
  job: Job,
  emit: Emit,
  deadline: number,
): Promise<void> {
  emit({ type: "status", status: "running" });
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
    if (!job.provider_id) throw new Error("Choose a model in Settings › Models first");
    const provider = await loadProvider(uc, job.provider_id);
    if (provider.is_local || LOCAL_PRESETS.has(provider.preset)) {
      throw new Error("Local models run on your device, not on the server");
    }
    const apiKey = await providerSecret(sc, provider.id);
    if (provider.auth_kind !== "none" && !apiKey) {
      throw new Error(`Add an API key for ${provider.name || provider.preset} in Settings › Models`);
    }
    const model = job.model || provider.default_model;
    if (!model) throw new Error(`Choose a model for ${provider.name || provider.preset} in Settings › Models`);
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
    const content = normalizeContent(contentRow?.content);

    const { data: profile } = await uc
      .from("profiles")
      .select("display_name")
      .eq("id", user.id)
      .maybeSingle();
    const userName = profile?.display_name || user.email?.split("@")[0] || "the user";

    const recordings = new Map<string, RecordingInfo>();
    for (
      const r of await rows<
        { id: string; duration_ms: number; status: string; transcript: RecordingInfo["transcript"] }
      >(
        uc.from("recordings").select("id, duration_ms, status, transcript").eq("page_id", page.id),
      )
    ) {
      recordings.set(r.id, { id: r.id, duration_ms: r.duration_ms, status: r.status, transcript: r.transcript ?? [] });
    }

    const subpages = (await rows<{ id: string; title: string }>(
      uc.from("pages").select("id, title").eq("parent_id", page.id).is("deleted_at", null).order("sort_key"),
    )).map((p) => ({ id: p.id, title: p.title }));

    const comments = await rows<CommentRow>(
      uc.from("comments")
        .select("id, block_id, parent_comment_id, author_id, author_kind, agent_name, body, resolved, created_at")
        .eq("page_id", page.id)
        .is("deleted_at", null)
        .order("created_at"),
    );
    const authorIds = [...new Set(comments.map((c) => c.author_id).filter((id): id is string => !!id))];
    const names = new Map<string, string>();
    if (authorIds.length) {
      for (
        const p of await rows<{ id: string; display_name: string }>(
          uc.from("profiles").select("id, display_name").in("id", authorIds),
        )
      ) names.set(p.id, p.display_name);
    }
    // Anyone who can comment can post an agent comment (agents on devices
    // reply through their user), so an agent comment names who asked. A
    // guest is a visitor of the page's public link, posted by its owner.
    const personOf = (c: CommentRow) => (c.author_id && names.get(c.author_id)) || "Someone";
    const authorOf = (c: CommentRow) =>
      c.author_kind === "agent"
        ? `${c.agent_name || "Agent"} (agent, asked by ${personOf(c)})`
        : c.author_kind === "guest"
          ? `${c.agent_name || "Guest"} (guest, through the page's public link)`
          : personOf(c);

    const threadsFor = (list: CommentRow[]) => {
      const roots = list.filter((c) => !c.parent_comment_id);
      return roots.map((root) => ({
        block_id: root.block_id,
        resolved: root.resolved,
        thread: [root, ...list.filter((c) => c.parent_comment_id === root.id)].map((c) => ({
          author: authorOf(c),
          body: c.body,
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
        thread = [threadRoot, ...comments.filter((c) => c.parent_comment_id === threadRoot!.id)]
          .map((c) => ({ author: authorOf(c), body: c.body }));
      }
    }

    // Other pages are offered only on a page that is the caller's alone: on
    // a shared page, someone else's writing could have the agent read the
    // caller's other pages into it.
    let crossPage = job.request?.allow_cross_page === true;
    if (crossPage) {
      const { data: members, error: membersError } = await uc.rpc("page_members", { p_page_id: page.id });
      crossPage = !membersError && Array.isArray(members) && members.length > 0 &&
        members.every((m: { user_id: string }) => m.user_id === user.id);
    }
    let toolsEnabled = provider.supports_tools !== false;

    // --- tools ----------------------------------------------------------
    const session = new PageEditSession(content, {
      readPage: (current: PageContent) =>
        Promise.resolve({
          page: { id: page.id, title: page.title },
          blocks: pageViews(current, recordings),
          subpages,
          comments: threadsFor(comments),
        }),
      createSubpage: async (title: string, subContent: PageContent) => {
        const siblings = await rows<{ sort_key: string }>(
          uc.from("pages").select("sort_key").eq("parent_id", page.id).order("sort_key", { ascending: false }).limit(1),
        );
        const sortKey = siblings[0]?.sort_key ? `${siblings[0].sort_key}m` : "a0";
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
        if (subContentError) throw new ToolError(`Could not save the subpage: ${subContentError.message}`);
        subpages.push({ id, title });
        return { id, space_id: page.space_id, parent_id: page.id, title, sort_key: sortKey };
      },
      listPages: async (): Promise<PageSummary[]> => (await rows<PageSummary>(
        uc.from("pages").select("id, title, parent_id").eq("space_id", page.space_id).is("deleted_at", null)
          .eq("is_template", false).neq("id", page.id).limit(500),
      )),
      readOtherPage: async (otherId: string) => {
        const { data: other } = await uc
          .from("pages")
          .select("id, title")
          .eq("id", otherId)
          .eq("space_id", page.space_id)
          .is("deleted_at", null)
          .maybeSingle();
        if (!other) throw new ToolError(`No page with id "${otherId}" that you can read`);
        const { data: otherContent } = await uc
          .from("page_content")
          .select("content")
          .eq("page_id", otherId)
          .maybeSingle();
        const otherSubpages = await rows<{ id: string; title: string }>(
          uc.from("pages").select("id, title").eq("parent_id", otherId).is("deleted_at", null),
        );
        return {
          page: { id: other.id, title: other.title },
          blocks: pageViews(normalizeContent(otherContent?.content)),
          subpages: otherSubpages,
          comments: [],
        };
      },
    }, { canEdit, crossPage, author: agentName });

    // --- messages -------------------------------------------------------
    const promptContext = {
      agentName,
      surface: job.surface,
      canEdit,
      crossPage,
      toolsEnabled,
      pageTitle: page.title,
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
    }

    const views = pageViews(content, recordings);
    const focusIds = new Set(job.focus_block_ids ?? []);
    if (threadRoot) focusIds.add(threadRoot.block_id);
    const focus: BlockView[] = views.filter((v) => focusIds.has(v.id));
    const inlineViews = JSON.stringify(views).length <= INLINE_PAGE_LIMIT ? views : undefined;
    messages.push({
      role: "user",
      content: userMessage({
        instruction,
        focus,
        selectionText: job.request?.selection_text,
        pageViews: inlineViews,
        thread,
      }),
    });

    // --- loop -----------------------------------------------------------
    let tools: ToolDef[] = toolsEnabled ? toolDefinitions({ canEdit, crossPage }) : [];
    const edits: { summary: string; block_ids: string[] }[] = [];
    let reply = "";
    let finished = false;
    let outOfTime = false;
    for (let round = 0; round < MAX_ROUNDS; round++) {
      const left = deadline - Date.now();
      if (left < MIN_CALL_MS) {
        outOfTime = true;
        break;
      }
      let result;
      try {
        result = await chatCompletion(
          conn,
          { model, messages, tools },
          AbortSignal.timeout(Math.min(CALL_TIMEOUT_MS, left)),
        );
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
          continue;
        }
        throw e;
      }
      const message = result.message;
      messages.push(message);
      if (message.tool_calls?.length && tools.length) {
        for (const call of message.tool_calls) {
          const outcome = await session.call(call.function.name, call.function.arguments);
          for (const op of outcome.ops) emit({ type: "op", op, summary: outcome.summary ?? "" });
          if (outcome.ops.length && outcome.summary) {
            edits.push({ summary: outcome.summary, block_ids: outcome.blockIds });
          }
          messages.push({
            role: "tool",
            tool_call_id: call.id,
            content: truncate(JSON.stringify(outcome.result), TOOL_RESULT_LIMIT),
          });
        }
        continue;
      }
      reply = stripThinking(message.content ?? "");
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
    emit({ type: "text", delta: reply });

    // --- save -----------------------------------------------------------
    if (session.changed) {
      const { error: historyError } = await uc.from("page_history").insert({
        page_id: page.id,
        content: session.before,
        reason: "agent_run",
        ai_job_id: job.id,
      });
      if (historyError) console.error("page_history:", historyError.message);

      // Someone may save the page while the agent runs, or between reading
      // and saving here: replay the agent's ops on what they saved, and save
      // only over the revision that was read (protocol §1).
      const ops = session.ops.filter((op: Op) => op.op !== "create_page");
      let saved = false;
      for (let attempt = 0; attempt < 3 && !saved; attempt++) {
        const { data: latest, error: latestError } = await uc
          .from("page_content")
          .select("content, revision")
          .eq("page_id", page.id)
          .maybeSingle();
        if (latestError) throw new Error(`Could not save the page: ${latestError.message}`);
        const finalContent = (latest?.revision ?? null) === (contentRow?.revision ?? null)
          ? session.content
          : applyOps(normalizeContent(latest?.content), ops);
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
    }

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
    const message = e instanceof Error ? e.message : String(e);
    console.error("agent job failed:", message);
    await uc.from("ai_jobs").update({ status: "failed", error: message }).eq("id", job.id);
    emit({ type: "error", message });
  }
}
