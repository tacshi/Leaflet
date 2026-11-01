import { assert } from "jsr:@std/assert@1";
import { type Surface, systemPrompt } from "./prompt.ts";

const prompt = (surface: Surface) =>
  systemPrompt({
    agentName: "Claude",
    surface,
    canEdit: true,
    crossPage: false,
    toolsEnabled: true,
    pageTitle: "PRD",
    userName: "Sam",
    today: "2026-11-01",
  });

Deno.test("a question in the chat gets an answer, not an edit", () => {
  const p = prompt("sidebar");
  assert(p.includes("summarize it"));
  assert(p.includes("answer in your reply and leave the page as it is"));
  assert(p.includes("An answer to a question is not a change"));
});

Deno.test("a question about a selection or in a thread leaves the page as it is", () => {
  assert(prompt("selection").includes("answer in your reply and leave the page as it is"));
  assert(prompt("comment").includes("edit the page only if they ask for a change"));
});
