package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/web"
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
		"video": map[string]any{"type": "object", "description": "A video on YouTube, Vimeo, Bilibili, Dailymotion or Niconico, or a video file, that plays in the page.",
			"properties": map[string]any{"url": map[string]any{"type": "string", "description": "The video's link on its site, or a link to the video file (.mp4, .webm, .mov)."}, "title": map[string]any{"type": "string"},
				"notes": map[string]any{"type": "string", "description": "Plain-text notes shown under the video, a line each, no Markdown: a line that starts with a time, as 0:00 or 1:02:03, plays the video from there when clicked. For its summary, chapters or key moments."}},
			"required": []string{"url"}},
	},
	"required": []string{"type"},
}

// ordered are a schema's properties in the order a model should write
// them, as a map's are not.
type ordered []prop

// prop is one of a schema's properties.
type prop struct {
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
		tool("read_video_transcript", "Read what is said in a video block of this page, from its site's captions: lines of [m:ss] text, each the time it starts. Only YouTube's and Vimeo's videos have them, and not every one. Read it to summarize a video, write its notes, or cite a moment of it.",
			map[string]any{"id": map[string]any{"type": "string", "description": "The video block's ID."}}, "id"),
	}
	if !readOnly {
		out = append(out, editTools()...)
	}
	if crossPage {
		out = append(out,
			tool("list_pages", "List the user's other pages (titles and IDs).", map[string]any{}),
			tool("read_other_page", "Read another of the user's pages. Read-only.", map[string]any{"page_id": map[string]any{"type": "string"}}, "page_id"),
			tool("list_folders", "List the user's folders (names and IDs), to make a page in one with create_page.", map[string]any{}))
	}
	return out
}

// pageSchema is a new page's: its title, where it goes, and its content in
// Markdown or as blocks, or neither for an empty page.
func pageSchema(place ...prop) ordered {
	o := append(ordered{{"title", map[string]any{"type": "string"}}}, place...)
	return append(o,
		prop{"markdown", map[string]any{"type": "string", "description": "The page's content in Markdown, as write_markdown writes it: the way to write text. Not with blocks."}},
		prop{"blocks", map[string]any{"type": "array", "items": blockSchema}})
}

// WebToolDefinitions are the web's tools offered to a model: a search,
// when its provider searches, and reading a page.
func WebToolDefinitions(search bool) []map[string]any {
	var out []map[string]any
	if search {
		out = append(out, tool("web_search", "Search the web, for what is recent or what you do not know: this week's news, a price, a page's address. Returns the results' links and what they say; read one whole with read_web_page.",
			map[string]any{"query": map[string]any{"type": "string", "description": "What to search for, as you would type it into a search engine."}}, "query"))
	}
	return append(out, tool("read_web_page", "Read a web page: its title, its text, and the links in it. Once you have read the web in this run, read only a page the user gave, one on this page, or one a search found or a page you read links to.",
		map[string]any{"url": map[string]any{"type": "string"}}, "url"))
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
		tool("create_subpage", "Create a subpage inside this page, for content that is part of it, optionally with content: a link to it goes at the end of this page.", pageSchema(), "title"),
		tool("create_page", "Create a page of its own, outside this one, optionally with content: at the top level of the user's pages, or in one of their folders. Make \"a new page\" with this, unless the user asks for one inside or under this page.",
			pageSchema(prop{"folder_id", map[string]any{"type": "string", "description": "The folder to put it in, from list_folders; none for the top level."}}), "title"),
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
	// CreatePage makes a page of the user's own at the top level, in the
	// folder of ID folderID when it is not "", and returns its ID.
	CreatePage func(title string, nodes []*doc.Node, folderID string) (string, error)
	// ListPages, ReadOtherPage and ListFolders are nil when cross-page
	// reading is off.
	ListPages     func() []map[string]any
	ReadOtherPage func(id string) map[string]any
	ListFolders   func() []map[string]any
	// Transcript is the transcript of a recording on page pageID as text,
	// "" when there is none, or the recording is another page's.
	Transcript func(pageID, recordingID string) string
	// SiteInfo keeps in the blocks the tools read what sites wrote of
	// them, which the app read from them: a link card's title and
	// description, and a video's title. An MCP client's, whose writing
	// keeps to no URL guard of the app's, keeps it; a run reads a card's
	// link and a video's alone (withoutSiteInfo).
	SiteInfo bool
	// Captions reads a video's captions from its site; nil reads none.
	Captions func(v doc.Video) (web.Captions, error)
	// Request is what the user asked the run, whose URLs a block may load
	// as this page's may (checkURLs).
	Request string
	// SearchWeb searches the web through the run's provider, and ReadWeb
	// reads a page of it; nil when the run has no web, or its provider no
	// search.
	SearchWeb func(query string) (Search, error)
	ReadWeb   func(link string) (web.Page, error)
	// Workspace is the workspace agent's, whose tools are of the user's
	// pages rather than of one: nil on a page.
	Workspace *Workspace
}

// PageTools is the ToolContext of a page in the store: its subpages,
// comments and transcripts, subpages made in the store, and, with
// crossPage, reading the user's other pages.
func PageTools(st *store.Store, pageID, title, agent string, content *doc.Content, crossPage bool) ToolContext {
	tc := ToolContext{
		Content: content, PageID: pageID, PageTitle: title, AgentName: agent,
		// Only a recording of the page whose block names it, as the agent
		// function reads a page's: a block can name any recording's ID,
		// another page's among them.
		Transcript: func(pageID, id string) string {
			rec, err := st.Recording(id)
			if err != nil || rec.PageID != pageID {
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
		// A page of its own is the user's, in their space, wherever the page
		// the run is on is: one someone else shared is theirs.
		CreatePage: func(title string, nodes []*doc.Node, folderID string) (string, error) {
			if folderID != "" && !spaceFolder(st, folderID) {
				return "", noFolder(folderID)
			}
			var c *doc.Content
			if len(nodes) > 0 {
				c = doc.FromBlocks(nodes)
			}
			p, err := st.CreatePage(store.NewPageOptions{SpaceID: st.Session().SpaceID, FolderID: folderID, Title: title, Content: c})
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
				if len(out) == maxListedPages {
					break
				}
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
			return map[string]any{"page": map[string]any{"id": id, "title": p.DisplayTitle()}, "blocks": tc.viewsOf(id, c),
				"subpages": subpages, "comments": []map[string]any{}}
		}
		tc.ListFolders = func() []map[string]any { return spaceFolders(st) }
	}
	return tc
}

// maxListedPages is the most pages list_pages lists, as the agent
// function lists at most so many.
const maxListedPages = 500

// spaceFolders are the folders of the user's own space, as list_folders
// lists them.
func spaceFolders(st *store.Store) []map[string]any {
	space := st.Session().SpaceID
	folders, _ := st.Folders()
	out := []map[string]any{}
	for _, f := range folders {
		if f.SpaceID == space {
			out = append(out, map[string]any{"id": f.ID, "name": f.Name})
		}
	}
	return out
}

// spaceFolder reports whether id is a folder of the user's own space, not
// deleted: one a page can be made or put in.
func spaceFolder(st *store.Store, id string) bool {
	f, err := st.Folder(id)
	return err == nil && f.SpaceID == st.Session().SpaceID && f.DeletedAt.IsZero()
}

// noFolder is a call naming a folder the user has not, as the server says
// it.
func noFolder(id string) error {
	return terr("No folder %q. Call list_folders for the folders' IDs.", id)
}

// maxViewHTML is the longest embed source agents read, in characters. A
// longer one is left out, its length given instead: a few would be more
// than a model reads at once, or an MCP client takes from a tool, and
// the page could not be read at all.
const maxViewHTML = 30_000

// Views are the blocks of the page as agents read them (Finish).
func (tc ToolContext) Views(c *doc.Content) []map[string]any { return tc.viewsOf(tc.PageID, c) }

// viewsOf are the blocks of page pageID, whose content c is, as agents
// read them (Finish).
func (tc ToolContext) viewsOf(pageID string, c *doc.Content) []map[string]any {
	views := doc.BlockViews(c)
	for _, v := range views {
		tc.Finish(pageID, v)
	}
	return views
}

// Finish makes a block's view, as doc.BlockViews has it, one as agents
// read it: with the transcript of a recording of page pageID, which is
// not in the page itself; without an embed's source longer than
// maxViewHTML; and, but for SiteInfo, without what sites wrote of the
// block (withoutSiteInfo).
func (tc ToolContext) Finish(pageID string, v map[string]any) {
	if e, ok := v["embed"].(map[string]any); ok {
		if html, _ := e["html"].(string); utf8.RuneCountInString(html) > maxViewHTML {
			delete(e, "html")
			e["html_length"] = utf8.RuneCountInString(html)
		}
	}
	if rec, ok := v["recording"].(map[string]any); ok && tc.Transcript != nil {
		id, _ := rec["recording_id"].(string)
		if t := tc.Transcript(pageID, id); t != "" {
			rec["transcript"] = t
		}
	}
	if !tc.SiteInfo {
		withoutSiteInfo(v)
	}
}

// withoutSiteInfo takes out of a block's view what a site wrote of it,
// which the apps read from the site to show the block: a link card's
// title and description, its page's of itself, and a video's title, its
// uploader's, which the apps give a video that has none. That is the
// web's writing, read with none of the web's guard (checkURLs). A video's
// title is the site's but for one an agent wrote, and the block keeps no
// word of which, nor did pages kept before, so none is read. A run that
// would know what a page says reads it with the web, and a video's title
// comes with its transcript, read as the web is (readTranscript), each
// keeping the run to the URLs it may write after. Nor are the URLs in that
// text the user's, to write as the page's (begin).
func withoutSiteInfo(v map[string]any) {
	switch v["type"] {
	case "link_preview":
		if l, ok := v["link"].(map[string]any); ok {
			delete(l, "title")
			delete(l, "description")
		}
	case "video":
		if vid, ok := v["video"].(map[string]any); ok {
			delete(vid, "title")
		}
	}
}

// Edit is a short description of a change, with the blocks it touched,
// or the page it made.
type Edit struct {
	Summary  string   `json:"summary"`
	BlockIDs []string `json:"block_ids"`
	PageID   string   `json:"page_id,omitempty"`
}

// editOf is op as an edit summary says it.
func editOf(op doc.Op, summary string) Edit {
	return Edit{Summary: summary, BlockIDs: OpBlockIDs(op), PageID: OpPageID(op)}
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
	// allowed are the URLs the user gave the run, which a block may load
	// once the run reads beyond this page or the web (checkURLs): those on
	// it when the run began, and in the user's request; nil before the
	// run's first call.
	allowed map[string]bool
	// armed is a run that has read beyond this page, or follows turns
	// that may have; readPages, one that has itself.
	armed, readPages bool
	// found are the URLs the run came on in the web: those its searches
	// found, and those on the pages it read. Nil before it reads the web,
	// after which what it writes, and the pages it reads, keep to them
	// and those the user gave it (checkURLs, readWeb). sources are those
	// of them the run came on itself, its searches' results and the pages
	// it read that it picked from none it read, not the links on those:
	// the only ones a block the app loads by itself may hold (loads).
	found, sources map[string]bool
	// written counts the blocks the run's calls have written, toward
	// maxRunBlocks.
	written int
	// Refused is why the latest call was refused, said in words of the
	// code's own, with nothing the run read or wrote in them, for a log:
	// "" when it was not.
	Refused string
}

// Guarded reports whether the run's URL guard is on: it has read beyond
// its page or the web, or follows turns that did (checkURLs). What it
// says may then carry what it read, its reply too (ReplyBlocks).
func (x *Executor) Guarded() bool { return x.armed || x.found != nil }

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
// of unicode.IsSpace's, as the server's URL_RE: their scheme in any case,
// as the app takes a link's (doc.WebURL), and in ASCII's letters alone,
// as url.Parse has a scheme.
var urlRe = regexp.MustCompile(`[hH][tT][tT][pP][sS]?://[^\t\n\v\f\r \x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}<>"'()\[\]]+`)

// canonURL is u with a web link's scheme in lower case, as url.Parse
// reads it: the guard keeps and compares URLs so, as two that only their
// scheme's case sets apart are one link to the app.
func canonURL(u string) string {
	for _, scheme := range []string{"https://", "http://"} {
		if len(u) >= len(scheme) && asciiFold(u[:len(scheme)]) == scheme {
			return scheme + u[len(scheme):]
		}
	}
	return u
}

// asciiFold is s with its ASCII letters in lower case, and nothing else
// changed.
func asciiFold(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// see adds the URLs in v to set, their schemes in lower case (canonURL):
// strings that are one, and those in text, in v and in every list and map
// in it, a table's rows of cells among them, as the server's urlsIn.
func see(set map[string]bool, v any) {
	if s, ok := v.(string); ok {
		if c := canonURL(s); strings.HasPrefix(c, "http://") || strings.HasPrefix(c, "https://") {
			set[c] = true
		}
		for _, u := range urlRe.FindAllString(s, -1) {
			set[canonURL(u)] = true
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
// The web is written by anyone, to steer the agents that read it: a page
// could have one write what is on this page into such a URL, or read the
// page at one. So once a run reads the web, a video's captions among it,
// what it writes, and the pages it reads, may hold only a URL the user
// gave it, or one it found there: a search's result, or a link on a page
// it read. Not one it made up. A block the app loads by itself keeps to
// fewer: a search's result, or a page the run read but did not pick from
// another it read, not a link on one, read or not, which its writer could
// have put there by the hundred for the run to pick from, the one it
// picked spelling out what it read. And once it
// reads the user's other pages, it does not use the web at all
// (errWebClosed), as what it asked for there could carry what it read,
// and what it writes keeps to the URLs the user gave it, as above.
//
// checkURLs refuses blocks that hold a URL the run may not write, in a
// block the app loads or in text, and more text that does, as a subpage's
// title, once it has read beyond this page.
func (x *Executor) checkURLs(nodes []*doc.Node, more ...any) error {
	if !x.armed && x.found == nil {
		return nil
	}
	x.begin()
	// Every URL, as text holds them, and those the app loads by itself.
	urls, loaded := map[string]bool{}, map[string]bool{}
	see(urls, more)
	for _, n := range nodes {
		walk(n, func(k *doc.Node) {
			see(urls, k.Data)
			if loads[k.Type] {
				see(loaded, k.Data["url"])
			}
		})
	}
	first := func(set map[string]bool, may func(string) bool) string {
		bad := ""
		for u := range set {
			if !may(u) && (bad == "" || u < bad) {
				bad = u
			}
		}
		return bad
	}
	bad := first(urls, x.given)
	switch {
	case bad == "":
	case x.armed:
		return terr("After reading another page, what you write may only hold a URL that was on this page before you began or is in the user's request, which %s is not: a link written here could carry what you read, and a later edit could have the app load it. Name the page you read it on with a link to that page instead.", bad)
	default:
		return terr("After reading the web, what you write may only hold a URL you found there, that was on this page before you began, or that is in the user's request, which %s is not: one made up could carry what is on this page to whoever runs its site, as the app loads it.", bad)
	}
	if bad = first(loaded, x.mayLoad); bad != "" {
		return terr("After reading the web, a picture, a video or a link card may only hold a URL a search found, of a page you read, that was on this page before you began, or that is in the user's request, which %s is not: a page's links are its writer's choice, and the one picked could carry what is on this page, as the app loads it by itself. Link to it in text instead.", bad)
	}
	return nil
}

// loads are the blocks whose url the app loads by itself, as it shows
// them, as the server's LOADS.
var loads = map[string]bool{"image": true, "link_preview": true, "video": true}

// given reports whether the run may write or read the URL u: one the user
// gave it, or one it found on the web while it has not read beyond this
// page.
func (x *Executor) given(u string) bool {
	u = canonURL(u)
	return x.allowed[u] || !x.armed && x.found[u]
}

// mayLoad reports whether a block the app loads by itself may hold the
// URL u: one the user gave the run, or one of the web's it came on itself
// (sources) while it has not read beyond this page.
func (x *Executor) mayLoad(u string) bool {
	u = canonURL(u)
	return x.allowed[u] || !x.armed && x.sources[u]
}

// find keeps a URL the run came on in the web, a source of its own or
// only a link on one.
func (x *Executor) find(u string, source bool) {
	u = canonURL(u)
	x.found[u] = true
	if source {
		x.sources[u] = true
	}
}

// errWebClosed is a web tool's answer once the run has read the user's
// other pages, or follows turns that did.
const errWebClosed = "This run has read the user's other pages, or follows turns of this conversation that did, so it cannot use the web: what it asked for there could carry what it read. Work from what you have."

// addressRe finds a web address in a search's query, with its scheme, its
// www. or a path after its host.
var addressRe = regexp.MustCompile(`(?i)://|\bwww\.|\b[a-z0-9-]+(\.[a-z0-9-]+)+/`)

// searchWeb searches the web through the run's provider. The results'
// links are found (given), but not those its answer names, which the
// model that searched wrote and the query could have asked it for, nor
// those the query names itself. Once the run has read the web, a query
// may name no address: a search may read the page at one.
func (x *Executor) searchWeb(args map[string]any) (map[string]any, error) {
	if x.Ctx.SearchWeb == nil {
		return nil, terr("Searching the web is off.")
	}
	if x.armed {
		return nil, terr(errWebClosed)
	}
	q, err := str(args, "query")
	if err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	if x.found != nil && addressRe.MatchString(q) {
		return nil, terr("Search for words, not a web address: read a page you found with read_web_page.")
	}
	s, err := x.Ctx.SearchWeb(q)
	if err != nil {
		return nil, terr("The search failed: %v", err)
	}
	x.saw()
	results := []map[string]any{}
	for _, src := range s.Sources {
		if bare := strings.SplitN(src.URL, "://", 2); len(bare) < 2 || !strings.Contains(q, bare[1]) {
			x.find(src.URL, true)
		}
		r := map[string]any{"url": src.URL}
		if src.Title != "" {
			r["title"] = src.Title
		}
		if src.Snippet != "" {
			r["snippet"] = src.Snippet
		}
		results = append(results, r)
	}
	return map[string]any{"query": q, "answer": s.Answer, "results": results}, nil
}

// readWeb reads a web page for the run: any the run names until it has
// read the web, then only one it may (given).
func (x *Executor) readWeb(args map[string]any) (map[string]any, error) {
	if x.Ctx.ReadWeb == nil {
		return nil, terr("Reading the web is off.")
	}
	if x.armed {
		return nil, terr(errWebClosed)
	}
	link, err := str(args, "url")
	if err != nil {
		return nil, err
	}
	link = strings.TrimSpace(link)
	bare, _, _ := strings.Cut(link, "#")
	if x.found != nil && !x.given(link) && !x.given(bare) {
		return nil, terr("After reading the web, read only a page the user gave, one on this page, or one a search found or a page you read links to, which %s is not.", link)
	}
	// The page is a source of the run's own (loads) only when its link was
	// not one picked from a page the run read: the run's first page of the
	// web, one the user gave, or a search's result. A link a page had, read,
	// stays a link: reading it does not make it one a block may load.
	source := x.found == nil || x.mayLoad(link) || x.mayLoad(bare)
	p, err := x.Ctx.ReadWeb(link)
	if err != nil {
		return nil, terr("Could not read %s: %v", link, err)
	}
	x.saw()
	x.find(p.URL, source)
	for _, u := range p.Links {
		x.find(u, false)
	}
	out := map[string]any{"url": p.URL, "title": p.Title, "text": p.Text}
	if p.Truncated {
		out["truncated"] = true
	}
	return out, nil
}

// readTranscript reads what is said in a video block of the page, from
// its site's captions, as lines of the times they start and their words.
// Once the run has read the user's other pages, only a video the page had
// as it began, or the request names, may be read: a video the run put
// there itself could be named for what it read. The captions, whoever put
// the video up wrote, are the web's: the run that reads them keeps to the
// URLs it may write after reading the web (checkURLs).
func (x *Executor) readTranscript(args map[string]any) (map[string]any, error) {
	id, err := str(args, "id")
	if err != nil {
		return nil, err
	}
	n := x.block(id)
	switch {
	case n == nil:
		return nil, noBlock(id)
	case n.Type == "recording":
		return nil, terr("Block %s is a recording: its transcript is in its view, from read_page.", id)
	case n.Type != "video":
		return nil, terr("Block %s is a %s, not a video.", id, n.Type)
	}
	link := n.Str("url")
	v, ok := doc.ParseVideoURL(link)
	switch {
	case !ok:
		return nil, terr("Block %s has no video Leaflet plays.", id)
	case !web.HasCaptions(v):
		return nil, terr("Leaflet can read what is said only in YouTube's and Vimeo's videos, whose sites give their captions; this one is a %s video. Work from what the user tells you of it.", v.Provider)
	case x.Ctx.Captions == nil:
		return nil, terr("Reading videos' transcripts is off.")
	case x.armed && !x.given(strings.TrimSpace(link)):
		return nil, terr(errWebClosed)
	}
	c, err := x.Ctx.Captions(v)
	var restricted *web.RestrictedError
	switch {
	case errors.Is(err, web.ErrNoCaptions):
		return nil, terr("The video has no captions to read: its creator gave none, and its site made none. Work from what the user tells you of it.")
	case errors.Is(err, web.ErrRefused):
		return nil, terr("%s refused to give the video's captions to Leaflet just now. Tell the user; trying again later may work.", siteName(v))
	case errors.As(err, &restricted):
		return nil, terr("%s won't show this video here (%v), so its captions can't be read.", siteName(v), restricted)
	case err != nil:
		return nil, terr("Could not read the video's captions: %v", err)
	}
	// Read with the web or without it.
	x.saw()
	segs := make([]store.Segment, 0, len(c.Lines))
	for _, l := range web.Passages(c.Lines) {
		segs = append(segs, store.Segment{StartMs: l.StartMs, Text: l.Text})
	}
	out := map[string]any{"id": id, "language": c.Language, "auto_generated": c.Auto, "transcript": TranscriptText(segs)}
	if t := n.Str("title"); t != "" {
		out["title"] = t
	}
	return out, nil
}

// siteName is the name of a video's site.
func siteName(v doc.Video) string {
	switch v.Provider {
	case "vimeo":
		return "Vimeo"
	case "youtube":
		return "YouTube"
	}
	return v.Provider
}

// saw starts keeping the URLs the run finds on the web, as it first reads
// it, or as it starts after turns that did (RunLocal).
func (x *Executor) saw() {
	if x.found == nil {
		x.found, x.sources = map[string]bool{}, map[string]bool{}
	}
	x.begin()
}

// CallRaw runs a tool with JSON arguments. Invalid calls return {error}
// and change nothing.
func (x *Executor) CallRaw(name, raw string) map[string]any {
	args := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return x.refuse(terr("Arguments were not valid JSON."))
		}
		// Arguments that are no object are refused, as the server
		// refuses them, rather than taken as none.
		m, ok := v.(map[string]any)
		if !ok {
			return x.refuse(terr("Arguments must be a JSON object."))
		}
		args = m
	}
	return x.Call(name, args)
}

// Call runs a tool.
func (x *Executor) Call(name string, args map[string]any) map[string]any {
	out, err := x.dispatch(name, args)
	x.Refused = reason(err)
	return ToolAnswer(out, err)
}

// refuse is a call's answer that err refused it.
func (x *Executor) refuse(err error) map[string]any {
	x.Refused = reason(err)
	return ToolAnswer(nil, err)
}

// ToolAnswer is a tool's result as the model reads it: out, or else what
// err says was wrong, as {error}.
func ToolAnswer(out map[string]any, err error) map[string]any {
	if err != nil {
		return map[string]any{"error": toolMessage(err)}
	}
	return out
}

// terr is a tool's refusal, as the model is told it: format with args.
// The format is why too, for a log (reason), as it holds nothing the run
// read or wrote.
func terr(format string, args ...any) error {
	return &refusal{ToolError: doc.ToolError{Message: fmt.Sprintf(format, args...)}, why: format}
}

// refusal is a tool's refusal, with why in the code's own words.
type refusal struct {
	doc.ToolError
	why string
}

func (r *refusal) Unwrap() error { return &r.ToolError }

// reason is why err refused a call, for a log, with nothing the run read
// or wrote in it: a refusal's format, or the kind of error another is; ""
// for none.
func reason(err error) string {
	var r *refusal
	var te *doc.ToolError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &r):
		return r.why
	case errors.As(err, &te):
		return "The call's blocks were refused."
	}
	return "The call failed."
}

// begin keeps the URLs a block may load once the run reads beyond this
// page, before its first call changes the page: those on it, and in the
// user's request. RunLocal calls it before any call, as one streaming in
// changes the page before it is made. A run that may read nothing beyond
// it keeps none, as it needs none.
func (x *Executor) begin() {
	c := x.Ctx
	if x.allowed == nil && (x.armed || x.found != nil || c.ListPages != nil || c.ReadOtherPage != nil || c.ReadWeb != nil || c.SearchWeb != nil || c.Captions != nil || c.Workspace != nil) {
		x.allowed = map[string]bool{}
		views := doc.BlockViews(c.Content)
		for _, v := range views {
			withoutSiteInfo(v)
		}
		see(x.allowed, views)
		see(x.allowed, c.Request)
	}
}

func (x *Executor) dispatch(name string, args map[string]any) (map[string]any, error) {
	x.begin()
	if x.Ctx.Workspace != nil {
		return x.workspace(name, args)
	}
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
	case "write_markdown", "insert_blocks", "replace_block", "move_block", "delete_blocks", "create_subpage", "create_page":
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
	case "create_page":
		return x.createPage(args)
	case "list_folders":
		if x.Ctx.ListFolders == nil {
			return nil, terr("Reading other pages is off.")
		}
		// Only the user names their folders, and no page's words are in
		// them: a run that lists them has read nothing beyond this page, so
		// the URLs it may write stay those it may (checkURLs), and the web
		// stays open.
		return map[string]any{"folders": x.Ctx.ListFolders()}, nil
	case "list_pages":
		if x.Ctx.ListPages == nil {
			return nil, terr("Reading other pages is off.")
		}
		// The titles it lists are read too.
		x.armed, x.readPages = true, true
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
		x.armed, x.readPages = true, true
		return p, nil
	case "web_search":
		return x.searchWeb(args)
	case "read_web_page":
		return x.readWeb(args)
	case "read_video_transcript":
		return x.readTranscript(args)
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

// PageNodes is the title and the blocks a call gives a new page, written
// by author: as blocks, or in Markdown as write_markdown takes it, or
// neither for an empty page, as the server takes them.
func PageNodes(args map[string]any, author string) (string, []*doc.Node, error) {
	title, err := str(args, "title")
	if err != nil {
		return "", nil, err
	}
	md, err := optionalString(args, "markdown")
	if err != nil {
		return "", nil, err
	}
	var nodes []*doc.Node
	// No blocks, or none listed, is no content, as the server takes it.
	if list, ok := args["blocks"].([]any); args["blocks"] != nil && (!ok || len(list) > 0) {
		if md != "" {
			return "", nil, terr("Give the page markdown or blocks, not both.")
		}
		if nodes, err = Specs(args["blocks"], author); err != nil {
			return "", nil, err
		}
	} else if nodes = doc.MarkdownBlocks(md); len(nodes) > 0 {
		// As many blocks as a call of blocks makes, as write_markdown.
		if err := doc.CallNodes(nodes); err != nil {
			return "", nil, err
		}
	}
	// Without the spaces around it, as the server keeps it.
	return strings.TrimSpace(title), nodes, nil
}

// optionalString is the text args has at key: "" when it has none, and an
// error when it has something else.
func optionalString(args map[string]any, key string) (string, error) {
	switch v := args[key].(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	}
	return "", terr("%s must be a string.", key)
}

// record keeps an op that changes no block of the page, as create_page.
func (x *Executor) record(op doc.Op, summary string) {
	x.Ops = append(x.Ops, op)
	x.Edits = append(x.Edits, editOf(op, summary))
	if x.OnOp != nil {
		x.OnOp(op, summary)
	}
}

func (x *Executor) createSubpage(args map[string]any) (map[string]any, error) {
	title, nodes, err := PageNodes(args, x.Ctx.AgentName)
	if err != nil {
		return nil, err
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
	// Its card goes at the end of this page, as the app puts one there, so
	// the page shows its subpage and Undo takes both away.
	card := doc.SubpageNode(id)
	if err := x.emit(doc.InsertOp(doc.Position{At: "end"}, card.Clone()), summaryFor(locales.AgentEditInserted, []*doc.Node{card}), []string{card.ID}); err != nil {
		return nil, err
	}
	x.record(doc.Op{Op: "create_page", Page: &doc.NewPage{ID: id, ParentID: x.Ctx.PageID, Title: title}}, locales.AgentEditCreatedSubpage(title))
	return map[string]any{"page_id": id}, nil
}

func (x *Executor) createPage(args map[string]any) (map[string]any, error) {
	title, nodes, err := PageNodes(args, x.Ctx.AgentName)
	if err != nil {
		return nil, err
	}
	folder, err := optionalString(args, "folder_id")
	if err != nil {
		return nil, err
	}
	// Its title shows in the sidebar, where the next run that lists the
	// user's pages reads it.
	if err := x.checkURLs(nodes, title); err != nil {
		return nil, err
	}
	if err := x.room(nodes); err != nil {
		return nil, err
	}
	if x.Ctx.CreatePage == nil {
		return nil, terr("Pages cannot be made here.")
	}
	id, err := x.Ctx.CreatePage(title, nodes, folder)
	if err != nil {
		return nil, err
	}
	x.written += countNodes(nodes)
	x.record(doc.Op{Op: "create_page", Page: &doc.NewPage{ID: id, FolderID: folder, Title: title}}, locales.AgentEditCreatedPage(title))
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
	"subpage": locales.AgentEditSubpageLink,
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
	case op.Op == "create_page" && op.Page != nil && op.Page.ParentID == "":
		summary = locales.AgentEditCreatedPage(op.Page.Title)
	case op.Op == "create_page" && op.Page != nil:
		summary = locales.AgentEditCreatedSubpage(op.Page.Title)
	case op.Op == "move_page":
		summary = locales.AgentEditMovedPage(op.Title)
	case op.Op == "rename_page":
		summary = locales.AgentEditRenamedPage(op.PrevTitle, op.Title)
	case op.Op == "set_page_tags":
		summary = locales.AgentEditTaggedPage(op.Title)
	case op.Op == "create_folder" && op.Folder != nil:
		summary = locales.AgentEditCreatedFolder(op.Folder.Name)
	}
	return editOf(op, summary)
}

// OpPageID is the page an op made, or moved, renamed or tagged: "" for
// one that changed no page of its own.
func OpPageID(op doc.Op) string {
	switch {
	case op.Op == "create_page" && op.Page != nil:
		return op.Page.ID
	case op.Op == "move_page" || op.Op == "rename_page" || op.Op == "set_page_tags":
		return op.PageID
	}
	return ""
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
