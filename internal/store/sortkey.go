package store

// Fractional sort keys: a string that sorts between two others, so a page
// can be placed anywhere among its siblings without renumbering them.

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
	if k, ok := mid(a, b, b != ""); ok {
		return k
	}
	return b + string(digits[len(digits)/2])
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
