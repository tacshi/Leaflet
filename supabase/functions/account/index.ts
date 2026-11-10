import { handle } from "./handler.ts";
import { projectClients } from "../_shared/supabase.ts";

Deno.serve((req) => handle(req, projectClients));
