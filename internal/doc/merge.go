package doc

// Conflict is a block both sides changed since they last agreed. Last write
// wins, and the losing version is kept so the user can bring it back. A
// block one side deleted and the other changed is kept, with a nil node for
// the side that deleted it, so the user can delete it again.
type Conflict struct {
	BlockID        string
	Local, Remote  *Node
	LocalEditedAt  int64
	RemoteEditedAt int64
}

// Kept is the side the merge kept: "local" or "remote". A block one side
// deleted and the other edited is kept, and the deleted side's node is nil.
func (c Conflict) Kept() string {
	switch {
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

	// Start from whichever side edited most recently, then fold in the
	// other; at equal times, from the greater version, so that the merge
	// comes out the same whichever side is local.
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
	// document order so moves apply predictably. A move into a block only
	// the other side has waits until that block is in.
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
		// What a side changed since base, it changed whatever its times
		// say: an edit in the same millisecond as base, or from a device
		// whose clock is behind, still counts. Times only decide what
		// both sides changed.
		primaryEdited, otherEdited := b == nil || p.own != b.own, b == nil || o.own != b.own
		if primaryEdited && otherEdited && p.own != o.own {
			conflicts = append(conflicts, Conflict{
				BlockID:        id,
				Local:          localFlat[id].node.Clone(),
				Remote:         remoteFlat[id].node.Clone(),
				LocalEditedAt:  timeOf(local, id),
				RemoteEditedAt: timeOf(remote, id),
			})
		}
		// Equal times go to the greater version, so that every device
		// merging the same two picks the same one.
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
			if otherWins(movedSinceBase(p, primaryFlat, b, baseFlat), movedSinceBase(o, otherFlat, b, baseFlat), o.signature > p.signature) {
				if o.parent != "" && result.Find(o.parent) == nil {
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

	// A block on one side only was deleted on the other, or made on this
	// one. Deleted since base, it stays deleted if this side left it, and
	// the subpages under it, as they were in base; if this side changed
	// any of it, it comes back whole, whatever the times, as a conflict to
	// review. With no base to tell, the newer of the edit and the
	// tombstone wins.
	changed := func(c *Content, sideFlat map[string]*flat, id string) bool {
		changedHere := false
		var visit func(n *Node)
		visit = func(n *Node) {
			f, b := sideFlat[n.ID], baseFlat[n.ID]
			if b == nil || f.own != b.own || movedSinceBase(f, sideFlat, b, baseFlat) {
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
	// stays reports whether a block of side, missing from the other side,
	// stays, recording a conflict when it comes back over a delete.
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

	// Blocks only in the primary, in document order: a block that stays
	// keeps the blocks under it. One that goes leaves behind, in its
	// place, the blocks under it the other side still has, as one it moved
	// out before deleting it; the waiting moves below take them on.
	var dropPrimary func(nodes []*Node)
	dropPrimary = func(nodes []*Node) {
		for _, n := range nodes {
			id := n.ID
			if _, ok := otherFlat[id]; ok {
				dropPrimary(n.Children)
				continue
			}
			if stays(primary, primaryFlat, other.Deleted, id, true) {
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

	// Blocks only on the other side: add those that stay. Walk the other
	// side in order, so inserted runs keep their order; a block that stays
	// comes with the blocks under it.
	for _, n := range other.All() {
		id := n.ID
		if _, ok := primaryFlat[id]; ok {
			continue
		}
		if result.Find(id) != nil {
			continue // came in with a new parent
		}
		o := otherFlat[id]
		parentMissing := o.parent != "" && result.Find(o.parent) == nil
		if parentMissing {
			if _, ok := otherFlat[o.parent]; ok {
				continue // its parent stayed deleted, and it with it
			}
		}
		if !stays(other, otherFlat, primary.Deleted, id, false) {
			continue
		}
		pos, ok := positionIn(result, o)
		if !ok {
			pos = Position{At: "end"}
		}
		// Blocks under it that both sides have are already in, where
		// the merge above put them.
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
		delete(tombstones, id)
		var kids func(*Node)
		kids = func(n *Node) {
			for _, k := range n.Children {
				times[k.ID] = timeOf(other, k.ID)
				delete(tombstones, k.ID)
				kids(k)
			}
		}
		kids(node)
	}

	// Moves into blocks the other side made, now that those are in, in
	// the other side's order so each lands after the one before it.
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
// as opposed to blocks being deleted or added around it: it compares the
// nearest preceding sibling that both base and the side have.
func movedSinceBase(side *flat, sideFlat map[string]*flat, base *flat, baseFlat map[string]*flat) bool {
	if base == nil || side.parent != base.parent {
		return true
	}
	nearest := func(prevs []string, in map[string]*flat) string {
		for _, id := range prevs {
			if _, ok := in[id]; ok {
				return id
			}
		}
		return ""
	}
	return nearest(side.prevs, baseFlat) != nearest(base.prevs, sideFlat)
}

// positionIn is where the block goes in c, next to its neighbours where
// they are under its parent: one moved elsewhere, or still waiting to move
// in, is no place to put it.
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
	if c.Find(f.parent) != nil {
		return Position{Parent: f.parent}, true
	}
	return Position{}, false
}

// SameBlock reports whether two nodes are the same version of a block:
// the same type and data, whatever their children.
func SameBlock(a, b *Node) bool {
	return a != nil && b != nil && canonical([]any{a.Type, a.Data}) == canonical([]any{b.Type, b.Data})
}
