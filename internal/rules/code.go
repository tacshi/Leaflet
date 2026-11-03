package rules

import "strings"

// Code blocks: a line breaks indented as the one it leaves, and Tab
// indents the lines chosen by the block's own step. Offsets are in runes
// of the block's plain text, its lines apart by "\n".

// CodeEnter is what Enter does in code at caret. On the second blank line
// at the end, leave is true: the block keeps its text up to keep, without
// the blank lines, and the caret goes on in a paragraph after it. Else
// insert goes in at the caret: a line break and the indent of the caret's
// line, as far as the caret.
func CodeEnter(code string, caret int) (leave bool, keep int, insert string) {
	runes := []rune(code)
	caret = min(max(caret, 0), len(runes))
	lines := strings.Split(code, "\n")
	last := len(lines) - 1
	if caret == len(runes) && last >= 2 && strings.TrimSpace(lines[last]) == "" && strings.TrimSpace(lines[last-1]) == "" {
		return true, len([]rune(strings.Join(lines[:last-1], "\n"))), ""
	}
	start := lineStart(runes, caret)
	indent := start
	for indent < caret && (runes[indent] == ' ' || runes[indent] == '\t') {
		indent++
	}
	return false, 0, "\n" + string(runes[start:indent])
}

func lineStart(runes []rune, off int) int {
	for off > 0 && runes[off-1] != '\n' {
		off--
	}
	return off
}

// IndentCode indents the lines of code from the one anchor is on to the
// one focus is on, or takes an indent off them when out, by the block's
// own step: a tab when its lines are indented with tabs, else two spaces.
// A line the selection only reaches the start of is left out, and Tab at
// a caret puts a step there. It returns the code, and anchor and focus
// where they are in it then; ok is false when nothing changed, as lines
// with no indent outdented.
func IndentCode(code string, anchor, focus int, out bool) (_ string, _, _ int, ok bool) {
	runes := []rune(code)
	anchor, focus = min(max(anchor, 0), len(runes)), min(max(focus, 0), len(runes))
	unit := []rune("  ")
	if strings.Contains("\n"+code, "\n\t") {
		unit = []rune("\t")
	}
	a, b := min(anchor, focus), max(anchor, focus)
	if !out && a == b {
		made := string(runes[:a]) + string(unit) + string(runes[a:])
		return made, a + len(unit), a + len(unit), true
	}
	starts := []int{lineStart(runes, a)}
	for i := starts[0]; i < len(runes); i++ {
		if runes[i] == '\n' && i+1 < b {
			starts = append(starts, i+1)
		}
	}
	type edit struct{ at, removed, added int }
	var edits []edit
	for _, s := range starts {
		if !out {
			edits = append(edits, edit{s, 0, len(unit)})
			continue
		}
		k := 0
		for k < len(unit) && s+k < len(runes) && (runes[s+k] == ' ' || runes[s+k] == '\t') {
			k++
			if runes[s+k-1] == '\t' {
				break
			}
		}
		if k > 0 {
			edits = append(edits, edit{s, k, 0})
		}
	}
	if len(edits) == 0 {
		return code, anchor, focus, false
	}
	moved := func(x int) int {
		y := x
		for _, ed := range edits {
			if ed.at <= x {
				y += ed.added - min(ed.removed, x-ed.at)
			}
		}
		return y
	}
	for i := len(edits) - 1; i >= 0; i-- {
		ed := edits[i]
		var add []rune
		if ed.added > 0 {
			add = unit
		}
		runes = append(runes[:ed.at], append(add, runes[ed.at+ed.removed:]...)...)
	}
	return string(runes), moved(anchor), moved(focus), true
}
