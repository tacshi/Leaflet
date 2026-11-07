package doc

import "testing"

// The line being said is the last to have started; before the first there
// is none, and a line without a time stays with the one before.
func TestCurrentLine(t *testing.T) {
	lines := []NoteLine{{At: -1}, {At: 5}, {At: -1}, {At: 10}, {At: 20}}
	for sec, want := range map[float64]int{0: -1, 4.9: -1, 5: 1, 9.9: 1, 10: 3, 19: 3, 20: 4, 500: 4} {
		if got := CurrentLine(lines, sec); got != want {
			t.Errorf("CurrentLine(%v) = %d, want %d", sec, got, want)
		}
	}
}
