package agents

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
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

// writeSchema is where write_markdown writes: a position, or an empty
// block to write in place of.
var writeSchema = map[string]any{
	"type":        "object",
	"description": "Where to write. Exactly one of: replace (an empty block to write in place of, as the empty line the user asked from), after, before, parent (append as last children of a list block), at (\"start\" or \"end\").",
	"properties": map[string]any{
		"replace": map[string]any{"type": "string", "description": "ID of an empty block to write in place of."},
		"after":   map[string]any{"type": "string", "description": "Block ID to write after."},
		"before":  map[string]any{"type": "string", "description": "Block ID to write before."},
		"parent":  map[string]any{"type": "string", "description": "List block ID to nest under."},
		"at":      map[string]any{"type": "string", "enum": []string{"start", "end"}},
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
			"properties": map[string]any{"url": map[string]any{"type": "string"}}, "required": []string{"url"}},
		"level":   map[string]any{"type": "integer", "minimum": 1, "maximum": 6, "description": "Heading level, 1 to 6."},
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
			}, "required": []string{"name", "values"}}},
			"caption": map[string]any{"type": "string"},
		}, "required": []string{"chart_type", "labels", "series"}},
		"embed": map[string]any{"type": "object", "description": "A self-contained interactive HTML widget. It runs sandboxed with no network or storage access.",
			"properties": map[string]any{"title": map[string]any{"type": "string"}, "html": map[string]any{"type": "string"}, "height": map[string]any{"type": "integer", "minimum": 120, "maximum": 1200}},
			"required":   []string{"html"}},
		"image": map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}, "alt": map[string]any{"type": "string"}}, "required": []string{"url"}},
		"video": map[string]any{"type": "object", "description": "A YouTube or Vimeo video, or a video file, that plays in the page.",
			"properties": map[string]any{"url": map[string]any{"type": "string", "description": "The video's YouTube or Vimeo link, or a link to the video file (.mp4, .webm, .mov)."}, "title": map[string]any{"type": "string"}},
			"required":   []string{"url"}},
	},
	"required": []string{"type"},
}

// ordered are a schema's properties in the order a model should write
// them, as a map's are not.
type ordered []struct {
	key   string
	value any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	b := []byte{'{'}
	for i, p := range o {
		if i > 0 {
			b = append(b, ',')
		}
		k, _ := json.Marshal(p.key)
		v, err := json.Marshal(p.value)
		if err != nil {
			return nil, err
		}
		b = append(append(append(b, k...), ':'), v...)
	}
	return append(b, '}'), nil
}

// Properties are a tool's parameters' properties, as a map.
func Properties(params map[string]any) map[string]any {
	switch p := params["properties"].(type) {
	case ordered:
		m := map[string]any{}
		for _, kv := range p {
			m[kv.key] = kv.value
		}
		return m
	case map[string]any:
		return maps.Clone(p)
	}
	return map[string]any{}
}

func tool(name, desc string, props any, required ...string) map[string]any {
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
		tool("read_page", "Read the whole page: every block with its ID, subpage titles, and comment threads. An embed's html longer than 30,000 characters is left out, its html_length given instead: to change such an embed, write its html whole.", map[string]any{}),
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
		// Writing streams into the page as the model writes it.
		tool("write_markdown", "Write into the page in Markdown: the way to write prose, as a story, a draft, notes or a summary. Each line is a block: a paragraph, # to ### headings, - or 1. list items (indent two spaces to nest), - [ ] to-dos, > quotes, ``` fenced code, | tables, --- dividers; blank lines only separate them. It goes into the page as you write it.",
			ordered{{"position", writeSchema}, {"markdown", map[string]any{"type": "string"}}}, "position", "markdown"),
		// The position first, so the blocks can go in as they stream.
		tool("insert_blocks", "Insert new blocks at a position.", ordered{{"position", positionSchema}, {"blocks", map[string]any{"type": "array", "minItems": 1, "items": blockSchema}}}, "position", "blocks"),
		tool("replace_block", "Replace one block with a new one, keeping its ID (and its nested items when it stays a list).", ordered{{"id", map[string]any{"type": "string"}}, {"block", blockSchema}}, "id", "block"),
		tool("move_block", "Move a block (with its nested items) to a new position.", map[string]any{"id": map[string]any{"type": "string"}, "position": positionSchema}, "id", "position"),
		tool("delete_blocks", "Delete blocks by ID, with their nested items.", map[string]any{"ids": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}}}, "ids"),
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
	// Request is what the user asked the run, whose URLs a block may load
	// as this page's may (checkURLs).
	Request string
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
		// The other pages are those of this page's space, as the agent
		// function has them: a page someone else shared is theirs, and
		// what they wrote in it, its URLs among them, is not the user's.
		here, _ := st.Page(pageID)
		tc.ListPages = func() []map[string]any {
			pages, _ := st.Pages()
			out := []map[string]any{}
			for _, p := range pages {
				if p.ID != pageID && p.SpaceID == here.SpaceID {
					out = append(out, map[string]any{"id": p.ID, "title": p.DisplayTitle(), "parent_id": p.ParentID})
				}
			}
			return out
		}
		// Shaped as read_page's, as protocol §2 has it: its subpages, and
		// no comments, which are of this page's alone.
		tc.ReadOtherPage = func(id string) map[string]any {
			p, err := st.Page(id)
			c := st.Content(id)
			if err != nil || c == nil || !p.DeletedAt.IsZero() || p.SpaceID != here.SpaceID {
				return nil
			}
			subpages := []map[string]any{}
			if kids, err := st.Children(id, ""); err == nil {
				for _, k := range kids {
					subpages = append(subpages, map[string]any{"id": k.ID, "title": k.DisplayTitle()})
				}
			}
			return map[string]any{"page": map[string]any{"id": id, "title": p.DisplayTitle()}, "blocks": tc.Views(c),
				"subpages": subpages, "comments": []map[string]any{}}
		}
	}
	return tc
}

// maxViewHTML is the longest embed source agents read, in characters. A
// longer one is left out, its length given instead: a few would be more
// than a model reads at once, or an MCP client takes from a tool, and
// the page could not be read at all.
const maxViewHTML = 30_000

// Views are the blocks as agents read them, with the transcripts of
// recordings, which are not in the page itself, and without an embed's
// source longer than maxViewHTML.
func (tc ToolContext) Views(c *doc.Content) []map[string]any {
	views := doc.BlockViews(c)
	for _, v := range views {
		if e, ok := v["embed"].(map[string]any); ok {
			if html, _ := e["html"].(string); utf8.RuneCountInString(html) > maxViewHTML {
				delete(e, "html")
				e["html_length"] = utf8.RuneCountInString(html)
			}
		}
		if rec, ok := v["recording"].(map[string]any); ok && tc.Transcript != nil {
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
	Ctx  ToolContext
	OnOp func(op doc.Op, summary string)
	// OnRevise changes the op at at, as a draft grows: delta is what it
	// changes in the page. A nil op takes it away, the last.
	OnRevise func(at int, op *doc.Op, summary string, delta []doc.Op)
	Ops      []doc.Op
	Edits    []Edit
	// draft is an insert_blocks call going in as it streams.
	draft *draft
	// allowed are the URLs a block may load once the run reads beyond
	// this page (checkURLs): those on it when the run began, and in the
	// user's request; nil before the run's first call.
	allowed map[string]bool
	// armed is a run that has read beyond this page.
	armed bool
	// written counts the blocks the run's calls have written, toward
	// maxRunBlocks.
	written int
}

// maxRunBlocks bounds the blocks one run writes: a model that goes on and
// on, as one repeating itself, stops there.
const maxRunBlocks = 5000

// room refuses blocks that would take the run past maxRunBlocks.
func (x *Executor) room(nodes []*doc.Node) error {
	if x.written+countNodes(nodes) > maxRunBlocks {
		return terr("This run has written %d blocks, and one run writes at most %d. Stop here, and say in your reply what is left to do.", x.written, maxRunBlocks)
	}
	return nil
}

// countNodes counts blocks and the blocks inside them.
func countNodes(nodes []*doc.Node) int {
	n := 0
	for _, k := range nodes {
		walk(k, func(*doc.Node) { n++ })
	}
	return n
}

// urlRe finds the URLs in text the agent read, each to the first space
// of unicode.IsSpace's, as the server's URL_RE.
var urlRe = regexp.MustCompile(`https?://[^\t\n\v\f\r \x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}<>"'()\[\]]+`)

// see adds the URLs in v to set: strings that are one, and those in text,
// in v and in every list and map in it, a table's rows of cells among
// them, as the server's urlsIn.
func see(set map[string]bool, v any) {
	if s, ok := v.(string); ok {
		if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
			set[s] = true
		}
		for _, u := range urlRe.FindAllString(s, -1) {
			set[u] = true
		}
		return
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := range rv.Len() {
			see(set, rv.Index(i).Interface())
		}
	case reflect.Map:
		for it := rv.MapRange(); it.Next(); {
			see(set, it.Value().Interface())
		}
	}
}

// The URL guard: the app loads an image, a video or a link card by itself,
// as it shows them. An agent that has read another of the user's pages
// could write what it read into one, such as https://example.com/x.png?notes=…,
// and the app would send it to that server without anyone clicking. So once a run reads another page, or
// lists them, what it writes may hold only a URL the user gave it: one on
// this page as the run began, or in their request. Not one on a page it
// read, which someone else may have written there for it to use, nor one
// the run made up, which could carry what it read. Not in text either: a
// link a run writes in text would be on the page for the next run, which
// takes the page's URLs as the user's, to load in a block. A page it read
// it names with a link to that page, which loads nothing.
//
// checkURLs refuses blocks that hold a URL the user did not give the run,
// in a block the app loads or in text, and more text that does, as a
// subpage's title, once it has read beyond this page.
func (x *Executor) checkURLs(nodes []*doc.Node, more ...any) error {
	if !x.armed {
		return nil
	}
	x.begin()
	urls := map[string]bool{}
	see(urls, more)
	for _, n := range nodes {
		walk(n, func(k *doc.Node) { see(urls, k.Data) })
	}
	var bad string
	for u := range urls {
		if !x.allowed[u] && (bad == "" || u < bad) {
			bad = u
		}
	}
	if bad != "" {
		return terr("After reading another page, what you write may only hold a URL that was on this page before you began or is in the user's request, which %s is not: a link written here could carry what you read, and a later edit could have the app load it. Name the page you read it on with a link to that page instead.", bad)
	}
	return nil
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
		// Arguments that are no object are refused, as the server
		// refuses them, rather than taken as none.
		m, ok := v.(map[string]any)
		if !ok {
			return map[string]any{"error": "Arguments must be a JSON object."}
		}
		args = m
	}
	return x.Call(name, args)
}

// Call runs a tool.
func (x *Executor) Call(name string, args map[string]any) map[string]any {
	return ToolAnswer(x.dispatch(name, args))
}

// ToolAnswer is a tool's result as the model reads it: out, or else what
// err says was wrong, as {error}.
func ToolAnswer(out map[string]any, err error) map[string]any {
	if err != nil {
		return map[string]any{"error": toolMessage(err)}
	}
	return out
}

func terr(format string, args ...any) error {
	return &doc.ToolError{Message: fmt.Sprintf(format, args...)}
}

// begin keeps the URLs a block may load once the run reads beyond this
// page, before its first call changes the page: those on it, and in the
// user's request. RunLocal calls it before any call, as one streaming in
// changes the page before it is made.
func (x *Executor) begin() {
	if x.allowed == nil && (x.Ctx.ListPages != nil || x.Ctx.ReadOtherPage != nil) {
		x.allowed = map[string]bool{}
		see(x.allowed, doc.BlockViews(x.Ctx.Content))
		see(x.allowed, x.Ctx.Request)
	}
}

func (x *Executor) dispatch(name string, args map[string]any) (map[string]any, error) {
	x.begin()
	// Text on the page, in its comments or in what a tool returns may be
	// written to steer the agent, and only the prompt has it take that as
	// data. The tools that delete or rewrite blocks are not refused for
	// it: a run is asked for by the user, who may edit the page, in their
	// own words (a comment's run by their own comment), and its edits are
	// one undo step over a snapshot of the page kept before them, so what
	// such text has the agent take away comes back with Undo. Reading the
	// user's other pages, which could carry them off, is what is refused
	// where others write (crossPage).
	switch name {
	case "write_markdown", "insert_blocks", "replace_block", "move_block", "delete_blocks", "create_subpage":
		if x.Ctx.ReadOnly {
			return nil, terr("You can read this page but not edit it: the user has view access only.")
		}
	}
	switch name {
	case "read_page":
		return x.readPage(), nil
	case "write_markdown":
		return x.write(args)
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
		// The titles it lists are read too.
		x.armed = true
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
		x.armed = true
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

// block is the block of ID id a call may name, nil when there is none:
// not a table's cell, nor a block in one, which the server's tools cannot
// find either. A table changes whole, with replace_block.
func (x *Executor) block(id string) *doc.Node { return x.Ctx.Content.Addressable(id) }

func (x *Executor) has(id string) bool { return x.block(id) != nil }

// noBlock is a call naming a block that is not there, or not one it may
// name (block), as the server says it.
func noBlock(id any) error {
	return terr("No block with id \"%v\" on this page. Call read_page for current ids.", id)
}

// given are the keys of a position a model gave a value. Models that
// write every key of a schema give the others null or "", and at, which
// takes only start or end, one of those: at counts only alone. Keys a
// position has not are left out, as the server leaves them.
func given(raw any) map[string]any {
	m, _ := raw.(map[string]any)
	set := map[string]any{}
	for k, v := range m {
		switch k {
		case "after", "before", "parent", "at", "replace":
		default:
			continue
		}
		if v != nil && v != "" {
			set[k] = v
		}
	}
	if len(set) > 1 {
		delete(set, "at")
	}
	return set
}

func (x *Executor) position(raw any) (doc.Position, error) {
	// "start" or "end" alone is that end of the page, as the server takes
	// it.
	if s, ok := raw.(string); ok && (s == "start" || s == "end") {
		return doc.Position{At: s}, nil
	}
	if _, ok := raw.(map[string]any); !ok {
		return doc.Position{}, terr("position must be an object.")
	}
	set := given(raw)
	if len(set) != 1 {
		return doc.Position{}, terr("position needs exactly one of after, before, parent, at.")
	}
	for k, v := range set {
		s, _ := v.(string)
		switch k {
		case "after", "before":
			if !x.has(s) {
				return doc.Position{}, noBlock(v)
			}
			if k == "after" {
				return doc.Position{After: s}, nil
			}
			return doc.Position{Before: s}, nil
		case "parent":
			n := x.block(s)
			if n == nil {
				return doc.Position{}, noBlock(v)
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
	return doc.SpecsToNodes(list, author)
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
	if err := x.checkURLs(nodes); err != nil {
		return nil, err
	}
	if err := x.room(nodes); err != nil {
		return nil, err
	}
	var ids []string
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	op := doc.InsertOp(p, cloneAll(nodes)...)
	if err := x.emit(op, summaryFor(locales.AgentEditInserted, nodes), ids); err != nil {
		return nil, err
	}
	x.written += countNodes(nodes)
	return map[string]any{"inserted_ids": ids}, nil
}

// write writes Markdown in the page, as its call does streaming, whole.
func (x *Executor) write(args map[string]any) (map[string]any, error) {
	d, err := x.newDraft(args["position"], true)
	if err != nil {
		return nil, err
	}
	md, _ := args["markdown"].(string)
	nodes := doc.MarkdownBlocks(md)
	if len(nodes) == 0 {
		return nil, terr("markdown is empty.")
	}
	// As many blocks as a call of blocks makes, as on the server.
	if err := doc.CallNodes(nodes); err != nil {
		return nil, err
	}
	if err := x.room(nodes); err != nil {
		return nil, err
	}
	d.verb, x.draft = locales.AgentEditWrote, d
	if err := x.syncDraft(nodes, len(nodes)); err != nil {
		x.DropDraft()
		return nil, err
	}
	x.draft = nil
	x.written += countNodes(nodes)
	return map[string]any{"inserted_ids": d.ids}, nil
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
	prev := x.block(id)
	if prev == nil {
		return nil, noBlock(id)
	}
	spec, ok := args["block"].(map[string]any)
	if !ok {
		return nil, terr("block must be an object.")
	}
	n, err := x.replacement(prev, spec)
	if err != nil {
		return nil, err
	}
	op := doc.Op{Op: "replace", ID: id, Node: n, PrevNode: prev.Clone()}
	if err := x.emit(op, summaryFor(locales.AgentEditRewrote, []*doc.Node{n}), []string{id}); err != nil {
		return nil, err
	}
	return map[string]any{"id": id}, nil
}

// replacement is the block spec gives to replace prev with.
func (x *Executor) replacement(prev *doc.Node, spec map[string]any) (*doc.Node, error) {
	id := prev.ID
	n, err := doc.SpecToNode(spec, id, x.Ctx.AgentName)
	if err != nil {
		return nil, err
	}
	if err := x.checkURLs([]*doc.Node{n}); err != nil {
		return nil, err
	}
	// A block written without children, or with none, keeps the nested
	// items it had, as the server's replace does. It is the spec that says
	// so, not the node: a table's children are its cells, which no other
	// type keeps, so a new table has children of its own and still drops
	// the items.
	if kids, _ := spec["children"].([]any); len(prev.Children) > 0 && len(kids) == 0 && prev.Type != "table" {
		if doc.NestableTypes[n.Type] {
			for _, k := range prev.Children {
				n.Children = append(n.Children, k.Clone())
			}
		} else {
			return nil, terr("Block %s has nested items. Move or delete them first, or keep a list type.", id)
		}
	}
	return n, nil
}

func (x *Executor) move(args map[string]any) (map[string]any, error) {
	id, err := str(args, "id")
	if err != nil {
		return nil, err
	}
	if !x.has(id) {
		return nil, noBlock(id)
	}
	p, err := x.position(args["position"])
	if err != nil {
		return nil, err
	}
	if a := p.Anchor(); a != "" && x.Ctx.Content.IsSelfOrAncestor(id, a) {
		return nil, terr("A block cannot move next to or inside itself.")
	}
	if err := x.emit(x.Ctx.Content.MoveOp(id, p), locales.AgentEditMoved(), []string{id}); err != nil {
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
			return nil, noBlock(v)
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
	if err := x.emit(x.Ctx.Content.DeleteOp(roots), locales.AgentEditDeleted(len(roots)), roots); err != nil {
		return nil, err
	}
	return map[string]any{"deleted_ids": roots}, nil
}

func (x *Executor) createSubpage(args map[string]any) (map[string]any, error) {
	title, err := str(args, "title")
	if err != nil {
		return nil, err
	}
	// Without the spaces around it, as the server keeps it.
	title = strings.TrimSpace(title)
	var nodes []*doc.Node
	// No blocks, or none listed, is an empty subpage, as the server makes.
	if list, ok := args["blocks"].([]any); args["blocks"] != nil && (!ok || len(list) > 0) {
		if nodes, err = x.specs(args["blocks"]); err != nil {
			return nil, err
		}
	}
	// Its title shows on this page, in its card, as the next run reads it.
	if err := x.checkURLs(nodes, title); err != nil {
		return nil, err
	}
	if err := x.room(nodes); err != nil {
		return nil, err
	}
	if x.Ctx.CreateSubpage == nil {
		return nil, terr("Subpages cannot be made here.")
	}
	id, err := x.Ctx.CreateSubpage(title, nodes)
	if err != nil {
		return nil, err
	}
	x.written += countNodes(nodes)
	op := doc.Op{Op: "create_page", Page: &doc.NewPage{ID: id, ParentID: x.Ctx.PageID, Title: title}}
	summary := locales.AgentEditCreatedSubpage(title)
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

// kinds name the blocks an edit summary names by their kind, in the
// app's language.
var kinds = map[string]func() string{
	"chart": locales.AgentEditChart, "table": locales.AgentEditTable, "embed": locales.AgentEditEmbed, "heading": locales.AgentEditHeading,
	"image": locales.AgentEditImage, "video": locales.AgentEditVideo, "code": locales.AgentEditCode, "callout": locales.AgentEditCallout,
	"toggle_list": locales.AgentEditToggle, "link_preview": locales.AgentEditLinkCard, "math_equation": locales.AgentEditFormula, "diagram": locales.AgentEditDiagram,
}

// summaryFor is what an edit that wrote nodes did, as verb says it of
// what it wrote: their number, or the one block's kind and title.
func summaryFor(verb func(what string) string, nodes []*doc.Node) string {
	if len(nodes) > 1 {
		return verb(locales.AgentEditBlocks(len(nodes)))
	}
	n := nodes[0]
	what := locales.AgentEditBlocks(1)
	if kind, ok := kinds[n.Type]; ok {
		what = kind()
	}
	if n.Type == "chart" || n.Type == "embed" || n.Type == "video" {
		if title := n.Str("title"); title != "" {
			what += " · " + title
		}
	}
	return verb(what)
}

// opEdit is a cloud run's op as an edit, said as a local run's would be
// in the app's language: the server words its summaries in English. tool
// is the call that made it, as the server names it: write_markdown's
// blocks, inserted or in place of an empty one, are said as written, as
// a local run says them. An older server names none, and its inserts are
// said as insert_blocks' are.
func opEdit(op doc.Op, tool string) Edit {
	wrote := tool == "write_markdown"
	summary := locales.AgentEditedPage()
	switch {
	case op.Op == "insert" && len(op.Nodes) > 0 && wrote:
		summary = summaryFor(locales.AgentEditWrote, op.Nodes)
	case op.Op == "insert" && len(op.Nodes) > 0:
		summary = summaryFor(locales.AgentEditInserted, op.Nodes)
	case op.Op == "replace" && op.Node != nil && wrote:
		summary = summaryFor(locales.AgentEditWrote, []*doc.Node{op.Node})
	case op.Op == "replace" && op.Node != nil:
		summary = summaryFor(locales.AgentEditRewrote, []*doc.Node{op.Node})
	case op.Op == "move":
		summary = locales.AgentEditMoved()
	case op.Op == "delete" && len(op.IDs) > 0:
		summary = locales.AgentEditDeleted(len(op.IDs))
	case op.Op == "create_page" && op.Page != nil:
		summary = locales.AgentEditCreatedSubpage(op.Page.Title)
	}
	return Edit{Summary: summary, BlockIDs: OpBlockIDs(op)}
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
