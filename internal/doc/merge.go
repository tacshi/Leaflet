package doc

import (
	"maps"
	"slices"
)

type Conflict struct {
	BlockID        string
	Local, Remote  *Node
	LocalEditedAt  int64
	RemoteEditedAt int64
	kept           string
}

func (c Conflict) Kept() string {
	switch {
	case c.kept != "":
		return c.kept
	case c.Remote == nil:
		return "local"
	case c.Local == nil:
		return "remote"
	case c.LocalEditedAt != c.RemoteEditedAt:
		if c.LocalEditedAt > c.RemoteEditedAt {
			return "local"
		}
		return "remote"
	case canonical([]any{c.Local.Type, c.Local.Data}) >= canonical([]any{c.Remote.Type, c.Remote.Data}):
		return "local"
	}
	return "remote"
}

type flat struct {
	node      *Node
	parent    string
	prev      string
	next      string
	own       string
	signature string
}

func flatten(c *Content) map[string]*flat {
	out := map[string]*flat{}
	var visit func([]*Node, string)
	visit = func(nodes []*Node, parent string) {
		for i, n := range nodes {
			f := &flat{node: n, parent: parent}
			if i > 0 {
				f.prev = nodes[i-1].ID
			}
			if i+1 < len(nodes) {
				f.next = nodes[i+1].ID
			}
			f.own = canonical([]any{n.Type, n.Data})
			var p, pv any
			if parent != "" {
				p = parent
			}
			if f.prev != "" {
				pv = f.prev
			}
			f.signature = canonical([]any{p, pv, n.Type, n.Data})
			out[n.ID] = f
			visit(n.Children, n.ID)
		}
	}
	visit(c.Children, "")
	return out
}

func Merge(base, local, remote *Content) (*Content, []Conflict) {
	localFlat, remoteFlat := flatten(local), flatten(remote)
	baseFlat := map[string]*flat{}
	if base != nil {
		baseFlat = flatten(base)
	}
	var conflicts []Conflict
	timeOf := func(c *Content, id string) int64 {
		if c == nil {
			return 0
		}
		return c.Blocks[id]
	}

	var localLatest, remoteLatest int64
	for _, t := range local.Blocks {
		localLatest = max(localLatest, t)
	}
	for _, t := range remote.Blocks {
		remoteLatest = max(remoteLatest, t)
	}
	primaryIsLocal := localLatest > remoteLatest || localLatest == remoteLatest && local.Encode() >= remote.Encode()
	primary, other, primaryFlat, otherFlat := local, remote, localFlat, remoteFlat
	if !primaryIsLocal {
		primary, other, primaryFlat, otherFlat = remote, local, remoteFlat, localFlat
	}
	movedOn := map[*Content]map[string]bool{local: movedSince(local, base), remote: movedSince(remote, base)}
	moved := func(c *Content, id string) bool { return baseFlat[id] == nil || movedOn[c][id] }

	result := primary.Copy()
	times := map[string]int64{}
	for k, v := range primary.Blocks {
		times[k] = v
	}
	tombstones := map[string]int64{}
	for k, v := range other.Deleted {
		tombstones[k] = v
	}
	for k, v := range primary.Deleted {
		tombstones[k] = max(tombstones[k], v)
	}

	waiting := map[string]bool{}
	for _, n := range primary.All() {
		id := n.ID
		p := primaryFlat[id]
		o, ok := otherFlat[id]
		if !ok {
			continue
		}
		if p.signature == o.signature {
			times[id] = max(timeOf(primary, id), timeOf(other, id))
			continue
		}
		pt, ot := timeOf(primary, id), timeOf(other, id)
		b := baseFlat[id]
		primaryEdited, otherEdited := b == nil || p.own != b.own, b == nil || o.own != b.own
		if primaryEdited && otherEdited && p.own != o.own && !onlyPlaced(p.node, o.node) {
			conflicts = append(conflicts, Conflict{
				BlockID:        id,
				Local:          localFlat[id].node.Clone(),
				Remote:         remoteFlat[id].node.Clone(),
				LocalEditedAt:  timeOf(local, id),
				RemoteEditedAt: timeOf(remote, id),
			})
		}
		otherWins := func(primaryChanged, otherChanged, otherGreater bool) bool {
			return otherChanged && (!primaryChanged || ot > pt || ot == pt && otherGreater)
		}
		taken := false
		if p.own != o.own && otherWins(primaryEdited, otherEdited, o.own > p.own) {
			repl := o.node.Clone()
			repl.Children = result.Find(id).Children
			_ = result.Replace(id, repl)
			taken = true
		}
		if p.parent != o.parent || p.prev != o.prev {
			if otherWins(moved(primary, id), moved(other, id), o.signature > p.signature) {
				if o.parent != "" && result.Find(o.parent) == nil || o.prev != "" && primaryFlat[o.prev] == nil {
					waiting[id] = true
				} else if pos, ok := positionIn(result, o); ok && !result.IsSelfOrAncestor(id, pos.Anchor()) {
					_ = result.Move(id, pos)
					taken = true
				}
			}
		}
		times[id] = pt
		if taken {
			times[id] = max(pt, ot)
		}
	}

	changed := func(c *Content, sideFlat map[string]*flat, id string) bool {
		changedHere := false
		var visit func(n *Node)
		visit = func(n *Node) {
			f, b := sideFlat[n.ID], baseFlat[n.ID]
			if b == nil || f.own != b.own || moved(c, n.ID) {
				changedHere = true
			}
			for _, k := range n.Children {
				visit(k)
			}
		}
		visit(sideFlat[id].node)
		return changedHere
	}
	deletedConflict := func(id string, kept *Node, keptOnPrimary bool, editedAt, deletedAt int64) {
		c := Conflict{BlockID: id, Local: kept.Clone(), LocalEditedAt: editedAt, RemoteEditedAt: deletedAt}
		if keptOnPrimary != primaryIsLocal {
			c = Conflict{BlockID: id, Remote: kept.Clone(), LocalEditedAt: deletedAt, RemoteEditedAt: editedAt}
		}
		conflicts = append(conflicts, c)
	}
	stays := func(side *Content, sideFlat map[string]*flat, deletions map[string]int64, id string, sideIsPrimary bool) bool {
		deletedAt, hasDel := deletions[id]
		editedAt := timeOf(side, id)
		if _, inBase := baseFlat[id]; !inBase {
			return !hasDel || editedAt > deletedAt
		}
		if !changed(side, sideFlat, id) {
			return false
		}
		deletedConflict(id, sideFlat[id].node, sideIsPrimary, editedAt, deletedAt)
		return true
	}
	tableConflicts := map[string]bool{}
	for _, c := range conflicts {
		tableConflicts[c.BlockID] = true
	}
	cellGone := func(side *Content, sideFlat map[string]*flat, deletions map[string]int64, id string, sideIsPrimary bool) bool {
		if _, inBase := baseFlat[id]; !inBase {
			return false
		}
		table := ""
		for f := sideFlat[id]; f.parent != ""; f = sideFlat[f.parent] {
			if sideFlat[f.parent].node.Type == "table" {
				table = f.parent
				break
			}
		}
		if table == "" || localFlat[table] == nil || remoteFlat[table] == nil {
			return false
		}
		if tableConflicts[table] || !changed(side, sideFlat, id) {
			return true
		}
		tableConflicts[table] = true
		var editedAt int64
		var latest func(*Node)
		latest = func(n *Node) {
			editedAt = max(editedAt, timeOf(side, n.ID))
			for _, k := range n.Children {
				latest(k)
			}
		}
		latest(sideFlat[id].node)
		c := Conflict{BlockID: table, Local: localFlat[table].node.Clone(), Remote: remoteFlat[table].node.Clone()}
		if sideIsPrimary == primaryIsLocal {
			c.LocalEditedAt, c.RemoteEditedAt, c.kept = editedAt, deletions[id], "remote"
		} else {
			c.LocalEditedAt, c.RemoteEditedAt, c.kept = deletions[id], editedAt, "local"
		}
		conflicts = append(conflicts, c)
		return true
	}

	var dropPrimary func(nodes []*Node)
	dropPrimary = func(nodes []*Node) {
		for _, n := range nodes {
			id := n.ID
			if _, ok := otherFlat[id]; ok {
				dropPrimary(n.Children)
				continue
			}
			if !cellGone(primary, primaryFlat, other.Deleted, id, true) && stays(primary, primaryFlat, other.Deleted, id, true) {
				continue
			}
			var alive []*Node
			var find func(*Node)
			find = func(n *Node) {
				for _, k := range n.Children {
					if _, ok := otherFlat[k.ID]; ok {
						alive = append(alive, k)
					} else {
						find(k)
					}
				}
			}
			if r := result.Find(id); r != nil {
				find(r)
			}
			for _, k := range alive {
				_ = result.Move(k.ID, Position{Before: id})
			}
			result.Remove(id)
			delete(times, id)
			tombstones[id] = max(tombstones[id], other.Deleted[id], timeOf(primary, id))
			for _, k := range alive {
				if f := primaryFlat[k.ID]; f != nil {
					dropPrimary([]*Node{f.node})
				}
			}
		}
	}
	dropPrimary(primary.Children)

	for _, n := range other.All() {
		id := n.ID
		if _, ok := primaryFlat[id]; ok {
			continue
		}
		if result.Find(id) != nil {
			continue
		}
		o := otherFlat[id]
		parentMissing := o.parent != "" && result.Find(o.parent) == nil
		if parentMissing {
			if _, ok := otherFlat[o.parent]; ok {
				continue
			}
		}
		if cellGone(other, otherFlat, primary.Deleted, id, false) || !stays(other, otherFlat, primary.Deleted, id, false) {
			continue
		}
		pos, ok := positionIn(result, o)
		if !ok {
			pos = Position{At: "end"}
		}
		node := o.node.Clone()
		var shared func(*Node)
		shared = func(n *Node) {
			kept := n.Children[:0]
			for _, k := range n.Children {
				if _, ok := primaryFlat[k.ID]; !ok {
					shared(k)
					kept = append(kept, k)
				}
			}
			n.Children = kept
		}
		shared(node)
		_ = result.Insert(pos, node)
		times[id] = timeOf(other, id)
		var kids func(*Node)
		kids = func(n *Node) {
			for _, k := range n.Children {
				times[k.ID] = timeOf(other, k.ID)
				kids(k)
			}
		}
		kids(node)
	}

	for _, n := range other.All() {
		id := n.ID
		if !waiting[id] {
			continue
		}
		if pos, ok := positionIn(result, otherFlat[id]); ok && !result.IsSelfOrAncestor(id, pos.Anchor()) {
			_ = result.Move(id, pos)
			times[id] = max(timeOf(primary, id), timeOf(other, id))
		}
	}

	for _, n := range result.All() {
		p, o := primaryFlat[n.ID], otherFlat[n.ID]
		if n.Type == "table" && p != nil && o != nil && !samePlaces(p.node, o.node) {
			placeCells(n, p.node, o.node, times, max(localLatest, remoteLatest)+1)
		}
	}

	result.Children = liftStranded(result.Children)

	live := map[string]bool{}
	result.Walk(func(n, _ *Node) { live[n.ID] = true })
	result.Blocks = map[string]int64{}
	for id := range live {
		result.Blocks[id] = times[id]
		if t, ok := tombstones[id]; ok {
			result.Blocks[id] = max(times[id], t+1)
		}
	}
	result.Deleted = map[string]int64{}
	for id, t := range tombstones {
		if !live[id] {
			result.Deleted[id] = t
		}
	}
	return result, conflicts
}

func samePlaces(a, b *Node) bool {
	if len(a.Children) != len(b.Children) {
		return false
	}
	type place struct{ r, c int }
	places := make(map[string]place, len(a.Children))
	for _, cell := range a.Children {
		places[cell.ID] = place{cell.Int("rowPosition", -1), cell.Int("colPosition", -1)}
	}
	for _, cell := range b.Children {
		if at, ok := places[cell.ID]; !ok || at != (place{cell.Int("rowPosition", -1), cell.Int("colPosition", -1)}) {
			return false
		}
	}
	return true
}

func placeCells(table, primary, other *Node, times map[string]int64, at int64) {
	rows, shared := cellLines(table, primary, other, "rowPosition")
	cols := map[string]int{}
	if shared {
		cols, _ = cellLines(table, primary, other, "colPosition")
	}
	move := func(cell *Node, key string, line int, ok bool) {
		if ok && cell.Int(key, -1) != line {
			cell.Set(key, line)
			times[cell.ID] = at
		}
	}
	rowCount, colCount := 0, 0
	for _, cell := range table.Children {
		r, ok := rows[cell.ID]
		move(cell, "rowPosition", r, ok)
		c, ok := cols[cell.ID]
		move(cell, "colPosition", c, ok)
		rowCount, colCount = max(rowCount, cell.Int("rowPosition", -1)+1), max(colCount, cell.Int("colPosition", -1)+1)
	}
	if rowsLen, colsLen := table.Int("rowsLen", 0), table.Int("colsLen", 0); rowsLen < rowCount || colsLen < colCount {
		table.Set("rowsLen", max(rowsLen, rowCount))
		table.Set("colsLen", max(colsLen, colCount))
		times[table.ID] = at
	}
	SortTableCells(table)
}

func cellLines(table, primary, other *Node, key string) (lines map[string]int, shared bool) {
	kept := map[string]bool{}
	for _, cell := range table.Children {
		kept[cell.ID] = true
	}
	joined := map[string]string{}
	var line func(id string) string
	line = func(id string) string {
		up, ok := joined[id]
		if !ok {
			return id
		}
		top := line(up)
		joined[id] = top
		return top
	}
	sideLines := func(side *Node) []string {
		first := map[int]string{}
		for _, cell := range side.Children {
			at := cell.Int(key, -1)
			if at < 0 || !kept[cell.ID] {
				continue
			}
			if id, ok := first[at]; !ok {
				first[at] = cell.ID
			} else if a, b := line(id), line(cell.ID); a != b {
				joined[b] = a
			}
		}
		out := make([]string, 0, len(first))
		for _, at := range slices.Sorted(maps.Keys(first)) {
			out = append(out, first[at])
		}
		return out
	}
	mine, theirs := sideLines(primary), sideLines(other)
	tops := func(cells []string) []string {
		seen := map[string]bool{}
		var out []string
		for _, id := range cells {
			if top := line(id); !seen[top] {
				seen[top] = true
				out = append(out, top)
			}
		}
		return out
	}
	order, shared := bothOrders(tops(mine), tops(theirs))
	places := make(map[string]int, len(order))
	for i, top := range order {
		places[top] = i
	}
	lines = make(map[string]int, len(table.Children))
	for _, cell := range table.Children {
		if at, ok := places[line(cell.ID)]; ok {
			lines[cell.ID] = at
		}
	}
	return lines, shared
}

func bothOrders(first, second []string) (order []string, shared bool) {
	inFirst, inSecond := map[string]bool{}, map[string]bool{}
	for _, id := range first {
		inFirst[id] = true
	}
	for _, id := range second {
		inSecond[id] = true
	}
	j := 0
	own := func(end func() bool) {
		for ; j < len(second) && !end(); j++ {
			if !inFirst[second[j]] {
				order = append(order, second[j])
			}
		}
	}
	for _, id := range first {
		if inSecond[id] {
			shared = true
			own(func() bool { return second[j] == id })
			j++
		}
		order = append(order, id)
	}
	own(func() bool { return false })
	return order, shared
}

func onlyPlaced(a, b *Node) bool {
	var grid []string
	switch {
	case a.Type != b.Type:
		return false
	case a.Type == "table/cell":
		grid = []string{"rowPosition", "colPosition"}
	case a.Type == "table":
		grid = []string{"rowsLen", "colsLen"}
	default:
		return false
	}
	ungridded := func(n *Node) string {
		data := map[string]any{}
		for k, v := range n.Data {
			if !slices.Contains(grid, k) {
				data[k] = v
			}
		}
		return canonical(data)
	}
	return ungridded(a) == ungridded(b)
}

func (c *Content) Lifted() *Content {
	var stranded func(nodes []*Node) bool
	stranded = func(nodes []*Node) bool {
		for _, n := range nodes {
			if !inTable(n) && (!NestableTypes[n.Type] && len(n.Children) > 0 || stranded(n.Children)) {
				return true
			}
		}
		return false
	}
	if !stranded(c.Children) {
		return c
	}
	out := c.Copy()
	out.Children = liftStranded(out.Children)
	return out
}

func liftStranded(nodes []*Node) []*Node {
	var out []*Node
	for _, n := range nodes {
		out = append(out, n)
		if inTable(n) {
			continue
		}
		n.Children = liftStranded(n.Children)
		if !NestableTypes[n.Type] && len(n.Children) > 0 {
			out = append(out, n.Children...)
			n.Children = nil
		}
	}
	return out
}

func Rebase(old, after, current *Content) *Content {
	undone := old.Copy().Stamped(current, NowMs())
	undone.Deleted = map[string]int64{}
	merged, _ := Merge(after, undone, current)
	return merged
}

func movedSince(side, base *Content) map[string]bool {
	moved := map[string]bool{}
	if base == nil {
		return moved
	}
	type place struct {
		parent string
		index  int
	}
	inBase := map[string]place{}
	var index func([]*Node, string)
	index = func(nodes []*Node, parent string) {
		for i, n := range nodes {
			inBase[n.ID] = place{parent, i}
			index(n.Children, n.ID)
		}
	}
	index(base.Children, "")
	var visit func([]*Node, string)
	visit = func(nodes []*Node, parent string) {
		var ids []string
		var at []int
		var stayed []bool
		for _, n := range nodes {
			if b, ok := inBase[n.ID]; ok {
				if b.parent != parent {
					moved[n.ID] = true
				} else {
					ids, at = append(ids, n.ID), append(at, b.index)
					stayed = append(stayed, side.Blocks[n.ID] == base.Blocks[n.ID])
				}
			}
			visit(n.Children, n.ID)
		}
		kept := inOrder(at, stayed)
		for i, id := range ids {
			if !kept[i] {
				moved[id] = true
			}
		}
	}
	visit(side.Children, "")
	return moved
}

func inOrder(xs []int, stayed []bool) []bool {
	type run struct{ length, stayed, end int }
	better := func(a, b run) bool { return a.length > b.length || a.length == b.length && a.stayed > b.stayed }
	size := 0
	for _, x := range xs {
		size = max(size, x+1)
	}
	tree := make([]run, size+1)
	for i := range tree {
		tree[i].end = -1
	}
	prev := make([]int, len(xs))
	best := run{end: -1}
	for i, x := range xs {
		below := run{end: -1}
		for j := x; j > 0; j -= j & -j {
			if better(tree[j], below) {
				below = tree[j]
			}
		}
		r := run{below.length + 1, below.stayed, i}
		if stayed[i] {
			r.stayed++
		}
		prev[i] = below.end
		for j := x + 1; j <= size; j += j & -j {
			if better(r, tree[j]) {
				tree[j] = r
			}
		}
		if better(r, best) {
			best = r
		}
	}
	kept := make([]bool, len(xs))
	for i := best.end; i >= 0; i = prev[i] {
		kept[i] = true
	}
	return kept
}

func positionIn(c *Content, f *flat) (Position, bool) {
	under := func(id string) bool {
		loc := c.Locate(id)
		if loc == nil {
			return false
		}
		if loc.Parent == nil {
			return f.parent == ""
		}
		return loc.Parent.ID == f.parent
	}
	if f.prev != "" && under(f.prev) {
		return Position{After: f.prev}, true
	}
	if f.next != "" && under(f.next) {
		return Position{Before: f.next}, true
	}
	if f.parent == "" {
		if f.prev == "" {
			return Position{At: "start"}, true
		}
		return Position{}, false
	}
	if parent := c.Find(f.parent); parent != nil {
		if f.prev == "" && len(parent.Children) > 0 {
			if first := parent.Children[0].ID; first != f.node.ID {
				return Position{Before: first}, true
			}
			return Position{}, false
		}
		return Position{Parent: f.parent}, true
	}
	return Position{}, false
}

func SameBlock(a, b *Node) bool {
	return a != nil && b != nil && canonical([]any{a.Type, a.Data}) == canonical([]any{b.Type, b.Data})
}
