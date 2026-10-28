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
// each given the page it works on, and tools to find, make and file pages.

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
	"move_page":     {"Move Page", false, false},
	"create_folder": {"Create Folder", false, false},
	"rename_folder": {"Rename Folder", false, false},
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
	add("list_pages", "List the user's pages (IDs, titles, the page or folder each is in) and folders.", object(map[string]any{}))
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
			"folder_id": map[string]any{"type": "string", "description": "The folder to put it in, at the top level; not with parent_id."},
		}, "title"))
	add("rename_page", "Change a page's title.", object(map[string]any{"page_id": pageID, "title": map[string]any{"type": "string"}}, "page_id", "title"))
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

// errUnreadable is a page whose stored content cannot be read: it is left
// as it is.
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
	folders, _ := st.Folders()
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

// folder is the folder of ID id.
func (t *pageTools) folder(id any) (store.Folder, error) {
	s, _ := id.(string)
	if strings.TrimSpace(s) == "" {
		return store.Folder{}, toolErr("folder_id is required.")
	}
	folders, err := t.s.st.Folders()
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
	delete(args, "page_id")
	// The app may save the page while the tool runs: then run it again on
	// what the app saved, rather than save over it.
	for range 3 {
		row, err := st.ContentRow(p.ID)
		if err != nil {
			return nil, toolErr("The page's content is not kept on this device.")
		}
		// What cannot be read is refused rather than taken for an empty
		// page, which saving would then put in its place.
		content, err := doc.Decode(row.Content)
		if err != nil {
			return nil, errUnreadable
		}
		before := content.Copy()
		x := &agents.Executor{Ctx: agents.PageTools(st, p.ID, p.DisplayTitle(), t.s.agent, content, true)}
		out := x.Call(name, args)
		if len(x.Ops) == 0 {
			return out, nil
		}
		if _, _, err := st.SaveContentOver(p.ID, row.Content, content); errors.Is(err, store.ErrContentChanged) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := t.snapshot(p.ID, before); err != nil {
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
	parent, folderID, err := t.place(args)
	if err != nil {
		return nil, err
	}
	var content *doc.Content
	if args["blocks"] != nil {
		nodes, err := agents.Specs(args["blocks"], t.s.agent)
		if err != nil {
			return nil, err
		}
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
// it at its end.
func (t *pageTools) movePage(args map[string]any) (map[string]any, error) {
	st := t.s.st
	p, err := t.page(args, true)
	if err != nil {
		return nil, err
	}
	if p.Template {
		return nil, toolErr("A template doesn't move among the pages.")
	}
	parent, folderID, err := t.place(args)
	if err != nil {
		return nil, err
	}
	if parent.ID != "" && st.IsDescendant(parent.ID, p.ID) {
		return nil, toolErr("A page cannot move inside itself or one of its subpages.")
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
// it changed it.
func (t *pageTools) editLinks(parent store.Page, edit func(c *doc.Content) bool) error {
	st := t.s.st
	for range 3 {
		row, err := st.ContentRow(parent.ID)
		if err != nil {
			// The parent's content is not on this device: the page is
			// still inside it, in the sidebar.
			return nil
		}
		c, err := doc.Decode(row.Content)
		if err != nil {
			// Nor is a parent whose content cannot be read written over:
			// the page is inside it all the same.
			return nil
		}
		if !edit(c) {
			return nil
		}
		if _, _, err := st.SaveContentOver(parent.ID, row.Content, c); errors.Is(err, store.ErrContentChanged) {
			continue
		} else if err != nil {
			return err
		}
		return nil
	}
	return toolErr("The parent page kept changing while saving.")
}
