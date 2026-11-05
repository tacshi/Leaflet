package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// The tools: the in-app agents' page tools (internal/agents/tools.go),
// each given the page it works on, and tools to find, make and file pages.

// pageToolNames are the in-app tools that work on one page.
var pageToolNames = []string{"read_page", "write_markdown", "insert_blocks", "replace_block", "move_block", "delete_blocks"}

// hints are each tool's title, and whether it only reads, or changes what
// is there rather than adding to it, as a move does (the page history
// keeps a page's blocks all the same).
var hints = map[string]struct {
	title                 string
	readOnly, destructive bool
}{
	"list_pages":     {"List Pages", true, false},
	"search_pages":   {"Search Pages", true, false},
	"read_page":      {"Read Page", true, false},
	"write_markdown": {"Write Markdown", false, false},
	"insert_blocks":  {"Insert Blocks", false, false},
	"replace_block":  {"Replace Block", false, true},
	"move_block":     {"Move Block", false, true},
	"delete_blocks":  {"Delete Blocks", false, true},
	"create_page":    {"Create Page", false, false},
	"rename_page":    {"Rename Page", false, true},
	"set_page_tags":  {"Set Page Tags", false, true},
	"move_page":      {"Move Page", false, true},
	"create_folder":  {"Create Folder", false, false},
	"rename_folder":  {"Rename Folder", false, true},
}

func toolList() []map[string]any {
	defs := map[string]map[string]any{}
	for _, d := range agents.ToolDefinitions(false, false) {
		fn := d["function"].(map[string]any)
		defs[fn["name"].(string)] = fn
	}
	pageID := map[string]any{"type": "string", "description": "The page's ID, from list_pages or search_pages."}
	folderID := map[string]any{"type": "string", "description": "The folder's ID, from list_pages."}
	var out []map[string]any
	add := func(name, desc string, schema map[string]any) {
		h := hints[name]
		// What a tool reads stays on this device. What it writes does not:
		// sync sends it on, to whoever else has the page, and the app loads
		// a picture or a link card in it from its site by itself.
		annotations := map[string]any{"title": h.title, "readOnlyHint": h.readOnly, "openWorldHint": !h.readOnly}
		if !h.readOnly {
			annotations["destructiveHint"] = h.destructive
		}
		out = append(out, map[string]any{"name": name, "title": h.title, "description": desc, "inputSchema": schema, "annotations": annotations})
	}
	object := func(props map[string]any, required ...string) map[string]any {
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	add("list_pages", "List the user's pages (IDs, titles, tags, the page or folder each is in) and folders.", object(map[string]any{}))
	add("search_pages", "Find pages by title, tag or text, recordings by transcript, and comments. Words starting with # (as #work) keep to the pages with those tags; alone they list them.",
		object(map[string]any{"query": map[string]any{"type": "string"}}, "query"))
	for _, name := range pageToolNames {
		fn := defs[name]
		params := fn["parameters"].(map[string]any)
		props := agents.Properties(params)
		props["page_id"] = pageID
		required := append([]string{"page_id"}, params["required"].([]string)...)
		desc := fn["description"].(string)
		if name == "read_page" {
			desc = "Read a page: its tags, every block with its ID, subpage titles, and comment threads. A long page comes in parts: total_blocks says how many blocks it has, and next_offset, while there are more, is the offset to read on from. An embed's html longer than 30,000 characters is left out, its html_length given instead: to change such an embed, write its html whole."
			props["offset"] = map[string]any{"type": "integer", "minimum": 0, "description": "The block to start from, as next_offset gives it; the first when not given."}
			props["limit"] = map[string]any{"type": "integer", "minimum": 1, "description": "The most blocks to read; as many as fit when not given."}
		}
		add(name, desc, object(props, required...))
	}
	made := agents.Properties(defs["create_page"]["parameters"].(map[string]any))
	add("create_page", "Create a page, optionally with content. With parent_id it goes inside that page, and a link to it is added at the end of the parent.",
		object(map[string]any{
			"title":     made["title"],
			"markdown":  made["markdown"],
			"blocks":    made["blocks"],
			"parent_id": map[string]any{"type": "string", "description": "The page to put it inside; none for the top level."},
			"folder_id": map[string]any{"type": "string", "description": "The folder to put it in, at the top level; not with parent_id."},
		}, "title"))
	add("rename_page", "Change a page's title.", object(map[string]any{"page_id": pageID, "title": map[string]any{"type": "string"}}, "page_id", "title"))
	add("set_page_tags", "Set a page's tags, replacing the ones it has: an empty list takes them all off. Tags are short words or phrases, without #; reuse the ones list_pages shows where they fit.",
		object(map[string]any{"page_id": pageID, "tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "page_id", "tags"))
	add("move_page", "Move a page, with its subpages, inside another page (linked at its end), into a folder, or with neither to the top level outside folders.",
		object(map[string]any{
			"page_id":   pageID,
			"parent_id": map[string]any{"type": "string", "description": "The page to put it inside."},
			"folder_id": map[string]any{"type": "string", "description": "The folder to put it in, at the top level; not with parent_id."},
		}, "page_id"))
	add("create_folder", "Create a folder for top-level pages, last in the sidebar.", object(map[string]any{"name": map[string]any{"type": "string"}}, "name"))
	add("rename_folder", "Change a folder's name.", object(map[string]any{"folder_id": folderID, "name": map[string]any{"type": "string"}}, "folder_id", "name"))
	return out
}

// historyGap is how long after an agent's last edit of a page the next
// one keeps a new history snapshot: a client's tool calls have no runs,
// so edits close together count as one.
const historyGap = 10 * time.Minute

type pageTools struct {
	s *Server
	// app is a client that is an AI app, which what it reads on a page can
	// steer, not a command the user gave themselves (Server.Call).
	app bool
	// edited is when this client last edited each page.
	edited map[string]int64
	// read is each page as this client last read or edited it, of the
	// keptReads it read or edited last, those first in order: a block it
	// replaces or deletes that changed since, as the user typed in the
	// app, is kept in a snapshot first (conflictSince).
	read  map[string]*doc.Content
	order []string
}

// keptReads is how many pages the server keeps as the client last read or
// edited them: a client that reads page after page, as to search them,
// would have the server keep every one. An edit of one read longer ago
// keeps no snapshot of a change the client never saw, as an edit of a
// page never read does (conflictSince), but the one before its edits
// (snapshot).
const keptReads = 16

// keep keeps c as the client last read or edited page id, and forgets the
// page read longest ago past keptReads.
func (t *pageTools) keep(id string, c *doc.Content) {
	t.read[id] = c
	t.order = append(slices.DeleteFunc(t.order, func(k string) bool { return k == id }), id)
	if len(t.order) > keptReads {
		delete(t.read, t.order[0])
		t.order = t.order[1:]
	}
}

func (t *pageTools) call(name string, args map[string]any) map[string]any {
	clean, _ := cleanArg(args).(map[string]any)
	return agents.ToolAnswer(t.dispatch(name, clean))
}

// cleanArg is an argument of a call, its text fit to keep, in titles and
// names as in Markdown: a NUL, which the server's columns refuse, so that
// the page would never sync, and bytes that are no UTF-8, which go through
// JSON no further, each read as U+FFFD.
func cleanArg(v any) any {
	switch v := v.(type) {
	case string:
		return store.CleanText(v)
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cleanArg(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = cleanArg(e)
		}
		return out
	}
	return v
}

func toolErr(msg string) error { return &doc.ToolError{Message: msg} }

// errUnreadable is a page whose stored content cannot be read
// (store.ErrUnreadable), as the client is told: it is left as it is.
var errUnreadable = toolErr("The page's content could not be read, so it was left as it is.")

func (t *pageTools) dispatch(name string, args map[string]any) (map[string]any, error) {
	st := t.s.st
	switch name {
	case "list_pages":
		return t.listPages()
	case "search_pages":
		q, _ := args["query"].(string)
		hits, err := st.Search(q, 20)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, h := range hits {
			// Templates stay out of agents' page lists.
			if h.Kind == "template" {
				continue
			}
			hit := map[string]any{"kind": h.Kind, "title": h.Title, "page_id": h.PageID}
			if h.Snippet != "" {
				hit["snippet"] = h.Snippet
			}
			if h.BlockID != "" {
				hit["block_id"] = h.BlockID
			}
			out = append(out, hit)
		}
		return map[string]any{"results": out}, nil
	case "create_page":
		return t.createPage(args)
	case "rename_page":
		p, err := t.page(args, true)
		if err != nil {
			return nil, err
		}
		title, _ := args["title"].(string)
		if strings.TrimSpace(title) == "" {
			return nil, toolErr("title is required.")
		}
		if err := st.RenamePage(p.ID, title); err != nil {
			return nil, err
		}
		return map[string]any{"page_id": p.ID}, nil
	case "set_page_tags":
		p, err := t.page(args, true)
		if err != nil {
			return nil, err
		}
		list, ok := args["tags"].([]any)
		if !ok {
			return nil, toolErr("tags is required, a list of strings.")
		}
		var tags []string
		for _, v := range list {
			if s, ok := v.(string); ok {
				tags = append(tags, s)
			}
		}
		if err := st.SetPageTags(p.ID, tags); err != nil {
			return nil, err
		}
		p, _ = st.Page(p.ID)
		return map[string]any{"page_id": p.ID, "tags": p.Tags}, nil
	case "move_page":
		return t.movePage(args)
	case "create_folder":
		name, _ := args["name"].(string)
		if name = strings.TrimSpace(name); name == "" {
			return nil, toolErr("name is required.")
		}
		f, err := st.CreateFolder(name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"folder_id": f.ID}, nil
	case "rename_folder":
		f, err := t.folder(args["folder_id"])
		if err != nil {
			return nil, err
		}
		name, _ := args["name"].(string)
		if name = strings.TrimSpace(name); name == "" {
			return nil, toolErr("name is required.")
		}
		if err := st.RenameFolder(f, name); err != nil {
			return nil, err
		}
		return map[string]any{"folder_id": f.ID}, nil
	}
	if slices.Contains(pageToolNames, name) {
		return t.onPage(name, args)
	}
	return nil, toolErr("Unknown tool " + name + ".")
}

func (t *pageTools) listPages() (map[string]any, error) {
	st := t.s.st
	pages, err := st.Pages()
	if err != nil {
		return nil, err
	}
	folders, _ := t.folders()
	ids := map[string]bool{}
	outFolders := []map[string]any{}
	for _, f := range folders {
		ids[f.ID] = true
		outFolders = append(outFolders, map[string]any{"id": f.ID, "name": f.Name})
	}
	sess := st.Session()
	out := []map[string]any{}
	for _, p := range pages {
		row := map[string]any{"id": p.ID, "title": p.DisplayTitle()}
		if len(p.Tags) > 0 {
			row["tags"] = p.Tags
		}
		if p.ParentID != "" {
			row["parent_id"] = p.ParentID
		}
		if ids[p.FolderID] && p.ParentID == "" {
			row["folder_id"] = p.FolderID
		}
		if p.SpaceID != sess.SpaceID {
			row["shared"] = true
		}
		out = append(out, row)
	}
	return map[string]any{"pages": out, "folders": outFolders}, nil
}

// folders are the folders of the user's own space, the ones the sidebar
// shows.
func (t *pageTools) folders() ([]store.Folder, error) {
	all, err := t.s.st.Folders()
	if err != nil {
		return nil, err
	}
	space := t.s.st.Session().SpaceID
	return slices.DeleteFunc(all, func(f store.Folder) bool { return f.SpaceID != space }), nil
}

// folder is the folder of ID id.
func (t *pageTools) folder(id any) (store.Folder, error) {
	s, _ := id.(string)
	if strings.TrimSpace(s) == "" {
		return store.Folder{}, toolErr("folder_id is required.")
	}
	folders, err := t.folders()
	if err != nil {
		return store.Folder{}, err
	}
	for _, f := range folders {
		if f.ID == s {
			return f, nil
		}
	}
	return store.Folder{}, toolErr("No folder " + s + ".")
}

// place is where args put a page: inside the page parent_id, which the
// user may change, or in the folder folder_id, or at the top level.
func (t *pageTools) place(args map[string]any) (parent store.Page, folderID string, err error) {
	parentID, _ := args["parent_id"].(string)
	folderID, _ = args["folder_id"].(string)
	if parentID != "" && folderID != "" {
		return store.Page{}, "", toolErr("Give parent_id or folder_id, not both: a page inside another isn't in a folder.")
	}
	if parentID != "" {
		parent, err = t.page(map[string]any{"page_id": parentID}, true)
		return parent, "", err
	}
	if folderID != "" {
		if _, err := t.folder(folderID); err != nil {
			return store.Page{}, "", err
		}
	}
	return store.Page{}, folderID, nil
}

// page is the page args name, which edit asks to be one the user may
// change.
func (t *pageTools) page(args map[string]any, edit bool) (store.Page, error) {
	id, _ := args["page_id"].(string)
	if strings.TrimSpace(id) == "" {
		return store.Page{}, toolErr("page_id is required.")
	}
	p, err := t.s.st.Page(id)
	if err != nil || !p.DeletedAt.IsZero() {
		return store.Page{}, toolErr("No page " + id + ".")
	}
	// Templates stay out of agents' page lists, and out of their reach by
	// ID too: nor does a page go inside one, which would make it one.
	if p.Template {
		return store.Page{}, toolErr("Page " + id + " is a template, which agents leave alone.")
	}
	if edit && !t.s.st.CanEdit(p) {
		return store.Page{}, toolErr("The page is shared with the user to view, not to edit.")
	}
	return p, nil
}

// onPage runs one of the in-app agents' tools on a page, and stores what
// it changed.
func (t *pageTools) onPage(name string, args map[string]any) (map[string]any, error) {
	st := t.s.st
	p, err := t.page(args, name != "read_page")
	if err != nil {
		return nil, err
	}
	args = maps.Clone(args)
	offset, limit, err := partOf(args)
	if err != nil {
		return nil, err
	}
	delete(args, "page_id")
	delete(args, "offset")
	delete(args, "limit")
	// The app may save the page while the tool runs: then it runs again on
	// what the app saved, rather than save over it (UpdateContent). The
	// edit keeps one snapshot, however many times it runs.
	var out map[string]any
	var saved *doc.Content
	snapped := false
	err = st.UpdateContent(p.ID, func(content *doc.Content, kept bool) (bool, error) {
		if !kept {
			return false, toolErr("The page's content is not kept on this device.")
		}
		// Not with the user's other pages, which MCP's own tools read.
		// What sites wrote of the blocks, a link card's title and a
		// video's, which the in-app agents read only with the web (agents'
		// Finish), the client reads as the page has it: it keeps to no URL
		// guard of the app's.
		tc := agents.PageTools(st, p.ID, p.DisplayTitle(), t.s.agent, content, false)
		tc.SiteInfo = true
		if name == "read_page" {
			out = readPart(tc, p, content, offset, limit)
			t.keep(p.ID, content)
			return false, nil
		}
		before := content.Copy()
		x := &agents.Executor{Ctx: tc}
		out = x.Call(name, args)
		if len(x.Ops) == 0 {
			return false, nil
		}
		// The snapshot goes first: an edit that could not keep one is not
		// saved, so trying it again does not make it twice.
		if !snapped {
			if err := t.snapshot(p.ID, before); err != nil {
				return false, err
			}
			snapped = true
		}
		if conflictSince(t.read[p.ID], before, x.Ops) {
			if err := t.s.st.AddHistory(p.ID, before, "conflict", "", "Before "+t.s.agent); err != nil {
				return false, err
			}
		}
		beforeSave(p.ID)
		saved = content
		return true, nil
	})
	switch {
	case errors.Is(err, store.ErrUnreadable):
		// What cannot be read is refused rather than taken for an empty
		// page, which saving would then put in its place.
		return nil, errUnreadable
	case errors.Is(err, store.ErrContentChanged):
		return nil, toolErr("The page kept changing while saving. Read it again and retry.")
	case err != nil:
		return nil, err
	}
	if saved != nil {
		t.edited[p.ID] = doc.NowMs()
		t.keep(p.ID, saved.Copy())
		// Not kept, the client's next edit only keeps a snapshot more.
		if err := st.SetValue(editedPrefix+p.ID, editMark(t.s.agent, saved)); err != nil {
			log.Printf("mcp: %v", err)
		}
	}
	return out, nil
}

// maxRead bounds a read_page answer's blocks, in bytes of JSON: what MCP
// clients take from a tool is limited (Claude Code's 25,000 tokens, say),
// and a longer page, of a few embeds, would not be read at all. It comes
// in parts instead, each saying where the next starts.
const maxRead = 60_000

// partOf is the part of a page read_page's args ask for: from the block
// at offset, at most limit blocks (0, as many as fit in maxRead).
func partOf(args map[string]any) (offset, limit int, err error) {
	count := func(key string, least int) (int, error) {
		v, ok := args[key]
		if !ok || v == nil {
			return 0, nil
		}
		f, ok := v.(float64)
		if !ok || f != math.Trunc(f) || f < float64(least) || f > math.MaxInt32 {
			return 0, toolErr(fmt.Sprintf("%s must be a whole number, %d or more.", key, least))
		}
		return int(f), nil
	}
	if offset, err = count("offset", 0); err != nil {
		return 0, 0, err
	}
	limit, err = count("limit", 1)
	return offset, limit, err
}

// readPart is read_page's answer of the part of page p, of content c,
// from the block at offset, at most limit of them, or as many as fit in
// maxRead, though always one: it says how many blocks the page has, and
// where the next part starts, when there is more. The page's subpages and
// comments come with the first part. Only the part's blocks are read as
// agents read them (agents' Finish), with their transcripts: a long page
// read in parts would have each read every one.
func readPart(tc agents.ToolContext, p store.Page, c *doc.Content, offset, limit int) map[string]any {
	out := map[string]any{"page": map[string]any{"id": p.ID, "title": p.DisplayTitle(), "tags": p.Tags}}
	size := 0
	if offset == 0 {
		out["subpages"], out["comments"] = tc.Subpages(), tc.Comments()
		for _, k := range []string{"page", "subpages", "comments"} {
			raw, _ := json.Marshal(out[k])
			size += len(raw)
		}
	}
	blocks := doc.BlockViews(c)
	start := min(offset, len(blocks))
	end := start
	for end < len(blocks) && (limit == 0 || end-start < limit) {
		tc.Finish(p.ID, blocks[end])
		raw, _ := json.Marshal(blocks[end])
		if end > start && size+len(raw) > maxRead {
			break
		}
		size += len(raw)
		end++
	}
	out["blocks"] = blocks[start:end]
	out["total_blocks"] = len(blocks)
	if end < len(blocks) {
		out["next_offset"] = end
	}
	return out
}

// conflictSince reports whether ops replace or delete a block that changed
// in now since read, the page as the client last had it: the client
// writes over a change it never saw, as the user's in the app, which a
// snapshot then keeps to restore. A page it never read tells nothing.
func conflictSince(read, now *doc.Content, ops []doc.Op) bool {
	if read == nil {
		return false
	}
	changed := func(id string) bool {
		was, is := read.Find(id), now.Find(id)
		if was == nil || is == nil {
			return false
		}
		a, _ := json.Marshal(was)
		b, _ := json.Marshal(is)
		return string(a) != string(b)
	}
	for _, op := range ops {
		switch op.Op {
		case "replace":
			if changed(op.ID) {
				return true
			}
		case "delete":
			if slices.ContainsFunc(op.IDs, changed) {
				return true
			}
		}
	}
	return false
}

// beforeSave runs between an edit and saving it; tests save there,
// standing in for the app.
var beforeSave = func(pageID string) {}

// editedPrefix starts the key-value settings that say how a client left a
// page: editedPrefix + its ID is the page's editMark as last saved here.
const editedPrefix = "mcp_edited:"

// editMark names content as agent saved it, for a client started again to
// tell the page it finds from one changed since: the blocks alone, as a
// save stamps their times.
func editMark(agent string, c *doc.Content) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + (&doc.Content{Children: c.Children}).Encode()))
	return hex.EncodeToString(sum[:])
}

// snapshot keeps the page as it was before this client's edits, once per
// stretch of them: none while the last edit saved is recent, nor while
// the page's newest snapshot is a recent one of this client's. A client
// started again for each edit, as the command line is, remembers no edit
// of its own, and would otherwise keep one a run, pushing the page's
// older history out. Either way the page must be as this client left it:
// what the user typed since is in no snapshot, as a snapshot this recent
// holds autosave back, and an edit after it would leave it in none.
func (t *pageTools) snapshot(pageID string, before *doc.Content) error {
	label := "Before " + t.s.agent
	if mark, ok := t.s.st.Value(editedPrefix + pageID); ok && mark == editMark(t.s.agent, before) {
		if last, ok := t.edited[pageID]; ok && doc.NowMs()-last < historyGap.Milliseconds() {
			return nil
		}
		if h, err := t.s.st.NewestHistory(pageID); err == nil && h.Reason == "agent_run" && h.Label == label && time.Since(h.CreatedAt) < historyGap {
			return nil
		}
	}
	return t.s.st.AddHistory(pageID, before, "agent_run", "", label)
}

func (t *pageTools) createPage(args map[string]any) (map[string]any, error) {
	st := t.s.st
	// Its content as the app's agents give a page theirs.
	title, nodes, err := agents.PageNodes(args, t.s.agent)
	if err != nil {
		return nil, err
	}
	parent, folderID, err := t.place(args)
	if err != nil {
		return nil, err
	}
	var content *doc.Content
	if len(nodes) > 0 {
		content = doc.FromBlocks(nodes)
	}
	p, err := st.CreatePage(store.NewPageOptions{ParentID: parent.ID, FolderID: folderID, Title: title, Content: content})
	if err != nil {
		return nil, err
	}
	if parent.ID != "" {
		// As a page made from the sidebar, it shows in the page it is
		// inside.
		if err := t.linkSubpage(parent, p.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"page_id": p.ID}, nil
}

// movePage moves a page as the sidebar does, and keeps the links to it in
// step: a page it leaves loses its link, and a page it goes into links to
// it at its end. A page stays in its space, as the server has it: it goes
// inside a page of that space, or to the top level only of the user's own.
func (t *pageTools) movePage(args map[string]any) (map[string]any, error) {
	st := t.s.st
	p, err := t.page(args, true)
	if err != nil {
		return nil, err
	}
	// Neither p nor parent is a template: page refuses them.
	parent, folderID, err := t.place(args)
	if err != nil {
		return nil, err
	}
	switch {
	case parent.ID != "" && parent.SpaceID != p.SpaceID:
		return nil, toolErr("A page cannot move inside a page of another space.")
	case parent.ID == "" && p.SpaceID != st.Session().SpaceID:
		return nil, toolErr("A page shared with the user stays inside a page of its space: it cannot move to the top level or into a folder.")
	case parent.ID != "" && st.IsDescendant(parent.ID, p.ID):
		return nil, toolErr("A page cannot move inside itself or one of its subpages.")
	// As the workspace agent's rule has it: with this one call a page the
	// user alone has would go to others, its subpages with it, on what a
	// page may have told the client. The user does that themselves, in
	// the app or with a command of their own.
	case t.app && parent.ID != "" && agents.ShowsToOthers(st, p, parent):
		return nil, toolErr("Other people may have that page: inside it, this page and its subpages would be theirs to read. The user can move it there themselves, in the app.")
	}
	if p.ParentID == parent.ID && (parent.ID != "" || p.FolderID == folderID) {
		return map[string]any{"page_id": p.ID}, nil
	}
	if err := st.MovePage(p.ID, parent.ID, folderID, ""); err != nil {
		return nil, err
	}
	if p.ParentID != parent.ID {
		if old, err := st.Page(p.ParentID); err == nil && st.CanEdit(old) {
			if err := t.unlinkSubpage(old, p.ID); err != nil {
				return nil, err
			}
		}
		if parent.ID != "" {
			if err := t.linkSubpage(parent, p.ID); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{"page_id": p.ID}, nil
}

// linkSubpage links page id at the end of parent, unless it links it
// already.
func (t *pageTools) linkSubpage(parent store.Page, id string) error {
	return t.editLinks(parent, func(c *doc.Content) bool {
		if len(subpageLinks(c, id)) > 0 {
			return false
		}
		_ = c.Insert(doc.Position{At: "end"}, &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": id}})
		return true
	})
}

// unlinkSubpage removes parent's links to page id.
func (t *pageTools) unlinkSubpage(parent store.Page, id string) error {
	return t.editLinks(parent, func(c *doc.Content) bool {
		links := subpageLinks(c, id)
		for _, l := range links {
			c.Remove(l)
		}
		return len(links) > 0
	})
}

// subpageLinks are the IDs of c's links to page id.
func subpageLinks(c *doc.Content, id string) []string {
	var out []string
	c.Walk(func(n, _ *doc.Node) {
		if n.Type == "subpage" && n.Str("page_id") == id {
			out = append(out, n.ID)
		}
	})
	return out
}

// editLinks saves parent's content changed by edit, which reports whether
// it changed it. A parent whose content is not on this device, or cannot
// be read, is left as it is: the page is inside it all the same, in the
// sidebar.
func (t *pageTools) editLinks(parent store.Page, edit func(c *doc.Content) bool) error {
	err := t.s.st.UpdateContent(parent.ID, func(c *doc.Content, kept bool) (bool, error) {
		return kept && edit(c), nil
	})
	switch {
	case errors.Is(err, store.ErrUnreadable):
		return nil
	case errors.Is(err, store.ErrContentChanged):
		return toolErr("The parent page kept changing while saving.")
	}
	return err
}
