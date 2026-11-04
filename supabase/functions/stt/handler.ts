// Speech to text (protocol §5): short-lived Soniox keys for live streaming,
// saving the user's Soniox key, and server-side transcription of uploaded
// recordings through Soniox or any OpenAI-compatible /audio/transcriptions.
// index.ts serves it; tests give it a project of their own.

import type { SupabaseClient, User } from "jsr:@supabase/supabase-js@2.118.0";
import { errorResponse, HttpError, json, preflight, readJson } from "../_shared/cors.ts";
import { type Clients, loadProvider, providerSecret, requireTokenUntil, requireUser } from "../_shared/supabase.ts";
import { LOCAL_PRESETS, MAX_ANSWER_BYTES, normalizeBaseUrl, readText } from "../_shared/openai.ts";
import { isTimeout, type safeFetch, UnsafeUrlError } from "../_shared/net.ts";
import { SAVE_MS } from "../_shared/deadline.ts";
import { type Segment, tokensToSegments } from "../_shared/segments.ts";

const SONIOX_API = "https://api.soniox.com/v1";
const SONIOX_ASYNC_MODEL = "stt-async-v3";
const POLL_MS = 2_000;
// Removing the audio and transcription from Soniox, after the deadline too.
const CLEANUP_MS = 5_000;
// The quick calls: a temporary key, checking a key.
const TIMEOUT_MS = 20_000;
const TOO_LONG = "The transcription took too long on the server. Try again, or choose an on-device model.";
// What a transcription that failed says, in the recording, which everyone
// who can view the page reads, and to the caller: the transcriber's own
// words, and the address it was sent to, go to the log only.
const FAILED = "The transcription failed on the server. Try again, or choose an on-device model.";
const UNREACHABLE = "Could not reach the transcription endpoint.";

export interface SttDeps {
  clients: Clients;
  /** fetch for Soniox's API, whose address is fixed. */
  fetch: typeof fetch;
  /** fetch for an endpoint someone typed: safeFetch, which keeps it off private addresses. */
  send: typeof safeFetch;
  /**
   * When a transcription gives up, from its start, before the platform
   * stops the function (150 s on the free plan, 400 s on paid plans),
   * keeping time to mark the recording failed and remove what went to
   * Soniox.
   */
  deadlineMs: number;
  /**
   * The longest audio transcribed here. It is streamed from Storage to the
   * transcriber, never held whole, so the deadline bounds it, not memory.
   */
  maxAudioBytes: number;
  /** Waits between asking Soniox whether it is done; tests wait less. */
  pollMs?: number;
}

interface Body {
  action: "soniox_key" | "save_key" | "transcribe";
  provider?: string;
  api_key?: string;
  recording_id?: string;
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
        requireTokenUntil(req, started + deps.deadlineMs + SAVE_MS);
        return json(await transcribe(uc, sc, user, body, started, deps));
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

/** A signal that aborts the provider calls at the transcription's deadline. */
function until(deadline: number): AbortSignal {
  return AbortSignal.timeout(Math.max(1, deadline - Date.now()));
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

async function transcribe(
  uc: SupabaseClient,
  sc: SupabaseClient,
  user: User,
  body: Body,
  started: number,
  deps: SttDeps,
) {
  const deadline = started + deps.deadlineMs;
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
  // that cannot work leave the recording as it was.
  const sonioxApiKey = useSoniox ? await sonioxKey(sc, user) : null;
  const endpoint = useSoniox ? null : await endpointFor(uc, sc, settings);

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
    open: (signal) => openAudio(uc, rec.audio_path, deps.maxAudioBytes, signal),
  };
  try {
    const { segments, model } = sonioxApiKey
      ? await sonioxFile(sonioxApiKey, audio, settings, language, deadline, deps)
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
    // Its page was shared view-only, or the recording deleted, meanwhile.
    if (!saved?.length) {
      throw new HttpError(403, "The transcript could not be saved: the recording is no longer yours to edit");
    }
    return { segments };
  } catch (e) {
    // A provider call stopped at the deadline: the recording fails rather
    // than staying transcribing when the platform stops the function.
    // Only this function's own words are told: what the transcriber
    // answered, which may be anything, goes to the log.
    const timedOut = isTimeout(e);
    const told = timedOut
      ? TOO_LONG
      : e instanceof HttpError
      ? e.message
      : e instanceof UnsafeUrlError
      ? UNREACHABLE
      : FAILED;
    if (!timedOut && !(e instanceof HttpError)) console.error("transcription:", e);
    await uc.from("recordings").update({ status: "failed", error: told, transcribing_at: null }).eq("id", rec.id);
    throw e instanceof HttpError ? e : new HttpError(timedOut ? 504 : 502, told);
  }
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
  path: string,
  maxBytes: number,
  signal: AbortSignal,
): Promise<ReadableStream<Uint8Array>> {
  const { data, error } = await uc.storage.from("recordings").download(path, {}, { signal }).asStream();
  if (error || !data) {
    console.error("recording audio:", error?.message ?? "missing");
    throw new HttpError(502, "Could not read the recording's audio");
  }
  let read = 0;
  return (data as ReadableStream<Uint8Array>).pipeThrough(
    new TransformStream<Uint8Array, Uint8Array>({
      transform(chunk, out) {
        read += chunk.byteLength;
        if (read > maxBytes) out.error(new HttpError(413, "This recording is too long to transcribe on the server"));
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
): Promise<{ segments: Segment[]; model: string }> {
  const model = settings.model || "whisper-1";

  const send = (format: "verbose_json" | "json") => {
    const fields: [string, string][] = [["model", model], ["response_format", format]];
    if (format === "verbose_json") fields.push(["timestamp_granularities[]", "segment"]);
    if (language) fields.push(["language", language]);
    const signal = until(deadline);
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
        text: String(s.text ?? "").trim(),
        ...(s.speaker ? { speaker: String(s.speaker) } : {}),
      }))
      .filter((s: Segment) => s.text)
    : String(data?.text ?? "").trim()
    ? [{ start_ms: 0, end_ms: durationMs, text: String(data.text).trim() }]
    : [];
  return { segments, model };
}

async function sonioxFile(
  key: string,
  audio: Audio,
  settings: SttSettings,
  language: string | null,
  deadline: number,
  deps: SttDeps,
): Promise<{ segments: Segment[]; model: string }> {
  const fetch = deps.fetch;
  const pollMs = deps.pollMs ?? POLL_MS;
  const auth = { Authorization: `Bearer ${key}` };
  const model = settings.model?.startsWith("stt-async") ? settings.model : SONIOX_ASYNC_MODEL;

  const signal = until(deadline);
  const upload = await postAudio(audio, [], signal, (body, contentType) =>
    fetch(`${SONIOX_API}/files`, {
      method: "POST",
      headers: { ...auth, "Content-Type": contentType },
      body,
      signal,
    }));
  if (!upload.ok) throw await sonioxFailure(upload, "upload");
  const fileId: string = (await upload.json()).id;

  let transcriptionId: string | null = null;
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
      signal: until(deadline),
    });
    if (!create.ok) throw await sonioxFailure(create, "transcription");
    transcriptionId = (await create.json()).id as string;

    while (true) {
      const status = await fetch(`${SONIOX_API}/transcriptions/${transcriptionId}`, {
        headers: auth,
        signal: until(deadline),
      });
      if (!status.ok) throw await sonioxFailure(status, "status");
      const info = await status.json();
      if (info.status === "completed") break;
      if (info.status === "error") {
        console.error("Soniox transcription:", info.error_message);
        throw new HttpError(502, "Soniox could not transcribe the recording");
      }
      if (Date.now() + pollMs >= deadline) {
        throw new HttpError(504, "Soniox is still transcribing; try again in a minute");
      }
      await new Promise((r) => setTimeout(r, pollMs));
    }

    const transcript = await fetch(`${SONIOX_API}/transcriptions/${transcriptionId}/transcript`, {
      headers: auth,
      signal: until(deadline),
    });
    if (!transcript.ok) throw await sonioxFailure(transcript, "transcript");
    const data = await transcript.json();
    return { segments: tokensToSegments(data.tokens ?? []), model };
  } finally {
    // Past the deadline too, each on its own short timeout.
    const remove = (path: string) =>
      fetch(`${SONIOX_API}/${path}`, { method: "DELETE", headers: auth, signal: AbortSignal.timeout(CLEANUP_MS) })
        .then((res) => res.body?.cancel())
        .catch(() => {});
    if (transcriptionId) await remove(`transcriptions/${transcriptionId}`);
    await remove(`files/${fileId}`);
  }
}
