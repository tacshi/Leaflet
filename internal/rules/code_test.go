package rules

import "testing"

// Enter in code starts the new line as indented as the caret's, up to the
// caret; on the second blank line at the end it leaves the block, without
// the blank lines.
func TestCodeEnter(t *testing.T) {
	for _, tc := range []struct {
		code   string
		caret  int
		leave  bool
		keep   int
		insert string
	}{
		{"if x {", 6, false, 0, "\n"},
		{"if x {\n\ty", 9, false, 0, "\n\t"},
		{"  a  b", 3, false, 0, "\n  "},
		{"    ", 2, false, 0, "\n  "},
		{"x\n  \n", 5, true, 1, ""},
		{"x\n\n", 3, true, 1, ""},
		// Two blank lines before the end leave nothing: the caret is not
		// on the second.
		{"x\n\n\ny", 3, false, 0, "\n"},
		{"一\n\n", 3, true, 1, ""},
	} {
		leave, keep, insert := CodeEnter(tc.code, tc.caret)
		if leave != tc.leave || keep != tc.keep || insert != tc.insert {
			t.Errorf("CodeEnter(%q, %d) = %v %d %q", tc.code, tc.caret, leave, keep, insert)
		}
	}
}

// Tab indents the lines chosen by the block's own step, a tab where it
// indents with tabs; Shift-Tab takes one off, as far as the step goes and
// no further than a tab. A line the selection only reaches the start of
// stays, and the selection moves with its text.
func TestIndentCode(t *testing.T) {
	for _, tc := range []struct {
		name          string
		code          string
		anchor, focus int
		out           bool
		want          string
		wantA, wantF  int
		ok            bool
	}{
		{"a caret", "ab", 1, 1, false, "a  b", 3, 3, true},
		{"a caret in tabbed code", "x\n\ty", 0, 0, false, "\tx\n\ty", 1, 1, true},
		{"lines", "a\nb\nc", 0, 3, false, "  a\n  b\nc", 2, 7, true},
		{"a line reached at its start", "a\nb", 0, 2, false, "  a\nb", 2, 4, true},
		{"backwards", "a\nb", 3, 1, false, "  a\n  b", 7, 3, true},
		{"out", "  a\n   b\n c", 0, 11, true, "a\n b\nc", 0, 6, true},
		{"out by a tab's step", "\t\ta\n  b", 0, 6, true, "\ta\n b", 0, 4, true},
		{"out of spaces by a tab's step", "  a\n\tb", 0, 6, true, " a\nb", 0, 4, true},
		{"out of space and a tab", " \tx", 3, 3, true, "x", 1, 1, true},
		{"nothing to take off", "a\nb", 0, 3, true, "a\nb", 0, 3, false},
	} {
		got, a, f, ok := IndentCode(tc.code, tc.anchor, tc.focus, tc.out)
		if got != tc.want || a != tc.wantA || f != tc.wantF || ok != tc.ok {
			t.Errorf("%s: %q %d %d %v, want %q %d %d %v", tc.name, got, a, f, ok, tc.want, tc.wantA, tc.wantF, tc.ok)
		}
	}
}
