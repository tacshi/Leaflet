package doc

// Conflict is a block both sides changed since they last agreed. Last write
// wins, and the losing version is kept so the user can bring it back.
type Conflict struct {
	BlockID        string
	Local, Remote  *Node
	LocalEditedAt  int64
	RemoteEditedAt int64
}

// Kept is the side last-write-wins kept: "local" or "remote".
func (c Conflict) Kept() string {
	if c.LocalEditedAt >= c.RemoteEditedAt {
		return "local"
	}
	return "remote"
}

type flat struct {
	node   *Node
	parent string
	// prevs are the preceding siblings, nearest first.
	prevs     []string
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
			for j := i - 1; j >= 0; j-- {
				f.prevs = append(f.prevs, nodes[j].ID)
			}
			if len(f.prevs) > 0 {
				f.prev = f.prevs[0]
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

// Merge merges two versions of a page, last write wins per block. base is
// the version both sides last agreed on (the last synced server version),
// which tells "changed on one side" from "changed on both"; it may be nil.
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

	// Start from whichever side edited most recently, then fold in the other.
	var localLatest, remoteLatest int64
	for _, t := range local.Blocks {
		localLatest = max(localLatest, t)
	}
	for _, t := range remote.Blocks {
		remoteLatest = max(remoteLatest, t)
	}
	primaryIsLocal := localLatest >= remoteLatest
	primary, other, primaryFlat, otherFlat := local, remote, localFlat, remoteFlat
	if !primaryIsLocal {
		primary, other, primaryFlat, otherFlat = remote, local, remoteFlat, localFlat
	}

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

	// Blocks on both sides: the newer version of each wins. Iterate in
	// document order so moves apply predictably.
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
		baseTime := timeOf(base, id)
		localChanged := b == nil || (localFlat[id].own != b.own && timeOf(local, id) > baseTime)
		remoteChanged := b == nil || (remoteFlat[id].own != b.own && timeOf(remote, id) > baseTime)
		if localChanged && remoteChanged && p.own != o.own {
			conflicts = append(conflicts, Conflict{
				BlockID:        id,
				Local:          localFlat[id].node.Clone(),
				Remote:         remoteFlat[id].node.Clone(),
				LocalEditedAt:  timeOf(local, id),
				RemoteEditedAt: timeOf(remote, id),
			})
		}
		if ot > pt {
			current := result.Find(id)
			if p.own != o.own {
				repl := o.node.Clone()
				repl.Children = current.Children
				result.Replace(id, repl)
			}
			if (p.parent != o.parent || p.prev != o.prev) && movedSinceBase(o, otherFlat, baseFlat[id]) {
				if pos, ok := positionIn(result, o); ok && !result.IsSelfOrAncestor(id, pos.Anchor()) {
					result.Move(id, pos)
				}
			}
			times[id] = ot
		} else {
			times[id] = pt
		}
	}

	// Blocks only in the primary: drop them if the other side deleted them
	// later than they were last edited.
	for id := range primaryFlat {
		if _, ok := otherFlat[id]; ok {
			continue
		}
		deletedAt, hasDel := other.Deleted[id]
		editedAt := timeOf(primary, id)
		_, existedAtBase := baseFlat[id]
		deletedOnOther := hasDel || (existedAtBase && editedAt <= timeOf(base, id))
		at := editedAt
		if hasDel {
			at = deletedAt
		}
		if deletedOnOther && at >= editedAt {
			result.Remove(id)
			delete(times, id)
			tombstones[id] = max(tombstones[id], at)
		}
	}

	// Blocks only on the other side: add them unless the primary deleted
	// them later than they were last edited. Walk the other side in order,
	// so inserted runs keep their order.
	for _, n := range other.All() {
		id := n.ID
		if _, ok := primaryFlat[id]; ok {
			continue
		}
		if result.Find(id) != nil {
			continue // came in with a new parent
		}
		deletedAt, hasDel := primary.Deleted[id]
		editedAt := timeOf(other, id)
		_, existedAtBase := baseFlat[id]
		deletedOnPrimary := hasDel || (existedAtBase && editedAt <= timeOf(base, id))
		at := editedAt
		if hasDel {
			at = deletedAt
		}
		if deletedOnPrimary && at >= editedAt {
			continue
		}
		o := otherFlat[id]
		parentMissing := o.parent != "" && result.Find(o.parent) == nil
		if parentMissing {
			if _, ok := otherFlat[o.parent]; ok {
				continue // added with its parent's subtree
			}
		}
		pos, ok := positionIn(result, o)
		if !ok {
			pos = Position{At: "end"}
		}
		result.Insert(pos, o.node.Clone())
		times[id] = editedAt
		delete(tombstones, id)
		var kids func(*Node)
		kids = func(n *Node) {
			for _, k := range n.Children {
				times[k.ID] = timeOf(other, k.ID)
				delete(tombstones, k.ID)
				kids(k)
			}
		}
		kids(o.node)
	}

	live := map[string]bool{}
	result.Walk(func(n, _ *Node) { live[n.ID] = true })
	result.Blocks = map[string]int64{}
	for id := range live {
		result.Blocks[id] = times[id]
	}
	result.Deleted = map[string]int64{}
	for id, t := range tombstones {
		if !live[id] {
			result.Deleted[id] = t
		}
	}
	return result, conflicts
}

// movedSinceBase reports whether a side really moved the block since base,
// as opposed to its neighbours being deleted around it.
func movedSinceBase(side *flat, sideFlat map[string]*flat, base *flat) bool {
	if base == nil || side.parent != base.parent {
		return true
	}
	basePrev := ""
	for _, id := range base.prevs {
		if _, ok := sideFlat[id]; ok {
			basePrev = id
			break
		}
	}
	return side.prev != basePrev
}

func positionIn(c *Content, f *flat) (Position, bool) {
	if f.prev != "" && c.Find(f.prev) != nil {
		return Position{After: f.prev}, true
	}
	if f.next != "" && c.Find(f.next) != nil {
		return Position{Before: f.next}, true
	}
	if f.parent == "" {
		if f.prev == "" {
			return Position{At: "start"}, true
		}
		return Position{}, false
	}
	if c.Find(f.parent) != nil {
		return Position{Parent: f.parent}, true
	}
	return Position{}, false
}
