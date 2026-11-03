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

/**
 * A corpus, as fixture has one: many cases, each on a line of its own, so
 * the file stays small enough to keep and a change to it shows case by
 * case. Of cases out of date, the first is shown.
 */
export async function corpus(name: string, made: unknown[], stored: unknown[]): Promise<void> {
  const url = new URL(`../_shared/testdata/${name}`, import.meta.url);
  const lines = made.map((c) => JSON.stringify(c));
  if ((await Deno.permissions.query({ name: "write", path: url })).state === "granted") {
    await Deno.writeTextFile(url, `[\n${lines.join(",\n")}\n]\n`);
    return;
  }
  const stale = `testdata/${name} is out of date: run deno test --allow-write=_shared/testdata`;
  assertEquals(stored.length, lines.length, stale);
  lines.forEach((line, i) => assertEquals(stored[i], JSON.parse(line), `${stale} (case ${i})`));
}
