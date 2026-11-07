// Speech to text (protocol §5): short-lived Soniox keys for live streaming,
// saving the user's Soniox key, and server-side transcription of uploaded
// recordings through Soniox or any OpenAI-compatible /audio/transcriptions.
// index.ts serves it; tests give it a project of their own.

import type { SupabaseClient, User } from "jsr:@supabase/supabase-js@2.118.0";
import { errorResponse, HttpError, json, preflight, readJson } from "../_shared/cors.ts";
import { type Clients, loadProvider, providerSecret, requireTokenUntil, requireUser } from "../_shared/supabase.ts";
import { LOCAL_PRESETS, MAX_ANSWER_BYTES, normalizeBaseUrl, readText } from "../_shared/openai.ts";
import { isTimeout, type safeFetch, UnsafeUrlError } from "../_shared/net.ts";
import { SAVE_MS, timeoutSignal, workDeadline } from "../_shared/deadline.ts";
import { storable } from "../_shared/blocks.ts";
import { type Segment, tokensToSegments } from "../_shared/segments.ts";

const SONIOX_API = "https://api.soniox.com/v1";
const SONIOX_ASYNC_MODEL = "stt-async-v3";
const POLL_MS = 2_000;
// Removing the audio and transcription from Soniox, once it is done with them.
const CLEANUP_MS = 5_000;
// The quick calls: a temporary key, checking a key.
const TIMEOUT_MS = 20_000;
// Sending the audio or waiting on an endpoint outlasted the deadline, as it
// would again.
const TOO_LONG =
  "This recording took too long to transcribe on the server, and would take as long again. Choose an on-device model for it.";
// Soniox has the audio and goes on with it past the deadline, which a long
// recording is often past: the next call picks its transcription up.
const STILL =
  "Soniox is still transcribing this recording. Ask again in a minute or two: it goes on there, and asking again costs nothing more.";
const still = () => new HttpError(504, STILL, "still_transcribing");
// A call on a worker old enough to cut its time short, or shutting down,
// says nothing of how long a recording takes: asked again, it has a new
// worker's whole time. Less lost than this, a call had its time.
const CUT_SHORT = "The server had too little time for this recording just now. Ask again in a minute.";
const CUT_SHORT_MS = 10_000;
// What a transcription that failed says, in the recording, which everyone
// who can view the page reads, and to the caller: the transcriber's own
// words, and the address it was sent to, go to the log only.
const FAILED = "The transcription failed on the server. Try again, or choose an on-device model.";
const UNREACHABLE = "Could not reach the transcription endpoint.";
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface SttDeps {
  clients: Clients;
  /** fetch for Soniox's API, whose address is fixed. */
  fetch: typeof fetch;
  /** fetch for an endpoint someone typed: safeFetch, which keeps it off private addresses. */
  send: typeof safeFetch;
  /**
   * When a call gives up, from its start, before the gateway gives up on
   * its answer, which comes only at the end (150 s on any plan), and before
   * the platform stops its worker, keeping time to say how it ended.
   */
  deadlineMs: number;
  /** When the worker serving this started; none counts from the request. */
  workerStarted?: number;
  /** Aborts as the platform is about to shut the worker down. */
  unloading?: AbortSignal;
  /**
   * The longest audio transcribed here. It is streamed from Storage to the
   * transcriber, never held whole, so the deadline bounds it, not memory.
   */
  maxAudioBytes: number;
  /** Waits between asking Soniox whether it is done; tests wait less. */
  pollMs?: number;
}

interface Body {
  action: "soniox_key" | "save_key" | "transcribe" | "transcribed";
  provider?: string;
  api_key?: string;
  recording_id?: string;
  /** An audio file to transcribe, in place of recording_id. */
  file_id?: string;
  language?: string;
}

interface SttSettings {
  provider: string;
  model: string | null;
  base_url: string | null;
  provider_id: string | null;
  language: string;
  speaker_labels: boolean;
}

export async function handle(req: Request, deps: SttDeps): Promise<Response> {
  const started = Date.now();
  const pre = preflight(req);
  if (pre) return pre;
  try {
    const body = await readJson<Body>(req);
    const uc = deps.clients.user(req);
    const user = await requireUser(req, uc);
    const sc = deps.clients.service();
    switch (body.action) {
      case "soniox_key":
        return json(await sonioxTemporaryKey(sc, user, deps));
      case "save_key":
        return json(await saveKey(uc, body, deps));
      case "transcribe":
        // The recording's transcript, or its failure, is saved as the caller.
        requireTokenUntil(req, workDeadline(started, deps.deadlineMs, deps.workerStarted) + SAVE_MS);
        return json(
          body.file_id
            ? await transcribeFile(uc, sc, user, body, started, deps)
            : await transcribe(uc, sc, user, body, started, deps),
        );
      case "transcribed":
        return json(await fileTranscribed(sc, user, body, deps));
      default:
        throw new HttpError(400, "Unknown action");
    }
  } catch (e) {
    return errorResponse(e);
  }
}

async function sonioxKey(sc: SupabaseClient, user: User): Promise<string> {
  const { data, error } = await sc.rpc("get_stt_secret", {
    p_user_id: user.id,
    p_provider: "soniox",
  });
  if (error) throw new Error(error.message);
  // Every user brings their own Soniox key; the server has none of its own.
  const key = data as string | null;
  if (!key) throw new HttpError(400, "Add a Soniox API key in Settings › Speech to text");
  return key;
}

/**
 * A Soniox call that failed, said by its status. What Soniox wrote goes to
 * the log only: a failed transcription keeps its error in the recording,
 * which everyone who can view the page reads.
 */
async function sonioxFailure(res: Response, what: string): Promise<HttpError> {
  const text = await readText(res, 64 << 10, true).catch(() => "");
  console.error(`Soniox ${what}: ${res.status} ${text.slice(0, 300)}`);
  const why = res.status === 401 || res.status === 403
    ? "Soniox rejected the API key"
    : `Soniox ${what} failed (${res.status})`;
  return new HttpError(res.status === 401 ? 400 : 502, why);
}

async function sonioxTemporaryKey(sc: SupabaseClient, user: User, deps: SttDeps) {
  const key = await sonioxKey(sc, user);
  const res = await deps.fetch(`${SONIOX_API}/auth/temporary-api-key`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      usage_type: "transcribe_websocket",
      expires_in_seconds: 3600,
      client_reference_id: user.id,
    }),
    signal: AbortSignal.timeout(TIMEOUT_MS),
  });
  if (!res.ok) throw await sonioxFailure(res, "temporary key");
  const data = await res.json();
  return { api_key: data.api_key, expires_at: data.expires_at };
}

async function saveKey(uc: SupabaseClient, body: Body, deps: SttDeps) {
  if (body.provider !== "soniox") throw new HttpError(400, "Only Soniox keys are saved here");
  const key = (body.api_key ?? "").trim();
  if (key) {
    const check = await deps.fetch(`${SONIOX_API}/models`, {
      headers: { Authorization: `Bearer ${key}` },
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
    // Only its status says anything; the body goes unread, so closed.
    await check.body?.cancel().catch(() => {});
    if (check.status === 401 || check.status === 403) throw new HttpError(400, "Soniox rejected this API key");
  }
  const { error } = await uc.rpc("set_stt_secret", { p_provider: "soniox", p_secret: key });
  if (error) throw new Error(error.message);
  return { ok: true };
}

function languageCode(raw?: string | null): string | null {
  const code = (raw ?? "").trim().toLowerCase();
  return /^[a-z]{2,3}$/.test(code) ? code : null;
}

/** A signal that aborts the provider calls at the transcription's deadline, or as the worker is shut down. */
function until(deadline: number, deps: SttDeps): AbortSignal {
  return timeoutSignal(Math.max(1, deadline - Date.now()), deps.unloading);
}

/**
 * Where an OpenAI-compatible transcription goes, and with which key. A
 * provider's key goes only to the provider's own address, which
 * providers.save checked: the endpoint in the settings, which the client
 * writes, may name it but not send the key anywhere else. A local
 * provider's address is the device's, which providers.save never checks
 * and the server never calls.
 */
export async function endpointFor(
  uc: SupabaseClient,
  sc: SupabaseClient,
  settings: Pick<SttSettings, "base_url" | "provider_id">,
): Promise<{ baseUrl: string; apiKey: string | null }> {
  if (!settings.provider_id) return { baseUrl: normalizeBaseUrl(settings.base_url!), apiKey: null };
  const provider = await loadProvider(uc, settings.provider_id);
  if (provider.is_local || LOCAL_PRESETS.has(provider.preset)) {
    throw new HttpError(400, "Local providers are reached from your device, not from the server");
  }
  const baseUrl = normalizeBaseUrl(provider.base_url, provider.preset);
  if (settings.base_url && new URL(normalizeBaseUrl(settings.base_url)).href !== new URL(baseUrl).href) {
    throw new HttpError(
      400,
      `The endpoint is not ${baseUrl}, the address of the provider whose key it uses. Choose no key, or that address.`,
    );
  }
  return { baseUrl, apiKey: await providerSecret(sc, provider.id) };
}

/** Where a caller's audio goes: their settings, the language asked for, and the key or the address. */
async function transcriberOf(uc: SupabaseClient, sc: SupabaseClient, user: User, body: Body) {
  const { data: settingsRow } = await uc
    .from("stt_settings")
    .select("provider, model, base_url, provider_id, language, speaker_labels")
    .eq("user_id", user.id)
    .maybeSingle();
  const settings: SttSettings = settingsRow ?? {
    provider: "openai-compatible",
    model: null,
    base_url: null,
    provider_id: null,
    language: "auto",
    speaker_labels: false,
  };
  const useSoniox = settings.provider === "soniox";
  if (!useSoniox && !settings.provider_id && !settings.base_url) {
    throw new HttpError(
      400,
      "On-device models transcribe on your device. Choose Soniox or an OpenAI-compatible endpoint to transcribe on the server.",
    );
  }
  const language = languageCode(body.language ?? settings.language);
  // Where the audio goes, and its key, before anything is claimed: settings
  // that cannot work leave what was asked for as it was.
  const sonioxApiKey = useSoniox ? await sonioxKey(sc, user) : null;
  const endpoint = useSoniox ? null : await endpointFor(uc, sc, settings);
  return { settings, language, useSoniox, sonioxApiKey, endpoint };
}

/** What a transcription that failed says, and how: only this function's own words, what the transcriber answered going to the log. */
function failure(e: unknown, started: number, deadline: number, deps: SttDeps) {
  // A provider call stopped at the deadline.
  const timedOut = isTimeout(e);
  const cutShort = timedOut && (started + deps.deadlineMs - deadline >= CUT_SHORT_MS || !!deps.unloading?.aborted);
  const told = cutShort
    ? CUT_SHORT
    : timedOut
    ? TOO_LONG
    : e instanceof HttpError
    ? e.message
    : e instanceof UnsafeUrlError
    ? UNREACHABLE
    : FAILED;
  if (!timedOut && !(e instanceof HttpError)) console.error("transcription:", e);
  const raised = cutShort
    ? new HttpError(504, told, "still_transcribing")
    : e instanceof HttpError
    ? e
    : new HttpError(timedOut ? 504 : 502, told);
  return { told, raised };
}

async function transcribe(
  uc: SupabaseClient,
  sc: SupabaseClient,
  user: User,
  body: Body,
  started: number,
  deps: SttDeps,
) {
  const deadline = workDeadline(started, deps.deadlineMs, deps.workerStarted);
  if (!body.recording_id) throw new HttpError(400, "recording_id is required");
  const { data: rec, error } = await uc
    .from("recordings")
    .select("id, page_id, audio_path, duration_ms")
    .eq("id", body.recording_id)
    .maybeSingle();
  if (error) throw new Error(error.message);
  if (!rec) throw new HttpError(404, "Recording not found");
  // recordings/{user_id}/{recording_id}.wav, as the app uploads it.
  const slash = rec.audio_path?.lastIndexOf("/") ?? -1;
  const fileName = rec.audio_path?.slice(slash + 1) ?? "";
  if (!fileName) throw new HttpError(400, "This recording's audio has not been uploaded");

  const { settings, language, useSoniox, sonioxApiKey, endpoint } = await transcriberOf(uc, sc, user, body);

  const folder = rec.audio_path.slice(0, Math.max(slash, 0));
  const { data: listed, error: listError } = await uc.storage.from("recordings").list(folder, {
    search: fileName,
    limit: 100,
  });
  if (listError) throw new Error(`Could not read the audio: ${listError.message}`);
  const object = listed?.find((o) => o.name === fileName);
  if (!object) throw new HttpError(404, "This recording's audio is not on the server");
  const size = Number(object.metadata?.size ?? 0);
  if (size > deps.maxAudioBytes) {
    throw new HttpError(
      413,
      `This recording is too long to transcribe on the server (over ${
        Math.round(deps.maxAudioBytes / 1024 / 1024)
      } MB). Choose an on-device model to transcribe it.`,
    );
  }

  // The recording is claimed, so two calls at once, two devices or a
  // second tap, do not both spend the key: transcribing_at says since when
  // a call has it. A claim older than a call can run was left by one the
  // platform stopped, and is taken again. The app sets the recording's
  // status itself, so the status cannot say whether a call has it.
  //
  // The transcript is saved as the caller, which RLS allows only an editor
  // of the recording's page: for a viewer the update matches no row and
  // says nothing, so they would spend their key on a transcript that is
  // not saved. Claiming, before any of that, asks first.
  const staleBefore = new Date(started - deps.deadlineMs - SAVE_MS - 60_000).toISOString();
  const { data: claimed, error: claimError } = await uc.from("recordings")
    .update({ status: "transcribing", error: null, transcribing_at: new Date(started).toISOString() })
    .eq("id", rec.id)
    .or(`transcribing_at.is.null,transcribing_at.lt.${staleBefore}`)
    .select("id");
  if (claimError) throw new Error(claimError.message);
  if (!claimed?.length) {
    const { data: editable } = await uc.rpc("can_edit_page", { p_page_id: rec.page_id });
    if (editable === true) throw new HttpError(409, "This recording is being transcribed already");
    throw new HttpError(403, "You can view this recording but not edit its page");
  }
  const audio: Audio = {
    name: fileName,
    type: mediaType(object.metadata?.mimetype),
    open: (signal) => openAudio(uc, "recordings", rec.audio_path, deps.maxAudioBytes, signal),
  };
  try {
    const { segments, model, finish } = sonioxApiKey
      ? await sonioxFile(
        recordingJobs(sc, { recording_id: rec.id, user_id: user.id }),
        sonioxApiKey,
        audio,
        settings,
        language,
        deadline,
        deps,
      )
      : await openAiCompatible(endpoint!, audio, settings, language, rec.duration_ms ?? 0, deadline, deps);

    const { data: saved, error: saveError } = await uc.from("recordings").update({
      transcript: segments,
      status: "ready",
      stt_provider: useSoniox ? "soniox" : "openai-compatible",
      stt_model: model,
      language,
      error: null,
      transcribing_at: null,
    }).eq("id", rec.id).select("id");
    if (saveError) throw new Error(saveError.message);
    await finish?.();
    // Its page was shared view-only, or the recording deleted, meanwhile.
    if (!saved?.length) {
      throw new HttpError(403, "The transcript could not be saved: the recording is no longer yours to edit");
    }
    return { segments };
  } catch (e) {
    // A provider call stopped at the deadline: the recording fails rather
    // than staying transcribing when the platform stops the function,
    // Soniox's going on from there at the next call.
    const { told, raised } = failure(e, started, deadline, deps);
    await uc.from("recordings").update({ status: "failed", error: told, transcribing_at: null }).eq("id", rec.id);
    throw raised;
  }
}

/**
 * Transcribes an audio file of a page and answers with its segments, which
 * the caller's app puts in the file's notes: no row here holds them. One
 * call at a time has it, for each person, whose key it spends. Soniox keeps
 * what it made until the app says it has it (transcribed), so an app
 * stopped before the answer reached it asks again for nothing.
 */
async function transcribeFile(
  uc: SupabaseClient,
  sc: SupabaseClient,
  user: User,
  body: Body,
  started: number,
  deps: SttDeps,
) {
  const deadline = workDeadline(started, deps.deadlineMs, deps.workerStarted);
  const id = body.file_id!;
  if (!UUID.test(id)) throw new HttpError(400, "file_id is not a file's");
  const { data: file, error } = await uc
    .from("files")
    .select("id, page_id, storage_path, mime_type, deleted_at")
    .eq("id", id)
    .maybeSingle();
  if (error) throw new Error(error.message);
  if (!file || file.deleted_at) throw new HttpError(404, "File not found");
  // Its notes are its page's, which a viewer can't change: nothing of their
  // key goes on what they could not keep.
  if (file.page_id) {
    const { data: editable } = await uc.rpc("can_edit_page", { p_page_id: file.page_id });
    if (editable !== true) throw new HttpError(403, "You can view this file but not edit its page");
  }
  const path: string = file.storage_path ?? "";
  const slash = path.lastIndexOf("/");
  const stored = path.slice(slash + 1);
  if (!stored) throw new HttpError(404, "This file has not finished uploading");
  const { settings, language, sonioxApiKey, endpoint } = await transcriberOf(uc, sc, user, body);

  const { data: listed, error: listError } = await uc.storage.from("files").list(path.slice(0, Math.max(slash, 0)), {
    search: stored,
    limit: 100,
  });
  if (listError) throw new Error(`Could not read the audio: ${listError.message}`);
  const object = listed?.find((o) => o.name === stored);
  if (!object) throw new HttpError(404, "This file has not finished uploading");
  if (Number(object.metadata?.size ?? 0) > deps.maxAudioBytes) {
    throw new HttpError(
      413,
      `This file is too large to transcribe on the server (over ${
        Math.round(deps.maxAudioBytes / 1024 / 1024)
      } MB). Choose an on-device model to transcribe it.`,
    );
  }

  const mine = { source_file_id: id, user_id: user.id };
  const now = new Date(started).toISOString();
  const staleBefore = new Date(started - deps.deadlineMs - SAVE_MS - 60_000).toISOString();
  const { data: made, error: claimError } = await sc.from("stt_file_jobs")
    .upsert({ ...mine, claimed_at: now }, { onConflict: "source_file_id,user_id", ignoreDuplicates: true })
    .select("source_file_id");
  if (claimError) throw new Error(claimError.message);
  if (!made?.length) {
    const { data: retaken, error: retakeError } = await sc.from("stt_file_jobs")
      .update({ claimed_at: now })
      .match(mine)
      .or(`claimed_at.is.null,claimed_at.lt.${staleBefore}`)
      .select("source_file_id");
    if (retakeError) throw new Error(retakeError.message);
    if (!retaken?.length) throw new HttpError(409, "This file is being transcribed already");
  }
  // What the call leaves: Soniox's job for the next, or nothing at all.
  const release = async () => {
    const { error } = await sc.from("stt_file_jobs").update({ claimed_at: null }).match(mine);
    if (error) console.error("stt_file_jobs:", error.message);
    const gone = await sc.from("stt_file_jobs").delete().match(mine).is("transcription_id", null);
    if (gone.error) console.error("stt_file_jobs:", gone.error.message);
  };
  const ext = /\.[A-Za-z0-9]{1,8}$/.exec(stored)?.[0] ?? "";
  const audio: Audio = {
    // Its extension is all the transcriber needs of a name that is the
    // user's: it says what the bytes are.
    name: "audio" + ext,
    type: mediaType(object.metadata?.mimetype ?? file.mime_type),
    open: (signal) => openAudio(uc, "files", path, deps.maxAudioBytes, signal),
  };
  try {
    const { segments } = sonioxApiKey
      ? await sonioxFile(fileJobs(sc, mine), sonioxApiKey, audio, settings, language, deadline, deps)
      : await openAiCompatible(endpoint!, audio, settings, language, 0, deadline, deps);
    await release();
    return { segments };
  } catch (e) {
    const { raised } = failure(e, started, deadline, deps);
    await release();
    throw raised;
  }
}

/** The app has a file's transcript: what Soniox keeps of it goes. */
async function fileTranscribed(sc: SupabaseClient, user: User, body: Body, deps: SttDeps) {
  const id = body.file_id ?? "";
  if (!UUID.test(id)) throw new HttpError(400, "file_id is not a file's");
  const mine = { source_file_id: id, user_id: user.id };
  const { data: left, error } = await sc.from("stt_file_jobs")
    .select("file_id, transcription_id, model")
    .match(mine)
    .maybeSingle();
  if (error) throw new Error(error.message);
  if (left?.transcription_id) {
    await sonioxRemove(deps.fetch, await sonioxKey(sc, user), left as SonioxJob);
  }
  await sc.from("stt_file_jobs").delete().match(mine);
  return { ok: true };
}

/** What a transcriber made, and what is to go once it is saved. */
interface Transcribed {
  segments: Segment[];
  model: string;
  finish?(): Promise<void>;
}

/**
 * A recording's audio as it is sent: its file name and media type, and its
 * bytes, streamed from Storage anew each time it is sent.
 */
interface Audio {
  name: string;
  type: string;
  open(signal: AbortSignal): Promise<ReadableStream<Uint8Array>>;
}

/** A media type as a part's header may carry it, or the one for any bytes. */
function mediaType(raw: unknown): string {
  const type = String(raw ?? "");
  return /^[\w.+-]+\/[\w.+-]+$/.test(type) ? type : "application/octet-stream";
}

/**
 * The recording's audio from Storage, as a stream that fails past maxBytes:
 * Storage told its size before the recording was claimed, but its recorder
 * may upload it again meanwhile.
 */
async function openAudio(
  uc: SupabaseClient,
  bucket: "recordings" | "files",
  path: string,
  maxBytes: number,
  signal: AbortSignal,
): Promise<ReadableStream<Uint8Array>> {
  const { data, error } = await uc.storage.from(bucket).download(path, {}, { signal }).asStream();
  if (error || !data) {
    console.error("audio:", error?.message ?? "missing");
    throw new HttpError(502, "Could not read the audio");
  }
  let read = 0;
  return (data as ReadableStream<Uint8Array>).pipeThrough(
    new TransformStream<Uint8Array, Uint8Array>({
      transform(chunk, out) {
        read += chunk.byteLength;
        if (read > maxBytes) out.error(new HttpError(413, "This audio is too long to transcribe on the server"));
        else out.enqueue(chunk);
      },
    }),
  );
}

/**
 * Posts the audio, after fields, as multipart/form-data through post. The
 * audio's bytes go on as Storage sends them, so a recording is never in
 * memory whole; the body has no length, and goes chunked, or in HTTP/2's
 * frames. The download stops once post has answered, read whole or not.
 */
async function postAudio(
  audio: Audio,
  fields: [string, string][],
  signal: AbortSignal,
  post: (body: ReadableStream<Uint8Array>, contentType: string) => Promise<Response>,
): Promise<Response> {
  const answered = new AbortController();
  try {
    const bytes = await audio.open(AbortSignal.any([signal, answered.signal]));
    const boundary = `leaflet-${crypto.randomUUID()}`;
    // The file's name goes in quotes: none of its own, nor a line break.
    const name = audio.name.replace(/["\r\n]/g, (c) => encodeURIComponent(c));
    const head = fields
      .map(([field, value]) => `--${boundary}\r\nContent-Disposition: form-data; name="${field}"\r\n\r\n${value}\r\n`)
      .join("") +
      `--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="${name}"\r\n` +
      `Content-Type: ${audio.type}\r\n\r\n`;
    const text = new TextEncoder();
    const body = ReadableStream.from(between(text.encode(head), bytes, text.encode(`\r\n--${boundary}--\r\n`)));
    return await post(body, `multipart/form-data; boundary=${boundary}`);
  } finally {
    answered.abort();
  }
}

/** head, then the bytes as they come, then tail. */
async function* between(
  head: Uint8Array,
  bytes: ReadableStream<Uint8Array>,
  tail: Uint8Array,
): AsyncGenerator<Uint8Array> {
  yield head;
  yield* bytes;
  yield tail;
}

async function openAiCompatible(
  { baseUrl, apiKey }: { baseUrl: string; apiKey: string | null },
  audio: Audio,
  settings: SttSettings,
  language: string | null,
  durationMs: number,
  deadline: number,
  deps: SttDeps,
): Promise<Transcribed> {
  const model = settings.model || "whisper-1";

  const send = (format: "verbose_json" | "json") => {
    const fields: [string, string][] = [["model", model], ["response_format", format]];
    if (format === "verbose_json") fields.push(["timestamp_granularities[]", "segment"]);
    if (language) fields.push(["language", language]);
    const signal = until(deadline, deps);
    // The base URL is the user's: safeFetch keeps it off private addresses.
    return postAudio(audio, fields, signal, (body, contentType) =>
      deps.send(`${baseUrl}/audio/transcriptions`, {
        method: "POST",
        headers: { ...(apiKey ? { Authorization: `Bearer ${apiKey}` } : {}), "Content-Type": contentType },
        body,
        signal,
      }));
  };

  // Some models (gpt-4o-transcribe) only return plain json: ask them for it
  // rather than sending the audio twice. Other models get one retry, only
  // when the endpoint turns verbose_json down.
  const jsonOnly = /^gpt-4o.*transcribe/i.test(model);
  let res = await send(jsonOnly ? "json" : "verbose_json");
  if (!jsonOnly && res.status === 400) {
    const text = await readText(res, 64 << 10, true).catch(() => "");
    if (/response_format|verbose_json|timestamp_granularities/i.test(text)) {
      res = await send("json");
    } else {
      res = new Response(null, { status: 400, statusText: res.statusText });
    }
  }
  if (!res.ok) {
    await res.body?.cancel().catch(() => {});
    throw new HttpError(502, `Transcription failed: the endpoint answered ${res.status}`);
  }
  // As much as an answer may be: an endpoint that answers without end is
  // cut off. One that is no JSON fails as any other failure does.
  const data = JSON.parse(await readText(res, MAX_ANSWER_BYTES));
  const segments: Segment[] = Array.isArray(data?.segments) && data.segments.length
    ? data.segments
      .map((s: { start?: number; end?: number; text?: string; speaker?: string }) => ({
        start_ms: Math.round((s.start ?? 0) * 1000),
        end_ms: Math.round((s.end ?? 0) * 1000),
        text: storable(String(s.text ?? "")).trim(),
        ...(s.speaker ? { speaker: storable(String(s.speaker)) } : {}),
      }))
      .filter((s: Segment) => s.text)
    : storable(String(data?.text ?? "")).trim()
    ? [{ start_ms: 0, end_ms: durationMs, text: storable(String(data.text)).trim() }]
    : [];
  return { segments, model };
}

/** A transcription Soniox has of a recording, which a later call can pick up (stt_jobs). */
interface SonioxJob {
  file_id: string;
  transcription_id: string;
  model: string;
}

/** Where a transcription's job is kept, for a later call to pick up. */
interface SonioxJobs {
  load(): Promise<SonioxJob | null>;
  save(job: SonioxJob): Promise<void>;
  forget(): Promise<void>;
  /** Whether Soniox's copy goes as soon as the call has it, or waits for word from the app. */
  keeps: boolean;
}

/** A recording's job: its transcript is saved before Soniox's goes. */
function recordingJobs(sc: SupabaseClient, mine: { recording_id: string; user_id: string }): SonioxJobs {
  return {
    keeps: false,
    load: async () => {
      const { data, error } = await sc.from("stt_jobs").select("file_id, transcription_id, model").match(mine)
        .maybeSingle();
      if (error) throw new Error(error.message);
      return data as SonioxJob | null;
    },
    save: async (job) => {
      const { error } = await sc.from("stt_jobs").insert({ ...mine, ...job });
      if (error) throw new Error(error.message);
    },
    forget: async () => {
      const { error } = await sc.from("stt_jobs").delete().match(mine);
      if (error) console.error("stt_jobs:", error.message);
    },
  };
}

/** A file's job, on the row its call claimed. */
function fileJobs(sc: SupabaseClient, mine: { source_file_id: string; user_id: string }): SonioxJobs {
  return {
    keeps: true,
    load: async () => {
      const { data, error } = await sc.from("stt_file_jobs").select("file_id, transcription_id, model").match(mine)
        .maybeSingle();
      if (error) throw new Error(error.message);
      return data?.transcription_id ? data as SonioxJob : null;
    },
    save: async (job) => {
      const { error } = await sc.from("stt_file_jobs").update(job).match(mine);
      if (error) throw new Error(error.message);
    },
    forget: async () => {
      const { error } = await sc.from("stt_file_jobs")
        .update({ file_id: null, transcription_id: null, model: null })
        .match(mine);
      if (error) console.error("stt_file_jobs:", error.message);
    },
  };
}

/** Takes from Soniox what a job left there. */
async function sonioxRemove(fetch: typeof globalThis.fetch, key: string, job: SonioxJob) {
  const remove = (path: string) =>
    fetch(`${SONIOX_API}/${path}`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${key}` },
      signal: AbortSignal.timeout(CLEANUP_MS),
    })
      .then((res) => res.body?.cancel())
      .catch(() => {});
  await remove(`transcriptions/${job.transcription_id}`);
  await remove(`files/${job.file_id}`);
}

/**
 * Transcribes audio through Soniox's files and transcriptions. What
 * it spends goes once: the audio is sent and a transcription started, kept
 * as the caller's job on the recording before anything waits on it, and a
 * call that runs out of time, or is stopped, leaves it going on at Soniox
 * for the next call to pick up, with no audio sent and no key spent again.
 * What went to Soniox goes from it only once it is done with it, as it
 * keeps what it is still transcribing, and with nothing done it deletes it
 * by itself within 30 days.
 */
async function sonioxFile(
  jobs: SonioxJobs,
  key: string,
  audio: Audio,
  settings: SttSettings,
  language: string | null,
  deadline: number,
  deps: SttDeps,
): Promise<Transcribed> {
  const fetch = deps.fetch;
  const pollMs = deps.pollMs ?? POLL_MS;
  const auth = { Authorization: `Bearer ${key}` };
  const remove = (path: string) =>
    fetch(`${SONIOX_API}/${path}`, { method: "DELETE", headers: auth, signal: AbortSignal.timeout(CLEANUP_MS) })
      .then((res) => res.body?.cancel())
      .catch(() => {});
  const finish = async (job: SonioxJob) => {
    await sonioxRemove(fetch, key, job);
    await jobs.forget();
  };

  // Sends the audio and starts transcribing it.
  const start = async (): Promise<SonioxJob> => {
    const model = settings.model?.startsWith("stt-async") ? settings.model : SONIOX_ASYNC_MODEL;
    const signal = until(deadline, deps);
    const upload = await postAudio(audio, [], signal, (body, contentType) =>
      fetch(`${SONIOX_API}/files`, {
        method: "POST",
        headers: { ...auth, "Content-Type": contentType },
        body,
        signal,
      }));
    if (!upload.ok) throw await sonioxFailure(upload, "upload");
    const fileId: string = (await upload.json()).id;
    try {
      const create = await fetch(`${SONIOX_API}/transcriptions`, {
        method: "POST",
        headers: { ...auth, "Content-Type": "application/json" },
        body: JSON.stringify({
          model,
          file_id: fileId,
          language_hints: language ? [language] : ["en", "zh"],
          enable_speaker_diarization: settings.speaker_labels,
        }),
        signal: until(deadline, deps),
      });
      if (!create.ok) throw await sonioxFailure(create, "transcription");
      const job = { file_id: fileId, transcription_id: (await create.json()).id as string, model };
      await jobs.save(job);
      return job;
    } catch (e) {
      await remove(`files/${fileId}`);
      throw e;
    }
  };

  // Waits on a job as long as this call may, or null when Soniox no longer
  // has it. A failure of a call here, Soniox's or the network's, leaves the
  // job as it is for the next call: only its own end is one.
  const wait = async (job: SonioxJob): Promise<Transcribed | null> => {
    try {
      while (true) {
        const status = await fetch(`${SONIOX_API}/transcriptions/${job.transcription_id}`, {
          headers: auth,
          signal: until(deadline, deps),
        });
        if (status.status === 404) {
          await status.body?.cancel().catch(() => {});
          return null;
        }
        if (!status.ok) throw await sonioxFailure(status, "status");
        const info = await status.json();
        if (info.status === "completed") break;
        if (info.status === "error") {
          console.error("Soniox transcription:", info.error_message);
          await finish(job);
          throw new HttpError(502, "Soniox could not transcribe the recording");
        }
        if (Date.now() + pollMs >= deadline || deps.unloading?.aborted) throw still();
        await new Promise((r) => setTimeout(r, pollMs));
      }

      const transcript = await fetch(`${SONIOX_API}/transcriptions/${job.transcription_id}/transcript`, {
        headers: auth,
        signal: until(deadline, deps),
      });
      if (!transcript.ok) throw await sonioxFailure(transcript, "transcript");
      const data = await transcript.json();
      return {
        segments: tokensToSegments(data.tokens ?? []),
        model: job.model,
        ...(jobs.keeps ? {} : { finish: () => finish(job) }),
      };
    } catch (e) {
      throw isTimeout(e) ? still() : e;
    }
  };

  const left = await jobs.load();
  if (left) {
    const done = await wait(left);
    if (done) return done;
    await jobs.forget();
  }
  const done = await wait(await start());
  if (!done) throw new HttpError(502, "Soniox could not transcribe the recording");
  return done;
}
