// What a video's captions read as on the server, which internal/web reads
// as well (testdata/captions.json).

import { assertEquals, assertThrows } from "jsr:@std/assert@1";
import {
  ask,
  type Caption,
  CaptionsError,
  passages,
  vimeoTrack,
  vttLines,
  youtubeLines,
  youtubeTrack,
} from "./captions.ts";
import { emptyContent, type VideoRef } from "./blocks.ts";
import { PageEditSession, type ToolHooks } from "./tools.ts";
import { fixture } from "../_testing/fixture.ts";
import stored from "./testdata/captions.json" with { type: "json" };

/** What reading s with read makes: its value, or the kind of error it throws. */
function outcome<T>(read: () => T): { value: T } | { error: string } {
  try {
    return { value: read() };
  } catch (e) {
    return { error: e instanceof CaptionsError ? e.kind : "unreadable" };
  }
}

const player = (status: string, reason: string, tracks: string, audio = `{"defaultCaptionTrackIndex":0}`) =>
  `{"playabilityStatus":{"status":"${status}","reason":"${reason}"},"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[${tracks}],"audioTracks":[${audio}]}}}`;
const ja = `{"baseUrl":"https://www.youtube.com/api/timedtext?v=x&lang=ja","languageCode":"ja"}`;
const en = `{"baseUrl":"https://www.youtube.com/api/timedtext?v=x&lang=en","languageCode":"en-US"}`;
const asr = `{"baseUrl":"https://www.youtube.com/api/timedtext?v=x&kind=asr&lang=en","languageCode":"en","kind":"asr"}`;

const players = [
  player("OK", "", `${ja},${asr},${en}`),
  player("OK", "", `${ja},${asr}`),
  player("OK", "", `${ja},${en}`),
  player("OK", "", `${ja},${en}`, ""),
  player("OK", "", `${ja},{"languageCode":"de"}`, `{"defaultCaptionTrackIndex":7}`),
  player("OK", "", ""),
  player("LOGIN_REQUIRED", "Sign in to confirm you’re not a bot", en),
  player("LOGIN_REQUIRED", "This video is private", en),
  player("ERROR", "", en),
  `{"playabilityStatus":{"status":"OK"}}`,
  player("OK", "", `{"baseUrl":"https://www.youtube.com/api/timedtext?v=x&exp=xpe&lang=en","languageCode":"en"}`),
  "<html>",
];

const xmls = [
  `<?xml version="1.0" encoding="utf-8" ?><timedtext format="3"><head><ws id="0"/></head><body>
<w t="0" id="1" wp="1" ws="1"/>
<p t="320" d="14260" w="1">[Music]</p>
<p t="18790" w="1" a="1">
</p>
<p t="18800" d="7160" w="1"><s ac="0">We&#39;re</s><s t="239" ac="0"> no</s><s t="559" ac="0"> strangers</s></p>
<p t="22640" d="4320">♪ You know the rules
and so do I ♪</p>
<p t="27040">Tom &amp; Jerry &lt;i&gt;shout&lt;/i&gt; &#x1F600; &nbsp;&nbsp; &copy; &#0; &#xD800;</p>
<p d="5" t="31000">a&amp;amp;b</p>
<p t="1.5">half</p>
</body></timedtext>`,
  `<?xml version="1.0" encoding="utf-8" ?><transcript><text start="1.36" dur="1.68">Hello &amp;amp; welcome</text>` +
  `<text start="3.0405" dur="2">back</text><text start="x">no time</text><text dur="1">  </text></transcript>`,
  "",
];

const configs = [
  `{"request":{"text_tracks":[{"lang":"de","url":"https://captions.vimeo.com/captions/170.vtt?sig=1","kind":"subtitles","default":true},` +
  `{"lang":"en","url":"https://captions.vimeo.com/captions/140662.vtt?sig=2","kind":"subtitles"}]}}`,
  `{"request":{"text_tracks":[{"lang":"de","url":"https://captions.vimeo.com/captions/170.vtt?sig=1","kind":"subtitles","default":true}]}}`,
  `{"request":{"text_tracks":[{"lang":"de","url":"https://captions.vimeo.com/c/1.vtt","kind":"subtitles"},{"lang":"fr","url":"/texttrack/9.vtt?token=3","kind":"captions"}]}}`,
  `{"request":{"text_tracks":[{"lang":"en","url":"https://captions.vimeo.com/c/2.vtt","kind":"subtitles"},` +
  `{"lang":"fr-x-autogen","url":"https://captions.vimeo.com/c/7.vtt","kind":"captions"},{"lang":"fr-CA","url":"https://captions.vimeo.com/c/8.vtt"}]}}`,
  `{"request":{"text_tracks":[{"lang":"en","url":"https://captions.vimeo.com/c/2.vtt"},{"lang":"es-x-autogen","url":"https://captions.vimeo.com/c/7.vtt"}]}}`,
  `{"request":{"text_tracks":[]}}`,
  `{"request":{"text_tracks":[{"lang":"en","url":"javascript:alert(1)"},{"lang":"en","url":7}]}}`,
  `{"request":{}}`,
  "nope",
];

const vtts = [
  "WEBVTT\r\nKind: captions\r\n\r\nNOTE made by hand\r\n\r\nSTYLE\r\n::cue { color: red }\r\n\r\n1\r\n00:00:05.237 --> 00:00:08.043 align:start\r\n" +
  "<i>Here at Vimeo, there's always</i>\r\n<i>one thing on our minds:</i>\r\n\r\n" +
  "01:02.5 --> 01:04.000\n<v Roger>Fish &amp; chips</v>\n\n2\n1:00:00.000 --> 1:00:02.000\n\n\nbad --> 00:00:01.000\nnot a cue\n\n" +
  "00:01:00 --> 00:01:02\nno fraction\n\n00:+1:00.000 --> x\nsigned\n\n00:01:00.12a --> x\nletters\n",
  "WEBVTT\n\n\n\n00:00:00.000 --> 00:00:01.000\n\n",
];

const passageCases: Caption[][] = [
  [
    { start_ms: 0, text: "Hello." },
    { start_ms: 2000, text: "Today we look" },
    { start_ms: 6500, text: "at caches." },
    { start_ms: 9000, text: "They hit" },
    { start_ms: 12000, text: "and they miss" },
    { start_ms: 30000, text: "until" },
    { start_ms: 31000, text: "they don't.”" },
    { start_ms: 38000, text: "「終わり」。" },
    { start_ms: 45000, text: "Next" },
    { start_ms: 52000, text: "Really?)" },
    { start_ms: 59000, text: "Yes" },
  ],
  [],
];

Deno.test("captions read as the app reads them (testdata/captions.json)", async () => {
  const made = {
    youtube_tracks: players.map((p) => ({ player: p, ...outcome(() => youtubeTrack(p)) })),
    youtube_lines: xmls.map((xml) => ({ xml, lines: youtubeLines(xml) })),
    vimeo_tracks: configs.map((c) => ({ config: c, ...outcome(() => vimeoTrack(c)) })),
    vtt_lines: vtts.map((vtt) => ({ vtt, lines: vttLines(vtt) })),
    passages: passageCases.map((lines) => ({ lines, passages: passages(lines) })),
  };
  await fixture("captions.json", made, stored);
});

Deno.test("captions pick the creator's in the language spoken", () => {
  assertEquals(youtubeTrack(players[0]), {
    url: "https://www.youtube.com/api/timedtext?v=x&lang=en",
    language: "en-US",
    auto: false,
  });
  assertEquals(vimeoTrack(configs[3]).url, "https://captions.vimeo.com/c/8.vtt");
  assertThrows(() => youtubeTrack(players[5]), CaptionsError, "no captions");
  assertEquals(
    youtubeLines(xmls[1]).map((l) => l.text),
    ["Hello & welcome", "back", "no time"],
  );
});

Deno.test("read_video_transcript reads a video block's captions in passages", async () => {
  const content = emptyContent();
  content.document.children = [
    { id: "yt", type: "video", data: { url: "https://youtu.be/dQw4w9WgXcQ", title: "Talk" } },
    { id: "bili", type: "video", data: { url: "https://www.bilibili.com/video/BV1KofNYjEuK" } },
    { id: "vm", type: "video", data: { url: "https://vimeo.com/76979871/8272103f6e" } },
    { id: "rec", type: "recording", data: { recording_id: "r1" } },
  ];
  const asked: VideoRef[] = [];
  let fail: Error | null = null;
  const hooks: ToolHooks = {
    readPage: () => Promise.resolve({}),
    createSubpage: () => Promise.reject(new Error("no")),
    createPage: () => Promise.reject(new Error("no")),
    listPages: () => Promise.resolve([]),
    readOtherPage: () => Promise.resolve({}),
    listFolders: () => Promise.resolve([]),
    readCaptions: (v) => {
      asked.push(v);
      if (fail) return Promise.reject(fail);
      return Promise.resolve({
        language: "en",
        auto: true,
        lines: [{ start_ms: 0, text: "Hello." }, { start_ms: 2000, text: "Today, caches." }, {
          start_ms: 83000,
          text: "They miss.",
        }],
      });
    },
  };
  const session = new PageEditSession(content, hooks, { canEdit: true, crossPage: true });
  const read = async (id: string) =>
    (await session.call("read_video_transcript", { id })).result as Record<string, unknown>;
  assertEquals(await read("yt"), {
    id: "yt",
    language: "en",
    auto_generated: true,
    transcript: "[0:00] Hello. Today, caches.\n[1:23] They miss.",
    title: "Talk",
  });
  await read("vm");
  assertEquals(asked, [{ provider: "youtube", id: "dQw4w9WgXcQ", hash: "" }, {
    provider: "vimeo",
    id: "76979871",
    hash: "8272103f6e",
  }]);
  fail = new CaptionsError("none", "none");
  for (
    const [id, want] of [["nope", "No block"], ["rec", "recording"], ["bili", "only in YouTube's and Vimeo's"], [
      "yt",
      "no captions",
    ]]
  ) {
    const error = String((await read(id)).error ?? "");
    assertEquals(error.includes(want), true, `${id}: ${error}`);
  }
  fail = new CaptionsError("refused", "refused");
  assertEquals(String((await read("yt")).error).includes("YouTube refused"), true);
});

// A site's redirects are followed as the app's client follows them: a 302
// asks again with GET, a 307 as it asked, and the cookie goes on only to
// the host it was for or one under it.
Deno.test("a site's captions are read through its redirects", async () => {
  const asked: { url: string; method: string; cookie: string | null; body: unknown }[] = [];
  const answers = [
    new Response(null, { status: 307, headers: { location: "/again" } }),
    new Response(null, { status: 302, headers: { location: "http://m.example.com/b" } }),
    new Response(null, { status: 301, headers: { location: "https://cdn.example.net/c" } }),
    new Response("lines"),
  ];
  const real = globalThis.fetch;
  globalThis.fetch = (input: string | URL | Request, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    asked.push({ url: String(input), method: init?.method ?? "GET", cookie: headers.get("cookie"), body: init?.body });
    const next = answers.shift();
    return next ? Promise.resolve(next) : Promise.reject(new TypeError("no answer"));
  };
  try {
    const resolve = () => Promise.resolve(["93.184.216.34"]);
    assertEquals(await ask("https://example.com/a", { body: "{}", cookie: "CONSENT=YES+1", resolve }), "lines");
    assertEquals(asked, [
      { url: "https://example.com/a", method: "POST", cookie: "CONSENT=YES+1", body: "{}" },
      { url: "https://example.com/again", method: "POST", cookie: "CONSENT=YES+1", body: "{}" },
      { url: "https://m.example.com/b", method: "GET", cookie: "CONSENT=YES+1", body: undefined },
      { url: "https://cdn.example.net/c", method: "GET", cookie: null, body: undefined },
    ]);
  } finally {
    globalThis.fetch = real;
  }
});
