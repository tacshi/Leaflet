// Transcript segments: [{start_ms, end_ms, text, speaker?}] (recordings.transcript).

import { isSpace, jsonText, trimSpace } from "./blocks.ts";

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

/** What ends the sentence a token, a word or punctuation, ends, "" for one that ends none (recording.sentenceEnd). */
function sentenceEnd(text: string): string {
  const t = trimSpace(text);
  return SENTENCE_ENDS.find((p) => t.endsWith(p)) ?? "";
}

/**
 * Whether a token starts a word of its own rather than carrying on the
 * one before, as the 5 of 3.5 does (recording.startsWord): it starts with
 * a space, or in a script written without them.
 */
function startsWord(text: string): boolean {
  const [c] = text;
  return c !== undefined && (isSpace(c) || /\p{Script=Han}|\p{Script=Hiragana}|\p{Script=Katakana}/u.test(c));
}

/**
 * Groups Soniox tokens into transcript segments, as the app groups those
 * it streams (recording.TokensToSegments): a new one on a new speaker,
 * after a pause of more than a second, or at a sentence's end after four
 * seconds, whether the word ends the sentence or punctuation of its own
 * does. Punctuation that comes late ends the words before it rather than
 * starting a segment of its own.
 */
export function tokensToSegments(tokens: SonioxToken[]): Segment[] {
  const segments: Segment[] = [];
  let text = "";
  let start = -1, end = 0;
  let speaker = "";
  // What ended the segment's sentence, four seconds in, "" until then:
  // the next word starts a segment, once the quotes and brackets closing
  // it are in. After a full stop, which numbers and addresses have inside
  // them, only a word of its own does.
  let ended = "";
  const flush = () => {
    const t = trimSpace(text);
    if (t) segments.push({ start_ms: Math.max(start, 0), end_ms: end, text: t, ...(speaker ? { speaker } : {}) });
    text = "";
    start = -1;
    ended = "";
  };
  for (const token of tokens) {
    const tokenText = typeof token.text === "string" ? token.text : "";
    const s = token.start_ms ?? 0;
    const e = Math.max(token.end_ms ?? 0, s);
    const who = jsonText(token.speaker);
    if (isPunct(tokenText)) {
      if (text) {
        text += tokenText;
        const p = sentenceEnd(tokenText);
        if (p && end - start > 4000) ended = p;
      } else if (segments.length) segments[segments.length - 1].text += trimSpace(tokenText);
      continue;
    }
    if (text && ((who && who !== speaker) || s - end > 1000 || (ended && (ended !== "." || startsWord(tokenText))))) {
      flush();
    }
    if (start < 0) start = s;
    if (who) speaker = who;
    text += tokenText;
    end = e;
    ended = end - start > 4000 ? sentenceEnd(tokenText) : "";
  }
  flush();
  return segments;
}
