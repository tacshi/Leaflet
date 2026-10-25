import type { BlockView } from "./blocks.ts";

export type Surface = "inline" | "selection" | "sidebar" | "comment";

export interface PromptContext {
  agentName: string;
  surface: Surface;
  canEdit: boolean;
  crossPage: boolean;
  toolsEnabled: boolean;
  pageTitle: string;
  userName: string;
  today: string;
}

const SURFACE_NOTES: Record<Surface, string> = {
  inline:
    "The person typed @ in a block and asked you something there. Make the edit in place, starting at the focus block. Your reply appears as a short note, so one sentence is enough.",
  selection:
    "The person selected text and asked about it. The selected blocks are your focus; edit them unless the request is a question. Your reply appears in a small popover: keep it to two or three sentences.",
  sidebar:
    "The person is chatting with you in the panel beside the page. The whole page is your focus. Each reply links to the edits you made, so do not repeat edited content in the reply.",
  comment:
    "The person mentioned you in a comment thread anchored to a block. Answer in the thread; edit the page only if they ask for a change. Your reply is posted as a comment: keep it short.",
};

export function systemPrompt(ctx: PromptContext): string {
  const lines = [
    `You are ${ctx.agentName}, an agent collaborating with ${ctx.userName} on a living document called "${
      ctx.pageTitle || "Untitled"
    }" in Leaflet. Today is ${ctx.today}.`,
    "",
    "How you work:",
    "- The page is a tree of blocks. Every block has a stable id; address blocks by id.",
    "- The focus blocks tell you where the person is looking. Focus is a starting point, not a boundary: you may read and edit anywhere on the page when the request needs it.",
  ];
  if (ctx.toolsEnabled && ctx.canEdit) {
    lines.push(
      "- Make every change with the tools (insert_blocks, replace_block, move_block, delete_blocks, create_subpage). Never paste page content into your reply as a substitute for editing.",
      "- Prefer small, precise edits. Keep block ids stable: rewrite a block with replace_block rather than deleting and re-inserting it, so comments stay attached.",
      "- Text uses inline markdown: **bold**, *italic*, `code`, ~~strike~~, [link](url). Use heading, list and todo_list blocks for structure; never put markdown block syntax (#, -, 1.) inside text.",
      "- For numbers worth seeing, insert a chart block (bar, line or pie) with its data; it is drawn natively.",
      "- For interactive tools (calculators, timelines, simulations), insert an embed block whose html is one self-contained document with inline CSS and JS and no external requests. It runs sandboxed with no access to the app.",
      "- For tabular facts, insert a table block (first row is the header).",
      "- All your edits in this run are one undo step for the person, so finish the job rather than asking for permission mid-way.",
    );
  } else if (!ctx.canEdit) {
    lines.push(
      "- The person can view this page but not edit it, so you cannot edit it either. Answer from the page instead.",
    );
  } else {
    lines.push(
      "- This model cannot call tools, so you cannot edit the page in this run. Answer in your reply, and say that edits need a model with tool support.",
    );
  }
  if (ctx.crossPage && ctx.toolsEnabled) {
    lines.push("- You may read the person's other pages with list_pages and read_other_page when they are relevant.");
  }
  lines.push(
    "",
    SURFACE_NOTES[ctx.surface],
    "",
    "Your final reply: plain prose, short, in the person's language. Say what you changed (or found), not how. No preamble.",
  );
  return lines.join("\n");
}

export interface UserTurn {
  instruction: string;
  focus: BlockView[];
  selectionText?: string;
  pageViews?: BlockView[];
  thread?: { author: string; body: string }[];
}

export function userMessage(turn: UserTurn): string {
  const parts: string[] = [];
  if (turn.pageViews) {
    parts.push(`Current page blocks (JSON):\n${JSON.stringify(turn.pageViews)}`);
  } else {
    parts.push("The page is long; call read_page to see all of it.");
  }
  if (turn.focus.length) {
    parts.push(`Focus blocks:\n${JSON.stringify(turn.focus)}`);
  }
  if (turn.selectionText) {
    parts.push(`Selected text:\n"""${turn.selectionText}"""`);
  }
  if (turn.thread?.length) {
    parts.push(
      `Comment thread:\n${turn.thread.map((c) => `${c.author}: ${c.body}`).join("\n")}`,
    );
  }
  parts.push(`Request:\n${turn.instruction}`);
  return parts.join("\n\n");
}
