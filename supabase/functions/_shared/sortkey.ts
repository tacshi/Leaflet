// Fractional sort keys, as the app makes them (internal/store/sortkey.go):
// strings of these digits, compared byte by byte, so a page can go between
// two others without renumbering them. A key never ends in "0", the
// lowest digit: no key would sort between "a" and "a0".
//
// A key put last counts on from the one before, as the app's keyPast
// does, so a list keeps short keys however long it grows: the key holds an
// integer, a head "a" to "z" that says how many digits follow it, one to
// 26, then those digits ("a5", "b0Z"). The next key is the next integer
// ("a6"; after "az", "b01").

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/**
 * A key after key ("" for the first of a list), as the app makes one for a
 * page put last (KeyBetween(key, "") in sortkey.go): the integer after the
 * one key holds, or, where it holds none, the first one, "a1", after key.
 * Keys from other clients, or from earlier versions, may hold characters
 * that are no digits.
 */
export function keyAfter(key: string): string {
  const at = integerIn(key);
  if (at) {
    const next = nextInteger(key.slice(at.start, at.end));
    if (next) return key.slice(0, at.start) + next;
  }
  if (key === "" || key[0] < "a") return "a1";
  return key + "a1";
}

/** Where the first integer in key starts and ends: a head and as many digits as it says. */
function integerIn(key: string): { start: number; end: number } | null {
  for (let p = 0; p < key.length; p++) {
    if (key[p] < "a" || key[p] > "z") continue;
    const end = p + 2 + key.charCodeAt(p) - 97;
    if (end <= key.length && [...key.slice(p + 1, end)].every((c) => digits.includes(c))) {
      return { start: p, end };
    }
  }
  return null;
}

/** The integer after i, not ending in 0, or null after the last there is ("z" and 26 "z"s). */
function nextInteger(i: string): string | null {
  let b = [...i];
  for (let k = b.length - 1; k > 0; k--) {
    const d = digits.indexOf(b[k]);
    if (d + 1 < digits.length) {
      b[k] = digits[d + 1];
      break;
    }
    b[k] = "0";
    if (k === 1) {
      // Every digit went round: the next head, a digit longer.
      if (b[0] === "z") return null;
      b = [String.fromCharCode(b[0].charCodeAt(0) + 1), ...b.slice(1), "0"];
    }
  }
  if (b[b.length - 1] === "0") b[b.length - 1] = "1";
  return b.join("");
}

/**
 * A key of a page's own after key, as store.NewKey makes one: keyAfter's,
 * then two random digits but 0. A device putting a page last from the same
 * keys would make the same key otherwise, and the pages would sort apart
 * differently on each device.
 */
export function newKeyAfter(key: string): string {
  const r = crypto.getRandomValues(new Uint8Array(2));
  return keyAfter(key) + digits[1 + (r[0] % (digits.length - 1))] + digits[1 + (r[1] % (digits.length - 1))];
}

/** The last of keys in the app's order, byte by byte, not the database's collation. */
export function lastKey(keys: string[]): string {
  return keys.reduce((last, k) => (k > last ? k : last), "");
}
