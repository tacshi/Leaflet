// One OpenAI-compatible client for every cloud preset (OpenAI, Claude via
// Anthropic's OpenAI compatibility layer, xAI, OpenRouter, DeepSeek, GLM) and
// any custom base URL.

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
  constructor(public status: number, message: string, public body = "") {
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

async function failure(res: Response): Promise<ProviderError> {
  const body = await res.text().catch(() => "");
  let message = `${res.status} ${res.statusText}`.trim();
  try {
    const parsed = JSON.parse(body);
    const detail = parsed?.error?.message ?? parsed?.error ?? parsed?.message;
    if (typeof detail === "string" && detail) message = `${message}: ${detail}`;
  } catch {
    if (body) message = `${message}: ${body.slice(0, 300)}`;
  }
  return new ProviderError(res.status, message, body.slice(0, 2000));
}

export async function chatCompletion(
  conn: Connection,
  request: ChatRequest,
  signal?: AbortSignal,
): Promise<ChatResult> {
  const body: Record<string, unknown> = {
    model: request.model,
    messages: request.messages,
  };
  if (request.tools && request.tools.length > 0) body.tools = request.tools;
  if (request.temperature !== undefined) body.temperature = request.temperature;
  if (request.max_tokens !== undefined) body.max_tokens = request.max_tokens;

  const res = await fetch(`${conn.baseUrl}/chat/completions`, {
    method: "POST",
    headers: headers(conn),
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok) throw await failure(res);
  const data = await res.json();
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
  return { message, finishReason: choice.finish_reason ?? null };
}

export async function listModels(
  conn: Connection,
  signal?: AbortSignal,
): Promise<string[]> {
  const res = await fetch(`${conn.baseUrl}/models`, {
    headers: headers(conn),
    signal,
  });
  if (!res.ok) throw await failure(res);
  const data = await res.json();
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
