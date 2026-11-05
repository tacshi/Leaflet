// Speech to text (protocol §5): serves handler.ts on the project the
// function runs in.

import { handle } from "./handler.ts";
import { projectClients } from "../_shared/supabase.ts";
import { safeFetch } from "../_shared/net.ts";
import { envMs, unloadSignal, workerStarted } from "../_shared/deadline.ts";

// A transcription gives up at its deadline, before the gateway gives up on
// an answer that has not begun, at 150 s on every plan: no plan takes it
// further, and Soniox's go on past it at the next call.
const deadlineMs = envMs("STT_DEADLINE_MS", 120_000, 10_000, 120_000);
// Longer recordings transcribe on the device. 100 MiB is about 54 minutes
// of the app's 16 kHz WAV.
const maxAudioBytes = (Number(Deno.env.get("STT_MAX_AUDIO_MB")) || 100) * 1024 * 1024;

const unloading = unloadSignal();

Deno.serve((req) =>
  handle(req, { clients: projectClients, fetch, send: safeFetch, deadlineMs, workerStarted, unloading, maxAudioBytes })
);
