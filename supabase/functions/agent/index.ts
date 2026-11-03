// Agent worker (protocol §4): serves handler.ts on the project the function
// runs in.

import { handle } from "./handler.ts";
import { projectClients } from "../_shared/supabase.ts";
import { chatCompletion } from "../_shared/openai.ts";
import { envMs } from "../_shared/deadline.ts";

declare const EdgeRuntime: { waitUntil(promise: Promise<unknown>): void } | undefined;

// An Edge Function is stopped at its wall-clock limit (150 s on the free
// plan, 400 s on paid plans), so a run stops calling the model at its
// deadline, keeping time to save what it did. On a paid plan, raise
// AGENT_DEADLINE_MS (at most 380 s).
const deadlineMs = envMs("AGENT_DEADLINE_MS", 120_000, 10_000, 380_000);
// One model call; a call never runs past the deadline either.
const callTimeoutMs = envMs("AGENT_CALL_TIMEOUT_MS", 60_000, 5_000, 380_000);

Deno.serve((req) =>
  handle(req, {
    clients: projectClients,
    chat: chatCompletion,
    deadlineMs,
    callTimeoutMs,
    // Keep running (and saving) even if the client disconnects.
    waitUntil: typeof EdgeRuntime !== "undefined" ? (work) => EdgeRuntime.waitUntil(work) : undefined,
  })
);
