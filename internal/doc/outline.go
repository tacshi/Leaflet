package doc

type Heading struct {
	ID          string
	Level       int
	Depth       int
	Parent      string
	HasChildren bool
}

func Outline(heads []*Node) []Heading {
	out := make([]Heading, 0, len(heads))
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
