import { assertEquals } from "jsr:@std/assert@1";
import { handle, ownAudioPath } from "./handler.ts";
import { Project, Whole } from "../_testing/project.ts";

Deno.test("a recording's audio is removed only at its own path", () => {
  assertEquals(ownAudioPath("bob/rec.wav", "bob", "rec"), true);
  assertEquals(ownAudioPath("bob/rec", "bob", "rec"), true);
  assertEquals(ownAudioPath("bob/rec.m4a.wav", "bob", "rec"), true);
  assertEquals(ownAudioPath("ann/rec.wav", "bob", "rec"), false);
  assertEquals(ownAudioPath("bob/other.wav", "bob", "rec"), false);
  assertEquals(ownAudioPath("bob/x/rec.wav", "bob", "rec"), false);
  assertEquals(ownAudioPath("bob/", "bob", "rec"), false);
  assertEquals(ownAudioPath("/rec.wav", null, "rec"), false);
  assertEquals(ownAudioPath("gone/rec.wav", null, "rec"), true);
});

const ANN = "a0000000-0000-4000-8000-000000000001";

Deno.test("an account's kept recordings all stay, past a thousand", async () => {
  const p = new Project({ maxRows: 1000 });
  p.user(ANN, "ann-token");
  p.tables.spaces = [{ id: "ann-space", owner_id: ANN }];
  const kept = Array.from({ length: 1500 }, (_, i) => `${ANN}/kept-${String(i).padStart(4, "0")}.wav`);
  p.objects.recordings = new Map([...kept, `${ANN}/own.wav`].map((path) => [path, new Uint8Array(1)]));
  p.rpcs.release_kept_recordings = (args, caller) => {
    assertEquals([args.p_user_id, caller], [ANN, "service"]);
    return new Whole(kept);
  };
  const res = await handle(p.request("ann-token", { action: "delete" }), p.clients());
  assertEquals([res.status, await res.json()], [200, { ok: true }]);
  assertEquals([...p.objects.recordings.keys()].sort(), kept);
  assertEquals(p.deletedUsers, [ANN]);
});

Deno.test("an account whose audio to keep is not known stays", async () => {
  const p = new Project();
  p.user(ANN, "ann-token");
  p.tables.spaces = [];
  p.objects.recordings = new Map([[`${ANN}/kept.wav`, new Uint8Array(1)]]);
  p.rpcs.release_kept_recordings = () => ({ path: `${ANN}/kept.wav` });
  const res = await handle(p.request("ann-token", { action: "delete" }), p.clients());
  assertEquals(res.status, 500);
  await res.body?.cancel();
  assertEquals([...p.objects.recordings.keys()], [`${ANN}/kept.wav`]);
  assertEquals(p.deletedUsers, []);
});

Deno.test("an account deletion that fails part way leaves the account, and runs whole again", async () => {
  const p = new Project();
  p.user(ANN, "ann-token");
  p.tables.spaces = [{ id: "ann-space", owner_id: ANN }];
  const kept = [`${ANN}/kept.wav`];
  p.objects.recordings = new Map([...kept, `${ANN}/own.wav`].map((path) => [path, new Uint8Array(1)]));
  p.objects.files = new Map([["ann-space/a.txt", new Uint8Array(1)]]);
  p.objects.images = new Map([["ann-space/b.png", new Uint8Array(1)]]);
  p.rpcs.release_kept_recordings = () => new Whole(kept);
  const fetch = p.fetch;
  let failing = true;
  p.fetch = (input, init) => {
    const req = new Request(input, init);
    if (failing && req.method === "DELETE" && new URL(req.url).pathname === "/storage/v1/object/files") {
      return Promise.resolve(
        Response.json({ statusCode: "500", error: "internal", message: "disk on fire" }, { status: 500 }),
      );
    }
    return fetch(req);
  };

  let res = await handle(p.request("ann-token", { action: "delete" }), p.clients());
  assertEquals([res.status, await res.json()], [500, { error: "Something went wrong. Try again." }]);
  assertEquals(p.deletedUsers, []);
  assertEquals([...p.objects.recordings.keys()], kept);
  assertEquals([...p.objects.files.keys()], ["ann-space/a.txt"]);

  failing = false;
  res = await handle(p.request("ann-token", { action: "delete" }), p.clients());
  assertEquals([res.status, await res.json()], [200, { ok: true }]);
  assertEquals(p.deletedUsers, [ANN]);
  assertEquals([...p.objects.recordings.keys()], kept);
  assertEquals([p.objects.files.size, p.objects.images.size], [0, 0]);
});
