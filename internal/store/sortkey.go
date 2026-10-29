package store

import (
	"fmt"
	"strings"
)

// Fractional sort keys: a string that sorts between two others, so a page
// can be placed anywhere among its siblings without renumbering them.

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// KeyBetween returns a key that sorts after a ("" for first) and before b
// ("" for last). No key sorts between a and a followed by 0s, which this
// never makes but another client may have, as the agent function once
// made "a0": then it returns one just after b, so the page goes next to
// where it was put rather than nowhere.
func KeyBetween(a, b string) string {
	if a != "" && b != "" && a >= b {
		panic(fmt.Sprintf("%s must sort before %s", a, b))
	}
	if b != "" && strings.HasPrefix(b, a) && strings.Trim(b[len(a):], "0") == "" {
		return b + mid("", "", false)
	}
	return mid(a, b, b != "")
}

func mid(a, b string, hasB bool) string {
	if hasB {
		n := 0
		for n < len(b) {
			ca := byte('0')
			if n < len(a) {
				ca = a[n]
			}
			if ca != b[n] {
				break
			}
			n++
		}
		if n > 0 {
			rest := ""
			if len(a) > n {
				rest = a[n:]
			}
			return b[:n] + mid(rest, b[n:], true)
		}
	}
	da := 0
	if a != "" {
		da = strings.IndexByte(digits, a[0])
	}
	db := len(digits)
	if hasB {
		db = strings.IndexByte(digits, b[0])
	}
	if db-da > 1 {
		return string(digits[(da+db+1)/2])
	}
	if hasB && len(b) > 1 {
		return b[:1]
	}
	rest := ""
	if len(a) > 1 {
		rest = a[1:]
	}
	return string(digits[da]) + mid(rest, "", false)
}
