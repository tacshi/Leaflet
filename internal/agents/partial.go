package agents

import "strings"

func closeJSON(s string) (string, bool) {
	var stack []byte
	safe, safeStack := -1, []byte(nil)
	mark := func(n int) { safe, safeStack = n, append(safeStack[:0], stack...) }
	inStr, esc, key := false, false, false
	expectKey := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
				if !key {
					mark(i + 1)
				}
			}
			continue
		}
		switch c {
		case '"':
			inStr, key = true, expectKey
			expectKey = false
		case '{':
			stack = append(stack, '}')
			expectKey = true
			mark(i + 1)
		case '[':
			stack = append(stack, ']')
			mark(i + 1)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			expectKey = false
			mark(i + 1)
		case ',':
			if safe < i {
				mark(i)
			}
			expectKey = len(stack) > 0 && stack[len(stack)-1] == '}'
		case ':':
			expectKey = false
		}
	}
	var b strings.Builder
	closers := safeStack
	switch {
	case inStr && !key:
		t := s
		if esc {
			t = t[:len(t)-1]
		} else if i := strings.LastIndex(t, `\u`); i >= 0 && len(t)-i < 6 && !strings.ContainsAny(t[i+2:], `"\`) && escapes(t, i) {
			t = t[:i]
		}
		b.WriteString(t)
		b.WriteByte('"')
		closers = stack
	case safe < 0:
		return "", false
	default:
		b.WriteString(s[:safe])
	}
	for i := len(closers) - 1; i >= 0; i-- {
		b.WriteByte(closers[i])
	}
	return b.String(), true
}

func escapes(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 0
}
