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
  // Soniox's end of it: what went there goes, with nothing left to pick up.
  assertEquals(s.calls.filter((c) => c.startsWith("DELETE")), [
    "DELETE /v1/transcriptions/job",
    "DELETE /v1/files/file",
  ]);
  assertEquals(p.rows("stt_jobs"), []);

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

// A recording that outlasts a call goes on at Soniox: the next call picks
// its transcription up, with the audio sent and paid for once, and what
// went to Soniox goes only as it is done.
Deno.test("a transcription Soniox is still working on is picked up, not paid for again", async () => {
  const p = project();
  const state = { status: "processing" };
  const s = soniox(state);
  const short = { ...deps(p, s.fetch), deadlineMs: 5, pollMs: 60_000 };
  const first = await transcribe(p, short);
  assertEquals(first.status, 504);
  assertEquals(first.body.code, "still_transcribing");
  assert(String(first.body.error).includes("Ask again"), first.body.error);
  assertEquals([rec(p).status, rec(p).error, rec(p).transcribing_at], ["failed", first.body.error, null]);
  assertEquals(s.calls.filter((c) => c.startsWith("DELETE")), []);
  assertEquals(p.rows("stt_jobs").map((j) => [j.recording_id, j.user_id, j.transcription_id]), [["rec", "ann", "job"]]);

  // Not yet, nor does asking again send anything.
  assertEquals((await transcribe(p, short)).status, 504);

  state.status = "completed";
  const done = await transcribe(p, short);
  assertEquals(done.status, 200);
  assertEquals(rec(p).status, "ready");
  assertEquals(s.files.length, 1);
  assertEquals(s.calls.filter((c) => c === "POST /v1/transcriptions").length, 1);
  assertEquals(s.calls.filter((c) => c.startsWith("DELETE")), [
    "DELETE /v1/transcriptions/job",
    "DELETE /v1/files/file",
  ]);
  assertEquals(p.rows("stt_jobs"), []);
});

// Time running out, or the worker shutting down, in the middle of asking
// Soniox is no end of its transcription either.
Deno.test("a call stopped while it waits leaves the transcription to the next", async () => {
  const p = project();
  const s = soniox({ status: "processing" });
  const timesOut: typeof globalThis.fetch = (input, init) =>
    String(input).endsWith("/transcriptions/job")
      ? Promise.reject(new DOMException("late", "TimeoutError"))
      : s.fetch(input, init);
  const late = await transcribe(p, deps(p, timesOut));
  assertEquals(late.status, 504);
  assert(String(late.body.error).includes("Ask again"), late.body.error);
  assertEquals(p.rows("stt_jobs").length, 1);

  const shutting = await transcribe(p, { ...deps(p, s.fetch), unloading: AbortSignal.abort() });
  assertEquals(shutting.status, 504);
  assertEquals(s.files.length, 1);
  assertEquals(p.rows("stt_jobs").length, 1);
});

// A failure of Soniox's on the way, as a status it can't give for now,
// leaves what it has: the next call goes on from there.
Deno.test("Soniox failing for now spends nothing again", async () => {
  const p = project();
  const s = soniox();
  let down = true;
  const flaky: typeof globalThis.fetch = (input, init) =>
    down && String(input).endsWith("/transcript")
      ? Promise.resolve(new Response("busy", { status: 503 }))
      : s.fetch(input, init);
  assertEquals((await transcribe(p, deps(p, flaky))).status, 502);
  assertEquals(p.rows("stt_jobs").length, 1);
  down = false;
  assertEquals((await transcribe(p, deps(p, flaky))).status, 200);
  assertEquals([s.files.length, p.rows("stt_jobs").length], [1, 0]);
});

// A transcription Soniox no longer has, as after its 30 days, is started
// again, once.
Deno.test("a transcription gone from Soniox starts again", async () => {
  const p = project();
  p.tables.stt_jobs = [{ recording_id: "rec", user_id: "ann", file_id: "old", transcription_id: "lost", model: "m" }];
  const s = soniox();
  const missing: typeof globalThis.fetch = (input, init) =>
    String(input).endsWith("/transcriptions/lost")
      ? Promise.resolve(new Response(null, { status: 404 }))
      : s.fetch(input, init);
  assertEquals((await transcribe(p, deps(p, missing))).status, 200);
  assertEquals([s.files.length, rec(p).status, p.rows("stt_jobs").length], [1, "ready", 0]);
});

// Another caller's transcription is theirs to pick up, by their own key.
Deno.test("a transcription another caller started is left to them", async () => {
  const p = project();
  p.rpcs.can_edit_page = () => true;
  p.rules.writes = () => true;
  p.tables.stt_jobs = [{
    recording_id: "rec",
    user_id: "bob",
    file_id: "theirs",
    transcription_id: "other",
    model: "m",
  }];
  const s = soniox();
  assertEquals((await transcribe(p, deps(p, s.fetch))).status, 200);
  assertEquals(s.files.length, 1);
  assertEquals(p.rows("stt_jobs").map((j) => j.user_id), ["bob"]);
});

// Audio sent that the deadline cuts off is no job to pick up: it would
// take as long again, and the words say what to do instead.
Deno.test("a recording too long to send is refused in words that say so", async () => {
  const p = project();
  const late: typeof globalThis.fetch = () => Promise.reject(new DOMException("late", "TimeoutError"));
  const failed = await transcribe(p, deps(p, late));
  assertEquals(failed.status, 504);
  assert(String(failed.body.error).includes("on-device"), failed.body.error);
  assert(!/try again|ask again/i.test(failed.body.error), failed.body.error);
  assertEquals(p.rows("stt_jobs"), []);
});

// A worker old enough to cut a call short says nothing of the recording:
// it goes back to the app, which asks again and gets a new worker's whole
// time, not told a recording that would take as long again is too long.
Deno.test("a call a warm worker cut short is asked again", async () => {
  const late: SttDeps["send"] = () => Promise.reject(new DOMException("late", "TimeoutError"));
  const p = project({ provider: "openai-compatible", base_url: "https://stt.example/v1" });
  const warm = { ...deps(p, soniox().fetch, late), deadlineMs: 120_000, workerStarted: Date.now() - 70_000 };
  const cut = await transcribe(p, warm);
  assertEquals([cut.status, cut.body.code], [504, "still_transcribing"]);
  assert(String(cut.body.error).includes("Ask again"), cut.body.error);
  assertEquals([rec(p).status, rec(p).error, rec(p).transcribing_at], ["failed", cut.body.error, null]);

  // Audio cut off on its way to Soniox, the same.
  const q = project();
  const sending: typeof globalThis.fetch = () => Promise.reject(new DOMException("late", "TimeoutError"));
  const sent = await transcribe(q, { ...deps(q, sending), deadlineMs: 120_000, workerStarted: Date.now() - 70_000 });
  assertEquals(sent.body.code, "still_transcribing");

  // So is a worker shutting down, whatever its age.
  const r = project({ provider: "openai-compatible", base_url: "https://stt.example/v1" });
  const shutting = await transcribe(r, { ...deps(r, soniox().fetch, late), unloading: AbortSignal.abort() });
  assertEquals(shutting.body.code, "still_transcribing");
});

// A call that had its time, on a new worker or one a second or two old,
// did not outlast it by the worker's doing: the words stand, and the app
// stops asking.
Deno.test("a call that had its time is told so", async () => {
  const late: SttDeps["send"] = () => Promise.reject(new DOMException("late", "TimeoutError"));
  const p = project({ provider: "openai-compatible", base_url: "https://stt.example/v1" });
  const fresh = { ...deps(p, soniox().fetch, late), deadlineMs: 120_000, workerStarted: Date.now() - 2_000 };
  const full = await transcribe(p, fresh);
  assertEquals([full.status, full.body.code], [504, undefined]);
  assert(String(full.body.error).includes("on-device"), full.body.error);
});

// An endpoint's text with what the database can't keep is saved without it.
Deno.test("a transcript keeps to what the database takes", async () => {
  const p = project({ provider: "openai-compatible", base_url: "https://stt.example/v1" });
  const send: SttDeps["send"] = () => Promise.resolve(Response.json({ text: "Hel\u0000lo \ud83d." }));
  assertEquals((await transcribe(p, deps(p, soniox().fetch, send))).status, 200);
  assertEquals((rec(p).transcript as { text: string }[]).map((t) => t.text), ["Hello \ufffd."]);
});
