package agents

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

const WorkspaceID = "workspace"

type WorkspacePage struct {
	ID, Title, ParentID, FolderID string
	Tags                          []string
	Shared                        bool
	People                        bool
	Untitled                      bool
}

func (p WorkspacePage) title() string {
	if p.Untitled {
		return ""
	}
	return p.Title
}

type Workspace struct {
	Reading      bool
	Pages        func() ([]WorkspacePage, error)
	Page         func(id string) (WorkspacePage, bool)
	ReadPage     func(id string) (map[string]any, error)
	Search       func(query string) ([]map[string]any, error)
	Folders      func() []map[string]any
	CreatePage   func(title string, nodes []*doc.Node, parentID, folderID string) (doc.NewPage, error)
	MovePage     func(id, parentID, folderID string) (from, to doc.PagePlace, err error)
	RenamePage   func(id, title string) error
	SetTags      func(id string, tags []string) ([]string, error)
	CreateFolder func(name, parentID string) (doc.NewFolder, error)
	MoveFolder   func(id, parentID string) (name string, from, to doc.FolderPlace, err error)
}

func WorkspaceToolDefinitions(reading bool) []map[string]any {
	pageID := map[string]any{"type": "string", "description": "The page's ID, from list_pages or search_pages, or the open page's."}
	var out []map[string]any
	if reading {
		out = append(out,
			tool("list_pages", "List the user's pages, at most 500, fewer where their titles are long (more_pages says how many more; search_pages finds them): their IDs, titles and tags, the page or folder each is in, and whether others share it; and their folders.", map[string]any{}),
			tool("search_pages", "Find the user's pages by title, tag or text. Words starting with # (as #work) keep to the pages with those tags.",
				map[string]any{"query": map[string]any{"type": "string"}}, "query"),
			tool("read_page", "Read one of the user's pages: every block with its ID, its tags and its subpages. Read-only.", map[string]any{"page_id": pageID}, "page_id"))
	}
	return append(out,
		tool("list_folders", "List the user's folders: their names and IDs, and the folder each is in.", map[string]any{}),
		tool("create_page", "Create a page, optionally with content: at the top level of the user's pages, in one of their folders, or inside one of their pages, at whose end a link to it goes.",
			pageSchema(prop{"parent_id", map[string]any{"type": "string", "description": "The page to put it inside; none for the top level."}},
				prop{"folder_id", map[string]any{"type": "string", "description": "The folder to put it in, at the top level; not with parent_id."}}), "title"),
		tool("move_page", "Move a page, with its subpages: inside another page, at whose end a link to it goes; into a folder; or with neither to the top level outside folders.",
			ordered{{"page_id", pageID}, {"parent_id", map[string]any{"type": "string", "description": "The page to put it inside."}},
				{"folder_id", map[string]any{"type": "string", "description": "The folder to put it in, at the top level; not with parent_id."}}}, "page_id"),
		tool("rename_page", "Change a page's title.", ordered{{"page_id", pageID}, {"title", map[string]any{"type": "string"}}}, "page_id", "title"),
		tool("set_page_tags", "Set a page's tags, replacing the ones it has: an empty list takes them all off. Tags are short words or phrases, without #; reuse the user's where they fit.",
			ordered{{"page_id", pageID}, {"tags", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}, "page_id", "tags"),
		tool("create_folder", "Create a folder for top-level pages, last among the folders where it goes: at the top level, or in another folder.",
			ordered{{"name", map[string]any{"type": "string"}}, {"parent_folder_id", parentFolder}}, "name"),
		tool("move_folder", "Move a folder, with what is in it, last into another folder, or with no parent_folder_id to the top level.",
			ordered{{"folder_id", map[string]any{"type": "string", "description": "The folder to move, from list_folders."}}, {"parent_folder_id", parentFolder}}, "folder_id"))
}

var parentFolder = map[string]any{"type": "string", "description": "The folder to put it in, from list_folders; none for the top level."}

func (x *Executor) workspace(name string, args map[string]any) (map[string]any, error) {
	w := x.Ctx.Workspace
	switch name {
	case "list_pages", "search_pages", "read_page":
		if !w.Reading {
			return nil, terr("Reading the user's pages is off.")
		}
	}
	switch name {
	case "list_pages":
		x.armed, x.readPages = true, true
		all, err := w.Pages()
		if err != nil {
			return nil, terr("Could not read the user's pages: %v", err)
		}
		folders := w.Folders()
		pages := []map[string]any{}
		for _, p := range all[:min(len(all), maxListedPages)] {
			pages = append(pages, pageView(p))
		}
		pages = pages[:listed(len(pages), jsonCost(folders), func(i int) int { return jsonCost(pages[i]) })]
		out := map[string]any{"pages": pages, "folders": folders}
		if more := len(all) - len(pages); more > 0 {
			out["more_pages"] = more
		}
		return out, nil
	case "search_pages":
		q, err := str(args, "query")
		if err != nil {
			return nil, err
		}
		x.armed, x.readPages = true, true
		results, err := w.Search(strings.TrimSpace(q))
		if err != nil {
			return nil, terr("The search failed: %v", err)
		}
		for _, r := range results {
			id, _ := r["page_id"].(string)
			if p, ok := w.Page(id); !ok || p.People {
				delete(r, "snippet")
			}
		}
		return map[string]any{"results": results}, nil
	case "read_page":
		p, err := x.workspacePage(args, "page_id")
		if err != nil {
			return nil, err
		}
		if p.People {
			return nil, terr("“%s” is shared with other people, who write in it too: the agent here reads none of it. To work with it, ask the agent on that page.", p.Title)
		}
		x.armed, x.readPages = true, true
		out, err := w.ReadPage(p.ID)
		if err != nil {
			return nil, terr("Could not read “%s”: %v", p.Title, err)
		}
		return out, nil
	case "list_folders":
		x.saw()
		return map[string]any{"folders": w.Folders()}, nil
	case "create_page":
		return x.workspaceCreate(args)
	case "move_page":
		return x.workspaceMove(args)
	case "rename_page":
		return x.workspaceRename(args)
	case "set_page_tags":
		return x.workspaceTags(args)
	case "create_folder":
		return x.workspaceFolder(args)
	case "move_folder":
		return x.workspaceMoveFolder(args)
	case "web_search":
		return x.searchWeb(args)
	case "read_web_page":
		return x.readWeb(args)
	}
	return nil, terr("Unknown tool %q.", name)
}

func pageView(p WorkspacePage) map[string]any {
	v := map[string]any{"id": p.ID, "title": p.Title}
	if len(p.Tags) > 0 {
		v["tags"] = p.Tags
	}
	if p.ParentID != "" {
		v["parent_id"] = p.ParentID
	}
	if p.FolderID != "" {
		v["folder_id"] = p.FolderID
	}
	if p.Shared {
		v["shared"] = true
	}
	if p.People {
		v["shared_with_people"] = true
	}
	return v
}

func (x *Executor) workspacePage(args map[string]any, key string) (WorkspacePage, error) {
	id, err := str(args, key)
	if err != nil {
		return WorkspacePage{}, err
	}
	p, ok := x.Ctx.Workspace.Page(id)
	if !ok {
		return WorkspacePage{}, terr("No page %q of the user's. Call list_pages or search_pages for the pages' IDs.", id)
	}
	return p, nil
}

func changeable(p WorkspacePage) error {
	if p.Shared {
		return terr("“%s” is shared with others: the agent here leaves it as it is, and puts nothing in it. To change it, ask the agent on that page.", p.Title)
	}
	return nil
}

func (x *Executor) place(args map[string]any) (parent WorkspacePage, folder string, err error) {
	parentID, err := optionalString(args, "parent_id")
	if err != nil {
		return parent, "", err
	}
	if folder, err = optionalString(args, "folder_id"); err != nil {
		return parent, "", err
	}
	if parentID == "" {
		return parent, folder, nil
	}
	if folder != "" {
		return parent, "", terr("Give parent_id or folder_id, not both: a page inside another is in no folder.")
	}
	if parent, err = x.workspacePage(args, "parent_id"); err == nil {
		err = changeable(parent)
	}
	return parent, "", err
}

func (x *Executor) workspaceCreate(args map[string]any) (map[string]any, error) {
	title, nodes, err := PageNodes(args, x.Ctx.AgentName)
	if err != nil {
		return nil, err
	}
	parent, folder, err := x.place(args)
	if err != nil {
		return nil, err
	}
	if err := x.checkURLs(nodes, title); err != nil {
		return nil, err
	}
	if err := x.room(nodes); err != nil {
		return nil, err
	}
	made, err := x.Ctx.Workspace.CreatePage(title, nodes, parent.ID, folder)
	if err != nil {
		return nil, err
	}
	x.written += countNodes(nodes)
	summary := locales.AgentEditCreatedPage(title)
	if parent.ID != "" {
		summary = locales.AgentEditCreatedSubpage(title)
	}
	made.ParentID, made.FolderID, made.Title = parent.ID, folder, title
	x.record(doc.Op{Op: "create_page", Page: &made}, summary)
	return map[string]any{"page_id": made.ID}, nil
}

func (x *Executor) workspaceMove(args map[string]any) (map[string]any, error) {
	p, err := x.workspacePage(args, "page_id")
	if err != nil {
		return nil, err
	}
	if err := changeable(p); err != nil {
		return nil, err
	}
	parent, folder, err := x.place(args)
	if err != nil {
		return nil, err
	}
	if parent.ID == p.ID {
		return nil, terr("A page cannot move inside itself.")
	}
	from, to, err := x.Ctx.Workspace.MovePage(p.ID, parent.ID, folder)
	if errors.Is(err, store.ErrMoveIntoSelf) {
		return nil, terr("A page cannot move inside itself or one of its subpages.")
	}
	if err != nil {
		return nil, err
	}
	if !from.Same(to) {
		x.record(doc.Op{Op: "move_page", PageID: p.ID, Title: p.title(), Place: &to, PrevPlace: &from}, locales.AgentEditMovedPage(p.Title))
	}
	return map[string]any{"page_id": p.ID}, nil
}

func (x *Executor) workspaceRename(args map[string]any) (map[string]any, error) {
	p, err := x.workspacePage(args, "page_id")
	if err != nil {
		return nil, err
	}
	if err := changeable(p); err != nil {
		return nil, err
	}
	title, err := str(args, "title")
	if err != nil {
		return nil, err
	}
	title = store.ClampTitle(strings.TrimSpace(title))
	if err := x.checkURLs(nil, title); err != nil {
		return nil, err
	}
	if had := p.title(); title != had {
		if err := x.Ctx.Workspace.RenamePage(p.ID, title); err != nil {
			return nil, err
		}
		x.record(doc.Op{Op: "rename_page", PageID: p.ID, Title: title, PrevTitle: had}, locales.AgentEditRenamedPage(p.Title, title))
	}
	return map[string]any{"page_id": p.ID}, nil
}

func (x *Executor) workspaceTags(args map[string]any) (map[string]any, error) {
	p, err := x.workspacePage(args, "page_id")
	if err != nil {
		return nil, err
	}
	if err := changeable(p); err != nil {
		return nil, err
	}
	raw, ok := args["tags"].([]any)
	if !ok {
		return nil, terr("tags must be a list of words.")
	}
	tags := make([]string, 0, len(raw))
	for _, t := range raw {
		s, ok := t.(string)
		if !ok {
			return nil, terr("tags must be a list of words.")
		}
		tags = append(tags, s)
	}
	if err := x.checkURLs(nil, tags); err != nil {
		return nil, err
	}
	kept, err := x.Ctx.Workspace.SetTags(p.ID, tags)
	if err != nil {
		return nil, err
	}
	if !sameTags(kept, p.Tags) {
		x.record(doc.Op{Op: "set_page_tags", PageID: p.ID, Title: p.title(), Tags: kept, PrevTags: p.Tags}, locales.AgentEditTaggedPage(p.Title))
	}
	return map[string]any{"page_id": p.ID, "tags": kept}, nil
}

func sameTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (x *Executor) workspaceFolder(args map[string]any) (map[string]any, error) {
	name, err := str(args, "name")
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if err := x.checkURLs(nil, name); err != nil {
		return nil, err
	}
	parent, err := optionalString(args, "parent_folder_id")
	if err != nil {
		return nil, err
	}
	f, err := x.Ctx.Workspace.CreateFolder(name, strings.TrimSpace(parent))
	if err != nil {
		return nil, err
	}
	x.record(doc.Op{Op: "create_folder", Folder: &f}, locales.AgentEditCreatedFolder(name))
	return map[string]any{"folder_id": f.ID}, nil
}

func (x *Executor) workspaceMoveFolder(args map[string]any) (map[string]any, error) {
	id, err := str(args, "folder_id")
	if err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	parent, err := optionalString(args, "parent_folder_id")
	if err != nil {
		return nil, err
	}
	name, from, to, err := x.Ctx.Workspace.MoveFolder(id, strings.TrimSpace(parent))
	if errors.Is(err, store.ErrFolderIntoSelf) {
		return nil, terr("A folder cannot move into itself or a folder inside it.")
	}
	if err != nil {
		return nil, err
	}
	if from != to {
		x.record(doc.Op{Op: "move_folder", FolderID: id, Title: name, FolderPlace: &to, PrevFolderPlace: &from}, locales.AgentEditMovedFolder(name))
	}
	return map[string]any{"folder_id": id}, nil
}

func (h *Hub) workspaceTools(ctx context.Context, agent string, settings store.Settings, online bool) ToolContext {
	st := h.st
	space := st.Session().SpaceID
	mine := func(p store.Page) bool { return p.SpaceID == space && !p.Template && p.DeletedAt.IsZero() }
	elsewhere := h.publicElsewhereIn(ctx, space, online)
	read := func(p store.Page, private func(store.Page) bool) bool { return !private(p) || elsewhere(p.ID) }
	shown := sync.OnceValue(func() func(id string) bool {
		pages, err := st.Pages()
		if err != nil {
			return func(string) bool { return true }
		}
		private, _ := h.privacy(pages)
		others := map[string]bool{}
		for _, p := range pages {
			if mine(p) && read(p, private) {
				others[p.ID] = true
			}
		}
		on := map[string]bool{}
		for _, p := range pages {
			if others[p.ParentID] {
				on[p.ID] = true
			}
		}
		for id := range others {
			if c := st.Content(id); c != nil {
				c.Walk(func(n, _ *doc.Node) {
					if n.Type == "subpage" {
						on[n.Str("page_id")] = true
					}
					if !n.IsText() {
						return
					}
					for _, r := range n.Delta() {
						if m := r.Attrs.Mention; m != nil && m.Kind == "page" {
							on[m.ID] = true
						}
					}
				})
			}
		}
		return func(id string) bool { return on[id] }
	})
	view := func(p store.Page, private, people func(store.Page) bool) WorkspacePage {
		return WorkspacePage{ID: p.ID, Title: p.DisplayTitle(), ParentID: p.ParentID, FolderID: p.FolderID, Tags: p.Tags,
			Shared: read(p, private) || shown()(p.ID), People: people(p), Untitled: strings.TrimSpace(p.Title) == ""}
	}
	page := func(id string) (WorkspacePage, bool) {
		p, err := st.Page(id)
		if err != nil || !mine(p) {
			return WorkspacePage{}, false
		}
		return view(p, h.private, func(p store.Page) bool { return sharedWithOthers(st, p) }), true
	}
	reader := ToolContext{Transcript: func(pageID, id string) string {
		rec, err := st.Recording(id)
		if err != nil || rec.PageID != pageID {
			return ""
		}
		return TranscriptText(rec.Transcript)
	}}
	w := &Workspace{
		Reading: settings.AgentsReadOtherPages,
		Pages: func() ([]WorkspacePage, error) {
			pages, err := st.Pages()
			if err != nil {
				return nil, err
			}
			private, people := h.privacy(pages)
			out := []WorkspacePage{}
			for _, p := range pages {
				if mine(p) {
					out = append(out, view(p, private, people))
				}
			}
			return out, nil
		},
		Page: page,
		ReadPage: func(id string) (map[string]any, error) {
			p, err := st.Page(id)
			if err != nil {
				return nil, err
			}
			c, err := stored(st, id)
			if err != nil {
				return nil, err
			}
			subpages := []map[string]any{}
			if kids, err := st.Children(id, ""); err == nil {
				for _, k := range kids {
					subpages = append(subpages, map[string]any{"id": k.ID, "title": k.DisplayTitle()})
				}
			}
			info := map[string]any{"id": id, "title": p.DisplayTitle()}
			if len(p.Tags) > 0 {
				info["tags"] = p.Tags
			}
			return map[string]any{"page": info, "blocks": reader.viewsOf(id, c), "subpages": subpages}, nil
		},
		Search: func(query string) ([]map[string]any, error) {
			hits, err := st.SearchPages(ctx, query, 20, mine)
			if err != nil {
				return nil, err
			}
			_, words := store.SplitTagQuery(query)
			out := []map[string]any{}
			for _, hit := range hits {
				r := map[string]any{"page_id": hit.PageID, "title": hit.Title}
				if hit.Snippet != "" {
					if s := ownSnippet(st, hit.PageID, words); s != "" {
						r["snippet"] = s
					}
				}
				out = append(out, r)
			}
			return out, nil
		},
		Folders: func() []map[string]any { return spaceFolders(st) },
		CreatePage: func(title string, nodes []*doc.Node, parentID, folderID string) (doc.NewPage, error) {
			if folderID != "" && !spaceFolder(st, folderID) {
				return doc.NewPage{}, noFolder(folderID)
			}
			var c *doc.Content
			if len(nodes) > 0 {
				c = doc.FromBlocks(nodes)
			}
			p, err := st.CreatePage(store.NewPageOptions{SpaceID: space, ParentID: parentID, FolderID: folderID, Title: title, Content: c})
			if err != nil {
				return doc.NewPage{}, err
			}
			made := doc.NewPage{ID: p.ID, SpaceID: p.SpaceID, ParentID: p.ParentID, FolderID: p.FolderID, Title: p.Title, SortKey: p.SortKey}
			if parentID == "" {
				return made, nil
			}
			if err := h.onMain(ctx, func() error { return h.pages().LinkSubpage(parentID, p.ID, "") }); err != nil {
				log.Printf("agent subpage card: %v", err)
			}
			return made, nil
		},
		MovePage: func(id, parentID, folderID string) (from, to doc.PagePlace, err error) {
			if folderID != "" && !spaceFolder(st, folderID) {
				return from, to, noFolder(folderID)
			}
			p, err := st.Page(id)
			if err != nil {
				return from, to, err
			}
			from = doc.PagePlace{ParentID: p.ParentID, FolderID: p.FolderID, SortKey: p.SortKey}
			if parentID == p.ParentID && (parentID != "" || folderID == p.FolderID) {
				return from, from, nil
			}
			err = h.onMain(ctx, func() error {
				left, err := h.pages().MovePageAt(id, parentID, nil, func() error { return st.MovePage(id, parentID, folderID, "") })
				from.Card = left
				return err
			})
			if p, perr := st.Page(id); err == nil && perr == nil {
				to = doc.PagePlace{ParentID: p.ParentID, FolderID: p.FolderID, SortKey: p.SortKey}
			}
			return from, to, err
		},
		RenamePage: st.RenamePage,
		SetTags: func(id string, tags []string) ([]string, error) {
			if err := st.SetPageTags(id, tags); err != nil {
				return nil, err
			}
			p, err := st.Page(id)
			return p.Tags, err
		},
		CreateFolder: func(name, parentID string) (doc.NewFolder, error) {
			if parentID != "" && !spaceFolder(st, parentID) {
				return doc.NewFolder{}, noFolder(parentID)
			}
			f, err := st.CreateFolderIn(name, parentID)
			return doc.NewFolder{ID: f.ID, SpaceID: f.SpaceID, ParentFolderID: f.ParentFolderID, Name: f.Name, SortKey: f.SortKey}, err
		},
		MoveFolder: func(id, parentID string) (name string, from, to doc.FolderPlace, err error) {
			if !spaceFolder(st, id) {
				return "", from, to, noFolder(id)
			}
			if parentID != "" && !spaceFolder(st, parentID) {
				return "", from, to, noFolder(parentID)
			}
			f, err := st.Folder(id)
			if err != nil {
				return "", from, to, err
			}
			from = doc.FolderPlace{ParentFolderID: f.ParentFolderID, SortKey: f.SortKey}
			if f.ParentFolderID == parentID {
				return f.Name, from, from, nil
			}
			if err := st.MoveFolder(id, parentID, ""); err != nil {
				return f.Name, from, from, err
			}
			if f, err = st.Folder(id); err == nil {
				to = doc.FolderPlace{ParentFolderID: f.ParentFolderID, SortKey: f.SortKey}
			}
			return f.Name, from, to, err
		},
	}
	return ToolContext{Content: doc.Empty(), PageID: WorkspaceID, AgentName: agent, Workspace: w}
}

func ownSnippet(st *store.Store, id, words string) string {
	c := st.Content(id)
	if c == nil {
		return ""
	}
	text := ownText(c)
	if !strings.Contains(strings.ToLower(text), strings.ToLower(words)) {
		return ""
	}
	from := 0
	if loc := regexp.MustCompile("(?i)" + regexp.QuoteMeta(words)).FindStringIndex(text); loc != nil {
		from = max(utf8.RuneCountInString(text[:loc[0]])-40, 0)
	}
	runes := []rune(text)
	return string(runes[min(from, len(runes)):min(from+utf8.RuneCountInString(words)+80, len(runes))])
}

func (h *Hub) onMain(ctx context.Context, fn func() error) error {
	var state atomic.Int32
	done := make(chan error, 1)
	h.host.Update(WorkspaceID, func() {
		if state.CompareAndSwap(0, 1) {
			done <- fn()
		}
	})
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if state.CompareAndSwap(0, 2) {
			return ctx.Err()
		}
		return <-done
	}
}

func (h *Hub) ChatWorkspace(text, openPageID string) {
	s := h.Session(WorkspaceID)
	t := strings.TrimSpace(text)
	if t == "" || s.Running {
		return
	}
	history := chatHistory(s)
	s.Messages = append(s.Messages, &Message{Mine: true, Text: t})
	reply := &Message{Text: locales.AgentWorking(), Pending: true}
	s.Messages = append(s.Messages, reply)
	h.Start(WorkspaceID, RunOptions{Surface: "workspace", Instruction: t, OpenPage: openPageID, History: history, KeptToPage: keptToPage(s), Reply: reply})
	h.saveChat(s)
}

func (h *Hub) openPage(id string) *OpenPage {
	p, err := h.st.Page(id)
	if id == "" || err != nil || p.SpaceID != h.st.Session().SpaceID || p.Template || !p.DeletedAt.IsZero() {
		return nil
	}
	return &OpenPage{ID: p.ID, Title: p.DisplayTitle(), Shared: !h.private(p)}
}
