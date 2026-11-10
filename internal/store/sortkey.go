package store

import (
	"crypto/rand"
	"strings"
)

const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

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

func nextInteger(i string) (string, bool) {
	b := []byte(i)
	for k := len(b) - 1; k > 0; k-- {
		if d := strings.IndexByte(digits, b[k]); d+1 < len(digits) {
			b[k] = digits[d+1]
			break
		}
		b[k] = '0'
		if k == 1 {
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
	lo, hi := -1, 256
	if a != "" {
		lo = int(a[0])
	}
	if hasB {
		hi = int(b[0])
	}
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
		if b[0] > '0' {
			return b[:1], true
		}
		if k, ok := mid("", b[1:], true); ok {
			return b[:1] + k, true
		}
	}
	if a != "" {
		k, _ := mid(a[1:], "", false)
		return a[:1] + k, true
	}
	return "", false
}

func NewKey(a, b string) string {
	k := KeyBetween(a, b)
	var r [2]byte
	_, _ = rand.Read(r[:])
	own := k + string(digits[1+int(r[0])%(len(digits)-1)]) + string(digits[1+int(r[1])%(len(digits)-1)])
	if b != "" && a < b && own >= b {
		return k
	}
	return own
}
