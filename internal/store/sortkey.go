package store

import (
	"crypto/rand"
	"strings"
)

// Fractional sort keys: a string that sorts between two others, so a page
// can be placed anywhere among its siblings without renumbering them.
//
// A key put last counts on from the one before, as fractional indexing
// does, so a list keeps short keys however long it grows: the key holds
// an integer, a head 'a' to 'z' that says how many digits follow it, one
// to 26, then those digits ("a5", "b0Z"), which may come after what keys
// from before it had, and come before digits of its own. The next key is
// the next integer ("a6"; after "az", "b01"): keys grow by a character
// every 62 times as many, not every few. A key between two others is
// made by mid.

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// KeyBetween returns a key that sorts after a ("" for first) and before b
// ("" for last). Keys from the server may be any text, with characters
// outside digits, and come in any order: a and b out of order give a key
// after a. No key of digits sorts between a and a followed by 0s, which
// this never makes but another client may have, as the agent function
// once made "a0": then it returns one just after b, so the page goes next
// to where it was put rather than nowhere.
func KeyBetween(a, b string) string {
	if b != "" && a >= b {
		b = ""
	}
	if b == "" {
		return keyPast(a)
	}
	if k, ok := mid(a, b, true); ok {
		return k
	}
	return b + string(digits[len(digits)/2])
}

// keyPast is a key after a, for a page put last: the integer after the
// one a holds, or, where it holds none, the first one, "a1", after a.
func keyPast(a string) string {
	if p, end, ok := integerIn(a); ok {
		if next, ok := nextInteger(a[p:end]); ok {
			return a[:p] + next
		}
	}
	if a == "" || a[0] < 'a' {
		return "a1"
	}
	return a + "a1"
}

// integerIn finds the first integer in key: a head and as many digits as
// it says. It returns where the integer starts and ends, or false.
func integerIn(key string) (start, end int, ok bool) {
	for p := 0; p < len(key); p++ {
		if key[p] < 'a' || key[p] > 'z' {
			continue
		}
		end := p + 2 + int(key[p]-'a')
		if end <= len(key) && strings.Trim(key[p+1:end], digits) == "" {
			return p, end, true
		}
	}
	return 0, 0, false
}

// nextInteger is the integer after i, not ending in 0, or false after the
// last there is ('z' and 26 'z's).
func nextInteger(i string) (string, bool) {
	b := []byte(i)
	for k := len(b) - 1; k > 0; k-- {
		if d := strings.IndexByte(digits, b[k]); d+1 < len(digits) {
			b[k] = digits[d+1]
			break
		}
		b[k] = '0'
		if k == 1 {
			// Every digit went round: the next head, a digit longer.
			if b[0] == 'z' {
				return "", false
			}
			b = append([]byte{b[0] + 1}, b[1:]...)
			b = append(b, '0')
		}
	}
	if b[len(b)-1] == '0' {
		b[len(b)-1] = '1'
	}
	return string(b), true
}

// mid returns a key of digits after a and before b (or, without b, after
// a), or false when there is none.
func mid(a, b string, hasB bool) (string, bool) {
	if hasB {
		n := 0
		for n < len(a) && n < len(b) && a[n] == b[n] {
			n++
		}
		if n > 0 {
			k, ok := mid(a[n:], b[n:], true)
			return b[:n] + k, ok
		}
	}
	// Here a and b differ at their first character, a's lower or none.
	lo, hi := -1, 256
	if a != "" {
		lo = int(a[0])
	}
	if hasB {
		hi = int(b[0])
	}
	// The digits strictly between, but 0: a key ending in 0 leaves none
	// between it and the key without that 0.
	i, j := 1, 1
	for i < len(digits) && int(digits[i]) <= lo {
		i++
	}
	for j < len(digits) && int(digits[j]) < hi {
		j++
	}
	if j > i {
		return string(digits[(i+j)/2]), true
	}
	if lo < '0' && '0' < hi {
		return "0" + string(digits[len(digits)/2]), true
	}
	if hasB && len(b) > 1 {
		// b's first character sorts after a and before b, and so does
		// it followed by a key before the rest of b.
		if b[0] > '0' {
			return b[:1], true
		}
		if k, ok := mid("", b[1:], true); ok {
			return b[:1] + k, true
		}
	}
	if a != "" {
		// a's first character, then a key after the rest of a.
		k, _ := mid(a[1:], "", false)
		return a[:1] + k, true
	}
	return "", false
}

// NewKey is a key for a page or folder put between a and b, as KeyBetween
// makes it, made its own by two random digits after it while it stays
// before b. Two devices, or a device and the server's agent, putting a
// page in the same place from the same keys would make the same key, and
// the pages would sort apart differently on each; and a key a deleted page
// had, back with Undo, would be another page's.
func NewKey(a, b string) string {
	k := KeyBetween(a, b)
	var r [2]byte
	_, _ = rand.Read(r[:])
	// Not 0: a key ending in 0 leaves none between it and the key without.
	own := k + string(digits[1+int(r[0])%(len(digits)-1)]) + string(digits[1+int(r[1])%(len(digits)-1)])
	if b != "" && a < b && own >= b {
		return k // b begins with k: the digits after would pass it
	}
	return own
}
