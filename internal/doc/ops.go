package doc

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Removed is a block a delete took out and where it was.
type Removed struct {
	Node     *Node    `json:"node"`
	Position Position `json:"position"`
}

// NewPage is the page a create_page op makes.
type NewPage struct {
	ID       string `json:"id"`
	SpaceID  string `json:"space_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Title    string `json:"title"`
	SortKey  string `json:"sort_key,omitempty"`
}

// Op is one block change by ID, as agents make and Undo run inverts
// (docs/protocol.md §3):
//
//	insert   position, nodes
//	replace  id, node, previous (node)
//	move     id, position, from
//	delete   ids, previous ([{node, position}])
//	create_page  page, content
//	delete_page  page_id
type Op struct {
	Op       string
	Position *Position
	Nodes    []*Node
	ID       string
	Node     *Node
	// PrevNode is a replace's previous node; Removed a delete's previous.
	PrevNode *Node
	Removed  []Removed
	From     *Position
	IDs      []string
	Page     *NewPage
	Content  *Content
	PageID   string
}

type opJSON struct {
	Op       string          `json:"op"`
	Position *Position       `json:"position,omitempty"`
	Nodes    []*Node         `json:"nodes,omitempty"`
	ID       string          `json:"id,omitempty"`
	Node     *Node           `json:"node,omitempty"`
	Previous json.RawMessage `json:"previous,omitempty"`
	From     *Position       `json:"from,omitempty"`
	IDs      []string        `json:"ids,omitempty"`
	Page     *NewPage        `json:"page,omitempty"`
	Content  *Content        `json:"content,omitempty"`
	PageID   string          `json:"page_id,omitempty"`
}

// MarshalJSON writes the op as the protocol has it.
func (o Op) MarshalJSON() ([]byte, error) {
	j := opJSON{Op: o.Op, Position: o.Position, Nodes: o.Nodes, ID: o.ID, Node: o.Node,
		From: o.From, IDs: o.IDs, Page: o.Page, Content: o.Content, PageID: o.PageID}
	var err error
	switch o.Op {
	case "replace":
		if o.PrevNode != nil {
			j.Previous, err = json.Marshal(o.PrevNode)
		}
	case "delete":
		removed := o.Removed
		if removed == nil {
			removed = []Removed{}
		}
		j.Previous, err = json.Marshal(removed)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads an op.
func (o *Op) UnmarshalJSON(raw []byte) error {
	var j opJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return err
	}
	*o = Op{Op: j.Op, Position: j.Position, Nodes: j.Nodes, ID: j.ID, Node: j.Node,
		From: j.From, IDs: j.IDs, Page: j.Page, Content: j.Content, PageID: j.PageID}
	if len(j.Previous) > 0 && string(j.Previous) != "null" {
		switch j.Op {
		case "replace":
			o.PrevNode = &Node{}
			if err := json.Unmarshal(j.Previous, o.PrevNode); err != nil {
				return err
			}
		case "delete":
			if err := json.Unmarshal(j.Previous, &o.Removed); err != nil {
				return err
			}
		}
	}
	return nil
}

// Apply applies a block op to the content. create_page and delete_page are
// the caller's: they touch the page tree rather than this page.
func (c *Content) Apply(o Op) error {
	switch o.Op {
	case "insert":
		p := Position{At: "end"}
		if o.Position != nil {
			p = *o.Position
		}
		nodes := make([]*Node, len(o.Nodes))
		for i, n := range o.Nodes {
			nodes[i] = n.Clone()
		}
		return c.Insert(p, nodes...)
	case "replace":
		if o.Node == nil {
			return fmt.Errorf("replace without a node")
		}
		return c.Replace(o.ID, o.Node.Clone())
	case "move":
		if o.Position == nil {
			return fmt.Errorf("move without a position")
		}
		return c.Move(o.ID, *o.Position)
	case "delete":
		for _, id := range o.IDs {
			c.Remove(id)
		}
	}
	return nil
}

// Invert returns the ops that undo o, in the order to apply them.
func (o Op) Invert() []Op {
	switch o.Op {
	case "insert":
		ids := make([]string, len(o.Nodes))
		removed := make([]Removed, len(o.Nodes))
		for i, n := range o.Nodes {
			ids[i] = n.ID
			p := Position{At: "end"}
			if i == 0 && o.Position != nil {
				p = *o.Position
			} else if i > 0 {
				p = Position{After: ids[i-1]}
			}
			removed[i] = Removed{Node: n, Position: p}
		}
		return []Op{{Op: "delete", IDs: ids, Removed: removed}}
	case "replace":
		return []Op{{Op: "replace", ID: o.ID, Node: o.PrevNode, PrevNode: o.Node}}
	case "move":
		return []Op{{Op: "move", ID: o.ID, Position: o.From, From: o.Position}}
	case "delete":
		var out []Op
		for _, r := range o.Removed {
			p := r.Position
			out = append(out, Op{Op: "insert", Position: &p, Nodes: []*Node{r.Node}})
		}
		return out
	case "create_page":
		if o.Page != nil {
			return []Op{{Op: "delete_page", PageID: o.Page.ID}}
		}
	}
	return nil
}

// InvertRun returns the ops that undo a whole run.
func InvertRun(ops []Op) []Op {
	var out []Op
	for i := len(ops) - 1; i >= 0; i-- {
		out = append(out, ops[i].Invert()...)
	}
	return out
}

// DeleteOp builds a delete of ids that records where each block was, so it
// can be inverted. The blocks are recorded in document order, each after
// its previous sibling, which is either still there or restored just
// before it; a first child goes before the next sibling that stays. A block
// inside another deleted one comes back with it, so it isn't recorded.
func (c *Content) DeleteOp(ids []string) Op {
	o := Op{Op: "delete", IDs: ids, Removed: []Removed{}}
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	var visit func(nodes []*Node, parent *Node)
	visit = func(nodes []*Node, parent *Node) {
		for i, n := range nodes {
			if !gone[n.ID] {
				visit(n.Children, n)
				continue
			}
			var p Position
			if i > 0 {
				p = Position{After: nodes[i-1].ID}
			} else if j := slices.IndexFunc(nodes, func(k *Node) bool { return !gone[k.ID] }); j >= 0 {
				p = Position{Before: nodes[j].ID}
			} else if parent != nil {
				p = Position{Parent: parent.ID}
			} else {
				p = Position{At: "start"}
			}
			o.Removed = append(o.Removed, Removed{Node: n.Clone(), Position: p})
		}
	}
	visit(c.Children, nil)
	return o
}

// ReplaceOp builds a replace of a block that records its previous version.
func (c *Content) ReplaceOp(id string, n *Node) Op {
	n.ID = id
	return Op{Op: "replace", ID: id, Node: n, PrevNode: c.Find(id).Clone()}
}

// MoveOp builds a move that records where the block was.
func (c *Content) MoveOp(id string, p Position) Op {
	from := c.PositionOf(id)
	return Op{Op: "move", ID: id, Position: &p, From: &from}
}

// InsertOp builds an insert.
func InsertOp(p Position, nodes ...*Node) Op {
	return Op{Op: "insert", Position: &p, Nodes: nodes}
}

// Touched returns the IDs of the blocks ops change.
func Touched(ops []Op) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, o := range ops {
		for _, n := range o.Nodes {
			add(n.ID)
		}
		add(o.ID)
	}
	return out
}
