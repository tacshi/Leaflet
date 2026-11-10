package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	"leaflet/internal/store"
)

var pageToolNames = []string{"read_page", "write_markdown", "insert_blocks", "replace_block", "move_block", "delete_blocks"}

var hints = map[string]struct {
	title                 string
	readOnly, destructive bool
}{
	"list_pages":     {"List Pages", true, false},
	"search_pages":   {"Search Pages", true, false},
	"current_page":   {"Current Page", true, false},
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
	"move_folder":    {"Move Folder", false, true},
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
	add("list_pages", "List the user's pages (IDs, titles, tags, the page or folder each is in) and folders (IDs, names, the folder each is in).", object(map[string]any{}))
	add("search_pages", "Find pages by title, tag or text, recordings by transcript, and comments. Words starting with # (as #work) keep to the pages with those tags; alone they list them.",
		object(map[string]any{"query": map[string]any{"type": "string"}}, "query"))
	add("current_page", "Find what the user has in front of them in the Leaflet app: the open page, and its selection or caret, with the selected text, its blocks and offsets, and its text color and highlight as hex. open is false when no page is open now, with the page opened last, if any, and no selection.", object(map[string]any{}))
	for _, name := range pageToolNames {
		fn := defs[name]
		params := fn["parameters"].(map[string]any)
		props := agents.Properties(params)
		props["page_id"] = pageID
		required := append([]string{"page_id"}, params["required"].([]string)...)
		desc := fn["description"].(string)
		if name == "read_page" {
			desc = "Read a page: its tags, every block with its ID, subpage titles, and comment threads. A long page comes in parts: total_blocks says how many blocks it has, and next_offset, while there are more, is the offset to read on from; more_subpages and more_comments count the subpages and comment threads a very long list leaves out. An embed's html longer than 30,000 characters is left out, its html_length given instead: to change such an embed, write its html whole."
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
	parentFolderID := map[string]any{"type": "string", "description": "The folder to put it in; none for the top level."}
	add("create_folder", "Create a folder for top-level pages, last among the folders where it goes.",
		object(map[string]any{"name": map[string]any{"type": "string"}, "parent_folder_id": parentFolderID}, "name"))
	add("rename_folder", "Change a folder's name.", object(map[string]any{"folder_id": folderID, "name": map[string]any{"type": "string"}}, "folder_id", "name"))
	add("move_folder", "Move a folder, with what is in it, last into another folder, or with no parent_folder_id to the top level.",
		object(map[string]any{"folder_id": folderID, "parent_folder_id": parentFolderID}, "folder_id"))
	return out
}

const historyGap = 10 * time.Minute

type pageTools struct {
	s      *Server
	app    bool
	edited map[string]int64
	read   map[string]*doc.Content
	order  []string
}

const keptReads = 16

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
	case "current_page":
		return t.currentPage()
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
		parent, err := t.parentFolder(args)
		if err != nil {
			return nil, err
		}
		f, err := st.CreateFolderIn(name, parent)
		if err != nil {
			return nil, err
		}
		return map[string]any{"folder_id": f.ID}, nil
	case "move_folder":
		f, err := t.folder(args["folder_id"])
		if err != nil {
			return nil, err
		}
		parent, err := t.parentFolder(args)
		if err != nil {
			return nil, err
		}
		if parent != "" && st.FolderUnder(parent, f.ID) {
			return nil, toolErr("A folder cannot move into itself or a folder inside it.")
		}
		if err := st.MoveFolder(f.ID, parent, ""); err != nil {
			return nil, err
		}
		return map[string]any{"folder_id": f.ID, "parent_folder_id": parent}, nil
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

func (t *pageTools) currentPage() (map[string]any, error) {
	st := t.s.st
	view, err := st.OpenView()
	if err != nil {
		return nil, err
	}
	id, _ := st.Value(store.LastPageKey)
	if view != nil {
		id = view.PageID
	}
	p, err := t.page(map[string]any{"page_id": id}, false)
	if err != nil {
		return map[string]any{"open": false}, nil
	}
	out := map[string]any{"open": view != nil, "page_id": p.ID, "title": p.DisplayTitle()}
	if view != nil {
		out["selection"] = view.Selection
		out["updated_at"] = view.At
	}
	return out, nil
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
	}
	for _, f := range folders {
		row := map[string]any{"id": f.ID, "name": f.Name}
		if ids[f.ParentFolderID] {
			row["parent_folder_id"] = f.ParentFolderID
		}
		outFolders = append(outFolders, row)
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

func (t *pageTools) folders() ([]store.Folder, error) {
	all, err := t.s.st.Folders()
	if err != nil {
		return nil, err
	}
	space := t.s.st.Session().SpaceID
	return slices.DeleteFunc(all, func(f store.Folder) bool { return f.SpaceID != space }), nil
}

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

func (t *pageTools) parentFolder(args map[string]any) (string, error) {
	id, _ := args["parent_folder_id"].(string)
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	f, err := t.folder(id)
	return f.ID, err
}

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

func (t *pageTools) page(args map[string]any, edit bool) (store.Page, error) {
	id, _ := args["page_id"].(string)
	if strings.TrimSpace(id) == "" {
		return store.Page{}, toolErr("page_id is required.")
	}
	p, err := t.s.st.Page(id)
	if err != nil || !p.DeletedAt.IsZero() {
		return store.Page{}, toolErr("No page " + id + ".")
	}
	if p.Template {
		return store.Page{}, toolErr("Page " + id + " is a template, which agents leave alone.")
	}
	if edit && !t.s.st.CanEdit(p) {
		return store.Page{}, toolErr("The page is shared with the user to view, not to edit.")
	}
	return p, nil
}

func (t *pageTools) onPage(name string, args map[string]any) (map[string]any, error) {
	st := t.s.st
	p, err := t.page(args, name != "read_page")
	if err != nil {
		return nil, err
	}
	args = maps.Clone(args)
	offset, limit, err := agents.PartArgs(args)
	if err != nil {
		return nil, err
	}
	delete(args, "page_id")
	delete(args, "offset")
	delete(args, "limit")
	var out map[string]any
	var saved *doc.Content
	snapped := false
	err = st.UpdateContent(p.ID, func(content *doc.Content, kept bool) (bool, error) {
		if !kept {
			return false, toolErr("The page's content is not kept on this device.")
		}
		tc := agents.PageTools(st, p.ID, p.DisplayTitle(), t.s.agent, content, false)
		tc.SiteInfo = true
		if name == "read_page" {
			out = tc.ReadPart(p.ID, map[string]any{"id": p.ID, "title": p.DisplayTitle(), "tags": p.Tags}, content, offset, limit)
			t.keep(p.ID, content)
			return false, nil
		}
		before := content.Copy()
		x := &agents.Executor{Ctx: tc}
		out = x.Call(name, args)
		if len(x.Ops) == 0 {
			return false, nil
		}
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
		return nil, errUnreadable
	case errors.Is(err, store.ErrContentChanged):
		return nil, toolErr("The page kept changing while saving. Read it again and retry.")
	case err != nil:
		return nil, err
	}
	if saved != nil {
		t.edited[p.ID] = doc.NowMs()
		t.keep(p.ID, saved.Copy())
		if err := st.SetValue(editedPrefix+p.ID, editMark(t.s.agent, saved)); err != nil {
			log.Printf("mcp: %v", err)
		}
	}
	return out, nil
}

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

var beforeSave = func(pageID string) {}

const editedPrefix = "mcp_edited:"

func editMark(agent string, c *doc.Content) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + (&doc.Content{Children: c.Children}).Encode()))
	return hex.EncodeToString(sum[:])
}

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
		if err := t.linkSubpage(parent, p.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"page_id": p.ID}, nil
}

func (t *pageTools) movePage(args map[string]any) (map[string]any, error) {
	st := t.s.st
	p, err := t.page(args, true)
	if err != nil {
		return nil, err
	}
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

func (t *pageTools) linkSubpage(parent store.Page, id string) error {
	return t.editLinks(parent, func(c *doc.Content) bool {
		if len(subpageLinks(c, id)) > 0 {
			return false
		}
		_ = c.Insert(doc.Position{At: "end"}, &doc.Node{ID: doc.NewBlockID(), Type: "subpage", Data: map[string]any{"page_id": id}})
		return true
	})
}

func (t *pageTools) unlinkSubpage(parent store.Page, id string) error {
	return t.editLinks(parent, func(c *doc.Content) bool {
		links := subpageLinks(c, id)
		for _, l := range links {
			c.Remove(l)
		}
		return len(links) > 0
	})
}

func subpageLinks(c *doc.Content, id string) []string {
	var out []string
	c.Walk(func(n, _ *doc.Node) {
		if n.Type == "subpage" && n.Str("page_id") == id {
			out = append(out, n.ID)
		}
	})
	return out
}

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
