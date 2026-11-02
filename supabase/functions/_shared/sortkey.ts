// Fractional sort keys, as the app makes them (internal/store/sortkey.go):
// strings of these digits, compared byte by byte, so a page can go between
// two others without renumbering them. A key never ends in "0", the
// lowest digit: no key would sort between "a" and "a0".

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/**
 * A key after key ("" for the first of a list), as the app makes it (mid
 * in sortkey.go). Keys from other clients may hold characters that are no
 * digits: a key goes after those by their codes too.
 */
export function keyAfter(key: string): string {
  // The digits after key's first character, but 0.
  const lo = key ? key.charCodeAt(0) : -1;
  let i = 1;
  while (i < digits.length && digits.charCodeAt(i) <= lo) i++;
  if (i < digits.length) return digits[Math.floor((i + digits.length) / 2)];
  return key[0] + keyAfter(key.slice(1));
}

/** The last of keys in the app's order, byte by byte, not the database's collation. */
export function lastKey(keys: string[]): string {
  return keys.reduce((last, k) => (k > last ? k : last), "");
}
