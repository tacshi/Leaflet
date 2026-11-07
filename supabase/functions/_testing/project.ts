// A Supabase project in memory, for the functions' tests: the REST API
// over tables of rows, its RPCs, Auth's user lookup and Storage's objects,
// as far as the functions use them, reached by real supabase-js clients
// through a fetch of its own. It applies no row-level security: a test
// says what a caller may write with a rule, where that matters.

import { createClient, type SupabaseClient } from "jsr:@supabase/supabase-js@2.118.0";
import type { Clients } from "../_shared/supabase.ts";

export type Row = Record<string, unknown>;

/** Who calls: a user's ID, or "service" for the service role. */
export type Caller = string;

export interface Rules {
  /** Whether caller may update or delete row; every row when absent. */
  writes?(table: string, row: Row, caller: Caller): boolean;
  /** Changes a row as the table's triggers would, before an update is stored. */
  onUpdate?(table: string, row: Row, old: Row): void;
  /** Why the database refuses this update, as a check or a trigger raises; none takes it. */
  refuses?(table: string, patch: Row): string | undefined;
  /** The most rows a read answers with, as the API's max_rows has it (supabase/config.toml); no most when absent. */
  maxRows?: number;
}

/**
 * What a function returning one value of an array type answers, as an
 * RPC's stand-in gives it: the array, whole, in one row, which no filter
 * or max_rows cuts, as one returning rows (setof) is cut.
 */
export class Whole {
  constructor(readonly value: unknown[]) {}
}

const URL_BASE = "http://project.test";
const SERVICE_KEY = "service-key";

export class Project {
  readonly tables: Record<string, Row[]> = {};
  readonly rpcs: Record<string, (args: Record<string, unknown>, caller: Caller) => unknown> = {};
  /** Storage objects by bucket, then path. */
  readonly objects: Record<string, Map<string, Uint8Array>> = {};
  /** Users by their access token. */
  readonly users = new Map<string, { id: string; email: string }>();
  /** Every request, as METHOD path?query. */
  readonly requests: string[] = [];
  /** The users Auth's admin API deleted, by ID. */
  readonly deletedUsers: string[] = [];

  constructor(public rules: Rules = {}) {}

  rows(table: string): Row[] {
    return (this.tables[table] ??= []);
  }

  /** A user signed in with token. */
  user(id: string, token: string, email = `${id}@example.com`): void {
    this.users.set(token, { id, email });
  }

  /** The clients a function takes: as the request's caller, and as the service role. */
  clients(): Clients {
    const options = (headers: Record<string, string> = {}) => ({
      global: { fetch: this.fetch, headers },
      auth: { persistSession: false, autoRefreshToken: false },
    });
    return {
      user: (req: Request) => {
        const auth = req.headers.get("Authorization") ?? "";
        return createClient(URL_BASE, "anon-key", options({ Authorization: auth }));
      },
      service: (): SupabaseClient => createClient(URL_BASE, SERVICE_KEY, options()),
    };
  }

  /** A request to a function, by the user whose token it is. */
  request(token: string, body: unknown): Request {
    return new Request("http://function.test", {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  }

  fetch = async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const req = new Request(input, init);
    const url = new URL(req.url);
    this.requests.push(`${req.method} ${url.pathname}${url.search}`);
    const token = (req.headers.get("Authorization") ?? "").replace(/^Bearer\s+/i, "");
    const caller = token === SERVICE_KEY ? "service" : this.users.get(token)?.id ?? "";
    // A turn of the event loop, as a request over the network takes: what
    // runs at once interleaves.
    await new Promise((r) => setTimeout(r, 0));
    try {
      if (url.pathname === "/auth/v1/user") {
        const user = this.users.get(token);
        return user ? reply({ id: user.id, email: user.email, aud: "authenticated" }) : reply({ msg: "bad jwt" }, 401);
      }
      const admin = url.pathname.match(/^\/auth\/v1\/admin\/users\/([^/]+)$/);
      if (admin && req.method === "DELETE" && caller === "service") {
        this.deletedUsers.push(admin[1]);
        return reply({ id: admin[1], aud: "authenticated" });
      }
      const rpc = url.pathname.match(/^\/rest\/v1\/rpc\/(\w+)$/);
      if (rpc) {
        const fn = this.rpcs[rpc[1]];
        if (!fn) return reply({ code: "PGRST202", message: `no function ${rpc[1]}` }, 404);
        const result = await fn(await req.json().catch(() => ({})), caller);
        if (result instanceof Whole) return reply(result.value);
        // A function's rows are read as a table's are: its filters, order
        // and limit, and no more than max_rows.
        if (!Array.isArray(result)) return reply(result ?? null);
        let rows = (result as Row[]).filter(filters(url.searchParams, (name) => this.rows(name)));
        const order = url.searchParams.get("order");
        if (order) rows = sorted(rows, order);
        const limit = url.searchParams.get("limit");
        return reply(rows.slice(0, limit === null ? undefined : Number(limit)).slice(0, this.rules.maxRows));
      }
      const table = url.pathname.match(/^\/rest\/v1\/(\w+)$/);
      if (table) return await this.rest(table[1], req, url, caller);
      if (url.pathname.startsWith("/storage/v1/object/")) return await this.storage(req, url);
      return reply({ message: `no route ${url.pathname}` }, 404);
    } catch (e) {
      return reply({ message: e instanceof Error ? e.message : String(e) }, 500);
    }
  };

  private async rest(name: string, req: Request, url: URL, caller: Caller): Promise<Response> {
    const table = this.rows(name);
    const match = filters(url.searchParams, (name) => this.rows(name));
    const prefer = req.headers.get("Prefer") ?? "";
    const single = (req.headers.get("Accept") ?? "").includes("vnd.pgrst.object");
    const out = (rows: Row[], status = 200) => {
      const shown = rows.map((r) => project(r, url.searchParams.get("select")));
      if (single) {
        return shown.length === 1
          ? reply(shown[0], status)
          : reply({ code: "PGRST116", message: "JSON object requested, multiple (or no) rows returned" }, 406);
      }
      return req.method === "GET" || prefer.includes("return=representation")
        ? reply(shown, status)
        : new Response(null, { status: 204 });
    };
    switch (req.method) {
      case "GET": {
        let rows = table.filter(match);
        const order = url.searchParams.get("order");
        if (order) rows = sorted(rows, order);
        const offset = Number(url.searchParams.get("offset") ?? 0);
        const limit = url.searchParams.get("limit");
        rows = rows.slice(offset, limit === null ? undefined : offset + Number(limit));
        return out(rows.slice(0, this.rules.maxRows));
      }
      case "POST": {
        const body = await req.json();
        const made: Row[] = [];
        const conflict = url.searchParams.get("on_conflict")?.split(",");
        for (const value of Array.isArray(body) ? body : [body]) {
          if (
            conflict && prefer.includes("resolution=ignore-duplicates") &&
            table.some((r) => conflict.every((k) => r[k] === value[k]))
          ) continue;
          const row: Row = { id: crypto.randomUUID(), created_at: now(), updated_at: now(), ...value };
          const key = name === "page_content" ? "page_id" : "id";
          if (table.some((r) => r[key] === row[key])) {
            return reply({ code: "23505", message: `duplicate key value violates unique constraint on ${name}` }, 409);
          }
          table.push(row);
          made.push(row);
        }
        return out(made, 201);
      }
      case "PATCH": {
        const patch = await req.json();
        const refused = this.rules.refuses?.(name, patch);
        if (refused) return reply({ code: "23514", message: refused }, 400);
        const changed: Row[] = [];
        for (const row of table.filter(match)) {
          if (this.rules.writes && !this.rules.writes(name, row, caller)) continue;
          const old = { ...row };
          Object.assign(row, patch, { updated_at: now() });
          this.rules.onUpdate?.(name, row, old);
          changed.push(row);
        }
        return out(changed);
      }
      case "DELETE": {
        const gone = table.filter((r) => match(r) && (!this.rules.writes || this.rules.writes(name, r, caller)));
        this.tables[name] = table.filter((r) => !gone.includes(r));
        return out(gone);
      }
    }
    return reply({ message: "method" }, 405);
  }

  private async storage(req: Request, url: URL): Promise<Response> {
    const rest = decodeURIComponent(url.pathname.slice("/storage/v1/object/".length));
    const list = rest.match(/^list\/([^/]+)$/);
    if (list) {
      const { prefix = "", search = "", limit = 100, offset = 0 } = await req.json();
      const bucket = this.objects[list[1]] ?? new Map();
      const items = [...bucket.entries()]
        .filter(([path]) => path.startsWith(prefix ? `${prefix}/` : ""))
        .map(([path, data]) => ({ name: path.slice(prefix ? prefix.length + 1 : 0), data }))
        .filter((o) => !o.name.includes("/") && o.name.includes(search))
        .map((o) => ({ id: crypto.randomUUID(), name: o.name, metadata: { size: o.data.length } }))
        .sort((a, b) => a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
      return reply(items.slice(offset, offset + limit));
    }
    const [bucketId, ...parts] = rest.split("/");
    const bucket = (this.objects[bucketId] ??= new Map());
    if (req.method === "DELETE") {
      const { prefixes = [] } = await req.json();
      for (const p of prefixes) bucket.delete(p);
      return reply(prefixes.map((name: string) => ({ name })));
    }
    const data = bucket.get(parts.join("/"));
    return data ? new Response(data.slice()) : reply({ message: "Object not found" }, 400);
  }
}

function now(): string {
  return new Date().toISOString();
}

function reply(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

/**
 * A row with only the columns select names: "*" or a list, embeds left out.
 * A column may be a JSON path, alias:col->key->>key, as PostgREST reads it:
 * ->> gives text, and a key that is not there null.
 */
function project(row: Row, select: string | null): Row {
  if (!select || select === "*") return { ...row };
  const out: Row = {};
  for (const col of select.replace(/\w+!?\w*\([^)]*\)/g, "").split(",")) {
    const path = col.match(/^(?:(\w+):)?(\w+)((?:->>?\w+)+)$/);
    if (path) {
      let v: unknown = row[path[2]];
      const steps = [...path[3].matchAll(/(->>?)(\w+)/g)];
      for (const [, arrow, key] of steps) {
        v = v && typeof v === "object" ? (v as Row)[key] ?? null : null;
        if (arrow === "->>" && v !== null && typeof v !== "string") v = JSON.stringify(v);
      }
      out[path[1] ?? steps[steps.length - 1][2]] = v;
    } else if (col) out[col] = row[col];
  }
  return out;
}

function sorted(rows: Row[], order: string): Row[] {
  const keys = order.split(",").map((k) => k.split("."));
  return [...rows].sort((a, b) => {
    for (const [col, dir] of keys) {
      const c = compare(a[col], String(b[col]));
      if (c) return dir === "desc" ? -c : c;
    }
    return 0;
  });
}

function compare(value: unknown, against: string): number {
  const v = value === null || value === undefined ? "" : String(value);
  const x = Number(v), y = Number(against);
  if (v !== "" && against !== "" && Number.isFinite(x) && Number.isFinite(y)) return x - y;
  return v < against ? -1 : v > against ? 1 : 0;
}

/** One PostgREST condition, col=op.value, or a not. of one. */
function condition(col: string, expr: string): (row: Row) => boolean {
  if (expr.startsWith("not.")) {
    const inner = condition(col, expr.slice(4));
    return (row) => !inner(row);
  }
  const dot = expr.indexOf(".");
  const op = expr.slice(0, dot), value = expr.slice(dot + 1);
  return (row) => {
    const v = row[col];
    switch (op) {
      case "eq":
        return v !== null && v !== undefined && compare(v, value) === 0;
      case "neq":
        return v !== null && v !== undefined && compare(v, value) !== 0;
      case "lt":
        return v !== null && v !== undefined && compare(v, value) < 0;
      case "gt":
        return v !== null && v !== undefined && compare(v, value) > 0;
      case "is":
        return value === "null" ? v === null || v === undefined : String(v) === value;
      case "in":
        return value.replace(/^\(|\)$/g, "").split(",").map((s) => s.replace(/^"|"$/g, "")).includes(String(v));
    }
    throw new Error(`filter ${op}`);
  };
}

/**
 * The rows a query's filters keep: its conditions, an or=(…) of them, and
 * those of a table embedded with !inner, as pages.space_id=eq.x with
 * pages!inner(…) in select: the rows whose row there, by its key (page_id
 * for pages), matches.
 */
function filters(params: URLSearchParams, tables: (name: string) => Row[]): (row: Row) => boolean {
  const all: ((row: Row) => boolean)[] = [];
  const inner = new Set([...(params.get("select") ?? "").matchAll(/(\w+)!inner\(/g)].map((m) => m[1]));
  for (const [key, expr] of params) {
    if (["select", "order", "limit", "offset", "columns", "on_conflict"].includes(key)) continue;
    if (key === "or") {
      const any = expr.replace(/^\(|\)$/g, "").split(",").map((part) => {
        const dot = part.indexOf(".");
        return condition(part.slice(0, dot), part.slice(dot + 1));
      });
      all.push((row) => any.some((c) => c(row)));
    } else if (!key.includes(".")) {
      all.push(condition(key, expr));
    } else if (inner.has(key.split(".")[0])) {
      const [embed, col] = key.split(".");
      const match = condition(col, expr);
      const fk = `${embed.replace(/s$/, "")}_id`;
      all.push((row) => tables(embed).some((r) => r.id === row[fk] && match(r)));
    }
  }
  return (row) => all.every((c) => c(row));
}
