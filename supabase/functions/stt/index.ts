// Speech to text (protocol §5): short-lived Soniox keys for live streaming,
// saving the user's Soniox key, and server-side transcription of uploaded
// recordings through Soniox or any OpenAI-compatible /audio/transcriptions.

import type { SupabaseClient, User } from "jsr:@supabase/supabase-js@2";
import { errorResponse, HttpError, json, preflight, readJson } from "../_shared/cors.ts";
import { loadProvider, providerSecret, requireUser, serviceClient, userClient } from "../_shared/supabase.ts";
import { normalizeBaseUrl } from "../_shared/openai.ts";
import { safeFetch } from "../_shared/net.ts";
import { type Segment, tokensToSegments } from "../_shared/segments.ts";

const SONIOX_API = "https://api.soniox.com/v1";
const SONIOX_ASYNC_MODEL = "stt-async-v3";
const POLL_MS = 2_000;
const POLL_LIMIT_MS = 110_000;
// The audio is read into memory to send on; longer recordings transcribe on
// the device. 100 MiB is about 54 minutes of the app's 16 kHz WAV.
const MAX_AUDIO_BYTES = (Number(Deno.env.get("STT_MAX_AUDIO_MB")) || 100) * 1024 * 1024;

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

Deno.serve(async (req) => {
  const pre = preflight(req);
  if (pre) return pre;
  try {
    const body = await readJson<Body>(req);
    const uc = userClient(req);
    const user = await requireUser(req, uc);
    const sc = serviceClient();
    switch (body.action) {
      case "soniox_key":
        return json(await sonioxTemporaryKey(sc, user));
      case "save_key":
        return json(await saveKey(uc, body));
      case "transcribe":
        return json(await transcribe(uc, sc, user, body));
      default:
        throw new HttpError(400, "Unknown action");
    }
  } catch (e) {
    return errorResponse(e);
  }
});

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

async function sonioxFailure(res: Response, what: string): Promise<HttpError> {
  const text = await res.text().catch(() => "");
  return new HttpError(res.status === 401 ? 400 : 502, `Soniox ${what} failed: ${res.status} ${text.slice(0, 300)}`);
}

async function sonioxTemporaryKey(sc: SupabaseClient, user: User) {
  const key = await sonioxKey(sc, user);
  const res = await fetch(`${SONIOX_API}/auth/temporary-api-key`, {
    method: "POST",
    headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      usage_type: "transcribe_websocket",
      expires_in_seconds: 3600,
      client_reference_id: user.id,
    }),
  });
  if (!res.ok) throw await sonioxFailure(res, "temporary key");
  const data = await res.json();
  return { api_key: data.api_key, expires_at: data.expires_at };
}

async function saveKey(uc: SupabaseClient, body: Body) {
  if (body.provider !== "soniox") throw new HttpError(400, "Only Soniox keys are saved here");
  const key = (body.api_key ?? "").trim();
  if (key) {
    const check = await fetch(`${SONIOX_API}/models`, { headers: { Authorization: `Bearer ${key}` } });
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

async function transcribe(uc: SupabaseClient, sc: SupabaseClient, user: User, body: Body) {
  if (!body.recording_id) throw new HttpError(400, "recording_id is required");
  const { data: rec, error } = await uc
    .from("recordings")
    .select("id, audio_path, duration_ms")
    .eq("id", body.recording_id)
    .maybeSingle();
  if (error) throw new Error(error.message);
  if (!rec) throw new HttpError(404, "Recording not found");
  if (!rec.audio_path) throw new HttpError(400, "This recording's audio has not been uploaded");

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

  const slash = rec.audio_path.lastIndexOf("/");
  const folder = rec.audio_path.slice(0, Math.max(slash, 0));
  const fileName = rec.audio_path.slice(slash + 1) || "audio.m4a";
  const { data: listed, error: listError } = await uc.storage.from("recordings").list(folder, {
    search: fileName,
    limit: 100,
  });
  if (listError) throw new Error(`Could not read the audio: ${listError.message}`);
  const object = listed?.find((o) => o.name === fileName);
  if (!object) throw new HttpError(404, "This recording's audio is not on the server");
  const size = Number(object.metadata?.size ?? 0);
  if (size > MAX_AUDIO_BYTES) {
    throw new HttpError(
      413,
      `This recording is too long to transcribe on the server (over ${
        Math.round(MAX_AUDIO_BYTES / 1024 / 1024)
      } MB). Choose an on-device model to transcribe it.`,
    );
  }

  await uc.from("recordings").update({ status: "transcribing", error: null }).eq("id", rec.id);
  try {
    const { data: audio, error: downloadError } = await uc.storage.from("recordings").download(rec.audio_path);
    if (downloadError || !audio) throw new Error(`Could not read the audio: ${downloadError?.message ?? "missing"}`);
    if (audio.size > MAX_AUDIO_BYTES) throw new Error("This recording is too long to transcribe on the server");

    const { segments, model } = useSoniox
      ? await sonioxFile(await sonioxKey(sc, user), audio, fileName, settings, language)
      : await openAiCompatible(uc, sc, audio, fileName, settings, language, rec.duration_ms ?? 0);

    const { error: saveError } = await uc.from("recordings").update({
      transcript: segments,
      status: "ready",
      stt_provider: useSoniox ? "soniox" : "openai-compatible",
      stt_model: model,
      language,
      error: null,
    }).eq("id", rec.id);
    if (saveError) throw new Error(saveError.message);
    return { segments };
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e);
    await uc.from("recordings").update({ status: "failed", error: message }).eq("id", rec.id);
    throw e;
  }
}

async function openAiCompatible(
  uc: SupabaseClient,
  sc: SupabaseClient,
  audio: Blob,
  fileName: string,
  settings: SttSettings,
  language: string | null,
  durationMs: number,
): Promise<{ segments: Segment[]; model: string }> {
  let baseUrl: string;
  let apiKey: string | null = null;
  if (settings.provider_id) {
    const provider = await loadProvider(uc, settings.provider_id);
    baseUrl = normalizeBaseUrl(settings.base_url || provider.base_url, provider.preset);
    apiKey = await providerSecret(sc, provider.id);
  } else {
    baseUrl = normalizeBaseUrl(settings.base_url!);
  }
  const model = settings.model || "whisper-1";

  const send = async (format: "verbose_json" | "json") => {
    const form = new FormData();
    form.append("file", audio, fileName);
    form.append("model", model);
    form.append("response_format", format);
    if (format === "verbose_json") form.append("timestamp_granularities[]", "segment");
    if (language) form.append("language", language);
    // The base URL is the user's: safeFetch keeps it off private addresses.
    return await safeFetch(`${baseUrl}/audio/transcriptions`, {
      method: "POST",
      headers: apiKey ? { Authorization: `Bearer ${apiKey}` } : {},
      body: form,
    });
  };

  // Some models (gpt-4o-transcribe) only return plain json: ask them for it
  // rather than sending the audio twice. Other models get one retry, only
  // when the endpoint turns verbose_json down.
  const jsonOnly = /^gpt-4o.*transcribe/i.test(model);
  let res = await send(jsonOnly ? "json" : "verbose_json");
  if (!jsonOnly && res.status === 400) {
    const text = await res.text().catch(() => "");
    if (/response_format|verbose_json|timestamp_granularities/i.test(text)) {
      res = await send("json");
    } else {
      res = new Response(null, { status: 400, statusText: res.statusText });
    }
  }
  if (!res.ok) {
    await res.body?.cancel().catch(() => {});
    throw new Error(`Transcription failed: the endpoint answered ${res.status} ${res.statusText}`.trim());
  }
  const data = await res.json();
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
  audio: Blob,
  fileName: string,
  settings: SttSettings,
  language: string | null,
): Promise<{ segments: Segment[]; model: string }> {
  const auth = { Authorization: `Bearer ${key}` };
  const model = settings.model?.startsWith("stt-async") ? settings.model : SONIOX_ASYNC_MODEL;

  const form = new FormData();
  form.append("file", audio, fileName);
  const upload = await fetch(`${SONIOX_API}/files`, { method: "POST", headers: auth, body: form });
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
    });
    if (!create.ok) throw await sonioxFailure(create, "transcription");
    transcriptionId = (await create.json()).id as string;

    const started = Date.now();
    while (true) {
      const status = await fetch(`${SONIOX_API}/transcriptions/${transcriptionId}`, { headers: auth });
      if (!status.ok) throw await sonioxFailure(status, "status");
      const info = await status.json();
      if (info.status === "completed") break;
      if (info.status === "error") throw new Error(`Soniox: ${info.error_message ?? "transcription failed"}`);
      if (Date.now() - started > POLL_LIMIT_MS) throw new Error("Soniox is still transcribing; try again in a minute");
      await new Promise((r) => setTimeout(r, POLL_MS));
    }

    const transcript = await fetch(`${SONIOX_API}/transcriptions/${transcriptionId}/transcript`, { headers: auth });
    if (!transcript.ok) throw await sonioxFailure(transcript, "transcript");
    const data = await transcript.json();
    return { segments: tokensToSegments(data.tokens ?? []), model };
  } finally {
    if (transcriptionId) {
      await fetch(`${SONIOX_API}/transcriptions/${transcriptionId}`, { method: "DELETE", headers: auth }).catch(
        () => {},
      );
    }
    await fetch(`${SONIOX_API}/files/${fileId}`, { method: "DELETE", headers: auth }).catch(() => {});
  }
}
