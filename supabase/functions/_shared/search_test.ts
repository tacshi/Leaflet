import { assertEquals, assertRejects } from "jsr:@std/assert@1";
import { anthropicSearch, chatSearch, responsesSearch, searchKind, searchWeb } from "./search.ts";
import searches from "./testdata/search.json" with { type: "json" };
import { fixture } from "../_testing/fixture.ts";

// Answers of each provider's search, as their docs give them.
const RESPONSES = [
  {
    type: "web_search_call",
    id: "ws_1",
    status: "completed",
    action: {
      type: "search",
      queries: ["AI videos Hacker News"],
      sources: [{ type: "url", url: "https://news.ycombinator.com/item?id=2" }, {
        type: "url",
        url: "https://www.youtube.com/watch?v=abc",
      }],
    },
  },
  {
    type: "message",
    role: "assistant",
    content: [{
      type: "output_text",
      text: "An AI video was on the front page ([youtube.com](https://www.youtube.com/watch?v=abc)).",
      annotations: [
        {
          type: "url_citation",
          url: "https://www.youtube.com/watch?v=abc",
          title: "An  AI video",
          start_index: 0,
          end_index: 10,
        },
      ],
    }],
  },
];

const ANTHROPIC = [
  { type: "server_tool_use", id: "srvtoolu_1", name: "web_search", input: { query: "AI videos" } },
  {
    type: "web_search_tool_result",
    tool_use_id: "srvtoolu_1",
    content: [
      {
        type: "web_search_result",
        url: "https://example.com/a",
        title: "A",
        encrypted_content: "x",
        page_age: "1 day",
      },
      { type: "web_search_result", url: "https://example.com/b", title: "B", encrypted_content: "y" },
    ],
  },
  {
    type: "text",
    text: "B says so.",
    citations: [{
      type: "web_search_result_location",
      url: "https://example.com/b",
      title: "B",
      cited_text: "So it is,\nsaid B.",
      encrypted_index: "z",
    }],
  },
];

const CHAT = {
  choices: [{
    message: {
      role: "assistant",
      content: "Two results.",
      annotations: [{
        type: "url_citation",
        url_citation: {
          url: "https://example.org/one",
          title: "One",
          content: "What one says",
          start_index: 0,
          end_index: 3,
        },
      }],
    },
  }],
  web_search: [
    { title: "Two", link: "https://example.org/two", content: "What two says", media: "Example", refer: "ref_1" },
    { title: "Not a link", link: "javascript:alert(1)" },
  ],
};

// A search's answer reads as the app reads it (Go's responsesSearch,
// anthropicSearch and the Chat Completions' search), which reads those of
// testdata/search.json alike.
Deno.test("searches read as the app reads them (testdata/search.json)", async () => {
  const made = {
    responses: { output: RESPONSES, search: responsesSearch(RESPONSES) },
    anthropic: { content: ANTHROPIC, search: anthropicSearch(ANTHROPIC) },
    chat: { data: CHAT, search: chatSearch(CHAT) },
  };
  assertEquals(made.responses.search.sources.map((s) => s.url), [
    "https://www.youtube.com/watch?v=abc",
    "https://news.ycombinator.com/item?id=2",
  ]);
  assertEquals(made.responses.search.sources[0].title, "An AI video");
  assertEquals(made.anthropic.search.sources[0], {
    url: "https://example.com/b",
    title: "B",
    snippet: "So it is, said B.",
  });
  assertEquals(made.chat.search.sources.map((s) => s.url), ["https://example.org/two", "https://example.org/one"]);
  await fixture("search.json", made, searches);
});

// Each provider is asked in its own way: the Responses API's web_search,
// Claude's server tool with its key in x-api-key alone, OpenRouter's and
// GLM's tools; another provider is not asked.
Deno.test("each provider searches in its own way", async () => {
  const asked: { url: string; headers: Headers; body: Record<string, unknown> }[] = [];
  const real = globalThis.fetch;
  globalThis.fetch = (input: string | URL | Request, init?: RequestInit) => {
    asked.push({ url: String(input), headers: new Headers(init?.headers), body: JSON.parse(String(init?.body)) });
    const host = new URL(String(input)).hostname;
    const answer = host === "api.anthropic.com"
      ? { content: ANTHROPIC }
      : host.includes("openai") || host === "api.x.ai"
      ? { output: RESPONSES }
      : CHAT;
    return Promise.resolve(Response.json(answer));
  };
  try {
    const conns = [
      "https://api.openai.com/v1",
      "https://api.x.ai/v1",
      "https://api.anthropic.com/v1",
      "https://openrouter.ai/api/v1",
      "https://open.bigmodel.cn/api/paas/v4",
    ];
    for (const baseUrl of conns) {
      const found = await searchWeb({ baseUrl, apiKey: "sk" }, "m", "AI videos", "2026-11-03");
      assertEquals(found.sources.length > 0, true, baseUrl);
    }
    assertEquals(asked.map((a) => a.url), [
      "https://api.openai.com/v1/responses",
      "https://api.x.ai/v1/responses",
      "https://api.anthropic.com/v1/messages",
      "https://openrouter.ai/api/v1/chat/completions",
      "https://open.bigmodel.cn/api/paas/v4/chat/completions",
    ]);
    assertEquals(asked[0].body.tools, [{ type: "web_search" }]);
    assertEquals(asked[0].body.include, ["web_search_call.action.sources"]);
    assertEquals(asked[1].body.include, undefined);
    assertEquals(asked[2].headers.get("x-api-key"), "sk");
    assertEquals(asked[2].headers.get("authorization"), null);
    assertEquals((asked[2].body.tools as { type: string }[])[0].type, "web_search_20250305");
    assertEquals((asked[3].body.tools as { type: string }[])[0].type, "openrouter:web_search");
    assertEquals((asked[4].body.tools as { web_search: { enable: boolean } }[])[0].web_search.enable, true);
    assertEquals(searchKind("https://api.deepseek.com/v1"), null);
    await assertRejects(
      () => searchWeb({ baseUrl: "https://api.deepseek.com/v1", apiKey: "sk" }, "m", "q", "2026-11-03"),
      Error,
      "does not search",
    );
  } finally {
    globalThis.fetch = real;
  }
});
