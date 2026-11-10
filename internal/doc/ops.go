package doc

import (
	"encoding/json"
	"fmt"
	"slices"
)

type Removed struct {
	Node     *Node    `json:"node"`
	Position Position `json:"position"`
}

type NewPage struct {
	ID       string `json:"id"`
	SpaceID  string `json:"space_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	FolderID string `json:"folder_id,omitempty"`
	Title    string `json:"title"`
	SortKey  string `json:"sort_key,omitempty"`
}

type PagePlace struct {
	ParentID string    `json:"parent_id,omitempty"`
	FolderID string    `json:"folder_id,omitempty"`
	SortKey  string    `json:"sort_key,omitempty"`
	Card     *Position `json:"card,omitempty"`
}

func (p PagePlace) Same(q PagePlace) bool {
	return p.ParentID == q.ParentID && p.FolderID == q.FolderID
}

type NewFolder struct {
	ID      string `json:"id"`
	SpaceID string `json:"space_id,omitempty"`
	Name    string `json:"name"`
	SortKey string `json:"sort_key,omitempty"`
}

type Op struct {
	Op        string
	Position  *Position
	Nodes     []*Node
	ID        string
	Node      *Node
	PrevNode  *Node
	Removed   []Removed
	From      *Position
	IDs       []string
	Page      *NewPage
	Content   *Content
	PageID    string
	Title     string
	Place     *PagePlace
	PrevPlace *PagePlace
	PrevTitle string
	Tags      []string
	PrevTags  []string
	Folder    *NewFolder
	FolderID  string
}

func (o Op) IsPageOp() bool {
	switch o.Op {
	case "create_page", "delete_page", "move_page", "rename_page", "set_page_tags", "create_folder", "delete_folder":
		return true
	}
	return false
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
	Title    *string         `json:"title,omitempty"`
	Place    *PagePlace      `json:"place,omitempty"`
	Tags     []string        `json:"tags,omitempty"`
	Folder   *NewFolder      `json:"folder,omitempty"`
	FolderID string          `json:"folder_id,omitempty"`
}

func (o Op) MarshalJSON() ([]byte, error) {
	j := opJSON{Op: o.Op, Position: o.Position, Nodes: o.Nodes, ID: o.ID, Node: o.Node,
		From: o.From, IDs: o.IDs, Page: o.Page, Content: o.Content, PageID: o.PageID,
		Place: o.Place, Tags: o.Tags, Folder: o.Folder, FolderID: o.FolderID}
	switch o.Op {
	case "move_page", "rename_page", "set_page_tags":
		j.Title = &o.Title
	}
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
	case "move_page":
		if o.PrevPlace != nil {
			j.Previous, err = json.Marshal(o.PrevPlace)
		}
	case "rename_page":
		j.Previous, err = json.Marshal(o.PrevTitle)
	case "set_page_tags":
		prev := o.PrevTags
		if prev == nil {
			prev = []string{}
		}
		j.Previous, err = json.Marshal(prev)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(j)
}

func (o *Op) UnmarshalJSON(raw []byte) error {
	var j opJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return err
	}
	*o = Op{Op: j.Op, Position: j.Position, Nodes: j.Nodes, ID: j.ID, Node: j.Node,
		From: j.From, IDs: j.IDs, Page: j.Page, Content: j.Content, PageID: j.PageID,
		Place: j.Place, Tags: j.Tags, Folder: j.Folder, FolderID: j.FolderID}
	if j.Title != nil {
		o.Title = *j.Title
	}
	if len(j.Previous) > 0 && string(j.Previous) != "null" {
		var err error
		switch j.Op {
		case "replace":
			o.PrevNode = &Node{}
			err = json.Unmarshal(j.Previous, o.PrevNode)
		case "delete":
			err = json.Unmarshal(j.Previous, &o.Removed)
		case "move_page":
			o.PrevPlace = &PagePlace{}
			err = json.Unmarshal(j.Previous, o.PrevPlace)
		case "rename_page":
			err = json.Unmarshal(j.Previous, &o.PrevTitle)
		case "set_page_tags":
			err = json.Unmarshal(j.Previous, &o.PrevTags)
		}
		if err != nil {
			return err
		}
	}
	o.Nodes = withoutNil(o.Nodes)
	o.Removed = slices.DeleteFunc(o.Removed, func(r Removed) bool { return r.Node == nil })
	for _, r := range o.Removed {
		r.Node.Children = withoutNil(r.Node.Children)
	}
	for _, n := range []*Node{o.Node, o.PrevNode} {
		if n != nil {
			n.Children = withoutNil(n.Children)
		}
	}
	return nil
}

func (c *Content) Apply(o Op) error {
	switch o.Op {
	case "insert":
		p := Position{At: "end"}
		if o.Position != nil {
			p = *o.Position
		}
		have := map[string]bool{}
		c.Walk(func(n, _ *Node) { have[n.ID] = true })
		var nodes []*Node
		for _, n := range o.Nodes {
			if k := withoutPresent(n.Clone(), have); k != nil {
				nodes = append(nodes, k)
			}
		}
		if len(nodes) == 0 {
			return nil
		}
		if a := p.Anchor(); a != "" {
			switch at := c.Locate(a); {
			case at == nil:
				p = Position{At: "end"}
			case p.Parent != "" && at.Node.Type == "table" && !allCells(nodes):
				p = Position{After: a}
			case p.Parent != "" && !NestableTypes[at.Node.Type] && !inTable(at.Node):
				p = Position{After: a}
			case p.Parent == "" && at.Parent != nil && at.Parent.Type == "table" && !allCells(nodes):
				p = Position{After: at.Parent.ID}
			}
		}
		return c.Insert(p, nodes...)
	case "replace":
		if o.Node == nil {
			return fmt.Errorf("replace without a node")
		}
		cur := c.Addressable(o.ID)
		if cur == nil {
			return fmt.Errorf("no block %s", o.ID)
		}
		n := o.Node.Clone()
		DropCells(n)
		if keptChildren(o) && canHoldChildren(n, cur) {
			n.Children = nil
			if len(cur.Children) > 0 {
				n.Children = cur.Children
			}
		}
		return c.Replace(o.ID, n)
	case "move":
		if o.Position == nil {
			return fmt.Errorf("move without a position")
		}
		return c.Move(o.ID, *o.Position)
	case "delete":
		for _, id := range o.IDs {
			if c.Addressable(id) != nil {
				c.Remove(id)
			}
		}
	}
	return nil
}

func (c *Content) Addressable(id string) *Node {
	var find func([]*Node) *Node
	find = func(nodes []*Node) *Node {
		for _, n := range nodes {
			if n.ID == id {
				return n
			}
			if n.Type != "table" {
				if hit := find(n.Children); hit != nil {
					return hit
				}
			}
		}
		return nil
	}
	return find(c.Children)
}

func allCells(nodes []*Node) bool {
	return !slices.ContainsFunc(nodes, func(n *Node) bool { return n.Type != "table/cell" })
}

func withoutPresent(n *Node, have map[string]bool) *Node {
	if n.ID != "" && have[n.ID] {
		return nil
	}
	var kids []*Node
	for _, k := range n.Children {
		if k := withoutPresent(k, have); k != nil {
			kids = append(kids, k)
		}
	}
	n.Children = kids
	return n
}

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
	case "move_page":
		return []Op{{Op: "move_page", PageID: o.PageID, Title: o.Title, Place: o.PrevPlace, PrevPlace: o.Place}}
	case "rename_page":
		return []Op{{Op: "rename_page", PageID: o.PageID, Title: o.PrevTitle, PrevTitle: o.Title}}
	case "set_page_tags":
		return []Op{{Op: "set_page_tags", PageID: o.PageID, Title: o.Title, Tags: o.PrevTags, PrevTags: o.Tags}}
	case "create_folder":
		if o.Folder != nil {
			return []Op{{Op: "delete_folder", FolderID: o.Folder.ID}}
		}
	}
	return nil
}

func InvertRun(ops []Op) []Op {
	var out []Op
	for i := len(ops) - 1; i >= 0; i-- {
		out = append(out, ops[i].Invert()...)
	}
	return out
}

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

func (c *Content) ReplaceOp(id string, n *Node) Op {
	n.ID = id
	return Op{Op: "replace", ID: id, Node: n, PrevNode: c.Find(id).Clone()}
}

func (c *Content) MoveOp(id string, p Position) Op {
	from := c.PositionOf(id)
	return Op{Op: "move", ID: id, Position: &p, From: &from}
}

func InsertOp(p Position, nodes ...*Node) Op {
	return Op{Op: "insert", Position: &p, Nodes: nodes}
}

func keptChildren(o Op) bool {
	if o.PrevNode == nil || o.PrevNode.Type == "table" || len(o.Node.Children) != len(o.PrevNode.Children) {
		return false
	}
	for i, k := range o.Node.Children {
		if k.ID != o.PrevNode.Children[i].ID {
			return false
		}
	}
	return true
}

func canHoldChildren(n, cur *Node) bool {
	return cur.Type != "table" && (NestableTypes[n.Type] || n.Type == cur.Type)
}
