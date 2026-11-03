// Account (protocol §5): serves handler.ts on the project the function runs
// in.

import { handle } from "./handler.ts";
import { projectClients } from "../_shared/supabase.ts";

Deno.serve((req) => handle(req, projectClients));
