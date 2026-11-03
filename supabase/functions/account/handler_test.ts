import { assertEquals } from "jsr:@std/assert@1";
import { ownAudioPath } from "./handler.ts";

// Only a recording's own path, {created_by}/{id}.{ext}, is removed with
// the pages it is on: a row naming another path would have the service
// role delete someone else's audio. One whose recorder's account went
// keeps the path it had, in any folder.
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
