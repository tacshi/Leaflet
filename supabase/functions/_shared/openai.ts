// One OpenAI-compatible client for every cloud preset (OpenAI, Claude via
// Anthropic's OpenAI compatibility layer, xAI, OpenRouter, DeepSeek, GLM) and
// any custom base URL. Every request goes through safeFetch: base URLs are
// typed by people, so they must not reach the server's own network.

import { safeFetch } from "./net.ts";

export const PRESET_BASE_URLS: Record<string, string> = {
  openai: "https://api.openai.com/v1",
  claude: "https://api.anthropic.com/v1",
  xai: "https://api.x.ai/v1",
  openrouter: "https://openrouter.ai/api/v1",
  deepseek: "https://api.deepseek.com/v1",
  glm: "https://open.bigmodel.cn/api/paas/v4",
  ollama: "http://localhost:11434/v1",
  lmstudio: "http://localhost:1234/v1",
  local: "http://localhost:8080/v1",
};

export const LOCAL_PRESETS = new Set(["ollama", "lmstudio", "local"]);

export const PRESET_NAMES: Record<string, string> = {
  openai: "OpenAI",
  claude: "Claude",
  xai: "xAI",
  openrouter: "OpenRouter",
  deepseek: "DeepSeek",
  glm: "GLM",
  custom: "Custom endpoint",
  ollama: "Ollama",
  lmstudio: "LM Studio",
  local: "Local server",
};

export interface Connection {
  baseUrl: string;
  apiKey?: string | null;
  preset?: string;
}

export interface ToolCall {
  id: string;
  type: "function";
  function: { name: string; arguments: string };
}

export interface ChatMessage {
  role: "system" | "user" | "assistant" | "tool";
  content: string | null;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
}

export interface ToolDef {
  type: "function";
  function: {
    name: string;
    description: string;
    parameters: Record<string, unknown>;
  };
}

export interface ChatRequest {
  model: string;
  messages: ChatMessage[];
  tools?: ToolDef[];
  temperature?: number;
  max_tokens?: number;
}

export interface ChatResult {
  message: ChatMessage;
  finishReason: string | null;
}

export class ProviderError extends Error {
  /** retryAfterMs: the wait the provider's Retry-After asked for, 0 for none. */
  constructor(public status: number, message: string, public body = "", public retryAfterMs = 0) {
    super(message);
  }

  /** True when the provider rejected the request because of the tools. */
  get rejectedTools(): boolean {
    return this.status >= 400 && this.status < 500 &&
      /tool|function/i.test(this.body);
  }
}

/** Trims slashes and endpoint suffixes; adds /v1 to a bare host. */
export function normalizeBaseUrl(raw: string, preset?: string): string {
  let url = (raw ?? "").trim();
  if (!url && preset && PRESET_BASE_URLS[preset]) {
    url = PRESET_BASE_URLS[preset];
  }
  if (!url) throw new Error("Base URL is required");
  if (!/^https?:\/\//i.test(url)) url = `https://${url}`;
  url = url.replace(/\/+$/, "");
  url = url.replace(/\/(chat\/completions|completions|models)$/i, "");
  const parsed = new URL(url);
  if (parsed.pathname === "" || parsed.pathname === "/") {
    url = `${parsed.origin}/v1`;
  }
  return url;
}

/**
 * Whether a provider's saved key may go where it now points: only to the
 * address, and the preset's headers, it was saved for. A base URL or preset
 * changed without a new key would send the old one to whatever server the
 * change names, so one stolen session could take the key Vault keeps.
 */
export function keyStaysWith(
  saved: { base_url: string; preset: string },
  next: { base_url: string; preset: string },
): boolean {
  const origin = (p: { base_url: string; preset: string }) => {
    try {
      return new URL(normalizeBaseUrl(p.base_url, p.preset)).origin;
    } catch {
      return "";
    }
  };
  return saved.preset === next.preset && origin(saved) !== "" && origin(saved) === origin(next);
}

function headers(conn: Connection): Record<string, string> {
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (conn.apiKey) {
    h["Authorization"] = `Bearer ${conn.apiKey}`;
    if (conn.preset === "claude") h["x-api-key"] = conn.apiKey;
  }
  if (conn.preset === "claude") h["anthropic-version"] = "2023-06-01";
  if (conn.preset === "openrouter") {
    h["HTTP-Referer"] = "https://leaflet.app";
    h["X-Title"] = "Leaflet";
  }
  return h;
}

/** One line of a provider's own error message, if it gave one. */
function providerMessage(body: string): string {
  let detail: unknown;
  try {
    const parsed = JSON.parse(body);
    detail = parsed?.error?.message ?? parsed?.error ?? parsed?.message;
  } catch {
    return "";
  }
  if (typeof detail !== "string") return "";
  // deno-lint-ignore no-control-regex
  const line = detail.replace(/[\u0000-\u001f\u007f]+/g, " ").replace(/\s+/g, " ").trim();
  return line.length > 200 ? `${line.slice(0, 200)}…` : line;
}

const STATUS_MESSAGES: Record<number, string> = {
  400: "The provider rejected the request",
  401: "The provider rejected the API key",
  403: "The provider refused access",
  404: "Not found: check the base URL and model",
  408: "The provider timed out",
  413: "The request is too large for the provider",
  429: "The provider is rate limiting requests; try again shortly",
};

/** The most of an answer read from a provider: a whole one, its stream, a list of models. */
export const MAX_ANSWER_BYTES = 8 << 20;
/** The most of an error's body read, for its message. */
const MAX_ERROR_BYTES = 64 << 10;

/**
 * A response's body as text, read only up to limit bytes: an address that
 * answers without end is cut off, rather than filling the function's
 * memory. Past the limit it fails, unless cut is set, which keeps the
 * start.
 */
export async function readText(res: Response, limit: number, cut = false): Promise<string> {
  return new TextDecoder().decode(await readBytes(res, limit, cut));
}

/** A response's body, read only up to limit bytes, as readText reads it. */
export async function readBytes(res: Response, limit: number, cut = false): Promise<Uint8Array> {
  if (!res.body) return new Uint8Array();
  const reader = res.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      if (size + value.length > limit) {
        if (!cut) throw new ProviderError(502, "The provider's answer was too large");
        chunks.push(value.subarray(0, limit - size));
        size = limit;
        break;
      }
      chunks.push(value);
      size += value.length;
    }
  } finally {
    await reader.cancel().catch(() => {});
  }
  const all = new Uint8Array(size);
  let at = 0;
  for (const c of chunks) {
    all.set(c, at);
    at += c.length;
  }
  return all;
}

/** A response's body as JSON, read only up to MAX_ANSWER_BYTES. */
async function readJsonCapped(res: Response): Promise<unknown> {
  const text = await readText(res, MAX_ANSWER_BYTES);
  try {
    return JSON.parse(text);
  } catch {
    throw new ProviderError(502, "The provider's answer was not JSON");
  }
}

/**
 * The wait a busy provider's Retry-After asks for, in seconds or until a
 * time, in ms; 0 when it asks none.
 */
export function retryAfterMs(value: string | null, now = Date.now()): number {
  const v = (value ?? "").trim();
  if (/^\d+$/.test(v)) return Number(v) * 1000;
  const at = Date.parse(v);
  return Number.isFinite(at) ? Math.max(0, at - now) : 0;
}

/**
 * A provider's error as a short message: its status, and the provider's
 * own JSON error message when it sent one. The raw body is never shown, as
 * it could be anything the address answered with.
 */
export async function failure(res: Response): Promise<ProviderError> {
  const body = (await readText(res, MAX_ERROR_BYTES, true).catch(() => "")).slice(0, 2000);
  const known = STATUS_MESSAGES[res.status] ??
    (res.status >= 500 ? "The provider had an error" : "The provider refused the request");
  const detail = providerMessage(body);
  return new ProviderError(
    res.status,
    `${known} (${res.status})${detail ? `: ${detail}` : ""}`,
    body,
    retryAfterMs(res.headers.get("retry-after")),
  );
}

/** The wait before asking a busy or failing model once more (Go's retryWait), when it says none. */
export const RETRY_WAIT_MS = 2_000;
/** The longest wait to ask again after (Go's maxRetryWait): a model asking for longer is not asked again. */
export const MAX_RETRY_WAIT_MS = 30_000;

/**
 * Whether a model's failure is worth asking again for, once, and after how
 * long (Go's retryable): it was busy (429) or failing (5xx), and asked for
 * no longer a wait than MAX_RETRY_WAIT_MS. null when it is not.
 */
export function retryWait(e: unknown): number | null {
  if (!(e instanceof ProviderError) || (e.status !== 429 && e.status < 500)) return null;
  const wait = e.retryAfterMs || RETRY_WAIT_MS;
  return wait <= MAX_RETRY_WAIT_MS ? wait : null;
}

/**
 * Asks the model. With onText the answer streams, onText getting each
 * piece of its text as it comes; a provider that does not stream answers
 * whole, its text then one piece.
 */
export async function chatCompletion(
  conn: Connection,
  request: ChatRequest,
  signal?: AbortSignal,
  onText?: (delta: string) => void,
): Promise<ChatResult> {
  const body: Record<string, unknown> = {
    model: request.model,
    messages: request.messages,
  };
  if (onText) body.stream = true;
  if (request.tools && request.tools.length > 0) body.tools = request.tools;
  if (request.temperature !== undefined) body.temperature = request.temperature;
  if (request.max_tokens !== undefined) body.max_tokens = request.max_tokens;

  const res = await safeFetch(`${conn.baseUrl}/chat/completions`, {
    method: "POST",
    headers: headers(conn),
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok) throw await failure(res);
  if (onText && res.headers.get("content-type")?.startsWith("text/event-stream")) {
    return await readStream(res, onText);
  }
  // deno-lint-ignore no-explicit-any
  const data = await readJsonCapped(res) as any;
  const choice = data?.choices?.[0];
  if (!choice?.message) {
    throw new ProviderError(502, "The provider returned no message", JSON.stringify(data).slice(0, 2000));
  }
  const raw = choice.message;
  const message: ChatMessage = {
    role: "assistant",
    content: typeof raw.content === "string"
      ? raw.content
      : Array.isArray(raw.content)
      ? raw.content.map((p: { text?: string }) => p?.text ?? "").join("")
      : null,
  };
  if (Array.isArray(raw.tool_calls) && raw.tool_calls.length > 0) {
    message.tool_calls = raw.tool_calls.map((c: ToolCall, i: number) => ({
      id: c.id || `call_${i}`,
      type: "function",
      function: {
        name: c.function?.name ?? "",
        arguments: typeof c.function?.arguments === "string"
          ? c.function.arguments
          : JSON.stringify(c.function?.arguments ?? {}),
      },
    }));
  }
  if (onText && message.content) onText(message.content);
  return { message, finishReason: choice.finish_reason ?? null };
}

interface StreamedCall {
  index?: number;
  id?: string;
  function?: { name?: string; arguments?: unknown };
}

/**
 * A streamed answer: server-sent events, each a piece of the message, its
 * tool calls coming in pieces by index.
 */
export async function readStream(res: Response, onText: (delta: string) => void): Promise<ChatResult> {
  let content = "";
  const calls: { id: string; name: string; arguments: string }[] = [];
  let finishReason: string | null = null;
  let done = false;
  let buffer = "";
  let read = 0;
  const take = (line: string) => {
    if (!line.startsWith("data:")) return;
    const data = line.slice(5).trim();
    if (data === "[DONE]") {
      done = true;
      return;
    }
    let event;
    try {
      event = JSON.parse(data);
    } catch {
      return;
    }
    if (event?.error) {
      const detail = providerMessage(data);
      throw new ProviderError(502, `The provider had an error${detail ? `: ${detail}` : ""}`, data.slice(0, 2000));
    }
    const choice = event?.choices?.[0];
    if (!choice) return;
    const delta = choice.delta ?? {};
    const text = typeof delta.content === "string"
      ? delta.content
      : Array.isArray(delta.content)
      ? delta.content.map((p: { text?: string }) => p?.text ?? "").join("")
      : "";
    if (text) {
      content += text;
      onText(text);
    }
    for (const c of (delta.tool_calls ?? []) as StreamedCall[]) {
      // A call without an index is a new one, unless it names one that
      // came before. Servers may number calls from 1, or skip numbers,
      // so calls has holes.
      let i = Number.isInteger(c.index) && c.index! >= 0 ? c.index! : calls.findIndex((k) => !!c.id && k?.id === c.id);
      if (i < 0) i = calls.length;
      calls[i] ??= { id: "", name: "", arguments: "" };
      if (c.id) calls[i].id = c.id;
      if (c.function?.name) calls[i].name = c.function.name;
      const args = c.function?.arguments;
      if (typeof args === "string") calls[i].arguments += args;
      else if (args !== undefined && args !== null) calls[i].arguments += JSON.stringify(args);
    }
    if (choice.finish_reason) finishReason = choice.finish_reason;
  };
  const reader = res.body!.pipeThrough(new TextDecoderStream()).getReader();
  // An error event, or a read that fails, ends the answer too: the
  // connection to the provider is closed then as well, not left open.
  try {
    while (!done) {
      const { value, done: ended } = await reader.read();
      if (ended) break;
      // The answer and the line it is on come to no more than a whole
      // answer may: a stream without end is cut off.
      read += value.length;
      if (read > MAX_ANSWER_BYTES) throw new ProviderError(502, "The provider's answer was too large");
      buffer += value;
      let nl;
      while (!done && (nl = buffer.indexOf("\n")) >= 0) {
        take(buffer.slice(0, nl).replace(/\r$/, ""));
        buffer = buffer.slice(nl + 1);
      }
    }
    if (!done) take(buffer.trim());
  } finally {
    await reader.cancel().catch(() => {});
  }
  if (!done && !finishReason) {
    throw new ProviderError(502, "The provider's answer was cut off");
  }
  const message: ChatMessage = { role: "assistant", content: content || null };
  const present = calls.filter(Boolean);
  if (present.length > 0) {
    message.tool_calls = present.map((c, i) => ({
      id: c.id || `call_${i}`,
      type: "function",
      function: { name: c.name, arguments: c.arguments || "{}" },
    }));
  }
  return { message, finishReason };
}

export async function listModels(
  conn: Connection,
  signal?: AbortSignal,
): Promise<string[]> {
  const res = await safeFetch(`${conn.baseUrl}/models`, {
    headers: headers(conn),
    signal,
  });
  if (!res.ok) throw await failure(res);
  // deno-lint-ignore no-explicit-any
  const data = await readJsonCapped(res) as any;
  const items: unknown[] = Array.isArray(data?.data)
    ? data.data
    : Array.isArray(data?.models)
    ? data.models
    : Array.isArray(data)
    ? data
    : [];
  const ids = items
    .map((m) =>
      typeof m === "string" ? m : (m as { id?: string; name?: string })?.id ??
        (m as { name?: string })?.name
    )
    .filter((id): id is string => typeof id === "string" && id.length > 0);
  return [...new Set(ids)].sort();
}
