import { assert, assertEquals, assertRejects, assertThrows } from "jsr:@std/assert@1";
import { assertPublicHost, isPrivateAddress, publicHttpsUrl, safeFetch, UnsafeUrlError } from "./net.ts";

Deno.test("private and reserved addresses", () => {
  for (
    const ip of [
      "0.0.0.0",
      "10.1.2.3",
      "100.64.0.1",
      "100.127.255.255",
      "127.0.0.1",
      "169.254.169.254",
      "172.16.0.1",
      "172.31.255.255",
      "192.168.1.1",
      "198.18.0.1",
      "224.0.0.1",
      "255.255.255.255",
      "::",
      "::1",
      "[::1]",
      "::ffff:127.0.0.1",
      "::ffff:7f00:1",
      "::ffff:a9fe:a9fe",
      "64:ff9b::a9fe:a9fe",
      "64:ff9b:1::1",
      "fc00::1",
      "fd12:3456::1",
      "fe80::1%en0",
      "fec0::1",
      "ff02::1",
      "2002:7f00:1::",
      "2001:db8::1",
      "not an ip",
    ]
  ) {
    assert(isPrivateAddress(ip), ip);
  }
});

Deno.test("public addresses", () => {
  for (
    const ip of [
      "1.1.1.1",
      "8.8.8.8",
      "100.63.255.255",
      "100.128.0.1",
      "172.15.0.1",
      "172.32.0.1",
      "2606:4700:4700::1111",
      "2a00:1450:4001:80b::200e",
      "::ffff:8.8.8.8",
      "64:ff9b::808:808",
    ]
  ) {
    assert(!isPrivateAddress(ip), ip);
  }
});

Deno.test("publicHttpsUrl accepts https to public hosts", () => {
  assertEquals(publicHttpsUrl("https://api.example.com/v1").hostname, "api.example.com");
  assertEquals(publicHttpsUrl("https://8.8.8.8/v1").hostname, "8.8.8.8");
});

Deno.test("publicHttpsUrl rejects other schemes, local names and private literals", () => {
  for (
    const raw of [
      "http://api.example.com/v1",
      "ftp://api.example.com",
      "file:///etc/passwd",
      "https://localhost:8080/v1",
      "https://foo.localhost/v1",
      "https://LOCALHOST./v1",
      "https://metadata/v1",
      "https://metadata.google.internal/v1",
      "https://printer.local/v1",
      "https://127.0.0.1/v1",
      "https://2130706433/v1",
      "https://0x7f.1/v1",
      "https://[::1]/v1",
      "https://[::ffff:127.0.0.1]/v1",
      "https://[fd00::1]/v1",
      "https://169.254.169.254/latest",
      "https://user:pass@api.example.com/v1",
      "not a url",
    ]
  ) {
    assertThrows(() => publicHttpsUrl(raw), UnsafeUrlError, undefined, raw);
  }
});

Deno.test("assertPublicHost checks every resolved address", async () => {
  const resolver = (records: Record<string, string[]>) => (host: string, type: "A" | "AAAA") =>
    records[`${type} ${host}`] ? Promise.resolve(records[`${type} ${host}`]) : Promise.reject(new Error("NXDOMAIN"));

  await assertPublicHost(new URL("https://good.example/v1"), resolver({ "A good.example": ["93.184.216.34"] }));
  await assertRejects(
    () =>
      assertPublicHost(
        new URL("https://rebind.example/v1"),
        resolver({ "A rebind.example": ["93.184.216.34"], "AAAA rebind.example": ["::1"] }),
      ),
    UnsafeUrlError,
  );
  await assertRejects(
    () => assertPublicHost(new URL("https://internal.example/v1"), resolver({ "A internal.example": ["10.0.0.5"] })),
    UnsafeUrlError,
  );
  await assertRejects(() => assertPublicHost(new URL("https://missing.example/v1"), resolver({})), UnsafeUrlError);
  // Preset hosts and literal public addresses need no lookup.
  await assertPublicHost(new URL("https://api.openai.com/v1"), resolver({}));
  await assertPublicHost(new URL("https://8.8.8.8/v1"), resolver({}));
});

Deno.test("safeFetch refuses private targets before fetching", async () => {
  await assertRejects(() => safeFetch("http://127.0.0.1:54321/rest/v1/"), UnsafeUrlError);
  await assertRejects(
    () => safeFetch("https://evil.example/v1", {}, () => Promise.resolve(["169.254.169.254"])),
    UnsafeUrlError,
  );
});
