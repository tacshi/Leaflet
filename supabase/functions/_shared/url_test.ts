import { assertEquals } from "jsr:@std/assert@1";
import { agentURL, goLower, parseURL, queryValue, webURL } from "./url.ts";
import { isVideoURL } from "./blocks.ts";

// Links are web links, links a block may load, and videos exactly where
// the app's net/url reads them so (doc.WebURL, agentURL, ParseVideoURL):
// a port of any number of digits, a host of any name, but no backslash in
// the host, no escape of ASCII in it, and no %zz anywhere. The browser's
// URL reads most of these the other way.
Deno.test("links read as the app's net/url reads them", () => {
  const cases: [string, boolean, boolean, boolean][] = [
    // link, web link, one a block loads, a video
    ["http:/example.com/c.png", false, false, false],
    ["https://example.com:99999/a.png", true, true, false],
    ["https://256.0.0.1/a.png", true, true, false],
    ["https://example.com/%zz.png", false, false, false],
    ["https://exa\\mple.com/a.png", false, false, false],
    ["https://example.com/a\\b.png", true, true, false],
    ["https://%41.com/a.png", false, false, false],
    ["https://%C3%A9.com/a.png", true, true, false],
    ["https://[::1]:8080/a.png", true, true, false],
    ["https://[1.2.3.4]/a.png", false, false, false],
    ["https://u:p@go.dev/", true, true, false],
    ["https://go.dev/a\u007fb", false, false, false],
    ["https://:80/a.png", true, false, false],
    ["https://a:b:c/x.png", false, false, false],
    ["ftp://x.com/a.png", false, false, false],
    // A link to a video site that names no video may be to a file.
    ["https://youtu.be/a/b.mp4", true, true, true],
    // The host in lower case as Go has it: İ is i.
    ["https://v\u0130meo.com/76979871", true, true, true],
    // The path unescaped, and the query as url.Values reads it.
    ["https://youtu.be/%64Qw4w9WgXcQ", true, true, true],
    ["https://www.youtube.com/watch?v=dQw4w9WgXcQ;x", true, true, false],
  ];
  for (const [link, web, agent, video] of cases) {
    assertEquals([webURL(link), agentURL(link), isVideoURL(link)], [web, agent, video], link);
  }
});

// A link's parts, as url.Parse gives them: the path and fragment
// unescaped, the query as it is, and an opaque link's rest.
Deno.test("a link's parts are url.Parse's", () => {
  assertEquals(parseURL("HTTPS://u@Ex.com:8080/a%20b?x=%41#t%3D1"), {
    scheme: "https",
    opaque: "",
    host: "Ex.com:8080",
    path: "/a b",
    rawQuery: "x=%41",
    fragment: "t=1",
  });
  assertEquals(parseURL("mailto:a@b.c")?.opaque, "a@b.c");
  assertEquals(parseURL("a:b/c"), { scheme: "a", opaque: "b/c", host: "", path: "", rawQuery: "", fragment: "" });
  // No scheme starts with a digit, and a path of no scheme has no colon in its first part.
  assertEquals(parseURL("1a/b:c"), { scheme: "", opaque: "", host: "", path: "1a/b:c", rawQuery: "", fragment: "" });
  assertEquals(parseURL("1a:b"), null);
  assertEquals(parseURL("ab:c/d")?.scheme, "ab");
  assertEquals(parseURL(":x"), null);
  assertEquals(parseURL("http://[fe80::1%25en0]/")?.host, "[fe80::1%en0]");
  assertEquals(parseURL("http://[fe80::1%25]/"), null);
  assertEquals(queryValue("a=1&v=x+y&v=z", "v"), "x y");
  assertEquals(queryValue("v=%zz&v=ok", "v"), "ok");
});

// Lower case a character at a time, as strings.ToLower: İ is i.
Deno.test("lower case is Go's", () => {
  assertEquals(goLower("\u0130NI"), "ini");
  assertEquals(goLower("ΑΣ"), "ασ");
});
