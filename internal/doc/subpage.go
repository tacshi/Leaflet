package doc

// SubpageNode is a card for page id: a subpage block, which opens it.
func SubpageNode(id string) *Node {
	return &Node{ID: NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": id}}
}

// SubpagePlace is where the first card for page id is in c, as a position
// to put one back at once it is taken out: after the block before it,
// before the one after it, or in its parent; ok is false without one.
func (c *Content) SubpagePlace(id string) (at Position, ok bool) {
	c.Walk(func(n, _ *Node) {
		if ok || n.Type != "subpage" || n.Str("page_id") != id {
			return
		}
		l := c.Locate(n.ID)
		switch {
		case l.Index > 0:
			at = Position{After: l.Siblings[l.Index-1].ID}
		case l.Index+1 < len(l.Siblings):
			at = Position{Before: l.Siblings[l.Index+1].ID}
		case l.Parent != nil:
			at = Position{Parent: l.Parent.ID}
		default:
			at = Position{At: "start"}
		}
		ok = true
	})
	return at, ok
}
