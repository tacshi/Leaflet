package doc

func SubpageNode(id string) *Node {
	return &Node{ID: NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": id}}
}

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
