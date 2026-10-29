import { assert, assertEquals } from "jsr:@std/assert@1";
import { keyAfter, lastKey } from "./sortkey.ts";

Deno.test("keys after others sort after them and never end in 0", () => {
  assertEquals(keyAfter(""), "V");
  for (const k of ["V", "a0", "z", "zz", "Zy", "x"]) {
    const next = keyAfter(k);
    assert(next > k, `${next} after ${k}`);
    assert(!next.endsWith("0"), next);
  }
});

Deno.test("the last key goes by bytes", () => {
  assertEquals(lastKey(["a", "B", "Z"]), "a");
  assertEquals(lastKey([]), "");
});
