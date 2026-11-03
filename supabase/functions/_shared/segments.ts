// Transcript segments: [{start_ms, end_ms, text, speaker?}] (recordings.transcript).

import { isSpace, trimSpace } from "./blocks.ts";

export interface Segment {
  start_ms: number;
  end_ms: number;
  text: string;
  speaker?: string;
}

interface SonioxToken {
  text: string;
  start_ms?: number;
  end_ms?: number;
  speaker?: string | number;
}

/** Whether text has only punctuation and spaces (recording.isPunct). */
function isPunct(text: string): boolean {
  for (const c of text) {
    if (!isSpace(c) && !/\p{P}/u.test(c)) return false;
  }
  return true;
}

const SENTENCE_ENDS = [".", "?", "!", "。", "？", "！"];

/**
 * Groups Soniox tokens into transcript segments, as the app groups those
 * it streams (recording.TokensToSegments): a new one on a new speaker,
 * after a pause of more than a second, or at a sentence's end after four
 * seconds. Punctuation that comes late ends the words before it rather
 * than starting a segment of its own.
 */
export function tokensToSegments(tokens: SonioxToken[]): Segment[] {
  const segments: Segment[] = [];
  let text = "";
  let start = -1, end = 0;
  let speaker = "";
  const flush = () => {
    const t = trimSpace(text);
    if (t) segments.push({ start_ms: Math.max(start, 0), end_ms: end, text: t, ...(speaker ? { speaker } : {}) });
    text = "";
    start = -1;
  };
  for (const token of tokens) {
    const tokenText = typeof token.text === "string" ? token.text : "";
    const s = token.start_ms ?? 0;
    const e = Math.max(token.end_ms ?? 0, s);
    const who = token.speaker === undefined || token.speaker === null ? "" : String(token.speaker);
    if (isPunct(tokenText)) {
      if (text) text += tokenText;
      else if (segments.length) segments[segments.length - 1].text += trimSpace(tokenText);
      continue;
    }
    if (text && ((who && who !== speaker) || s - end > 1000)) flush();
    if (start < 0) start = s;
    if (who) speaker = who;
    text += tokenText;
    end = e;
    const word = trimSpace(tokenText);
    if (SENTENCE_ENDS.some((p) => word.endsWith(p)) && end - start > 4000) flush();
  }
  flush();
  return segments;
}
