package editor

import (
	"maps"
	"reflect"

	"leaflet/internal/doc"
)

const maxSteps = 200

var maxHistory = 64 << 20

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
		var out []*doc.Node
		same := len(was) == len(nodes)
		for i, n := range nodes {
			count++
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
	if len(e.kept) > 2*count+256 {
		e.kept = make(map[string]*doc.Node, count)
		c.Walk(func(n, _ *doc.Node) { e.kept[n.ID] = n })
	}
	return c, size
}

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

func cloneData(data map[string]any) map[string]any {
	return (&doc.Node{Data: data}).Clone().Data
}

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

func stepsSize(steps []snapshot) int {
	size := 0
	for _, s := range steps {
		size += s.size
	}
	return size
}

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

func edits(steps []snapshot) int {
	n := 0
	for _, s := range steps {
		if s.after == nil {
			n++
		}
	}
	return n
}
