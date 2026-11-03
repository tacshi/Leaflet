// Searching the web for a run, through its provider: each searches in its
// own way, as a request of its own whose answer comes back to the run as a
// tool's result, its links among them, which the URL guard then knows
// (tools.ts searchWeb). Mirrors internal/agents/web.go.

import { type Connection, failure, readText } from "./openai.ts";
import { safeFetch } from "./net.ts";

export interface Source {
  title?: string;
  url: string;
  snippet?: string;
}

/** What a search of the web found: its sources, and what the model that searched said of them. */
export interface Search {
  answer: string;
  sources: Source[];
}

const MAX_SEARCH_ANSWER = 8000;
const MAX_SOURCES = 20;
const MAX_SNIPPET = 300;
const MAX_SEARCH_BYTES = 8 << 20;

type SearchKind = "responses" | "anthropic" | "openrouter" | "glm";

/**
 * How a provider at baseUrl searches the web: on OpenAI's and xAI's
 * Responses API, Claude's Messages API, or the Chat Completions of
 * OpenRouter or GLM, with their own search tools; null when it does not.
 */
export function searchKind(baseUrl: string): SearchKind | null {
  let host: string;
  try {
    host = new URL(baseUrl).hostname.toLowerCase();
  } catch {
    return null;
  }
  switch (host) {
    case "api.openai.com":
    case "api.x.ai":
      return "responses";
    case "api.anthropic.com":
      return "anthropic";
    case "openrouter.ai":
      return "openrouter";
    case "open.bigmodel.cn":
    case "api.z.ai":
      return "glm";
  }
  return null;
}

/** Asks the model that searches for what it found. */
function searchPrompt(query: string, today: string): string {
  return `Search the web for: ${query}\n\nToday is ${today}. Say briefly what the most relevant results say, each with its link. Answer from the results alone.`;
}

/** s cut to n characters, an ellipsis ending one it cut (Go's clip). */
function clip(s: string, n: number): string {
  const chars = [...s];
  return chars.length <= n ? s : `${chars.slice(0, n - 1).join("")}…`;
}

const words = (s: string) => s.split(/\s+/).filter(Boolean).join(" ");

/** A search's answer and sources, each source once (Go's searchFound). */
class Found {
  answer = "";
  sources: Source[] = [];
  private seen = new Map<string, number>();

  add(title: unknown, url: unknown, snippet: unknown): void {
    if (typeof url !== "string" || !/^https?:\/\//.test(url)) return;
    const t = typeof title === "string" ? words(title) : "";
    const sn = typeof snippet === "string" ? clip(words(snippet), MAX_SNIPPET) : "";
    const at = this.seen.get(url);
    if (at !== undefined) {
      const s = this.sources[at];
      if (!s.title && t) s.title = t;
      if (!s.snippet && sn) s.snippet = sn;
      return;
    }
    if (this.sources.length === MAX_SOURCES) return;
    this.seen.set(url, this.sources.length);
    const source: Source = { url };
    if (t) source.title = t;
    if (sn) source.snippet = sn;
    this.sources.push(source);
  }

  /** The sources of url_citation annotations, their fields in them or in url_citation. */
  citations(annotations: unknown): void {
    for (const a of Array.isArray(annotations) ? annotations : []) {
      if (a?.type !== "url_citation") continue;
      const c = a.url_citation ?? a;
      this.add(c?.title, c?.url, c?.content);
    }
  }

  search(): Search {
    return { answer: clip(this.answer.trim(), MAX_SEARCH_ANSWER), sources: this.sources };
  }
}

// deno-lint-ignore no-explicit-any
type Json = any;

/** What a Responses API search found: its answer's text and citations, then the sources it went through. */
export function responsesSearch(output: Json[]): Search {
  const found = new Found();
  const calls: Json[] = [];
  for (const item of Array.isArray(output) ? output : []) {
    if (item?.type === "message") {
      for (const part of Array.isArray(item.content) ? item.content : []) {
        if (part?.type !== "output_text") continue;
        if (typeof part.text === "string") found.answer += part.text;
        found.citations(part.annotations);
      }
    } else if (item?.type === "web_search_call") {
      calls.push(item);
    }
  }
  for (const c of calls) {
    for (const s of Array.isArray(c.action?.sources) ? c.action.sources : []) found.add(s?.title, s?.url, "");
  }
  return found.search();
}

/** What a search with Claude's web_search found: the answer's text and citations, then the results. */
export function anthropicSearch(content: Json[]): Search {
  const found = new Found();
  const results: Json[] = [];
  for (const block of Array.isArray(content) ? content : []) {
    if (block?.type === "text") {
      if (typeof block.text === "string") found.answer += block.text;
      for (const c of Array.isArray(block.citations) ? block.citations : []) found.add(c?.title, c?.url, c?.cited_text);
    } else if (block?.type === "web_search_tool_result" && Array.isArray(block.content)) {
      results.push(...block.content);
    }
  }
  for (const r of results) found.add(r?.title, r?.url, "");
  return found.search();
}

/** What a search through OpenRouter's or GLM's Chat Completions found. */
export function chatSearch(data: Json): Search {
  const found = new Found();
  for (const w of Array.isArray(data?.web_search) ? data.web_search : []) found.add(w?.title, w?.link, w?.content);
  const message = data?.choices?.[0]?.message;
  const content = message?.content;
  found.answer += typeof content === "string"
    ? content
    : Array.isArray(content)
    ? content.map((p: { text?: string }) => p?.text ?? "").join("")
    : "";
  found.citations(message?.annotations);
  return found.search();
}

/** Whether the provider of conn searches the web. */
export function canSearch(conn: Connection): boolean {
  return searchKind(conn.baseUrl) !== null;
}

/** Searches the web for query through the provider of conn, with model. */
export async function searchWeb(
  conn: Connection,
  model: string,
  query: string,
  today: string,
  signal?: AbortSignal,
): Promise<Search> {
  const user = [{ role: "user", content: searchPrompt(query, today) }];
  const kind = searchKind(conn.baseUrl);
  let path: string;
  let body: Record<string, unknown>;
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (conn.apiKey) headers["Authorization"] = `Bearer ${conn.apiKey}`;
  switch (kind) {
    case "responses":
      path = "/responses";
      body = { model, input: user, tools: [{ type: "web_search" }], store: false };
      // xAI takes include's values of its own.
      if (conn.baseUrl.includes("api.openai.com")) body.include = ["web_search_call.action.sources"];
      break;
    case "anthropic":
      // Claude's Messages API takes the key in x-api-key alone.
      path = "/messages";
      body = {
        model,
        max_tokens: 4096,
        messages: user,
        tools: [{ type: "web_search_20250305", name: "web_search", max_uses: 3 }],
      };
      delete headers["Authorization"];
      headers["x-api-key"] = conn.apiKey ?? "";
      headers["anthropic-version"] = "2023-06-01";
      break;
    case "openrouter":
      path = "/chat/completions";
      body = { model, messages: user, tools: [{ type: "openrouter:web_search", parameters: { max_results: 5 } }] };
      headers["HTTP-Referer"] = "https://leaflet.app";
      headers["X-Title"] = "Leaflet";
      break;
    case "glm":
      path = "/chat/completions";
      body = {
        model,
        messages: user,
        tools: [{
          type: "web_search",
          web_search: { enable: true, search_engine: "search_std", search_result: true, count: 5 },
        }],
      };
      break;
    default:
      throw new Error("This agent's provider does not search the web.");
  }
  const res = await safeFetch(`${conn.baseUrl}${path}`, {
    method: "POST",
    headers,
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok) throw await failure(res);
  let data: Json;
  try {
    data = JSON.parse(await readText(res, MAX_SEARCH_BYTES));
  } catch {
    throw new Error("The provider's answer was not JSON");
  }
  switch (kind) {
    case "responses":
      return responsesSearch(data?.output);
    case "anthropic":
      return anthropicSearch(data?.content);
    default:
      return chatSearch(data);
  }
}
