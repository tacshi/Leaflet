// Fractional sort keys, as the app makes them (internal/store/sortkey.go):
// strings of these digits, compared byte by byte, so a page can go between
// two others without renumbering them. A key never ends in "0", the
// lowest digit: no key would sort between "a" and "a0".

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/** A key after key ("" for the first of a list), as the app makes it. */
export function keyAfter(key: string): string {
  const da = key ? digits.indexOf(key[0]) : 0;
  if (digits.length - da > 1) return digits[Math.floor((da + digits.length + 1) / 2)];
  return digits[da] + keyAfter(key.slice(1));
}

/** The last of keys in the app's order, byte by byte, not the database's collation. */
export function lastKey(keys: string[]): string {
  return keys.reduce((last, k) => (k > last ? k : last), "");
}
