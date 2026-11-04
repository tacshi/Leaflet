import { assert, assertEquals } from "jsr:@std/assert@1";
import { handle, type SttDeps } from "./handler.ts";
import { UnsafeUrlError } from "../_shared/net.ts";
import { Project, type Row } from "../_testing/project.ts";

/** Ann's recording on her page, its audio uploaded, and her settings: Soniox, its key saved. */
function project(settings: Row = {}): Project {
  const p = new Project({ writes: (table, _row, caller) => table !== "recordings" || caller === "ann" });
  p.user("ann", "ann-token");
  p.user("bob", "bob-token");
  p.tables.recordings = [{
    id: "rec",
    page_id: "plan",
    audio_path: "ann/rec.wav",
    duration_ms: 1000,
    status: "uploading",
    error: null,
    transcribing_at: null,
  }];
  const stt = {
    provider: "soniox",
    model: null,
    base_url: null,
    provider_id: null,
    language: "en",
    speaker_labels: false,
  };
  p.tables.stt_settings = [
    { user_id: "ann", ...stt, ...settings },
    { user_id: "bob", ...stt, ...settings },
  ];
  p.tables.model_providers = [{
    id: "openai",
    user_id: "ann",
    preset: "openai",
    name: "OpenAI",
    base_url: "https://api.openai.com/v1",
    auth_kind: "api_key",
    has_secret: true,
    is_local: false,
  }];
  p.objects.recordings = new Map([["ann/rec.wav", new Uint8Array(64)]]);
  p.rpcs.get_stt_secret = () => "soniox-key";
  p.rpcs.get_provider_secret = () => "sk-openai";
  p.rpcs.can_edit_page = (_args, caller) => caller === "ann";
  return p;
}

/** The file a multipart/form-data request carries, and its other fields. */
async function upload(url: string, init?: RequestInit) {
  const form = await new Request(url, init).formData();
  const file = form.get("file") as File;
  form.delete("file");
  return {
    streamed: init?.body instanceof ReadableStream,
    name: file.name,
    bytes: new Uint8Array(await file.arrayBuffer()),
    fields: Object.fromEntries(form),
  };
}

/** Soniox, answering as status says, the calls it had, and the files sent it. */
function soniox(status: Row = { status: "completed" }) {
  const calls: string[] = [];
  const files: Awaited<ReturnType<typeof upload>>[] = [];
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const url = new URL(input instanceof Request ? input.url : input);
    const call = `${init?.method ?? "GET"} ${url.pathname}`;
    calls.push(call);
    const reply = (body: unknown) => Promise.resolve(Response.json(body));
    if (call === "POST /v1/files") {
      files.push(await upload(url.href, init));
      return reply({ id: "file" });
    }
    if (call === "POST /v1/transcriptions") return reply({ id: "job" });
    if (call === "GET /v1/transcriptions/job") return reply(status);
    if (call === "GET /v1/transcriptions/job/transcript") {
      return reply({ tokens: [{ text: "Hello.", start_ms: 0, end_ms: 400 }] });
    }
    return Promise.resolve(new Response(null, { status: 204 }));
  };
  return { fetch, calls, files };
}

function deps(
  p: Project,
  fetch: typeof globalThis.fetch,
  send: SttDeps["send"] = () => Promise.reject("unused"),
): SttDeps {
  return { clients: p.clients(), fetch, send, deadlineMs: 60_000, maxAudioBytes: 1 << 20, pollMs: 1 };
}

async function transcribe(p: Project, d: SttDeps, token = "ann-token") {
  const res = await handle(p.request(token, { action: "transcribe", recording_id: "rec" }), d);
  return { status: res.status, body: await res.json() };
}

const rec = (p: Project) => p.tables.recordings[0];

// Two calls for one recording at once transcribe it once: the second is
// refused while the first has it, and the audio goes to Soniox once.
Deno.test("a recording is transcribed by one call at a time", async () => {
  const p = project();
  const s = soniox();
  const results = await Promise.all([transcribe(p, deps(p, s.fetch)), transcribe(p, deps(p, s.fetch))]);
  assertEquals(results.map((r) => r.status).sort(), [200, 409]);
  assertEquals(s.calls.filter((c) => c === "POST /v1/files").length, 1);
  assertEquals([rec(p).status, rec(p).transcribing_at], ["ready", null]);
  assertEquals((rec(p).transcript as { text: string }[]).map((t) => t.text), ["Hello."]);
});

// A recording a call took and the platform stopped stays taken only as
// long as a call can run: after that, it is transcribed again.
Deno.test("a recording a stopped call left is taken again", async () => {
  const p = project();
  rec(p).status = "transcribing";
  rec(p).transcribing_at = new Date(Date.now() - 30 * 60_000).toISOString();
  assertEquals((await transcribe(p, deps(p, soniox().fetch))).status, 200);
  assertEquals(rec(p).status, "ready");

  rec(p).transcribing_at = new Date().toISOString();
  assertEquals((await transcribe(p, deps(p, soniox().fetch))).status, 409);
});

// Someone who can only view the recording's page is refused before any
// of their key is spent.
Deno.test("a viewer spends nothing", async () => {
  const p = project();
  const s = soniox();
  assertEquals((await transcribe(p, deps(p, s.fetch), "bob-token")).status, 403);
  assertEquals(s.calls, []);
  assertEquals(rec(p).status, "uploading");
});

// What the transcriber answered stays out of the recording's error, which
// everyone who can view the page reads, and out of the answer too: its
// own message and the address it was sent to go to the log.
Deno.test("a failed transcription says only the function's own words", async () => {
  const p = project();
  const s = soniox({ status: "error", error_message: "internal: shard 7 at 10.0.0.3" });
  const failed = await transcribe(p, deps(p, s.fetch));
  assertEquals(failed.status, 502);
  assertEquals(failed.body.error, "Soniox could not transcribe the recording");
  assertEquals([rec(p).status, rec(p).error, rec(p).transcribing_at], [
    "failed",
    "Soniox could not transcribe the recording",
    null,
  ]);

  const q = project({ provider: "openai-compatible", base_url: "https://stt.my-office.example/v1" });
  const unreachable = await transcribe(
    q,
    deps(q, soniox().fetch, () => Promise.reject(new UnsafeUrlError("Could not reach stt.my-office.example"))),
  );
  assertEquals(unreachable.body.error, "Could not reach the transcription endpoint.");
  assertEquals(rec(q).error, "Could not reach the transcription endpoint.");

  const r = project({ provider: "openai-compatible", base_url: "https://stt.example/v1" });
  const garbled = await transcribe(r, deps(r, soniox().fetch, () => Promise.resolve(new Response("<html>oops"))));
  assertEquals(garbled.status, 502);
  assert(!String(rec(r).error).includes("oops"), String(rec(r).error));
});

// A provider's key goes only to the provider's own address: an endpoint
// that names another is refused before anything is sent or claimed.
Deno.test("a provider's key goes only to its address", async () => {
  const p = project({ provider: "openai-compatible", provider_id: "openai", base_url: "https://elsewhere.example/v1" });
  const sent: string[] = [];
  const send: SttDeps["send"] = (raw) => {
    sent.push(String(raw));
    return Promise.resolve(Response.json({ text: "Hello." }));
  };
  const refused = await transcribe(p, deps(p, soniox().fetch, send));
  assertEquals(refused.status, 400);
  assertEquals(sent, []);
  assertEquals(rec(p).status, "uploading");

  p.tables.stt_settings[0].base_url = "api.openai.com/v1/";
  const done = await transcribe(p, deps(p, soniox().fetch, send));
  assertEquals(done.status, 200);
  assertEquals(sent, ["https://api.openai.com/v1/audio/transcriptions"]);
});

// A local provider is the device's to call: a transcription that names
// one is refused before its key is read or anything is sent or claimed,
// whatever address the provider names.
Deno.test("a local provider's key goes nowhere", async () => {
  for (const local of [{ preset: "ollama", is_local: true }, { preset: "lmstudio", is_local: false }]) {
    const p = project({ provider: "openai-compatible", provider_id: "local" });
    p.tables.model_providers.push({
      id: "local",
      user_id: "ann",
      name: "Local",
      base_url: "https://attacker.example/v1",
      auth_kind: "api_key",
      has_secret: true,
      ...local,
    });
    let read = false;
    p.rpcs.get_provider_secret = () => (read = true, "sk-local");
    const sent: string[] = [];
    const send: SttDeps["send"] = (raw) => (sent.push(String(raw)), Promise.resolve(Response.json({ text: "Hi" })));
    const refused = await transcribe(p, deps(p, soniox().fetch, send));
    assertEquals(refused.status, 400);
    assertEquals([sent, read, rec(p).status], [[], false, "uploading"]);
  }
});

// The audio goes to the transcriber as it is stored, streamed from Storage
// rather than read whole first: to Soniox once, and to an OpenAI-compatible
// endpoint each time it is asked, again when it turns verbose_json down.
Deno.test("the audio is streamed to the transcriber as it is stored", async () => {
  const audio = Uint8Array.from({ length: 300_000 }, (_, i) => i % 251);

  const p = project();
  p.objects.recordings.set("ann/rec.wav", audio);
  const s = soniox();
  assertEquals((await transcribe(p, deps(p, s.fetch))).status, 200);
  assertEquals(s.files.map((f) => [f.streamed, f.name, f.bytes]), [[true, "rec.wav", audio]]);

  const q = project({ provider: "openai-compatible", base_url: "https://stt.example/v1", model: "whisper-1" });
  q.objects.recordings.set("ann/rec.wav", audio);
  const sent: Awaited<ReturnType<typeof upload>>[] = [];
  const send: SttDeps["send"] = async (raw, init) => {
    const got = await upload(String(raw), init);
    sent.push(got);
    return got.fields.response_format === "verbose_json"
      ? new Response("response_format verbose_json is not supported", { status: 400 })
      : Response.json({ text: "Hello." });
  };
  const done = await transcribe(q, deps(q, soniox().fetch, send));
  assertEquals(done.status, 200);
  assertEquals(sent.map((f) => [f.streamed, f.name, f.fields.model, f.fields.response_format, f.bytes]), [
    [true, "rec.wav", "whisper-1", "verbose_json", audio],
    [true, "rec.wav", "whisper-1", "json", audio],
  ]);
  assertEquals((rec(q).transcript as { text: string }[]).map((t) => t.text), ["Hello."]);
});
