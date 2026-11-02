import { assertEquals, assertRejects } from "jsr:@std/assert@1";
import { readStream } from "./openai.ts";

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
