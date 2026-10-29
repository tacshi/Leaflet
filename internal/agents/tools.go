package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// The page tools every agent surface uses (docs/protocol.md §2), mirroring
// supabase/functions/_shared/tools.ts.

var positionSchema = map[string]any{
	"type":        "object",
	"description": "Where to put the blocks. Exactly one of: after, before, parent (append as last child of a list block), at (\"start\" or \"end\").",
	"properties": map[string]any{
		"after":  map[string]any{"type": "string", "description": "Block ID to insert after."},
		"before": map[string]any{"type": "string", "description": "Block ID to insert before."},
		"parent": map[string]any{"type": "string", "description": "List block ID to nest under."},
		"at":     map[string]any{"type": "string", "enum": []string{"start", "end"}},
	},
}

var blockSchema = map[string]any{
	"type":        "object",
	"description": "A block to write.",
	"properties": map[string]any{
		"type":      map[string]any{"type": "string", "enum": doc.AgentTypes},
		"text":      map[string]any{"type": "string", "description": "Inline markdown: **bold**, *italic*, `code`, ~~strike~~, [text](url). No block syntax such as # or -. For a code block, the code itself, verbatim, with its line breaks and no fence."},
		"language":  map[string]any{"type": "string", "description": "A code block's language, such as go, python or typescript."},
		"tone":      map[string]any{"type": "string", "enum": []string{"info", "tip", "warning", "danger"}, "description": "A callout's tone."},
		"collapsed": map[string]any{"type": "boolean", "description": "Whether a toggle_list starts folded, its children hidden."},
		"formula":   map[string]any{"type": "string", "description": "A math_equation's formula in TeX, without $ delimiters, shown in display style."},
		"source":    map[string]any{"type": "string", "description": "A diagram's Mermaid source: flowchart, sequenceDiagram, classDiagram, stateDiagram, gantt, pie, mindmap and the rest."},
		"link": map[string]any{"type": "object", "description": "A link_preview: a card for a web page, with its title and picture.",
			"properties": map[string]any{"url": map[string]any{"type": "string"}}},
		"level":   map[string]any{"type": "integer", "description": "Heading level 1-3."},
		"checked": map[string]any{"type": "boolean"},
		"due":     map[string]any{"type": "string", "description": "For todo_list: the day it is due, YYYY-MM-DD, or with a time of day, YYYY-MM-DDTHH:MM, in the user's own time"},
		"children": map[string]any{"type": "array", "description": "Nested blocks: list items under a list item, or any blocks inside a toggle_list.",
			"items": map[string]any{"type": "object"}},
		"rows": map[string]any{"type": "array", "description": "Table rows; the first row is the header.",
			"items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		"chart": map[string]any{"type": "object", "properties": map[string]any{
			"chart_type": map[string]any{"type": "string", "enum": []string{"bar", "line", "pie"}},
			"title":      map[string]any{"type": "string"},
			"labels":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"series": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"name":   map[string]any{"type": "string"},
				"values": map[string]any{"type": "array", "items": map[string]any{"type": "number"}},
			}}},
			"caption": map[string]any{"type": "string"},
		}},
		"embed": map[string]any{"type": "object", "description": "A self-contained interactive HTML widget. It runs sandboxed with no network or storage access.",
			"properties": map[string]any{"title": map[string]any{"type": "string"}, "html": map[string]any{"type": "string"}, "height": map[string]any{"type": "integer"}}},
		"image": map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}, "alt": map[string]any{"type": "string"}}},
		"video": map[string]any{"type": "object", "description": "A YouTube or Vimeo video, or a video file, that plays in the page.",
			"properties": map[string]any{"url": map[string]any{"type": "string", "description": "The video's YouTube or Vimeo link, or a link to the video file (.mp4, .webm, .mov)."}, "title": map[string]any{"type": "string"}}},
	},
	"required": []string{"type"},
}

func tool(name, desc string, props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "function", "function": map[string]any{
		"name": name, "description": desc,
		"parameters": map[string]any{"type": "object", "properties": props, "required": required},
	}}
}

// ToolDefinitions are the tools offered to a model: only the reading ones
// on a page the user may not edit.
func ToolDefinitions(crossPage, readOnly bool) []map[string]any {
	out := []map[string]any{
		tool("read_page", "Read the whole page: every block with its ID, subpage titles, and comment threads.", map[string]any{}),
	}
	if !readOnly {
		out = append(out, editTools()...)
	}
	if crossPage {
		out = append(out,
			tool("list_pages", "List the user's other pages (titles and IDs).", map[string]any{}),
			tool("read_other_page", "Read another of the user's pages. Read-only.", map[string]any{"page_id": map[string]any{"type": "string"}}, "page_id"))
	}
	return out
}

func editTools() []map[string]any {
	return []map[string]any{
		tool("insert_blocks", "Insert new blocks at a position.", map[string]any{"position": positionSchema, "blocks": map[string]any{"type": "array", "items": blockSchema}}, "position", "blocks"),
		tool("replace_block", "Replace one block with a new one, keeping its ID (and its nested items when it stays a list).", map[string]any{"id": map[string]any{"type": "string"}, "block": blockSchema}, "id", "block"),
		tool("move_block", "Move a block (with its nested items) to a new position.", map[string]any{"id": map[string]any{"type": "string"}, "position": positionSchema}, "id", "position"),
		tool("delete_blocks", "Delete blocks by ID, with their nested items.", map[string]any{"ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "ids"),
		tool("create_subpage", "Create a subpage of this page, optionally with content.", map[string]any{"title": map[string]any{"type": "string"}, "blocks": map[string]any{"type": "array", "items": blockSchema}}, "title"),
	}
}

// ToolContext is what the tools need beyond the page's blocks.
type ToolContext struct {
	Content   *doc.Content
	PageID    string
	PageTitle string
	AgentName string
	// ReadOnly is a page the user may view but not edit: the tools that
	// change it refuse.
	ReadOnly bool
	Subpages func() []map[string]any
	Comments func() []map[string]any
	// CreateSubpage makes the subpage and returns its ID.
	CreateSubpage func(title string, nodes []*doc.Node) (string, error)
	// ListPages and ReadOtherPage are nil when cross-page reading is off.
	ListPages     func() []map[string]any
	ReadOtherPage func(id string) map[string]any
	// Transcript is a recording's transcript as text, "" when there is none.
	Transcript func(recordingID string) string
}

// PageTools is the ToolContext of a page in the store: its subpages,
// comments and transcripts, subpages made in the store, and, with
// crossPage, reading the user's other pages.
func PageTools(st *store.Store, pageID, title, agent string, content *doc.Content, crossPage bool) ToolContext {
	tc := ToolContext{
		Content: content, PageID: pageID, PageTitle: title, AgentName: agent,
		Transcript: func(id string) string {
			rec, err := st.Recording(id)
			if err != nil {
				return ""
			}
			return TranscriptText(rec.Transcript)
		},
		Subpages: func() []map[string]any {
			kids, _ := st.Children(pageID, "")
			out := []map[string]any{}
			for _, k := range kids {
				out = append(out, map[string]any{"id": k.ID, "title": k.DisplayTitle()})
			}
			return out
		},
		Comments: func() []map[string]any {
			threads, _ := st.Threads(pageID)
			out := []map[string]any{}
			for _, t := range threads {
				var thread []map[string]any
				for _, c := range t.All() {
					body := c.Body
					if !c.DeletedAt.IsZero() {
						body = "(deleted)"
					}
					// A guest names themself: say so, as the agent function does.
					author := c.AuthorName
					if c.AuthorKind == "guest" {
						author += " (guest, through the page's public link)"
					}
					thread = append(thread, map[string]any{"author": author, "body": body})
				}
				out = append(out, map[string]any{"block_id": t.Root.BlockID, "resolved": t.Root.Resolved, "thread": thread})
			}
			return out
		},
		CreateSubpage: func(title string, nodes []*doc.Node) (string, error) {
			var c *doc.Content
			if len(nodes) > 0 {
				c = doc.FromBlocks(nodes)
			}
			p, err := st.CreatePage(store.NewPageOptions{ParentID: pageID, Title: title, Content: c})
			return p.ID, err
		},
	}
	if crossPage {
		tc.ListPages = func() []map[string]any {
			pages, _ := st.Pages()
			out := []map[string]any{}
			for _, p := range pages {
				if p.ID != pageID {
					out = append(out, map[string]any{"id": p.ID, "title": p.DisplayTitle(), "parent_id": p.ParentID})
				}
			}
			return out
		}
		tc.ReadOtherPage = func(id string) map[string]any {
			p, err := st.Page(id)
			c := st.Content(id)
			if err != nil || c == nil || !p.DeletedAt.IsZero() {
				return nil
			}
			return map[string]any{"page": map[string]any{"id": id, "title": p.DisplayTitle()}, "blocks": tc.Views(c)}
		}
	}
	return tc
}

// Views are the blocks as agents read them, with the transcripts of
// recordings, which are not in the page itself.
func (tc ToolContext) Views(c *doc.Content) []map[string]any {
	views := doc.BlockViews(c)
	if tc.Transcript == nil {
		return views
	}
	for _, v := range views {
		if rec, ok := v["recording"].(map[string]any); ok {
			id, _ := rec["recording_id"].(string)
			if t := tc.Transcript(id); t != "" {
				rec["transcript"] = t
			}
		}
	}
	return views
}

// Edit is a short description of a change, with the blocks it touched.
type Edit struct {
	Summary  string   `json:"summary"`
	BlockIDs []string `json:"block_ids"`
}

// Executor validates and applies tool calls, recording each as an op.
type Executor struct {
	Ctx   ToolContext
	OnOp  func(op doc.Op, summary string)
	Ops   []doc.Op
	Edits []Edit
}

// CallRaw runs a tool with JSON arguments. Invalid calls return {error}
// and change nothing.
func (x *Executor) CallRaw(name, raw string) map[string]any {
	args := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return map[string]any{"error": "Arguments were not valid JSON."}
		}
		if m, ok := v.(map[string]any); ok {
			args = m
		}
	}
	return x.Call(name, args)
}

// Call runs a tool.
func (x *Executor) Call(name string, args map[string]any) map[string]any {
	out, err := x.dispatch(name, args)
	if err != nil {
		var te *doc.ToolError
		if errors.As(err, &te) {
			return map[string]any{"error": te.Message}
		}
		return map[string]any{"error": err.Error()}
	}
	return out
}

func terr(format string, args ...any) error {
	return &doc.ToolError{Message: fmt.Sprintf(format, args...)}
}

func (x *Executor) dispatch(name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "insert_blocks", "replace_block", "move_block", "delete_blocks", "create_subpage":
		if x.Ctx.ReadOnly {
			return nil, terr("You can read this page but not edit it: the user has view access only.")
		}
	}
	switch name {
	case "read_page":
		return x.readPage(), nil
	case "insert_blocks":
		return x.insert(args)
	case "replace_block":
		return x.replace(args)
	case "move_block":
		return x.move(args)
	case "delete_blocks":
		return x.delete(args)
	case "create_subpage":
		return x.createSubpage(args)
	case "list_pages":
		if x.Ctx.ListPages == nil {
			return nil, terr("Reading other pages is off.")
		}
		return map[string]any{"pages": x.Ctx.ListPages()}, nil
	case "read_other_page":
		if x.Ctx.ReadOtherPage == nil {
			return nil, terr("Reading other pages is off.")
		}
		id, err := str(args, "page_id")
		if err != nil {
			return nil, err
		}
		p := x.Ctx.ReadOtherPage(id)
		if p == nil {
			return nil, terr("No page %s.", id)
		}
		return p, nil
	}
	return nil, terr("Unknown tool %q.", name)
}

func (x *Executor) readPage() map[string]any {
	out := map[string]any{
		"page":     map[string]any{"id": x.Ctx.PageID, "title": x.Ctx.PageTitle},
		"blocks":   x.Ctx.Views(x.Ctx.Content),
		"subpages": []map[string]any{},
		"comments": []map[string]any{},
	}
	if x.Ctx.Subpages != nil {
		out["subpages"] = x.Ctx.Subpages()
	}
	if x.Ctx.Comments != nil {
		out["comments"] = x.Ctx.Comments()
	}
	return out
}

func (x *Executor) has(id string) bool { return x.Ctx.Content.Find(id) != nil }

func (x *Executor) position(raw any) (doc.Position, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return doc.Position{}, terr("position must be an object.")
	}
	set := map[string]any{}
	for k, v := range m {
		if v != nil {
			set[k] = v
		}
	}
	if len(set) != 1 {
		return doc.Position{}, terr("position needs exactly one of after, before, parent, at.")
	}
	for k, v := range set {
		s, _ := v.(string)
		switch k {
		case "after", "before":
			if !x.has(s) {
				return doc.Position{}, terr("No block with ID %q.", fmt.Sprint(v))
			}
			if k == "after" {
				return doc.Position{After: s}, nil
			}
			return doc.Position{Before: s}, nil
		case "parent":
			n := x.Ctx.Content.Find(s)
			if n == nil {
				return doc.Position{}, terr("No block with ID %q.", fmt.Sprint(v))
			}
			if !doc.NestableTypes[n.Type] {
				return doc.Position{}, terr("Only list blocks can have nested items.")
			}
			return doc.Position{Parent: s}, nil
		case "at":
			if s != "start" && s != "end" {
				return doc.Position{}, terr("position.at must be \"start\" or \"end\".")
			}
			return doc.Position{At: s}, nil
		default:
			return doc.Position{}, terr("Unknown position key %q.", k)
		}
	}
	return doc.Position{}, nil
}

func (x *Executor) specs(raw any) ([]*doc.Node, error) { return Specs(raw, x.Ctx.AgentName) }

// Specs turns a tool call's list of blocks into nodes, written by author.
func Specs(raw any, author string) ([]*doc.Node, error) {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, terr("blocks must be a non-empty list.")
	}
	var out []*doc.Node
	for _, s := range list {
		m, ok := s.(map[string]any)
		if !ok {
			return nil, terr("Each block must be an object.")
		}
		n, err := doc.SpecToNode(m, "", author)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func (x *Executor) emit(op doc.Op, summary string, ids []string) error {
	if err := x.Ctx.Content.Apply(op); err != nil {
		return err
	}
	x.Ops = append(x.Ops, op)
	x.Edits = append(x.Edits, Edit{Summary: summary, BlockIDs: ids})
	if x.OnOp != nil {
		x.OnOp(op, summary)
	}
	return nil
}

func (x *Executor) insert(args map[string]any) (map[string]any, error) {
	p, err := x.position(args["position"])
	if err != nil {
		return nil, err
	}
	nodes, err := x.specs(args["blocks"])
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	op := doc.InsertOp(p, cloneAll(nodes)...)
	if err := x.emit(op, summaryFor("Inserted", nodes), ids); err != nil {
		return nil, err
	}
	return map[string]any{"inserted_ids": ids}, nil
}

func cloneAll(nodes []*doc.Node) []*doc.Node {
	out := make([]*doc.Node, len(nodes))
	for i, n := range nodes {
		out[i] = n.Clone()
	}
	return out
}

func (x *Executor) replace(args map[string]any) (map[string]any, error) {
	id, err := str(args, "id")
	if err != nil {
		return nil, err
	}
	prev := x.Ctx.Content.Find(id)
	if prev == nil {
		return nil, terr("No block with ID %q.", id)
	}
	spec, ok := args["block"].(map[string]any)
	if !ok {
		return nil, terr("block must be an object.")
	}
	n, err := doc.SpecToNode(spec, id, x.Ctx.AgentName)
	if err != nil {
		return nil, err
	}
	// A table's children are its cells, which no other type keeps.
	if len(prev.Children) > 0 && n.Children == nil && prev.Type != "table" {
		if doc.NestableTypes[n.Type] {
			for _, k := range prev.Children {
				n.Children = append(n.Children, k.Clone())
			}
		} else {
			return nil, terr("Block %s has nested items. Move or delete them first, or keep a list type.", id)
		}
	}
	op := doc.Op{Op: "replace", ID: id, Node: n, PrevNode: prev.Clone()}
	if err := x.emit(op, summaryFor("Rewrote", []*doc.Node{n}), []string{id}); err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

func (x *Executor) move(args map[string]any) (map[string]any, error) {
	id, err := str(args, "id")
	if err != nil {
		return nil, err
	}
	if !x.has(id) {
		return nil, terr("No block with ID %q.", id)
	}
	p, err := x.position(args["position"])
	if err != nil {
		return nil, err
	}
	if a := p.Anchor(); a != "" && x.Ctx.Content.IsSelfOrAncestor(id, a) {
		return nil, terr("A block cannot move next to or inside itself.")
	}
	if err := x.emit(x.Ctx.Content.MoveOp(id, p), "Moved a block", []string{id}); err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

func (x *Executor) delete(args map[string]any) (map[string]any, error) {
	raw, ok := args["ids"].([]any)
	if !ok || len(raw) == 0 {
		return nil, terr("ids must be a non-empty list.")
	}
	var ids []string
	for _, v := range raw {
		s, _ := v.(string)
		if s == "" || !x.has(s) {
			return nil, terr("No block with ID %q.", fmt.Sprint(v))
		}
		dup := false
		for _, e := range ids {
			dup = dup || e == s
		}
		if !dup {
			ids = append(ids, s)
		}
	}
	// Nested items go with their parent.
	var roots []string
	for _, id := range ids {
		nested := false
		for _, other := range ids {
			if other != id && x.Ctx.Content.IsSelfOrAncestor(other, id) {
				nested = true
			}
		}
		if !nested {
			roots = append(roots, id)
		}
	}
	summary := "Deleted a block"
	if len(roots) > 1 {
		summary = fmt.Sprintf("Deleted %d blocks", len(roots))
	}
	if err := x.emit(x.Ctx.Content.DeleteOp(roots), summary, roots); err != nil {
		return nil, err
	}
	return map[string]any{"deleted_ids": roots}, nil
}

func (x *Executor) createSubpage(args map[string]any) (map[string]any, error) {
	title, err := str(args, "title")
	if err != nil {
		return nil, err
	}
	var nodes []*doc.Node
	if args["blocks"] != nil {
		if nodes, err = x.specs(args["blocks"]); err != nil {
			return nil, err
		}
	}
	if x.Ctx.CreateSubpage == nil {
		return nil, terr("Subpages cannot be made here.")
	}
	id, err := x.Ctx.CreateSubpage(title, nodes)
	if err != nil {
		return nil, err
	}
	op := doc.Op{Op: "create_page", Page: &doc.NewPage{ID: id, ParentID: x.Ctx.PageID, Title: title}}
	summary := "Created subpage “" + title + "”"
	x.Ops = append(x.Ops, op)
	x.Edits = append(x.Edits, Edit{Summary: summary, BlockIDs: []string{}})
	if x.OnOp != nil {
		x.OnOp(op, summary)
	}
	return map[string]any{"page_id": id}, nil
}

func str(args map[string]any, key string) (string, error) {
	s, _ := args[key].(string)
	if strings.TrimSpace(s) == "" {
		return "", terr("%s is required.", key)
	}
	return s, nil
}

func summaryFor(verb string, nodes []*doc.Node) string {
	if len(nodes) > 1 {
		return fmt.Sprintf("%s %d blocks", verb, len(nodes))
	}
	n := nodes[0]
	kind := map[string]string{"chart": "chart", "table": "table", "embed": "interactive embed", "heading": "heading", "image": "image", "video": "video", "code": "code block",
		"callout": "callout", "toggle_list": "toggle", "link_preview": "link card", "math_equation": "formula", "diagram": "diagram"}[n.Type]
	title := ""
	if n.Type == "chart" || n.Type == "embed" || n.Type == "video" {
		title = n.Str("title")
	}
	what := "a block"
	if kind != "" {
		what = "a " + kind
	}
	if title != "" {
		what += " · " + title
	}
	return verb + " " + what
}

// OpBlockIDs are the blocks an op touches, for tinting them.
func OpBlockIDs(op doc.Op) []string {
	switch op.Op {
	case "insert":
		var ids []string
		for _, n := range op.Nodes {
			ids = append(ids, n.ID)
		}
		return ids
	case "replace", "move":
		return []string{op.ID}
	}
	return nil
}
