// The fixtures in _shared/testdata hold what the server makes, for the Go
// tests to check the client against (internal/doc/parity_test.go and
// others). Run deno test with --allow-write=_shared/testdata to write them
// anew.

import { assertEquals } from "jsr:@std/assert@1";

/**
 * Checks a fixture, as the test imported it (stored), against what this
 * side makes now, or writes it, when allowed to.
 */
export async function fixture(name: string, made: unknown, stored: unknown): Promise<void> {
  const url = new URL(`../_shared/testdata/${name}`, import.meta.url);
  const json = JSON.stringify(made, null, 2) + "\n";
  if ((await Deno.permissions.query({ name: "write", path: url })).state === "granted") {
    await Deno.writeTextFile(url, json);
    return;
  }
  assertEquals(
    stored,
    JSON.parse(json),
    `testdata/${name} is out of date: run deno test --allow-write=_shared/testdata`,
  );
}
