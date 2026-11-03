package doc

// Heading is a heading in a page's outline: its block and level, how
// deep it nests, the heading it is in ("" for none), and whether any are
// in it.
type Heading struct {
	ID          string
	Level       int
	Depth       int
	Parent      string
	HasChildren bool
}

// Outline nests headings, given in their order on the page, as the
// outline shows them, on the desktop and the phone alike: each in the
// nearest heading before it of a higher level.
func Outline(heads []*Node) []Heading {
	out := make([]Heading, 0, len(heads))
	// open are the headings a later one may be in, by index, the deepest
	// last.
	var open []int
	for _, h := range heads {
		level := h.Int("level", 1)
		for len(open) > 0 && out[open[len(open)-1]].Level >= level {
			open = open[:len(open)-1]
		}
		row := Heading{ID: h.ID, Level: level, Depth: len(open)}
		if len(open) > 0 {
			parent := &out[open[len(open)-1]]
			parent.HasChildren = true
			row.Parent = parent.ID
		}
		open = append(open, len(out))
		out = append(out, row)
	}
	return out
}
