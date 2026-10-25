// Transcript segments: [{start_ms, end_ms, text, speaker?}] (recordings.transcript).

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

/** Groups word tokens into sentence-sized segments, splitting on speaker changes. */
export function tokensToSegments(tokens: SonioxToken[]): Segment[] {
  const segments: Segment[] = [];
  let current: Segment | null = null;
  for (const token of tokens) {
    if (!token.text) continue;
    const speaker = token.speaker === undefined || token.speaker === null ? undefined : String(token.speaker);
    const start: number = token.start_ms ?? current?.end_ms ?? 0;
    const end: number = token.end_ms ?? start;
    if (!current || current.speaker !== speaker) {
      current = { start_ms: start, end_ms: end, text: "", ...(speaker ? { speaker } : {}) };
      segments.push(current);
    }
    current.text += token.text;
    current.end_ms = end;
    if (/[.!?。！？]\s*$/.test(token.text) && current.end_ms - current.start_ms > 3_000) current = null;
  }
  return segments
    .map((s) => ({ ...s, text: s.text.trim() }))
    .filter((s) => s.text);
}
