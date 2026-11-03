// Seeded random values for the corpora the parity tests write
// (_shared/corpus_test.ts): a seed draws the same values on every run, so
// a corpus changes only as the code that makes it does.

import { randomSeeded } from "jsr:@std/random@0.1.5";

export class Random {
  private readonly next: () => number;

  constructor(seed: number) {
    this.next = randomSeeded(BigInt(seed));
  }

  /** A whole number from 0 up to n, not n. */
  int(n: number): number {
    return Math.floor(this.next() * n);
  }

  /** A whole number from lo to hi, both included. */
  between(lo: number, hi: number): number {
    return lo + this.int(hi - lo + 1);
  }

  /** True as often as p says, a fraction of the time. */
  chance(p: number): boolean {
    return this.next() < p;
  }

  pick<T>(items: readonly T[]): T {
    return items[this.int(items.length)];
  }

  /** From min to max of make's values. */
  times<T>(min: number, max: number, make: (i: number) => T): T[] {
    return Array.from({ length: this.between(min, max) }, (_, i) => make(i));
  }

  /** Up to max of parts, joined. */
  text(parts: readonly string[], max: number): string {
    return this.times(0, max, () => this.pick(parts)).join("");
  }

  /** A fraction from 0 up to 1, for numbers of any kind. */
  fraction(): number {
    return this.next();
  }
}
