// Model providers (protocol §5): serves handler.ts on the project the
// function runs in.

import { handle } from "./handler.ts";
import { projectClients } from "../_shared/supabase.ts";
import { chatCompletion, listModels } from "../_shared/openai.ts";
import { checkPublicUrl } from "../_shared/net.ts";

Deno.serve((req) =>
  handle(req, { clients: projectClients, checkUrl: checkPublicUrl, listModels, chat: chatCompletion })
);
