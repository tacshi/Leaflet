package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/maphash"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"leaflet/internal/agents"
	"leaflet/internal/doc"
	mapsvc "leaflet/internal/maps"
	"leaflet/internal/store"
)

var pageToolNames = []string{"read_page", "write_markdown", "insert_blocks", "replace_block", "move_block", "delete_blocks"}

// hints are each tool's title and what it does to the user's pages and the
// world: an open world is one beyond the pages, as every change of them
// syncs, and the map services finding places and routes ask on the web.
var hints = map[string]struct {
	title                            string
	readOnly, destructive, openWorld bool
}{
	"list_pages":     {"List Pages", true, false, false},
	"search_pages":   {"Search Pages", true, false, false},
	"current_page":   {"Current Page", true, false, false},
	"read_page":      {"Read Page", true, false, false},
	"write_markdown": {"Write Markdown", false, false, true},
	"insert_blocks":  {"Insert Blocks", false, false, true},
	"replace_block":  {"Replace Block", false, true, true},
	"move_block":     {"Move Block", false, true, true},
	"delete_blocks":  {"Delete Blocks", false, true, true},
	"create_page":    {"Create Page", false, false, true},
	"rename_page":    {"Rename Page", false, true, true},
	"set_page_tags":  {"Set Page Tags", false, true, true},
	"move_page":      {"Move Page", false, true, true},
	"create_folder":  {"Create Folder", false, false, true},
	"rename_folder":  {"Rename Folder", false, true, true},
	"move_folder":    {"Move Folder", false, true, true},
	"upload_files":   {"Upload Files", false, false, true},
	"find_place":     {"Find Place", true, false, true},
	"find_route":     {"Find Route", true, false, true},
	"read_skill":     {"Read Skill", true, false, false},
}

// tools is the tools the server offers, made once, and toolNames their names.
var (
	tools     = sync.OnceValue(toolList)
	toolNames = sync.OnceValue(func() map[string]bool {
		names := map[string]bool{}
		for _, t := range tools() {
			names[t["name"].(string)] = true
		}
		return names
	})
)

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
		annotations := map[string]any{"title": h.title, "readOnlyHint": h.readOnly, "openWorldHint": h.openWorld}
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
	add("list_pages", "List the user's pages (IDs, titles, tags, the page or folder each is in) and folders (IDs, names, the folder each is in). Many pages come in parts, the folders with the first: total_pages says how many pages there are, and next_offset, while there are more, is the offset to list on from. search_pages finds a page without listing them all.",
		object(map[string]any{"offset": map[string]any{"type": "integer", "minimum": 0, "description": "The next_offset an earlier part gave, to list on from; none for the first part."}}))
	add("search_pages", "Find pages by title, tag or text, recordings by transcript, and comments. Words starting with # (as #work) keep to the pages with those tags; alone they list them.",
		object(map[string]any{"query": map[string]any{"type": "string"}}, "query"))
	add("current_page", "Find what the user has in front of them in the Leaflet app: the open page, and its selection or caret, with the selected text, its blocks and offsets, and its text color and highlight as hex. open is false when no page is open now, with the page opened last, if any, and no selection.", object(map[string]any{}))
	skill := agents.SkillToolDefinition(true)["function"].(map[string]any)
	add("read_skill", skill["description"].(string), skill["parameters"].(map[string]any))
	for _, name := range pageToolNames {
		fn := defs[name]
		params := fn["parameters"].(map[string]any)
		props := agents.Properties(params)
		props["page_id"] = pageID
		required := append([]string{"page_id"}, params["required"].([]string)...)
		desc := fn["description"].(string)
		switch name {
		case "read_page":
			desc = "Read a page: its tags, every block with its ID and version, subpage titles, and comment threads. A long page comes in parts: total_blocks says how many blocks it has, and next_offset, while there are more, is the offset to read on from; more_subpages and more_comments count the subpages and comment threads a very long list leaves out. An embed's html longer than 30,000 characters is left out, its html_length given instead: to change such an embed, write its html whole. Give a block's version to replace_block or delete_blocks to leave the block as it is if someone changes it after you read it."
		case "replace_block":
			props["version"] = map[string]any{"type": "string", "description": "The block's version from read_page. If the block has changed since, the page is left as it is, and the answer shows what the block holds now, with its new version."}
		case "delete_blocks":
			props["versions"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
				"description": "Each block's version from read_page, by its ID. If one has changed since, nothing is deleted, and the answer shows what it holds now, with its new version."}
		}
		add(name, desc, object(props, required...))
	}
	add("upload_files", "Add files on this computer to a page, as the user's Upload in the app does: a picture shows in the page, and any other file as a card that opens it, audio and video playing there. Pictures, audio, video and PDFs only. They go in the order given, each after the one before it, the first at the end of the page, or each where its position says. Returns each one's block ID, in order.",
		object(map[string]any{
			"page_id": pageID,
			"files": map[string]any{"type": "array", "minItems": 1, "maxItems": MaxUploads, "items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":     map[string]any{"type": "string", "description": "The file's absolute path on this computer, or one starting with ~/."},
					"alt":      map[string]any{"type": "string", "description": "For a picture: what it shows, for those who cannot see it. Without it, the file's name."},
					"position": agents.Properties(defs["insert_blocks"]["parameters"].(map[string]any))["position"],
				},
				"required": []string{"path"},
			}},
		}, "page_id", "files"))
	made := agents.Properties(defs["create_page"]["parameters"].(map[string]any))
	add("create_page", "Create a page, optionally with content, at the top level unless the user asks for it in a folder or inside a page. With parent_id it goes inside that page, and a link to it is added at the end of the parent.",
		object(map[string]any{
			"title":     made["title"],
			"markdown":  made["markdown"],
			"blocks":    made["blocks"],
			"parent_id": map[string]any{"type": "string", "description": "The page to put it inside, only when the user asks for a subpage or a page in or under that one, not because it is open; none for the top level."},
			"folder_id": map[string]any{"type": "string", "description": "The folder the user asks for it in, from list_pages, at the top level; not with parent_id."},
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
	for _, d := range agents.MapToolDefinitions() {
		fn := d["function"].(map[string]any)
		add(fn["name"].(string), fn["description"].(string), fn["parameters"].(map[string]any))
	}
	return out
}

const historyGap = 10 * time.Minute

type pageTools struct {
	s      *Server
	app    bool
	edited map[string]int64
	// read is what the client has seen of each page it read or edited, the
	// pages in order, the one seen last at the end, and kept how many blocks
	// they hold. A page whose sight was let go is forgot: what it saw then is
	// not known any more.
	read   map[string]sight
	order  []string
	kept   int
	forgot map[string]bool
	// routes are the routes find_route found this session, for the maps
	// its calls write to name by their ids.
	routes agents.RouteBook
	// findPlaces and findRoutes ask the map services, which tests answer
	// in their place.
	findPlaces func(q string, near *doc.LatLng) ([]mapsvc.Place, error)
	findRoutes func(from, to doc.LatLng, mode string, depart time.Time) ([]doc.MapRoute, error)
}

// keptReads is how many pages' sights a session keeps, and keptBlocks how
// many of their blocks, at most: the oldest are let go beyond them.
const (
	keptReads  = 256
	keptBlocks = 200_000
)

func (t *pageTools) keep(id string, c sight) {
	t.kept += len(c) - len(t.read[id])
	t.read[id] = c
	t.order = append(slices.DeleteFunc(t.order, func(k string) bool { return k == id }), id)
	for len(t.order) > 1 && (len(t.order) > keptReads || t.kept > keptBlocks) {
		old := t.order[0]
		t.kept -= len(t.read[old])
		delete(t.read, old)
		t.forgot[old] = true
		t.order = t.order[1:]
	}
}

func (t *pageTools) call(name string, args map[string]any) map[string]any {
	clean, _ := cleanArg(args).(map[string]any)
	out := agents.ToolAnswer(t.dispatch(name, clean))
	if out["error"] == doc.NeedsImageURL {
		out["error"] = doc.NeedsImageURL + " For a picture on this computer, use upload_files with its path."
	}
	return out
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
	case "find_place", "find_route":
		tc := agents.ToolContext{FindPlaces: t.findPlaces, FindRoutes: t.findRoutes}
		if tc.FindPlaces == nil {
			tc.FindPlaces = func(q string, near *doc.LatLng) ([]mapsvc.Place, error) {
				return mapsvc.FindPlaces(context.Background(), q, near, 5)
			}
		}
		if tc.FindRoutes == nil {
			tc.FindRoutes = func(from, to doc.LatLng, mode string, depart time.Time) ([]doc.MapRoute, error) {
				return mapsvc.FindRoutes(context.Background(), from, to, mode, depart)
			}
		}
		x := &agents.Executor{Ctx: tc}
		x.UseRoutes(&t.routes)
		out := x.Call(name, args)
		if e, ok := out["error"].(string); ok {
			return nil, toolErr(e)
		}
		return out, nil
	case "read_skill":
		return agents.ReadSkill(args, true)
	case "list_pages":
		return t.listPart(args)
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
		title, err := agents.TitleArg(args)
		if err != nil {
			return nil, err
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
		tags, err := agents.TagsArg(args)
		if err != nil {
			return nil, err
		}
		if err := st.SetPageTags(p.ID, tags); err != nil {
			return nil, err
		}
		p, _ = st.Page(p.ID)
		return map[string]any{"page_id": p.ID, "tags": p.Tags}, nil
	case "move_page":
		return t.movePage(args)
	case "upload_files":
		return t.uploadFiles(args)
	case "create_folder":
		name, err := agents.FolderNameArg(args)
		if err != nil {
			return nil, err
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
		name, err := agents.FolderNameArg(args)
		if err != nil {
			return nil, err
		}
		if err := st.RenameFolder(f, name); err != nil {
			return nil, err
		}
		return map[string]any{"folder_id": f.ID}, nil
	}
	if slices.Contains(pageToolNames, name) {
		return t.onPage(name, args, nil)
	}
	return nil, toolErr("Unknown tool " + name + ".")
}

func (t *pageTools) currentPage() (map[string]any, error) {
	st := t.s.st
	view, err := st.OpenView()
	if err != nil {
		return nil, err
	}
	// The app takes its view back as it quits; one it left as it crashed is
	// of no app now, and no page is open.
	if view != nil && !running(view.PID, view.Began) {
		view = nil
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

// listedAtOnce is how many pages a list_pages answer lists at most, and
// listBytes how many bytes of JSON its pages and folders take at most, so a
// client reads it whole: Claude Code, for one, reads no more than 25,000
// tokens of a tool's answer, which IDs and titles in other scripts fill at a
// few bytes a token.
const (
	listedAtOnce = 250
	listBytes    = 40_000
)

// listPart answers list_pages: the pages from offset on, as many as fit in an
// answer, with the folders in the first part, and while more pages are left,
// the offset to list on from.
func (t *pageTools) listPart(args map[string]any) (map[string]any, error) {
	offset, err := listOffset(args)
	if err != nil {
		return nil, err
	}
	all, err := t.listPages()
	if err != nil {
		return nil, err
	}
	pages, folders := all["pages"].([]map[string]any), all["folders"].([]map[string]any)
	out := map[string]any{"total_pages": len(pages)}
	spent := 0
	if offset == 0 {
		out["folders"] = folders
		spent = len(jsonText(folders))
	}
	offset = min(offset, len(pages))
	end := offset
	for end < len(pages) && end-offset < listedAtOnce {
		if spent += len(jsonText(pages[end])) + 1; spent > listBytes && end > offset {
			break
		}
		end++
	}
	out["pages"] = pages[offset:end]
	if end < len(pages) {
		out["next_offset"] = end
	}
	return out, nil
}

func listOffset(args map[string]any) (int, error) {
	switch v := args["offset"].(type) {
	case nil:
		return 0, nil
	case int:
		if v >= 0 {
			return v, nil
		}
	case float64:
		if v >= 0 && v == float64(int(v)) {
			return int(v), nil
		}
	}
	return 0, toolErr("offset must be a whole number, 0 or more: the next_offset list_pages gave.")
}

// listPages is every page and folder of the user's, as list_pages lists them.
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
	id, err := agents.ParentFolderArg(args)
	if err != nil || id == "" {
		return "", err
	}
	f, err := t.folder(id)
	return f.ID, err
}

// place is where a call puts a page: inside a page the user can edit, or in
// a folder of their space.
func (t *pageTools) place(args map[string]any) (parent store.Page, folderID string, err error) {
	parent, _, folderID, err = agents.Place(args, func(id string) (store.Page, error) {
		return t.page(map[string]any{"page_id": id}, true)
	}, func(id string) error {
		_, err := t.folder(id)
		return err
	})
	return parent, folderID, err
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

// onPage reads or edits a page with the tool name, or edits it with run, as
// that tool would: with its snapshot in history, a version from read_page
// checked, and a change not read kept as a conflict.
func (t *pageTools) onPage(name string, args map[string]any, run func(x *agents.Executor) map[string]any) (map[string]any, error) {
	st := t.s.st
	p, err := t.page(args, name != "read_page")
	if err != nil {
		return nil, err
	}
	args = maps.Clone(args)
	var offset, limit int
	if name == "read_page" {
		if offset, limit, err = agents.PartArgs(args); err != nil {
			return nil, err
		}
	}
	want, err := givenVersions(name, args)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"page_id", "offset", "limit", "version", "versions"} {
		delete(args, k)
	}
	var out map[string]any
	var saved, unsaved *doc.Content
	var ops []doc.Op
	var known sight
	// A page whose sight was let go was seen once, but how is not known: each
	// block an edit replaces or deletes may have changed since, until the
	// client reads the page again.
	stale := t.forgot[p.ID]
	// inHistory is the page as the history keeps it from before this edit: a
	// save that found the page changed edits it as it is now, and keeps that.
	inHistory := ""
	err = st.UpdateContent(p.ID, func(content *doc.Content, kept bool) (bool, error) {
		if !kept {
			return false, toolErr("The page's content is not kept on this device.")
		}
		tc := agents.PageTools(st, p.ID, p.DisplayTitle(), t.s.agent, content, false)
		tc.SiteInfo = true
		if name == "read_page" {
			// Each view has its version as the part is sized, so the part, versions
			// and all, keeps to what a part may hold.
			tc.Versions = blockVersions(content)
			out = tc.ReadPart(p.ID, map[string]any{"id": p.ID, "title": p.DisplayTitle(), "tags": p.Tags}, content, offset, limit)
			views, _ := out["blocks"].([]map[string]any)
			t.keep(p.ID, seen(content, t.read[p.ID], returned(content, views)))
			delete(t.forgot, p.ID)
			return false, nil
		}
		// A version that still matches shows the client has seen that block as
		// it is now; one that does not leaves the page as it is.
		known = t.read[p.ID]
		if stale && known == nil {
			known = sight{}
		}
		if len(want) > 0 {
			now := blockVersions(content)
			for _, id := range slices.Sorted(maps.Keys(want)) {
				if cur, ok := now[id]; ok && cur != want[id] {
					return false, changedSinceRead(tc, p.ID, content, id, cur)
				}
			}
			known = seen(content, known, withCells(content, want.ids()))
		}
		before := content.Copy()
		x := &agents.Executor{Ctx: tc}
		x.UseRoutes(&t.routes)
		if run != nil {
			out = run(x)
		} else {
			out = x.Call(name, args)
		}
		if len(x.Ops) == 0 || out["error"] != nil {
			return false, nil
		}
		// A change the agent never read makes the version before its edit a conflict,
		// kept once and shown as one, rather than a second copy beside its snapshot,
		// and the agent is told, to tell the user or work the change back in.
		changed := conflictSince(known, stale, before, x.Ops)
		read := before.Encode()
		switch {
		case len(changed) > 0:
			if read != inHistory {
				if err := t.s.st.AddHistoryEncoded(p.ID, read, "conflict", "", ""); err != nil {
					return false, err
				}
			}
			out["conflict"] = conflictNote(changed, stale)
		case read != inHistory:
			if err := t.snapshot(p.ID, before, read); err != nil {
				return false, err
			}
		}
		inHistory = read
		beforeSave(p.ID)
		saved, unsaved, ops = content, before, x.Ops
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
		t.keep(p.ID, seen(saved, known, written(unsaved, saved)))
		if err := st.SetValue(editedPrefix+p.ID, editMark(t.s.agent, saved)); err != nil {
			log.Printf("mcp: %v", err)
		}
		t.s.noteActivity(name, p.ID, unsaved, saved, ops)
	}
	return out, nil
}

// sight is what the client has seen of a page: each block it has seen, by ID,
// as it last saw the block itself. The blocks nested in one are blocks of their
// own, seen or not apart from it, as a version is of a block's own view.
type sight map[string]uint64

var ownSeed = maphash.MakeSeed()

// own is the block itself as a sight keeps it, a hash of its type and data,
// and of a table's rows and cells, which its view shows, but of no nested
// block: a session keeps the sights of many pages.
func own(n *doc.Node) uint64 {
	self := &doc.Node{Type: n.Type, Data: n.Data}
	if n.Type == "table" {
		self.Children = n.Children
	}
	raw, _ := json.Marshal(self)
	return maphash.Bytes(ownSeed, raw)
}

// seen is what the client has seen of the page now: a block in fresh as it is
// now, any other as it last saw it, so a change it has not read since stays a
// change, and one it has never seen, as someone added it since, not at all. Its
// first look at the page, read or edit, sees every block as it is then.
func seen(now *doc.Content, last sight, fresh map[string]bool) sight {
	out := sight{}
	now.Walk(func(n, _ *doc.Node) {
		if was, ok := last[n.ID]; ok && !fresh[n.ID] {
			out[n.ID] = was
		} else if last == nil || fresh[n.ID] {
			out[n.ID] = own(n)
		}
	})
	return out
}

// returned is the blocks of c a read_page answer holds: those it lists, and
// the rows and cells of a table it lists, which come with it.
func returned(c *doc.Content, views []map[string]any) map[string]bool {
	listed := map[string]bool{}
	for _, v := range views {
		if id, ok := v["id"].(string); ok {
			listed[id] = true
		}
	}
	return withCells(c, listed)
}

// withCells is the blocks of c ids names, with the rows and cells of a table
// among them, which its view and its version hold.
func withCells(c *doc.Content, ids map[string]bool) map[string]bool {
	out := map[string]bool{}
	var visit func([]*doc.Node, bool)
	visit = func(nodes []*doc.Node, in bool) {
		for _, n := range nodes {
			mine := in || ids[n.ID]
			if mine {
				out[n.ID] = true
			}
			visit(n.Children, mine && (in || n.Type == "table"))
		}
	}
	visit(c.Children, false)
	return out
}

// written is the blocks of after the client's edit made or changed.
func written(before, after *doc.Content) map[string]bool {
	was := map[string]uint64{}
	before.Walk(func(n, _ *doc.Node) { was[n.ID] = own(n) })
	out := map[string]bool{}
	after.Walk(func(n, _ *doc.Node) {
		if w, ok := was[n.ID]; !ok || w != own(n) {
			out[n.ID] = true
		}
	})
	return out
}

// conflictSince is the blocks of now that ops replace or delete though someone
// changed them since the client saw them, in the order of the ops: a block not
// as the client last saw it, and, of the blocks nested in the one an op names
// that go with it, one the client never saw, as someone added it since. A
// nested block the op keeps, as a rewrite of a list item's text keeps its
// nested items as they are, is not replaced. With stale, as the sight of a
// page seen long ago was let go, a block the client has not seen since may
// have changed too, the one an op names as well.
func conflictSince(read sight, stale bool, now *doc.Content, ops []doc.Op) []string {
	if read == nil {
		return nil
	}
	var out []string
	check := func(id string, named bool) {
		is := now.Find(id)
		if is == nil || slices.Contains(out, id) {
			return
		}
		if was, ok := read[id]; ok && was != own(is) || !ok && (!named || stale) {
			out = append(out, id)
		}
	}
	var takenOut func(n *doc.Node, kept map[string]bool)
	takenOut = func(n *doc.Node, kept map[string]bool) {
		if n.Type == "table" {
			return
		}
		for _, k := range n.Children {
			if !kept[k.ID] {
				check(k.ID, false)
			}
			takenOut(k, kept)
		}
	}
	for _, op := range ops {
		switch op.Op {
		case "replace":
			check(op.ID, true)
			if op.PrevNode != nil && op.Node != nil {
				kept := map[string]bool{}
				(&doc.Content{Children: op.Node.Children}).Walk(func(k, _ *doc.Node) { kept[k.ID] = true })
				takenOut(op.PrevNode, kept)
			}
		case "delete":
			for _, r := range op.Removed {
				check(r.Node.ID, true)
				takenOut(r.Node, nil)
			}
		}
	}
	return out
}

func conflictNote(ids []string, stale bool) string {
	if stale {
		blocks := "block " + ids[0]
		if len(ids) > 1 {
			blocks = "blocks " + strings.Join(ids, ", ")
		}
		return "You read this page too long ago for Leaflet to tell whether " + blocks + " changed since, so this edit may have replaced a change. The page as it was before the edit is kept in its history, as a conflict. Read the page again before you edit it further, and tell the user if what you replaced was theirs."
	}
	blocks := "Block " + ids[0] + " had"
	if len(ids) > 1 {
		blocks = "Blocks " + strings.Join(ids, ", ") + " had"
	}
	return blocks + " changed since you read the page, and this edit replaced that change. The page as it was before the edit is kept in its history, as a conflict. Read the page to see what changed, then tell the user or work the change back in."
}

// versionSet is the versions a call gives, by block ID: those it read the
// blocks at.
type versionSet map[string]string

func (v versionSet) ids() map[string]bool {
	out := map[string]bool{}
	for id := range v {
		out[id] = true
	}
	return out
}

func givenVersions(name string, args map[string]any) (versionSet, error) {
	switch name {
	case "replace_block":
		raw, ok := args["version"]
		if !ok || raw == nil {
			return nil, nil
		}
		v, _ := raw.(string)
		id, _ := args["id"].(string)
		if v == "" || id == "" {
			return nil, toolErr("version must be the block's version from read_page.")
		}
		return versionSet{id: v}, nil
	case "delete_blocks":
		raw, ok := args["versions"]
		if !ok || raw == nil {
			return nil, nil
		}
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, toolErr("versions must be an object: each block's version from read_page, by its ID.")
		}
		out := versionSet{}
		for id, raw := range m {
			v, _ := raw.(string)
			if v == "" {
				return nil, toolErr("versions must be an object: each block's version from read_page, by its ID.")
			}
			out[id] = v
		}
		return out, nil
	}
	return nil, nil
}

// blockVersions is each block's version, as read_page gives it: a hash of what
// read_page shows of the block, without where it sits, so a move keeps it.
func blockVersions(c *doc.Content) map[string]string {
	out := map[string]string{}
	for _, v := range doc.BlockViews(c) {
		id, _ := v["id"].(string)
		out[id] = versionOf(v)
	}
	return out
}

func versionOf(v map[string]any) string {
	shown := maps.Clone(v)
	for _, k := range []string{"id", "depth", "parent_id", "version"} {
		delete(shown, k)
	}
	raw, _ := json.Marshal(shown)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:5])
}

func changedSinceRead(tc agents.ToolContext, pageID string, c *doc.Content, id, version string) error {
	for _, v := range doc.BlockViews(c) {
		if v["id"] != id {
			continue
		}
		tc.Finish(pageID, v)
		v["version"] = version
		raw, _ := json.Marshal(v)
		return toolErr("Block " + id + " has changed since you read it, so the page was left as it is. It now reads " + string(raw) + ". Work that in, and try again with its new version.")
	}
	return toolErr("Block " + id + " has changed since you read it, so the page was left as it is. Read the page again.")
}

var beforeSave = func(pageID string) {}

const editedPrefix = "mcp_edited:"

func editMark(agent string, c *doc.Content) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + (&doc.Content{Children: c.Children}).Encode()))
	return hex.EncodeToString(sum[:])
}

// snapshot keeps before, encoded as encoded, in the page's history ahead of
// the agent's edit, labeled with the agent's name, which the app words as it
// shows it: not when the page is as the agent's own last edit of the last
// minutes left it, whose history has it from before that.
func (t *pageTools) snapshot(pageID string, before *doc.Content, encoded string) error {
	label := agents.RunLabel(t.s.agent, "")
	if mark, ok := t.s.st.Value(editedPrefix + pageID); ok && mark == editMark(t.s.agent, before) {
		if last, ok := t.edited[pageID]; ok && doc.NowMs()-last < historyGap.Milliseconds() {
			return nil
		}
		if h, err := t.s.st.NewestHistory(pageID); err == nil && h.Reason == "agent_run" && h.Label == label && time.Since(h.CreatedAt) < historyGap {
			return nil
		}
	}
	return t.s.st.AddHistoryEncoded(pageID, encoded, "agent_run", "", label)
}

func (t *pageTools) createPage(args map[string]any) (map[string]any, error) {
	st := t.s.st
	// A page made with its content goes around the executor, whose edits
	// name find_route's routes by their ids.
	args, err := agents.ResolveRoutes(&t.routes, "create_page", args)
	if err != nil {
		return nil, err
	}
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
	t.s.noteActivity("create_page", p.ID, nil, content, nil)
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
	case t.app && parent.ID != "" && publiclyLinked(st, parent.ID):
		return nil, toolErr("That page has a public link: inside it, this page's title would show to anyone with the link. The user can move it there themselves, in the app.")
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

// publiclyLinked is whether a page has a public link, as far as can be told: a
// move only shows a page's title.
func publiclyLinked(st *store.Store, pageID string) bool {
	linked, _ := st.PublicLink(pageID)
	return linked
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
