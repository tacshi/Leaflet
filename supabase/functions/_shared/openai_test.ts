import { assert, assertEquals, assertRejects, assertThrows } from "jsr:@std/assert@1";
import {
  failure,
  keyStaysWith,
  MAX_ANSWER_BYTES,
  normalizeBaseUrl,
  ProviderError,
  readStream,
  readText,
  retryAfterMs,
  retryWait,
} from "./openai.ts";

function events(...data: string[]): Response {
  const body = data.map((d) => `data: ${d}\n\n`).join("");
  return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
}

Deno.test("a streamed answer comes together from its pieces", async () => {
  const pieces: string[] = [];
  const result = await readStream(
    events(
      `{"choices":[{"delta":{"content":"Let me "}}]}`,
      `{"choices":[{"delta":{"content":"look."}}]}`,
      `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_page","arguments":"{\\"a"}}]}}]}`,
      `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\\":1}"}}]},"finish_reason":"tool_calls"}]}`,
      "[DONE]",
    ),
    (d) => pieces.push(d),
  );
  assertEquals(pieces, ["Let me ", "look."]);
  assertEquals(result.message.content, "Let me look.");
  assertEquals(result.message.tool_calls, [
    { id: "c1", type: "function", function: { name: "read_page", arguments: '{"a":1}' } },
  ]);
  assertEquals(result.finishReason, "tool_calls");
});

Deno.test("a streamed answer cut off is no answer", async () => {
  await assertRejects(() => readStream(events(`{"choices":[{"delta":{"content":"Half"}}]}`), () => {}));
});

Deno.test("an error in the stream fails the answer", async () => {
  await assertRejects(() => readStream(events(`{"error":{"message":"overloaded"}}`), () => {}), Error, "overloaded");
});

Deno.test("an error in the stream closes the connection", async () => {
  let cancelled = false;
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(`data: {"error":{"message":"overloaded"}}\n\n`));
      // The provider keeps the connection open after the error.
    },
    cancel() {
      cancelled = true;
    },
  });
  await assertRejects(() => readStream(new Response(body), () => {}), Error, "overloaded");
  assertEquals(cancelled, true);
});

Deno.test("calls numbered with gaps, and one after them without its number, come together", async () => {
  const result = await readStream(
    events(
      `{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c2","function":{"name":"read_page","arguments":"{}"}}]}}]}`,
      `{"choices":[{"delta":{"tool_calls":[{"id":"c3","function":{"name":"list_pages","arguments":"{}"}}]}}]}`,
      `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
      "[DONE]",
    ),
    () => {},
  );
  assertEquals(result.message.tool_calls?.map((c) => [c.id, c.function.name]), [
    ["c2", "read_page"],
    ["c3", "list_pages"],
  ]);
});

Deno.test("a saved key stays with the address and preset it was saved for", () => {
  const saved = { base_url: "https://api.openai.com/v1", preset: "openai" };
  assertEquals(keyStaysWith(saved, { base_url: "https://api.openai.com/v1/", preset: "openai" }), true);
  assertEquals(keyStaysWith(saved, { base_url: "api.openai.com/v1/models", preset: "openai" }), true);
  assertEquals(keyStaysWith(saved, { base_url: "https://attacker.example/v1", preset: "openai" }), false);
  assertEquals(
    keyStaysWith(saved, { base_url: "https://api.openai.com.attacker.example/v1", preset: "openai" }),
    false,
  );
  assertEquals(keyStaysWith(saved, { base_url: "http://api.openai.com/v1", preset: "openai" }), false);
  assertEquals(keyStaysWith(saved, { base_url: "https://api.openai.com/v1", preset: "claude" }), false);
  assertEquals(keyStaysWith({ base_url: "", preset: "openai" }, { base_url: "", preset: "openai" }), true);
});

// A base URL as people type it becomes the API's root: https added, slashes
// and an endpoint's own path taken off, /v1 added to a bare host.
Deno.test("base URLs come to the API's root", () => {
  assertEquals(normalizeBaseUrl("api.example.com"), "https://api.example.com/v1");
  assertEquals(normalizeBaseUrl("https://api.example.com/"), "https://api.example.com/v1");
  assertEquals(normalizeBaseUrl("https://api.example.com/v1/chat/completions"), "https://api.example.com/v1");
  assertEquals(normalizeBaseUrl("https://api.example.com/api/v2/models/"), "https://api.example.com/api/v2");
  assertEquals(normalizeBaseUrl("", "openai"), "https://api.openai.com/v1");
  assertEquals(normalizeBaseUrl("  ", "claude"), "https://api.anthropic.com/v1");
  assertThrows(() => normalizeBaseUrl("", "custom"));
});

// A provider's error says its status and its own JSON message, on one line
// and short; a body that is anything else, which any address could send,
// is never shown.
Deno.test("a provider's error never shows its raw body", async () => {
  const html = await failure(new Response("<html><script>steal()</script></html>", { status: 502 }));
  assertEquals(html.message, "The provider had an error (502)");
  const own = await failure(
    new Response(JSON.stringify({ error: { message: "Bad model\n\nname" } }), { status: 404 }),
  );
  assertEquals(own.message, "Not found: check the base URL and model (404): Bad model name");
  const long = await failure(new Response(JSON.stringify({ message: "x".repeat(500) }), { status: 400 }));
  assert(long.message.length < 300, long.message);
  const busy = await failure(new Response("", { status: 429, headers: { "Retry-After": "7" } }));
  assertEquals(busy.retryAfterMs, 7000);
});

// What a provider answers is read only so far: one that answers without
// end fails rather than filling the function's memory.
Deno.test("an answer past its limit is cut off", async () => {
  const endless = () =>
    new Response(
      new ReadableStream({
        pull(c) {
          c.enqueue(new Uint8Array(1 << 20));
        },
      }),
      { headers: { "Content-Type": "text/event-stream" } },
    );
  await assertRejects(() => readText(endless(), MAX_ANSWER_BYTES), ProviderError, "too large");
  assertEquals((await readText(endless(), 10, true)).length, 10);
  await assertRejects(() => readStream(endless(), () => {}), ProviderError, "too large");
});

// A busy (429) or failing (5xx) model is asked again once, after its
// Retry-After, or 2 s; one asking for more than 30 s, or another failure,
// is not.
Deno.test("only a busy or failing model is asked again", () => {
  assertEquals(retryAfterMs("12"), 12_000);
  assertEquals(retryAfterMs(new Date(Date.UTC(2026, 0, 1, 0, 0, 5)).toUTCString(), Date.UTC(2026, 0, 1)), 5000);
  assertEquals(retryAfterMs("soon"), 0);
  assertEquals(retryWait(new ProviderError(429, "busy")), 2000);
  assertEquals(retryWait(new ProviderError(503, "down", "", 4000)), 4000);
  assertEquals(retryWait(new ProviderError(503, "down", "", 31_000)), null);
  assertEquals(retryWait(new ProviderError(400, "bad")), null);
  assertEquals(retryWait(new Error("network")), null);
});
