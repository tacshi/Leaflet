// Speech to text (protocol §5): serves handler.ts on the project the
// function runs in.

import { handle } from "./handler.ts";
import { projectClients } from "../_shared/supabase.ts";
import { safeFetch } from "../_shared/net.ts";
import { envMs } from "../_shared/deadline.ts";

// A transcription gives up at its deadline, before the platform stops the
// function. On a paid plan, raise STT_DEADLINE_MS (at most 380 s).
const deadlineMs = envMs("STT_DEADLINE_MS", 120_000, 10_000, 380_000);
// Longer recordings transcribe on the device. 100 MiB is about 54 minutes
// of the app's 16 kHz WAV.
const maxAudioBytes = (Number(Deno.env.get("STT_MAX_AUDIO_MB")) || 100) * 1024 * 1024;

Deno.serve((req) => handle(req, { clients: projectClients, fetch, send: safeFetch, deadlineMs, maxAudioBytes }));
