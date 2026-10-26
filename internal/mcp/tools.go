package mcp

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// The tools: the in-app agents' page tools (internal/agents/tools.go),
// each given the page it works on, and tools to find and make pages.

// pageToolNames are the in-app tools that work on one page.
var pageToolNames = []string{"read_page", "insert_blocks", "replace_block", "move_block", "delete_blocks"}

// hints are each tool's title, and whether it only reads or may undo
// what is written (the page history keeps it all the same). Every tool
// stays on this device: none reaches the open world.
var hints = map[string]struct {
	title                 string
	readOnly, destructive bool
}{
	"list_pages":    {"List Pages", true, false},
	"search_pages":  {"Search Pages", true, false},
	"read_page":     {"Read Page", true, false},
	"insert_blocks": {"Insert Blocks", false, false},
	"replace_block": {"Replace Block", false, true},
	"move_block":    {"Move Block", false, false},
	"delete_blocks": {"Delete Blocks", false, true},
	"create_page":   {"Create Page", false, false},
	"rename_page":   {"Rename Page", false, false},
}

func toolList() []map[string]any {
	defs := map[string]map[string]any{}
	for _, d := range agents.ToolDefinitions(false) {
		fn := d["function"].(map[string]any)
		defs[fn["name"].(string)] = fn
	}
	pageID := map[string]any{"type": "string", "description": "The page's ID, from list_pages or search_pages."}
	var out []map[string]any
	add := func(name, desc string, schema map[string]any) {
		h := hints[name]
		annotations := map[string]any{"title": h.title, "readOnlyHint": h.readOnly, "openWorldHint": false}
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
	add("list_pages", "List the user's pages: IDs, titles, the page each is inside, and folders.", object(map[string]any{}))
	add("search_pages", "Find pages by title or text, recordings by transcript, and comments.",
		object(map[string]any{"query": map[string]any{"type": "string"}}, "query"))
	for _, name := range pageToolNames {
		fn := defs[name]
		params := fn["parameters"].(map[string]any)
		props := maps.Clone(params["properties"].(map[string]any))
		props["page_id"] = pageID
		required := append([]string{"page_id"}, params["required"].([]string)...)
		desc := fn["description"].(string)
		if name == "read_page" {
			desc = "Read a page: every block with its ID, subpage titles, and comment threads."
		}
		add(name, desc, object(props, required...))
	}
	sub := defs["create_subpage"]["parameters"].(map[string]any)["properties"].(map[string]any)
	add("create_page", "Create a page, optionally with content. With parent_id it goes inside that page, and a link to it is added at the end of the parent.",
		object(map[string]any{
			"title":     sub["title"],
			"blocks":    sub["blocks"],
			"parent_id": map[string]any{"type": "string", "description": "The page to put it inside; none for the top level."},
		}, "title"))
	add("rename_page", "Change a page's title.", object(map[string]any{"page_id": pageID, "title": map[string]any{"type": "string"}}, "page_id", "title"))
	return out
}

// historyGap is how long after an agent's last edit of a page the next
// one keeps a new history snapshot: a client's tool calls have no runs,
// so edits close together count as one.
const historyGap = 10 * time.Minute

type pageTools struct {
	s *Server
	// edited is when this client last edited each page.
	edited map[string]int64
}

func (t *pageTools) call(name string, args map[string]any) map[string]any {
	out, err := t.dispatch(name, args)
	if err != nil {
		var te *doc.ToolError
		if errors.As(err, &te) {
			return map[string]any{"error": te.Message}
		}
		return map[string]any{"error": err.Error()}
	}
	return out
}

func toolErr(msg string) error { return &doc.ToolError{Message: msg} }

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
	folders, _ := st.Folders()
	names := map[string]string{}
	for _, f := range folders {
		names[f.ID] = f.Name
	}
	sess := st.Session()
	out := []map[string]any{}
	for _, p := range pages {
		row := map[string]any{"id": p.ID, "title": p.DisplayTitle()}
		if p.ParentID != "" {
			row["parent_id"] = p.ParentID
		}
		if f := names[p.FolderID]; f != "" && p.ParentID == "" {
			row["folder"] = f
		}
		if p.SpaceID != sess.SpaceID {
			row["shared"] = true
		}
		out = append(out, row)
	}
	return map[string]any{"pages": out}, nil
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
	if edit && !t.canEdit(p) {
		return store.Page{}, toolErr("The page is shared with the user to view, not to edit.")
	}
	return p, nil
}

// canEdit is the app's rule: the user's own pages, and pages shared with
// them as an editor.
func (t *pageTools) canEdit(p store.Page) bool {
	sess := t.s.st.Session()
	if p.SpaceID == sess.SpaceID {
		return true
	}
	for _, m := range t.s.st.MembersOrNil(p.ID) {
		if m.UserID == sess.UserID {
			return m.Role == "editor" || m.Role == "owner"
		}
	}
	return true
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
	delete(args, "page_id")
	// The app may save the page while the tool runs: then run it again on
	// what the app saved, rather than save over it.
	for range 3 {
		row, err := st.ContentRow(p.ID)
		if err != nil {
			return nil, toolErr("The page's content is not kept on this device.")
		}
		content := doc.MustDecode(row.Content)
		before := content.Copy()
		x := &agents.Executor{Ctx: agents.PageTools(st, p.ID, p.DisplayTitle(), t.s.agent, content, true)}
		out := x.Call(name, args)
		if len(x.Ops) == 0 {
			return out, nil
		}
		if now, err := st.ContentRow(p.ID); err != nil || now.Content != row.Content {
			continue
		}
		if err := t.snapshot(p.ID, before); err != nil {
			return nil, err
		}
		if _, _, err := st.SaveContent(p.ID, content); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, toolErr("The page kept changing while saving. Read it again and retry.")
}

// snapshot keeps the page as it was before this client's edits, once per
// stretch of them.
func (t *pageTools) snapshot(pageID string, before *doc.Content) error {
	now := doc.NowMs()
	last, ok := t.edited[pageID]
	t.edited[pageID] = now
	if ok && now-last < historyGap.Milliseconds() {
		return nil
	}
	return t.s.st.AddHistory(pageID, before, "agent_run", "", "Before "+t.s.agent)
}

func (t *pageTools) createPage(args map[string]any) (map[string]any, error) {
	st := t.s.st
	title, _ := args["title"].(string)
	if strings.TrimSpace(title) == "" {
		return nil, toolErr("title is required.")
	}
	parentID, _ := args["parent_id"].(string)
	var parent store.Page
	if parentID != "" {
		var err error
		if parent, err = t.page(map[string]any{"page_id": parentID}, true); err != nil {
			return nil, err
		}
	}
	var content *doc.Content
	if args["blocks"] != nil {
		nodes, err := agents.Specs(args["blocks"], t.s.agent)
		if err != nil {
			return nil, err
		}
		content = doc.FromBlocks(nodes)
	}
	p, err := st.CreatePage(store.NewPageOptions{ParentID: parentID, Title: title, Content: content})
	if err != nil {
		return nil, err
	}
	if parentID != "" {
		// As a page made from the sidebar, it shows in the page it is
		// inside.
		if err := t.linkSubpage(parent, p.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"page_id": p.ID}, nil
}

func (t *pageTools) linkSubpage(parent store.Page, id string) error {
	st := t.s.st
	for range 3 {
		row, err := st.ContentRow(parent.ID)
		if err != nil {
			// The parent's content is not on this device: the page is
			// still inside it, in the sidebar.
			return nil
		}
		c := doc.MustDecode(row.Content)
		if err := c.Insert(doc.Position{At: "end"}, &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": id}}); err != nil {
			return err
		}
		if now, err := st.ContentRow(parent.ID); err != nil || now.Content != row.Content {
			continue
		}
		_, _, err = st.SaveContent(parent.ID, c)
		return err
	}
	return toolErr("The parent page kept changing while saving.")
}
