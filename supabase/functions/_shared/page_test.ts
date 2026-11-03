import { assert, assertEquals, assertRejects } from "jsr:@std/assert@1";
import { MAX_PAGE_TEXT, readHtml, readPage, truncate } from "./page.ts";
import pages from "./testdata/pages.json" with { type: "json" };
import { fixture } from "../_testing/fixture.ts";

const PAGES: [string, string][] = [
  [
    "https://news.example.com/item?id=1#top",
    `<!doctype html><html><head><title> Hello
 world </title><base href="/sub/"></head><body>
<nav><a href="/">Home</a> | <a href="#c">Comments</a></nav>
<h1>Big <em>news</em></h1>
<p>Read <a href="story.html">the story</a>, then <a href="https://ex.com/wiki/Foo_(bar)#x">Foo</a>.</p>
<ul><li>One<ul><li>Two <a href="mailto:a@b.c">mail</a></li></ul></li><li><p>Three</p></li></ul>
<table><tr><td>1.</td><td><a href="https://a.com/x">A</a></td></tr><tr><td>2.</td><td>B&nbsp;&nbsp;c</td></tr></table>
<a href="/card"><div>Card title</div><div>Card text</div></a>
<a href="/img"><img src="x.png" alt="x"></a>
<pre>line 1
  line 2

line 4</pre>
<script>alert(1)</script><style>p{}</style><svg><text>no</text></svg><math><mi>x</mi></math>
</body></html>`,
  ],
  [
    "https://news.ycombinator.com/",
    `<html><head><title>Hacker News</title></head><body><center><table>
<tr class="athing"><td class="title"><span class="rank">1.</span></td><td><a href="vote?id=2"><div class="votearrow"></div></a></td>
<td class="title"><span class="titleline"><a href="https://www.youtube.com/watch?v=abc">An AI video</a>
<span class="sitebit comhead"> (<a href="from?site=youtube.com"><span class="sitestr">youtube.com</span></a>)</span></span></td></tr>
<tr><td colspan="2"></td><td class="subtext"><span class="score">120 points</span> by <a href="user?id=pg">pg</a>
<span class="age"><a href="item?id=2">3 hours ago</a></span> | <a href="item?id=2">45&nbsp;comments</a></td></tr>
</table></center></body></html>`,
  ],
  [
    "https://example.com/zh/文章",
    `<meta charset="utf-8"><title>标题</title><h2>第一节</h2><p>正文，<b>加粗</b>的字。<br>下一行</p><hr><dl><dt>词</dt><dd>义</dd></dl>`,
  ],
  [
    "https://example.com",
    `<p>No head at all, <a href="HTTPS://Example.COM">itself</a>, <a href="//cdn.example.org/a b">a space</a>, <a href="javascript:void(0)">js</a> and <a>none</a>.</p>
<template><p>hidden</p></template><noscript>Enable JS</noscript><select><option>pick</option></select><button>Go</button>`,
  ],
];

// A page reads as the app's agents read it (Go's ReadHTML), which reads
// the pages of testdata/pages.json alike.
Deno.test("pages read as the app reads them (testdata/pages.json)", async () => {
  const made = PAGES.map(([page, html]) => ({ page, html, read: readHtml(page, html) }));
  const first = made[0].read;
  assertEquals(first.title, "Hello world");
  assert(first.text.includes("[the story](https://news.example.com/sub/story.html)"));
  assert(first.text.includes("[Foo](https://ex.com/wiki/Foo_%28bar%29)"));
  assert(first.text.includes("<https://news.example.com/card>"));
  assert(!first.text.includes("alert") && !first.text.includes("no"));
  assert(first.links.includes("https://a.com/x"));
  await fixture("pages.json", made, pages);
});

// A long page is cut at the end of a line near the most an agent reads.
Deno.test("a long page is cut at a line's end", () => {
  const line = "字".repeat(99);
  const text = Array.from({ length: 400 }, () => line).join("\n");
  const [cut, truncated] = truncate(text);
  assert(truncated);
  assert([...cut].length <= MAX_PAGE_TEXT);
  assert(cut.endsWith(line));
  assertEquals(truncate("short"), ["short", false]);
});

/** Answers fetch with these, in turn, keeping what it was asked. */
function stubFetch(...answers: Response[]): { asked: string[]; restore: () => void } {
  const asked: string[] = [];
  const real = globalThis.fetch;
  globalThis.fetch = (input: string | URL | Request) => {
    asked.push(String(input instanceof Request ? input.url : input));
    const next = answers.shift();
    return next ? Promise.resolve(next) : Promise.reject(new TypeError("no answer"));
  };
  return { asked, restore: () => globalThis.fetch = real };
}

const publicDns = () => Promise.resolve(["93.184.216.34"]);

// A page is read over https, its redirects followed, each to a public
// host, and in the encoding it says it is in.
Deno.test("a page is read through its redirects, in its encoding", async () => {
  const gbk = new Uint8Array([0xc4, 0xe3, 0xba, 0xc3]); // 你好 in GBK
  const body = new Uint8Array([...new TextEncoder().encode("<title>t</title><p>"), ...gbk]);
  const f = stubFetch(
    new Response(null, { status: 301, headers: { location: "http://www.example.com/b" } }),
    new Response(body, { headers: { "content-type": "text/html; charset=gbk" } }),
  );
  try {
    const page = await readPage("http://example.com/a", undefined, publicDns);
    assertEquals(f.asked, ["https://example.com/a", "https://www.example.com/b"]);
    assertEquals(page.url, "https://www.example.com/b");
    assertEquals(page.text, "你好");
  } finally {
    f.restore();
  }
});

// What is not text, as a picture, is not read; nor is a private address.
Deno.test("a page that is not text, or not public, is not read", async () => {
  const f = stubFetch(new Response(new Uint8Array([1, 2, 3]), { headers: { "content-type": "image/png" } }));
  try {
    await assertRejects(() => readPage("https://example.com/x.png", undefined, publicDns), Error, "not a page of text");
  } finally {
    f.restore();
  }
  await assertRejects(() => readPage("https://10.0.0.1/"), Error, "private");
  const g = stubFetch(
    new Response("plain words https://example.com/next.", { headers: { "content-type": "text/plain" } }),
  );
  try {
    const page = await readPage("https://example.com/notes.txt", undefined, publicDns);
    assertEquals(page.text, "plain words https://example.com/next.");
    assertEquals(page.links, ["https://example.com/next"]);
  } finally {
    g.restore();
  }
});
