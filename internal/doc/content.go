package doc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Content is one page's stored content: the blocks, with stable IDs, plus
// per-block edit times and tombstones. See docs/protocol.md §1.
type Content struct {
	Children []*Node
	// Blocks is the last time (ms) each block's own content, type or
	// position changed; Deleted holds tombstones.
	Blocks  map[string]int64
	Deleted map[string]int64
}

// Empty returns a page holding one empty paragraph.
func Empty() *Content {
	id := NewBlockID()
	return &Content{
		Children: []*Node{{ID: id, Type: "paragraph", Data: map[string]any{"delta": []any{}}}},
		Blocks:   map[string]int64{id: NowMs()},
		Deleted:  map[string]int64{},
	}
}

// FromBlocks returns a page of the given blocks, all stamped now.
func FromBlocks(children []*Node) *Content {
	c := &Content{Children: children, Blocks: map[string]int64{}, Deleted: map[string]int64{}}
	c.EnsureIDs()
	now := NowMs()
	c.Walk(func(n *Node, _ *Node) { c.Blocks[n.ID] = now })
	return c
}

type contentJSON struct {
	Document struct {
		Type     string  `json:"type"`
		ID       string  `json:"id"`
		Children []*Node `json:"children"`
	} `json:"document"`
	Meta struct {
		V       int              `json:"v"`
		Blocks  map[string]int64 `json:"blocks"`
		Deleted map[string]int64 `json:"deleted"`
	} `json:"meta"`
}

// MarshalJSON writes {document, meta}.
func (c *Content) MarshalJSON() ([]byte, error) {
	var j contentJSON
	j.Document.Type, j.Document.ID = "page", "root"
	j.Document.Children = c.Children
	if j.Document.Children == nil {
		j.Document.Children = []*Node{}
	}
	j.Meta.V = 1
	j.Meta.Blocks, j.Meta.Deleted = c.Blocks, c.Deleted
	if j.Meta.Blocks == nil {
		j.Meta.Blocks = map[string]int64{}
	}
	if j.Meta.Deleted == nil {
		j.Meta.Deleted = map[string]int64{}
	}
	return json.Marshal(j)
}

// ErrNotContent is JSON that is not a page's content: null, or an object
// without its document, which reading as an empty page would put an empty
// page in place of the real one; or content of a newer version than this
// app knows.
var ErrNotContent = errors.New("not a page's content")

// contentVersion is the newest meta.v this app reads.
const contentVersion = 1

// UnmarshalJSON reads {document, meta}, tolerating a missing meta and
// fractional times.
func (c *Content) UnmarshalJSON(raw []byte) error {
	var j struct {
		Document *struct {
			Children []*Node `json:"children"`
		} `json:"document"`
		Meta struct {
			V       int                `json:"v"`
			Blocks  map[string]float64 `json:"blocks"`
			Deleted map[string]float64 `json:"deleted"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return err
	}
	if j.Document == nil || j.Meta.V > contentVersion {
		return ErrNotContent
	}
	c.Children = j.Document.Children
	c.Blocks = map[string]int64{}
	c.Deleted = map[string]int64{}
	for k, v := range j.Meta.Blocks {
		c.Blocks[k] = int64(v)
	}
	for k, v := range j.Meta.Deleted {
		c.Deleted[k] = int64(v)
	}
	c.dropNil()
	c.EnsureIDs()
	return nil
}

func (c *Content) dropNil() {
	var clean func([]*Node) []*Node
	clean = func(nodes []*Node) []*Node {
		out := nodes[:0]
		for _, n := range nodes {
			if n == nil {
				continue
			}
			n.Children = clean(n.Children)
			out = append(out, n)
		}
		return out
	}
	c.Children = clean(c.Children)
}

// Decode reads stored content; blank content is an empty page.
func Decode(source string) (*Content, error) {
	if strings.TrimSpace(source) == "" {
		return Empty(), nil
	}
	c := &Content{}
	if err := json.Unmarshal([]byte(source), c); err != nil {
		return nil, err
	}
	return c, nil
}

// MustDecode reads stored content, falling back to an empty page.
func MustDecode(source string) *Content {
	c, err := Decode(source)
	if err != nil {
		return Empty()
	}
	return c
}

// Encode writes the content as stored.
func (c *Content) Encode() string {
	raw, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// Copy returns a deep copy.
func (c *Content) Copy() *Content {
	out := &Content{Blocks: make(map[string]int64, len(c.Blocks)), Deleted: make(map[string]int64, len(c.Deleted))}
	for _, n := range c.Children {
		out.Children = append(out.Children, n.Clone())
	}
	for k, v := range c.Blocks {
		out.Blocks[k] = v
	}
	for k, v := range c.Deleted {
		out.Deleted[k] = v
	}
	return out
}

// Walk visits every block depth first, in document order, with its parent
// (nil at the top level).
func (c *Content) Walk(fn func(n, parent *Node)) {
	var visit func([]*Node, *Node)
	visit = func(nodes []*Node, parent *Node) {
		for _, n := range nodes {
			fn(n, parent)
			visit(n.Children, n)
		}
	}
	visit(c.Children, nil)
}

// All returns every block in document order.
func (c *Content) All() []*Node {
	var out []*Node
	c.Walk(func(n, _ *Node) { out = append(out, n) })
	return out
}

// EnsureIDs gives every block an ID and replaces duplicates, which copy and
// paste can make.
func (c *Content) EnsureIDs() {
	seen := map[string]bool{}
	c.Walk(func(n, _ *Node) {
		if n.ID == "" || seen[n.ID] {
			n.ID = NewBlockID()
		}
		seen[n.ID] = true
	})
}

// Find returns the block with an ID, or nil.
func (c *Content) Find(id string) *Node {
	if loc := c.Locate(id); loc != nil {
		return loc.Node
	}
	return nil
}

// Location is where a block sits in the tree.
type Location struct {
	// Siblings is the list holding the block; Set replaces it.
	Siblings []*Node
	Index    int
	Node     *Node
	// Parent is nil at the top level.
	Parent *Node
	set    func([]*Node)
}

// SetSiblings replaces the list holding the block.
func (l *Location) SetSiblings(nodes []*Node) { l.set(nodes) }

// Locate finds a block.
func (c *Content) Locate(id string) *Location {
	var search func(nodes []*Node, parent *Node, set func([]*Node)) *Location
	search = func(nodes []*Node, parent *Node, set func([]*Node)) *Location {
		for i, n := range nodes {
			if n.ID == id {
				return &Location{Siblings: nodes, Index: i, Node: n, Parent: parent, set: set}
			}
			if len(n.Children) > 0 {
				node := n
				if hit := search(n.Children, n, func(k []*Node) { node.Children = k }); hit != nil {
					return hit
				}
			}
		}
		return nil
	}
	return search(c.Children, nil, func(k []*Node) { c.Children = k })
}

// Position is where to put blocks: exactly one of after, before, parent
// (append as last child), or at "start" or "end".
type Position struct {
	After  string `json:"after,omitempty"`
	Before string `json:"before,omitempty"`
	Parent string `json:"parent,omitempty"`
	At     string `json:"at,omitempty"`
}

// Anchor is the block a position is relative to, if any.
func (p Position) Anchor() string {
	switch {
	case p.After != "":
		return p.After
	case p.Before != "":
		return p.Before
	}
	return p.Parent
}

// Valid reports whether exactly one field is set.
func (p Position) Valid() bool {
	n := 0
	for _, s := range []string{p.After, p.Before, p.Parent, p.At} {
		if s != "" {
			n++
		}
	}
	return n == 1 && (p.At == "" || p.At == "start" || p.At == "end")
}

// PositionOf describes where a block is now, so it can be put back there.
func (c *Content) PositionOf(id string) Position {
	loc := c.Locate(id)
	switch {
	case loc == nil:
		return Position{At: "end"}
	case loc.Index > 0:
		return Position{After: loc.Siblings[loc.Index-1].ID}
	case loc.Index+1 < len(loc.Siblings):
		return Position{Before: loc.Siblings[loc.Index+1].ID}
	case loc.Parent == nil:
		return Position{At: "start"}
	}
	return Position{Parent: loc.Parent.ID}
}

// Insert puts blocks at a position.
func (c *Content) Insert(p Position, nodes ...*Node) error {
	insertAt := func(list []*Node, i int, set func([]*Node)) {
		out := make([]*Node, 0, len(list)+len(nodes))
		out = append(out, list[:i]...)
		out = append(out, nodes...)
		out = append(out, list[i:]...)
		set(out)
	}
	switch {
	case p.After != "":
		loc := c.Locate(p.After)
		if loc == nil {
			return fmt.Errorf("no block %s", p.After)
		}
		insertAt(loc.Siblings, loc.Index+1, loc.set)
	case p.Before != "":
		loc := c.Locate(p.Before)
		if loc == nil {
			return fmt.Errorf("no block %s", p.Before)
		}
		insertAt(loc.Siblings, loc.Index, loc.set)
	case p.Parent != "":
		parent := c.Find(p.Parent)
		if parent == nil {
			return fmt.Errorf("no block %s", p.Parent)
		}
		parent.Children = append(parent.Children, nodes...)
	case p.At == "start":
		insertAt(c.Children, 0, func(k []*Node) { c.Children = k })
	default:
		c.Children = append(c.Children, nodes...)
	}
	return nil
}

// Remove takes a block out of the tree and returns it.
func (c *Content) Remove(id string) *Node {
	loc := c.Locate(id)
	if loc == nil {
		return nil
	}
	out := make([]*Node, 0, len(loc.Siblings)-1)
	out = append(out, loc.Siblings[:loc.Index]...)
	out = append(out, loc.Siblings[loc.Index+1:]...)
	loc.set(out)
	return loc.Node
}

// Replace swaps a block for another, keeping its ID.
func (c *Content) Replace(id string, n *Node) error {
	loc := c.Locate(id)
	if loc == nil {
		return fmt.Errorf("no block %s", id)
	}
	n.ID = id
	loc.Siblings[loc.Index] = n
	return nil
}

// Move moves a block, with its children, to a position. It checks the
// position first, so a failed move leaves the block where it was.
func (c *Content) Move(id string, p Position) error {
	from := c.Locate(id)
	if from == nil {
		return fmt.Errorf("no block %s", id)
	}
	if a := p.Anchor(); a != "" {
		loc := c.Locate(a)
		if loc == nil {
			return fmt.Errorf("no block %s", a)
		}
		if c.IsSelfOrAncestor(id, a) {
			return fmt.Errorf("block %s can't move next to or inside itself", id)
		}
		// Only a list takes nested blocks; a table and its cells keep
		// theirs, and take no others.
		to := loc.Parent
		if p.Parent != "" {
			to = loc.Node
		}
		if p.Parent != "" && !NestableTypes[to.Type] && !inTable(to) {
			return fmt.Errorf("block %s can't hold blocks", a)
		}
		if (inTable(to) || inTable(from.Parent)) && to != from.Parent {
			return fmt.Errorf("block %s can't move into or out of a table", id)
		}
	} else if inTable(from.Parent) {
		return fmt.Errorf("block %s can't move out of a table", id)
	}
	return c.Insert(p, c.Remove(id))
}

// inTable reports whether n is a table or one of its cells.
func inTable(n *Node) bool {
	return n != nil && (n.Type == "table" || n.Type == "table/cell")
}

// IsSelfOrAncestor reports whether ancestor is id or holds it.
func (c *Content) IsSelfOrAncestor(ancestor, id string) bool {
	if ancestor == id {
		return true
	}
	a := c.Find(ancestor)
	if a == nil {
		return false
	}
	var search func(*Node) bool
	search = func(n *Node) bool {
		for _, k := range n.Children {
			if k.ID == id || search(k) {
				return true
			}
		}
		return false
	}
	return search(a)
}

// PlainText is the page's text, a line per block, for search.
func (c *Content) PlainText() string {
	var lines []string
	c.Walk(func(n, _ *Node) {
		var s string
		switch n.Type {
		case "chart", "embed", "video":
			s = n.Str("title")
		case "link_preview", "file":
			s = cardTitle(n)
		case "math_equation", "diagram":
			_, s = BlockSource(n)
		default:
			s = n.Delta().Plain()
		}
		if strings.TrimSpace(s) != "" {
			lines = append(lines, s)
		}
	})
	return strings.Join(lines, "\n")
}

// Signatures returns a string per block covering its own type and data and
// where it sits, but not its children, which have their own.
func Signatures(c *Content) map[string]string {
	out := map[string]string{}
	var visit func([]*Node, string)
	visit = func(nodes []*Node, parent string) {
		var prev any
		for _, n := range nodes {
			out[n.ID] = canonical([]any{parent, prev, n.Type, n.Data})
			visit(n.Children, n.ID)
			prev = n.ID
		}
	}
	visit(c.Children, "root")
	return out
}

// ChangedBlocks returns the IDs of blocks whose signature differs.
func ChangedBlocks(a, b *Content) map[string]bool {
	sa, sb := Signatures(a), Signatures(b)
	out := map[string]bool{}
	for id, s := range sa {
		if sb[id] != s {
			out[id] = true
		}
	}
	for id, s := range sb {
		if sa[id] != s {
			out[id] = true
		}
	}
	return out
}

// Stamped returns the content with its meta updated against previous:
// blocks that are new or whose content, type or position changed get now;
// blocks that went get a tombstone. A block's time never goes back, so an
// edit on a device whose clock is behind still beats the version it
// replaced.
func (c *Content) Stamped(previous *Content, now int64) *Content {
	stamp := func(id string) int64 {
		t := now
		if previous != nil {
			if p, ok := previous.Blocks[id]; ok {
				t = max(t, p+1)
			}
			if p, ok := previous.Deleted[id]; ok {
				t = max(t, p+1)
			}
		}
		return t
	}
	before := map[string]string{}
	if previous != nil {
		before = Signatures(previous)
	}
	sigs := Signatures(c)
	blocks := map[string]int64{}
	for id, sig := range sigs {
		if before[id] == sig {
			if previous != nil {
				if t, ok := previous.Blocks[id]; ok {
					blocks[id] = t
					continue
				}
			}
			if t, ok := c.Blocks[id]; ok {
				blocks[id] = t
				continue
			}
		}
		blocks[id] = stamp(id)
	}
	deleted := map[string]int64{}
	if previous != nil {
		for k, v := range previous.Deleted {
			deleted[k] = v
		}
	}
	for k, v := range c.Deleted {
		deleted[k] = v
	}
	for id := range before {
		if _, ok := sigs[id]; !ok {
			deleted[id] = stamp(id)
		}
	}
	for id := range sigs {
		delete(deleted, id)
	}
	return &Content{Children: c.Children, Blocks: blocks, Deleted: deleted}
}
