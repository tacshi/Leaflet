import { assert, assertEquals } from "jsr:@std/assert@1";
import { emptyContent, type PageContent } from "./blocks.ts";
import { MAX_LISTED_PAGES, PageEditSession, type PagePlace, TOOL_RESULT_LIMIT, ToolError } from "./tools.ts";
import { normalizeTags, type WorkspaceHooks, type WorkspacePage, workspaceToolDefinitions } from "./workspace.ts";
import { workspaceMessage, workspacePrompt } from "./prompt.ts";
import { corpus } from "../_testing/fixture.ts";
import { Random } from "../_testing/random.ts";
import { anyValue, CHARS, markdown, withSeededIds } from "../_testing/corpus.ts";
import callsCorpus from "./testdata/corpus/workspace_calls.json" with { type: "json" };
import tagsCorpus from "./testdata/corpus/tags.json" with { type: "json" };
import messagesCorpus from "./testdata/corpus/workspace_messages.json" with { type: "json" };

/**
 * The person's pages as both sides' corpora have them (Go's
 * TestCorpusWorkspaceCalls): a page with a subpage, one shared with a
 * friend, one in their folder, one untitled, one others read by its public
 * link; p-9 is someone else's.
 */
const WORLD: WorkspacePage[] = [
  { id: "p-1", title: "Plan", tags: ["plan"] },
  { id: "p-2", title: "Notes", parent_id: "p-1", tags: [] },
  { id: "p-3", title: "Shared", tags: [], shared: true, people: true },
  { id: "p-4", title: "Filed", folder_id: "f-1", tags: [] },
  { id: "p-5", title: "Untitled", tags: [], untitled: true },
  { id: "p-6", title: "Linked", tags: [], shared: true },
];

function placeOf(p: WorkspacePage): PagePlace {
  return { ...(p.parent_id ? { parent_id: p.parent_id } : {}), ...(p.folder_id ? { folder_id: p.folder_id } : {}) };
}

/** The workspace's hooks over WORLD, which they leave as it is: each call finds it the same. */
function worldHooks(reading: boolean, made: PageContent[] = []): WorkspaceHooks {
  const noFolder = (id: string) => new ToolError(`No folder "${id}"`);
  return {
    reading,
    listPages: () => Promise.resolve(WORLD),
    page: (id) => Promise.resolve(WORLD.find((p) => p.id === id) ?? null),
    readPage: (id) =>
      Promise.resolve({ page: { id, title: WORLD.find((p) => p.id === id)!.title }, blocks: [], subpages: [] }),
    searchPages: () =>
      Promise.resolve([{ page_id: "p-1", title: "Plan" }, {
        page_id: "p-3",
        title: "Shared",
        snippet: "What they wrote",
      }]),
    listFolders: () => Promise.resolve([{ id: "f-1", name: "Clippings" }]),
    createPage: (_title, content, _parent, folder) => {
      if (folder && folder !== "f-1") return Promise.reject(noFolder(folder));
      made.push(content);
      return Promise.resolve({ id: "page-new", space_id: "space-0", sort_key: "a0" });
    },
    movePage: (id, parent, folder) => {
      if (folder && folder !== "f-1") return Promise.reject(noFolder(folder));
      if (id === "p-1" && parent === "p-2") {
        return Promise.reject(new ToolError("A page cannot move inside one of its subpages"));
      }
      const to = { ...(parent ? { parent_id: parent } : {}), ...(folder ? { folder_id: folder } : {}) };
      return Promise.resolve({ from: placeOf(WORLD.find((p) => p.id === id)!), to });
    },
    renamePage: () => Promise.resolve(),
    setTags: (_id, tags) => Promise.resolve(normalizeTags(tags)),
    createFolder: (name) => Promise.resolve({ id: "f-new", space_id: "space-0", name, sort_key: "a1" }),
  };
}

function session(reading = true, request = "", workspace = worldHooks(reading)): PageEditSession {
  return new PageEditSession(emptyContent(), {
    readPage: () => Promise.resolve({}),
    createSubpage: () => Promise.reject(new Error("no")),
    createPage: () => Promise.reject(new Error("no")),
    listPages: () => Promise.resolve([]),
    readOtherPage: () => Promise.resolve({}),
    listFolders: () => Promise.resolve([]),
  }, { canEdit: true, crossPage: false, request, author: "Claude", workspace });
}

const errorOf = (result: unknown) => (result as { error?: string }).error;

// A page other people share is not read, nor is its text shown by a
// search, and it is changed by none of the agent's tools, nor has a page
// put in it; the person's own are, each change one op with what it changed
// from.
Deno.test("the workspace agent leaves shared pages as they are", async () => {
  const s = session();
  assert(errorOf((await s.call("read_page", { page_id: "p-3" })).result)?.includes("shared with other people"));
  assertEquals((await s.call("search_pages", { query: "plan" })).result, {
    results: [{ page_id: "p-1", title: "Plan" }, { page_id: "p-3", title: "Shared" }],
  });
  const listed = (await s.call("list_pages", {})).result as { pages: Record<string, unknown>[] };
  assertEquals(listed.pages.find((p) => p.id === "p-3"), {
    id: "p-3",
    title: "Shared",
    shared: true,
    shared_with_people: true,
  });
  for (
    const [tool, args] of [
      ["create_page", { title: "In it", parent_id: "p-3" }],
      ["move_page", { page_id: "p-3", folder_id: "f-1" }],
      ["move_page", { page_id: "p-1", parent_id: "p-3" }],
      ["rename_page", { page_id: "p-3", title: "Ours" }],
      ["set_page_tags", { page_id: "p-3", tags: ["x"] }],
      ["move_page", { page_id: "p-9" }],
    ] as const
  ) {
    assert(errorOf((await s.call(tool, args)).result), `${tool} ${JSON.stringify(args)}`);
  }
  assertEquals(s.ops, []);
  const moved = await s.call("move_page", { page_id: "p-4" });
  assertEquals(moved.ops, [{
    op: "move_page",
    page_id: "p-4",
    title: "Filed",
    place: {},
    previous: { folder_id: "f-1" },
  }]);
  assertEquals(moved.edits.map((e) => e.summary), ["Moved “Filed”"]);
  const tagged = await s.call("set_page_tags", { page_id: "p-1", tags: ["#plan", "Plan", " work "] });
  assertEquals(tagged.result, { page_id: "p-1", tags: ["plan", "work"] });
});

// The agent lists at most MAX_LISTED_PAGES of the person's pages, fewer
// where their titles would take more than a model reads at once, saying
// how many more there are, for search_pages to find: what it lists is
// never cut off with the result, its folders and its count before the
// pages (Go's TestWorkspaceListsAtMostSoManyPages).
Deno.test("the workspace agent lists at most so many pages", async () => {
  const list = async (title: string, n: number) => {
    const many: WorkspacePage[] = Array.from({ length: n }, (_, i) =>
      title.length > 10
        ? {
          id: `${String(i).padStart(8, "0")}-0000-0000-0000-000000000000`,
          title,
          parent_id: "00000000-0000-0000-0000-000000000000",
          tags: ["work", "plans"],
        }
        : { id: `p-${i}`, title, tags: [] });
    const s = session(true, "", { ...worldHooks(true), listPages: () => Promise.resolve(many) });
    return (await s.call("list_pages", {})).result as { pages: unknown[]; folders: unknown[]; more_pages?: number };
  };
  const short = await list("Page", MAX_LISTED_PAGES + 3);
  assertEquals([short.pages.length, short.more_pages], [MAX_LISTED_PAGES, 3]);
  const long = await list("Quarterly planning <notes> & ".repeat(3), MAX_LISTED_PAGES);
  assert(long.pages.length > 0 && long.pages.length < MAX_LISTED_PAGES, String(long.pages.length));
  assertEquals(long.more_pages, MAX_LISTED_PAGES - long.pages.length);
  assert(JSON.stringify(long).length <= TOOL_RESULT_LIMIT);
  assertEquals(Object.keys(long), ["folders", "more_pages", "pages"]);
});

// A search shows none of the text of a page the run does not know, as one
// made since it began, as of one other people write in.
Deno.test("the workspace agent's search shows no text of a page it does not know", async () => {
  const s = session(true, "", {
    ...worldHooks(true),
    searchPages: () =>
      Promise.resolve([{ page_id: "p-1", title: "Plan", snippet: "mine" }, {
        page_id: "p-new",
        title: "New",
        snippet: "who knows",
      }]),
  });
  assertEquals((await s.call("search_pages", { query: "x" })).result, {
    results: [{ page_id: "p-1", title: "Plan", snippet: "mine" }, { page_id: "p-new", title: "New" }],
  });
});

// With reading off, the agent reads none of the person's pages, and is not
// offered the tools that would.
Deno.test("the workspace agent with reading off reads no page", async () => {
  const s = session(false);
  for (const tool of ["list_pages", "search_pages", "read_page"]) {
    assertEquals(
      errorOf((await s.call(tool, { query: "x", page_id: "p-1" })).result),
      "Reading the person's pages is off",
    );
  }
  const names = workspaceToolDefinitions(false).map((t) => t.function.name);
  assert(!names.includes("list_pages") && names.includes("create_page"));
  assert(
    workspacePrompt({ agentName: "Claude", reading: false, toolsEnabled: true, userName: "Sam", today: "2026-11-04" })
      .includes("does not let agents read their pages"),
  );
});

// A run's workspace calls make the ops here that the app's agents make of
// them, and take and refuse the same calls (Go's TestCorpusWorkspaceCalls).
Deno.test("workspace calls are the app's (testdata/corpus/workspace_calls.json)", async () => {
  const r = new Random(13);
  const ids = ["p-1", "p-2", "p-3", "p-4", "p-5", "p-6", "p-9", "nope", ""];
  const urls = ["https://example.com/given.png", "https://elsewhere.com/x.png"];
  const id = () => r.chance(0.9) ? r.pick(ids) : r.pick([5, null]);
  const folder = () => r.pick(["f-1", "nope", "", null, 5]);
  const text = () =>
    r.chance(0.85) ? r.pick(["Notes", "  Plan　", " ", "Filed", r.pick(urls), r.text(CHARS, 6)]) : anyValue(r);
  const call = (): [string, unknown] => {
    switch (r.int(11)) {
      case 0:
        return ["list_pages", {}];
      case 1:
        return ["search_pages", { query: r.chance(0.9) ? r.pick(["plan", "#plan", " "]) : anyValue(r) }];
      case 2:
        return ["read_page", { page_id: id() }];
      case 3:
        return ["list_folders", {}];
      case 4:
      case 5:
        return ["create_page", {
          title: text(),
          ...(r.chance(0.4) ? { parent_id: id() } : {}),
          ...(r.chance(0.4) ? { folder_id: folder() } : {}),
          ...(r.chance(0.4) ? { markdown: r.chance(0.9) ? markdown(r) : anyValue(r) } : {}),
          ...(r.chance(0.3)
            ? {
              blocks: [{ type: "image", image: { url: r.pick(urls) } }, { type: "paragraph", text: "x" }].slice(
                0,
                r.int(3),
              ),
            }
            : {}),
        }];
      case 6:
        return ["move_page", {
          page_id: id(),
          ...(r.chance(0.5) ? { parent_id: id() } : {}),
          ...(r.chance(0.5) ? { folder_id: folder() } : {}),
        }];
      case 7:
        return ["rename_page", { page_id: id(), title: text() }];
      case 8:
        return ["set_page_tags", {
          page_id: id(),
          tags: r.chance(0.9)
            ? r.times(0, 3, () => r.pick(["plan", "Plan", "#work", " a  b ", r.pick(urls), ""]))
            : anyValue(r),
        }];
      case 9:
        return ["create_folder", { name: text() }];
      default:
        return [r.pick(["insert_blocks", "nope"]), {}];
    }
  };
  const made: unknown[] = [];
  for (let k = 0; k < 150; k++) {
    const reading = r.chance(0.8);
    const request = r.pick(["", "Save https://example.com/given.png"]);
    const pages: PageContent[] = [];
    const s = new PageEditSession(emptyContent(), {
      readPage: () => Promise.resolve({}),
      createSubpage: () => Promise.reject(new Error("no")),
      createPage: () => Promise.reject(new Error("no")),
      listPages: () => Promise.resolve([]),
      readOtherPage: () => Promise.resolve({}),
      listFolders: () => Promise.resolve([]),
    }, {
      canEdit: true,
      crossPage: false,
      request,
      author: "Claude",
      now: () => 1000,
      workspace: worldHooks(reading, pages),
    });
    const calls: Record<string, unknown>[] = [];
    // The blocks made take IDs drawn from r, the same each time.
    await withSeededIds(r, async () => {
      for (let i = r.between(1, 6); i > 0; i--) {
        const [tool, args] = call();
        const raw = JSON.stringify(args);
        const out = await s.call(tool, raw);
        const ok = !errorOf(out.result);
        calls.push({
          tool,
          args: raw,
          ok,
          ...(ok ? { result: out.result } : {}),
          edits: out.edits.map((e) => e.summary),
        });
      }
    });
    made.push({ reading, request, calls, ops: s.ops });
  }
  await corpus("corpus/workspace_calls.json", made, callsCorpus);
});

// Tags are tidied here as the app tidies them (store.NormalizeTags).
Deno.test("tags are tidied as the app tidies them (testdata/corpus/tags.json)", async () => {
  const r = new Random(14);
  const tag = () =>
    r.pick([
      () => r.text(CHARS, 8),
      () => "#" + r.text(CHARS, 4),
      () => r.pick(["Plan", "plan", "PLAN", "İstanbul", "istanbul", "ǅ", "ǆ", "ß", "SS"]),
      () => " 　".repeat(r.int(3)) + r.text(CHARS, 3) + "\u0085",
      () => "x".repeat(60) + "é".repeat(r.int(10)) + " y",
    ])();
  const made = Array.from({ length: 300 }, () => {
    const tags = r.times(0, r.chance(0.05) ? 60 : 6, tag);
    return { tags, kept: normalizeTags(tags) };
  });
  await corpus("corpus/tags.json", made, tagsCorpus);
});

// The person's turn of a workspace run is the app's to the character
// (agents.workspaceMessage).
Deno.test("a workspace run's request is worded as the app's (testdata/corpus/workspace_messages.json)", async () => {
  const r = new Random(15);
  const made = Array.from({ length: 100 }, () => {
    const open = r.chance(0.6)
      ? { id: `p-${r.int(9)}`, title: r.text(CHARS, 6), ...(r.chance(0.3) ? { shared: true } : {}) }
      : undefined;
    const instruction = r.text(CHARS, 10);
    return { open: open ?? null, instruction, message: workspaceMessage(instruction, open) };
  });
  await corpus("corpus/workspace_messages.json", made, messagesCorpus);
});

// The workspace prompt has the device's rules (agents.WorkspacePrompt).
Deno.test("a cloud workspace run has the device's rules", () => {
  const p = workspacePrompt({
    agentName: "Claude",
    reading: true,
    toolsEnabled: true,
    userName: "Sam",
    today: "2026-11-04",
    web: true,
    search: true,
  });
  for (
    const rule of [
      "You do not change what a page says",
      "you leave as it is",
      "you cannot read either",
      "not a summary, unless",
      "Read the web first",
      "list_pages and search_pages",
    ]
  ) {
    assert(p.includes(rule), rule);
  }
});

// An untitled page renamed had no title, not the word shown for one: Undo
// gives it back none (Go's TestUndoOfARenameKeepsAPageUntitled).
Deno.test("renaming an untitled page keeps no title to give back", async () => {
  const s = session();
  const out = await s.call("rename_page", JSON.stringify({ page_id: "p-5", title: "Trip" }));
  assertEquals(out.edits.map((e) => e.summary), ["Renamed “Untitled” to “Trip”"]);
  assertEquals(s.ops, [{ op: "rename_page", page_id: "p-5", title: "Trip", previous: "" }]);
  assertEquals(
    errorOf((await s.call("rename_page", JSON.stringify({ page_id: "p-5", title: "Untitled" }))).result),
    undefined,
  );
  assertEquals(s.ops.length, 2);
});
