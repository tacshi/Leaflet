package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/web"
	"leaflet/locales"
)

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
		"type":      map[string]any{"type": "string", "enum": append(slices.Clone(doc.AgentTypes), "file")},
		"text":      map[string]any{"type": "string", "description": "Inline markdown: **bold**, *italic*, `code`, ~~strike~~, [text](url), and text color and highlight as <span style=\"color:#ef4444;background:#fde68a\">text</span>, either alone, in hex, not nested. Keep the spans read_page shows when rewriting text. No block syntax such as # or -. For a code block, the code itself, verbatim, with its line breaks and no fence."},
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
		"file": map[string]any{"type": "object", "description": "For replace_block on a file block, keeping the file itself: its notes. No tool makes a new file.",
			"properties": map[string]any{"notes": map[string]any{"type": "string", "description": "Plain-text notes shown under the file, a line each, no Markdown: for an audio or video file, a line that starts with a time, as 0:00 or 1:02:03, plays it from there when clicked."}}},
	},
	"required": []string{"type"},
}

type ordered []prop

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

func pageSchema(place ...prop) ordered {
	o := append(ordered{{"title", map[string]any{"type": "string"}}}, place...)
	return append(o,
		prop{"markdown", map[string]any{"type": "string", "description": "The page's content in Markdown, as write_markdown writes it: the way to write text. Not with blocks."}},
		prop{"blocks", map[string]any{"type": "array", "items": blockSchema}})
}

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
		tool("write_markdown", "Write into the page in Markdown: the way to write prose, as a story, a draft, notes or a summary. Each line is a block: a paragraph, # to ### headings, - or 1. list items (indent two spaces to nest), - [ ] to-dos, > quotes, ``` fenced code, | tables, --- dividers; blank lines only separate them. It goes into the page as you write it.",
			ordered{{"position", writeSchema}, {"markdown", map[string]any{"type": "string"}}}, "position", "markdown"),
		tool("insert_blocks", "Insert new blocks at a position.", ordered{{"position", positionSchema}, {"blocks", map[string]any{"type": "array", "minItems": 1, "items": blockSchema}}}, "position", "blocks"),
		tool("replace_block", "Replace one block with a new one, keeping its ID (and its nested items when it stays a list).", ordered{{"id", map[string]any{"type": "string"}}, {"block", blockSchema}}, "id", "block"),
		tool("move_block", "Move a block (with its nested items) to a new position.", map[string]any{"id": map[string]any{"type": "string"}, "position": positionSchema}, "id", "position"),
		tool("delete_blocks", "Delete blocks by ID, with their nested items.", map[string]any{"ids": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}}}, "ids"),
		tool("create_subpage", "Create a subpage inside this page, for content that is part of it, optionally with content: a link to it goes at the end of this page.", pageSchema(), "title"),
		tool("create_page", "Create a page of its own, outside this one, optionally with content: at the top level of the user's pages, or in one of their folders. Make \"a new page\" with this, unless the user asks for one inside or under this page.",
			pageSchema(prop{"folder_id", map[string]any{"type": "string", "description": "The folder to put it in, its ID from list_folders where that is offered; none for the top level."}}), "title"),
	}
}

type ToolContext struct {
	Content       *doc.Content
	PageID        string
	PageTitle     string
	AgentName     string
	ReadOnly      bool
	Subpages      func() []map[string]any
	Comments      func() []map[string]any
	CreateSubpage func(title string, nodes []*doc.Node) (string, error)
	CreatePage    func(title string, nodes []*doc.Node, folderID string) (string, error)
	ListPages     func() []map[string]any
	ReadOtherPage func(id string) map[string]any
	ListFolders   func() []map[string]any
	Alone         func(pageID string) bool
	LinkedNow     func() bool
	Transcript    func(pageID, recordingID string) string
	SiteInfo      bool
	Captions      func(v doc.Video) (web.Captions, error)
	Request       string
	SearchWeb     func(query string) (Search, error)
	ReadWeb       func(link string) (web.Page, error)
	Workspace     *Workspace
}

func PageTools(st *store.Store, pageID, title, agent string, content *doc.Content, crossPage bool) ToolContext {
	tc := ToolContext{
		Content: content, PageID: pageID, PageTitle: title, AgentName: agent,
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
		here, _ := st.Page(pageID)
		tc.ListPages = func() []map[string]any {
			pages, _ := st.Pages()
			var others []store.Page
			for _, p := range pages {
				if p.ID != pageID && p.SpaceID == here.SpaceID {
					others = append(others, p)
				}
			}
			out := []map[string]any{}
			for _, p := range others {
				out = append(out, map[string]any{"id": p.ID, "title": p.DisplayTitle(), "parent_id": p.ParentID})
			}
			return out[:listed(len(out), 0, func(i int) int {
				if out[i]["parent_id"] == "" {
					return jsonCost(out[i]) + jsonCost(nil) - jsonCost("")
				}
				return jsonCost(out[i])
			})]
		}
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

const maxListedPages = 500

const listBudget = toolResultLimit - 2_000

func jsonCost(v any) int {
	switch x := v.(type) {
	case string:
		n := 2
		for _, r := range x {
			switch {
			case r < 0x20 || strings.ContainsRune(`"\<>&`, r) || r == 0x2028 || r == 0x2029 || r == utf8.RuneError:
				n += 6
			case r > 0xFFFF:
				n += 2
			default:
				n++
			}
		}
		return n
	case bool:
		if x {
			return 4
		}
		return 5
	case int:
		return len(strconv.Itoa(x))
	case []string:
		n := 2 + max(len(x)-1, 0)
		for _, e := range x {
			n += jsonCost(e)
		}
		return n
	case []map[string]any:
		n := 2 + max(len(x)-1, 0)
		for _, e := range x {
			n += jsonCost(e)
		}
		return n
	case []any:
		n := 2 + max(len(x)-1, 0)
		for _, e := range x {
			n += jsonCost(e)
		}
		return n
	case map[string]any:
		n := 2 + max(len(x)-1, 0)
		for k, e := range x {
			n += jsonCost(k) + 1 + jsonCost(e)
		}
		return n
	}
	return 4
}

func listed(n, spent int, cost func(i int) int) int {
	for i := range min(n, maxListedPages) {
		if spent += cost(i) + 1; spent > listBudget {
			return i
		}
	}
	return min(n, maxListedPages)
}

func spaceFolders(st *store.Store) []map[string]any {
	space := st.Session().SpaceID
	folders, _ := st.Folders()
	out := []map[string]any{}
	mine := map[string]bool{}
	for _, f := range folders {
		mine[f.ID] = f.SpaceID == space
	}
	for _, f := range folders {
		if f.SpaceID == space {
			row := map[string]any{"id": f.ID, "name": f.Name}
			if mine[f.ParentFolderID] {
				row["parent_folder_id"] = f.ParentFolderID
			}
			out = append(out, row)
		}
	}
	return out
}

func spaceFolder(st *store.Store, id string) bool {
	f, err := st.Folder(id)
	return err == nil && f.SpaceID == st.Session().SpaceID && f.DeletedAt.IsZero()
}

func noFolder(id string) error {
	return terr("No folder %q. Call list_folders for the folders' IDs.", id)
}

const maxViewHTML = 30_000

func (tc ToolContext) Views(c *doc.Content) []map[string]any { return tc.viewsOf(tc.PageID, c) }

func (tc ToolContext) viewsOf(pageID string, c *doc.Content) []map[string]any {
	views := doc.BlockViews(c)
	for _, v := range views {
		tc.Finish(pageID, v)
	}
	return views
}

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

func ownText(c *doc.Content) string {
	c = c.Copy()
	c.Walk(func(n, _ *doc.Node) {
		switch n.Type {
		case "link_preview":
			delete(n.Data, "title")
			delete(n.Data, "description")
		case "video":
			delete(n.Data, "title")
		}
	})
	return c.PlainText()
}

type Edit struct {
	Summary  string   `json:"summary"`
	BlockIDs []string `json:"block_ids"`
	PageID   string   `json:"page_id,omitempty"`
}

func editOf(op doc.Op, summary string) Edit {
	return Edit{Summary: summary, BlockIDs: OpBlockIDs(op), PageID: OpPageID(op)}
}

type Executor struct {
	Ctx              ToolContext
	OnOp             func(op doc.Op, summary string)
	OnRevise         func(at int, op *doc.Op, summary string, delta []doc.Op)
	Ops              []doc.Op
	Edits            []Edit
	draft            *draft
	allowed          map[string]bool
	armed, readPages bool
	readOthers       bool
	linked, ended    bool
	found, sources   map[string]bool
	followed         int
	written          int
	Refused          string
}

func (x *Executor) Guarded() bool { return x.armed || x.found != nil }

const maxRunBlocks = 5000

func (x *Executor) room(nodes []*doc.Node) error {
	if x.written+countNodes(nodes) > maxRunBlocks {
		return terr("This run has written %d blocks, and one run writes at most %d. Stop here, and say in your reply what is left to do.", x.written, maxRunBlocks)
	}
	return nil
}

func countNodes(nodes []*doc.Node) int {
	n := 0
	for _, k := range nodes {
		walk(k, func(*doc.Node) { n++ })
	}
	return n
}

var urlRe = regexp.MustCompile(`[hH][tT][tT][pP][sS]?://[^\t\n\v\f\r \x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}<>"'()\[\]]+`)

func canonURL(u string) string {
	for _, scheme := range []string{"https://", "http://"} {
		if len(u) >= len(scheme) && asciiFold(u[:len(scheme)]) == scheme {
			return scheme + u[len(scheme):]
		}
	}
	return u
}

func asciiFold(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

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

func (x *Executor) checkURLs(nodes []*doc.Node, more ...any) error {
	if !x.armed && x.found == nil {
		return nil
	}
	x.begin()
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

var loads = map[string]bool{"image": true, "link_preview": true, "video": true}

func (x *Executor) given(u string) bool {
	u = canonURL(u)
	return x.allowed[u] || !x.armed && x.found[u]
}

func (x *Executor) mayLoad(u string) bool {
	u = canonURL(u)
	return x.allowed[u] || !x.armed && x.sources[u]
}

func (x *Executor) find(u string, source bool) {
	u = canonURL(u)
	x.found[u] = true
	if source {
		x.sources[u] = true
	}
}

const errWebClosed = "This run has read the user's other pages, or follows turns of this conversation that did, so it cannot use the web: what it asked for there could carry what it read. Work from what you have."

const maxFollowedLinks = 10

const errNowLinked = "This page has a public link now: its visitors read what you write here, so this run reads no other page. Work from this page."

var errLinkedStop = errors.New("the run's page has a public link now")

func (x *Executor) linkedNow() bool {
	if !x.linked && x.Ctx.LinkedNow != nil && x.Ctx.LinkedNow() {
		x.linked = true
	}
	return x.linked
}

func (x *Executor) endsHere() bool { return x.armed && x.linkedNow() }

var addressRe = regexp.MustCompile(`(?i)://|\bwww\.|\b[a-z0-9-]+(\.[a-z0-9-]+)+/`)

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
	source := x.found == nil || x.mayLoad(link) || x.mayLoad(bare)
	if !source {
		if x.followed >= maxFollowedLinks {
			return nil, terr("This run has followed %d links from the pages it read, the most one run follows. Work from what you have, or search for what is missing.", maxFollowedLinks)
		}
		x.followed++
	}
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

func siteName(v doc.Video) string {
	switch v.Provider {
	case "vimeo":
		return "Vimeo"
	case "youtube":
		return "YouTube"
	}
	return v.Provider
}

func (x *Executor) saw() {
	if x.found == nil {
		x.found, x.sources = map[string]bool{}, map[string]bool{}
	}
	x.begin()
}

func (x *Executor) CallRaw(name, raw string) map[string]any {
	args := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return x.refuse(terr("Arguments were not valid JSON."))
		}
		m, ok := v.(map[string]any)
		if !ok {
			return x.refuse(terr("Arguments must be a JSON object."))
		}
		args = m
	}
	return x.Call(name, args)
}

func (x *Executor) Call(name string, args map[string]any) map[string]any {
	out, err := x.dispatch(name, args)
	x.Refused = reason(err)
	return ToolAnswer(out, err)
}

func (x *Executor) refuse(err error) map[string]any {
	x.Refused = reason(err)
	return ToolAnswer(nil, err)
}

func ToolAnswer(out map[string]any, err error) map[string]any {
	if err != nil {
		return map[string]any{"error": toolMessage(err)}
	}
	return out
}

func terr(format string, args ...any) error {
	return &refusal{ToolError: doc.ToolError{Message: fmt.Sprintf(format, args...)}, why: format}
}

type refusal struct {
	doc.ToolError
	why string
}

func (r *refusal) Unwrap() error { return &r.ToolError }

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

func (x *Executor) begin() {
	c := x.Ctx
	if x.allowed == nil && (x.armed || x.found != nil || c.ListPages != nil || c.ReadOtherPage != nil || c.ListFolders != nil || c.ReadWeb != nil || c.SearchWeb != nil || c.Captions != nil || c.Workspace != nil) {
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
	switch name {
	case "write_markdown", "insert_blocks", "replace_block", "move_block", "delete_blocks", "create_subpage", "create_page":
		if x.Ctx.ReadOnly {
			return nil, terr("You can read this page but not edit it: the user has view access only.")
		}
		if x.endsHere() {
			x.ended = true
			return nil, terr(errNowLinked)
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
		if x.linkedNow() {
			return nil, terr(errNowLinked)
		}
		x.saw()
		return map[string]any{"folders": x.Ctx.ListFolders()}, nil
	case "list_pages":
		if x.Ctx.ListPages == nil {
			return nil, terr("Reading other pages is off.")
		}
		if x.linkedNow() {
			return nil, terr(errNowLinked)
		}
		x.armed, x.readPages = true, true
		return map[string]any{"pages": x.Ctx.ListPages()}, nil
	case "read_other_page":
		if x.Ctx.ReadOtherPage == nil {
			return nil, terr("Reading other pages is off.")
		}
		if x.linkedNow() {
			return nil, terr(errNowLinked)
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
		if x.Ctx.Alone != nil && !x.Ctx.Alone(id) {
			x.readOthers = true
		}
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

func (x *Executor) block(id string) *doc.Node { return x.Ctx.Content.Addressable(id) }

func (x *Executor) has(id string) bool { return x.block(id) != nil }

func noBlock(id any) error {
	return terr("No block with id \"%v\" on this page. Call read_page for current ids.", id)
}

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

func (x *Executor) replacement(prev *doc.Node, spec map[string]any) (*doc.Node, error) {
	id := prev.ID
	if spec["type"] == "file" && prev.Type == "file" {
		n, err := doc.FileWithNotes(prev, spec)
		if err != nil {
			return nil, err
		}
		if err := x.checkURLs([]*doc.Node{n}); err != nil {
			return nil, err
		}
		return n, nil
	}
	n, err := doc.SpecToNode(spec, id, x.Ctx.AgentName)
	if err != nil {
		return nil, err
	}
	if err := x.checkURLs([]*doc.Node{n}); err != nil {
		return nil, err
	}
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
	if list, ok := args["blocks"].([]any); args["blocks"] != nil && (!ok || len(list) > 0) {
		if md != "" {
			return "", nil, terr("Give the page markdown or blocks, not both.")
		}
		if nodes, err = Specs(args["blocks"], author); err != nil {
			return "", nil, err
		}
	} else if nodes = doc.MarkdownBlocks(md); len(nodes) > 0 {
		if err := doc.CallNodes(nodes); err != nil {
			return "", nil, err
		}
	}
	return store.ClampTitle(strings.TrimSpace(title)), nodes, nil
}

const errNoOwnPages = "Someone else writes or reads this page: a page of the user's own made from it could take what they wrote into the user's own pages. Make a subpage of this page with create_subpage instead."

const errReadOthers = "This run read a page someone else writes or reads: a page of the user's own made now could take what they wrote into the user's own pages."

func optionalString(args map[string]any, key string) (string, error) {
	switch v := args[key].(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	}
	return "", terr("%s must be a string.", key)
}

func (x *Executor) record(op doc.Op, summary string) {
	x.Ops = append(x.Ops, op)
	x.Edits = append(x.Edits, editOf(op, summary))
	if x.OnOp != nil {
		x.OnOp(op, summary)
	}
}

func (x *Executor) createSubpage(args map[string]any) (map[string]any, error) {
	if x.readOthers {
		return nil, terr(errReadOthers)
	}
	title, nodes, err := PageNodes(args, x.Ctx.AgentName)
	if err != nil {
		return nil, err
	}
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
	card := doc.SubpageNode(id)
	if err := x.emit(doc.InsertOp(doc.Position{At: "end"}, card.Clone()), summaryFor(locales.AgentEditInserted, []*doc.Node{card}), []string{card.ID}); err != nil {
		return nil, err
	}
	x.record(doc.Op{Op: "create_page", Page: &doc.NewPage{ID: id, ParentID: x.Ctx.PageID, Title: title}}, locales.AgentEditCreatedSubpage(title))
	return map[string]any{"page_id": id}, nil
}

func (x *Executor) createPage(args map[string]any) (map[string]any, error) {
	if x.Ctx.CreatePage == nil {
		return nil, terr(errNoOwnPages)
	}
	if x.readOthers {
		return nil, terr(errReadOthers)
	}
	title, nodes, err := PageNodes(args, x.Ctx.AgentName)
	if err != nil {
		return nil, err
	}
	folder, err := optionalString(args, "folder_id")
	if err != nil {
		return nil, err
	}
	if err := x.checkURLs(nodes, title); err != nil {
		return nil, err
	}
	if err := x.room(nodes); err != nil {
		return nil, err
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

var kinds = map[string]func() string{
	"chart": locales.AgentEditChart, "table": locales.AgentEditTable, "embed": locales.AgentEditEmbed, "heading": locales.AgentEditHeading,
	"image": locales.AgentEditImage, "video": locales.AgentEditVideo, "code": locales.AgentEditCode, "callout": locales.AgentEditCallout,
	"toggle_list": locales.AgentEditToggle, "link_preview": locales.AgentEditLinkCard, "math_equation": locales.AgentEditFormula, "diagram": locales.AgentEditDiagram,
	"subpage": locales.AgentEditSubpageLink,
}

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

func OpPageID(op doc.Op) string {
	switch {
	case op.Op == "create_page" && op.Page != nil:
		return op.Page.ID
	case op.Op == "move_page" || op.Op == "rename_page" || op.Op == "set_page_tags":
		return op.PageID
	}
	return ""
}

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
