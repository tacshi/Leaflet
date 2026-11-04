package editor

import (
	"maps"
	"reflect"

	"leaflet/internal/doc"
)

// The steps of undo and redo keep the page as it was, frozen: blocks of
// their own, which nothing changes after and the steps share with each
// other. A step keeps anew only the blocks an edit changed since the step
// before, and the lists of blocks around them, so a step on a large page
// costs what the edit changed, not the page.

// maxSteps is the most steps undo keeps, and maxHistory about the most
// bytes the steps of undo and redo keep of their own, beside the page;
// it is a variable for tests.
const maxSteps = 200

var maxHistory = 64 << 20

// freeze is the page as it is now, for a step of undo or redo: a block
// the steps kept as it is now is that one, shared, and any other a copy
// of its own, which the steps keep from then on. The steps never change
// what they keep, but for keepFold, which changes it in every step alike;
// size is about how many bytes it kept anew.
func (e *Editor) freeze() (c *doc.Content, size int) {
	if e.kept == nil {
		e.kept = map[string]*doc.Node{}
	}
	count := 0
	var visit func(nodes, was []*doc.Node) []*doc.Node
	visit = func(nodes, was []*doc.Node) []*doc.Node {
		if len(nodes) == 0 {
			return nil
		}
		// The list kept before is shared while it holds the same blocks.
		var out []*doc.Node
		same := len(was) == len(nodes)
		for i, n := range nodes {
			count++
			// A block is mostly where it was, kept in was as in kept.
			var f *doc.Node
			if i < len(was) && was[i].ID == n.ID {
				f = was[i]
			} else {
				f = e.kept[n.ID]
			}
			var kids []*doc.Node
			if f != nil {
				kids = visit(n.Children, f.Children)
			} else {
				kids = visit(n.Children, nil)
			}
			if f == nil || f.Type != n.Type || !sameNodes(f.Children, kids) || !sameData(f.Data, n.Data) {
				f = &doc.Node{ID: n.ID, Type: n.Type, Data: cloneData(n.Data), Children: kids}
				e.kept[n.ID] = f
				size += nodeSize(f)
			}
			if same && was[i] == f {
				continue
			}
			if out == nil {
				// The blocks before this one are as was has them.
				out = make([]*doc.Node, len(nodes))
				copy(out, was[:min(i, len(was))])
				same = false
				size += 8 * len(nodes)
			}
			out[i] = f
		}
		if out == nil {
			return was
		}
		return out
	}
	var top []*doc.Node
	if e.frozen != nil {
		top = e.frozen.Children
	}
	c = &doc.Content{Children: visit(e.Content.Children, top)}
	if e.frozen != nil && maps.Equal(e.frozen.Blocks, e.Content.Blocks) && maps.Equal(e.frozen.Deleted, e.Content.Deleted) {
		c.Blocks, c.Deleted = e.frozen.Blocks, e.frozen.Deleted
	} else {
		c.Blocks, c.Deleted = maps.Clone(e.Content.Blocks), maps.Clone(e.Content.Deleted)
		size += 24 * (len(c.Blocks) + len(c.Deleted))
	}
	e.frozen = c
	// What kept blocks the page no longer has, past twice those it has, is
	// let go: the next steps keep what they need of the page as it is.
	if len(e.kept) > 2*count+256 {
		e.kept = make(map[string]*doc.Node, count)
		c.Walk(func(n, _ *doc.Node) { e.kept[n.ID] = n })
	}
	return c, size
}

// sameNodes reports whether two lists hold the same blocks, as kept.
func sameNodes(a, b []*doc.Node) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// cloneData is a copy of a block's data, as Clone makes one.
func cloneData(data map[string]any) map[string]any {
	return (&doc.Node{Data: data}).Clone().Data
}

// sameData reports whether two blocks' data are the same, as JSON values:
// of the same kinds, maps and lists alike all the way down.
func sameData(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !sameValue(v, w) {
			return false
		}
	}
	return true
}

func sameValue(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case int:
		y, ok := b.(int)
		return ok && x == y
	case int64:
		y, ok := b.(int64)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameValue(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		return ok && sameData(x, y)
	}
	return reflect.TypeOf(a) == reflect.TypeOf(b) && reflect.DeepEqual(a, b)
}

// nodeSize is about how many bytes a block kept holds of its own, its
// children's list but not them.
func nodeSize(n *doc.Node) int {
	return 64 + len(n.ID) + len(n.Type) + valueSize(n.Data)
}

func valueSize(v any) int {
	switch x := v.(type) {
	case string:
		return 16 + len(x)
	case []any:
		size := 24
		for _, e := range x {
			size += 16 + valueSize(e)
		}
		return size
	case map[string]any:
		size := 48
		for k, e := range x {
			size += 32 + len(k) + valueSize(e)
		}
		return size
	}
	return 16
}

// adopt makes nodes the page's blocks, as a change from outside brings
// them or a step of undo kept them: a block of the page that is the same
// as one of them, but for its children, stands for it, so it keeps its
// layout and look, as a block untouched by the change; nodes of its own,
// as from outside, stand for themselves otherwise, and kept ones, which
// the steps go on keeping, are copied.
func (e *Editor) adopt(nodes []*doc.Node, own bool) []*doc.Node {
	page := map[string]*doc.Node{}
	e.Content.Walk(func(n, _ *doc.Node) {
		if _, ok := page[n.ID]; !ok {
			page[n.ID] = n
		}
	})
	var visit func([]*doc.Node) []*doc.Node
	visit = func(nodes []*doc.Node) []*doc.Node {
		if len(nodes) == 0 {
			return nil
		}
		out := make([]*doc.Node, len(nodes))
		for i, f := range nodes {
			kids := visit(f.Children)
			n := page[f.ID]
			switch {
			case n != nil && (n == f || n.Type == f.Type && sameData(n.Data, f.Data)):
				delete(page, f.ID)
			case own:
				n = f
			default:
				n = &doc.Node{ID: f.ID, Type: f.Type, Data: cloneData(f.Data)}
			}
			n.Children = kids
			out[i] = n
		}
		return out
	}
	return visit(nodes)
}

// stepsSize is how many bytes steps keep of their own, about.
func stepsSize(steps []snapshot) int {
	size := 0
	for _, s := range steps {
		size += s.size
	}
	return size
}

// trimHistory drops the oldest steps of undo, and then of redo, while
// there are more than maxSteps steps of undo, or the steps keep more than
// maxHistory of their own: a page's history keeps what edits made, up to
// about that much, and the newest step of each, whatever it holds. A
// change from outside left at the bottom goes too, with no step under it
// to take back.
func (e *Editor) trimHistory() {
	total := stepsSize(e.undo) + stepsSize(e.redo)
	for _, steps := range []*[]snapshot{&e.undo, &e.redo} {
		for (total > maxHistory || steps == &e.undo && len(*steps) > maxSteps) && edits(*steps) > 1 {
			total -= (*steps)[0].size
			*steps = (*steps)[1:]
			for len(*steps) > 0 && (*steps)[0].after != nil {
				total -= (*steps)[0].size
				*steps = (*steps)[1:]
			}
		}
	}
}

// edits is how many of steps are steps of an edit, not changes from
// outside marked on them.
func edits(steps []snapshot) int {
	n := 0
	for _, s := range steps {
		if s.after == nil {
			n++
		}
	}
	return n
}
