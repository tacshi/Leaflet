import { assert, assertEquals } from "jsr:@std/assert@1";
import { keyAfter, lastKey, newKeyAfter } from "./sortkey.ts";
import { fixture } from "../_testing/fixture.ts";
import sortKeys from "./testdata/sort_keys.json" with { type: "json" };

Deno.test("keys after others sort after them and never end in 0", () => {
  assertEquals(keyAfter(""), "a1");
  // Characters that are no digits, as other clients may write, too.
  for (const k of ["V", "a0", "az", "z", "zz", "Zy", "x", "~", "z~", "é", "-", "a-b", "z" + "z".repeat(26)]) {
    const next = keyAfter(k);
    assert(next > k, `${next} after ${k}`);
    assert(!next.endsWith("0"), next);
  }
});

// Keys put last count on, so 5000 of them stay short, from an empty list
// and from keys an earlier version made, as the app's do.
Deno.test("keys put last stay short", () => {
  for (const start of ["", "V", "lXY", "zzV", "a0", "~"]) {
    let k = start;
    for (let i = 0; i < 5000; i++) {
      const next = newKeyAfter(k);
      assert(next > k && !next.endsWith("0"), `from ${start}, key ${i}: ${next} after ${k}`);
      k = next;
    }
    assert(k.length <= start.length + 6, `from ${start}, 5000 keys later: ${k}`);
  }
});

// The keys the server puts last are the app's: internal/store checks
// KeyBetween(after, "") against testdata/sort_keys.json.
Deno.test("keys put last are the app's (testdata/sort_keys.json)", async () => {
  const afters = [
    "",
    "V",
    "a0",
    "a1",
    "az",
    "b0z",
    "bzz",
    "lXY",
    "zzV",
    "~",
    "é",
    "a-b",
    "Zy",
    "z~",
    "-a5",
    "z".repeat(27),
  ];
  for (let k = "", i = 0; i < 70; i++) afters.push(k = keyAfter(k));
  await fixture("sort_keys.json", afters.map((after) => ({ after, key: keyAfter(after) })), sortKeys);
});

Deno.test("the last key goes by bytes", () => {
  assertEquals(lastKey(["a", "B", "Z"]), "a");
  assertEquals(lastKey([]), "");
});

Deno.test("a page's own key goes after the key, apart from another's", () => {
  const keys = new Set<string>();
  for (let i = 0; i < 50; i++) {
    const k = newKeyAfter("V");
    assert(k > "V" && k.startsWith(keyAfter("V")) && !k.endsWith("0"), k);
    keys.add(k);
  }
  assert(keys.size > 40, `${keys.size} keys of 50`);
});
