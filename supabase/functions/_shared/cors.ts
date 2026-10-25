export const corsHeaders: Record<string, string> = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Headers": "authorization, x-client-info, apikey, content-type",
  "Access-Control-Allow-Methods": "POST, OPTIONS",
};

export class HttpError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export function preflight(req: Request): Response | null {
  if (req.method === "OPTIONS") {
    return new Response("ok", { headers: corsHeaders });
  }
  return null;
}

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { ...corsHeaders, "Content-Type": "application/json" },
  });
}

export function errorResponse(error: unknown): Response {
  if (error instanceof HttpError) {
    return json({ error: error.message }, error.status);
  }
  const message = error instanceof Error ? error.message : String(error);
  console.error(error);
  return json({ error: message }, 500);
}

export async function readJson<T>(req: Request): Promise<T> {
  if (req.method !== "POST") {
    throw new HttpError(405, "Use POST");
  }
  try {
    return (await req.json()) as T;
  } catch {
    throw new HttpError(400, "Body must be JSON");
  }
}
