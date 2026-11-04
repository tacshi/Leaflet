package agents

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/locales"
)

// The workspace agent: a chat of the user's own, not of a page, whose
// agent finds, reads, makes and files the user's pages, and changes no
// page's blocks, which the agent on the page does (docs/protocol.md §2),
// mirroring supabase/functions/_shared/workspace.ts.

// WorkspaceID is the workspace agent's session, as a page's ID is its
// agent's.
const WorkspaceID = "workspace"

// WorkspacePage is a page as the workspace agent sees it.
type WorkspacePage struct {
	ID, Title, ParentID, FolderID string
	Tags                          []string
	// Shared is a page someone else reads or writes: shared with others,
	// with a guest's comment or a public link, or whose title they read,
	// as its card is on such a page. The agent changes nothing of it, nor
	// puts a page in it: what someone else wrote there could have it copy
	// the user's other pages in, or write them where others read it.
	Shared bool
	// People is a page shared with other people, who write in it too: the
	// agent reads none of it either, as what they wrote could steer what
	// it does to the user's other pages.
	People bool
	// Untitled is a page of no title, whose Title is the app's word for
	// one: renamed, it had "", for Undo to give back.
	Untitled bool
}

// title is the page's title as it keeps it: "" for one untitled, which
// its ops name so, as the app says it in the user's language.
func (p WorkspacePage) title() string {
	if p.Untitled {
		return ""
	}
	return p.Title
}

// Workspace is what the workspace agent's tools need: the user's pages,
// those of their own space but templates, and the changes to them. A page
// of another space, a template or one deleted is none of them.
type Workspace struct {
	// Reading lets the agent read the pages, as AgentsReadOtherPages
	// does: without it, it reads none, but makes and files them.
	Reading  bool
	Pages    func() ([]WorkspacePage, error)
	Page     func(id string) (WorkspacePage, bool)
	ReadPage func(id string) (map[string]any, error)
	Search   func(query string) ([]map[string]any, error)
	Folders  func() []map[string]any
	// CreatePage makes a page, in folderID at the top level or inside
	// parentID, linked at its end, and returns it as made.
	CreatePage func(title string, nodes []*doc.Node, parentID, folderID string) (doc.NewPage, error)
	// MovePage moves a page, returning where it was and where it went:
	// the same place, and the page as it was, where it is already.
	MovePage   func(id, parentID, folderID string) (from, to doc.PagePlace, err error)
	RenamePage func(id, title string) error
	// SetTags sets a page's tags, returning them as the page keeps them.
	SetTags      func(id string, tags []string) ([]string, error)
	CreateFolder func(name string) (doc.NewFolder, error)
}

// WorkspaceToolDefinitions are the workspace agent's tools, offered to a
// model: with reading, those that read the user's pages too.
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
		tool("list_folders", "List the user's folders (names and IDs).", map[string]any{}),
		tool("create_page", "Create a page, optionally with content: at the top level of the user's pages, in one of their folders, or inside one of their pages, at whose end a link to it goes.",
			pageSchema(prop{"parent_id", map[string]any{"type": "string", "description": "The page to put it inside; none for the top level."}},
				prop{"folder_id", map[string]any{"type": "string", "description": "The folder to put it in, at the top level; not with parent_id."}}), "title"),
		tool("move_page", "Move a page, with its subpages: inside another page, at whose end a link to it goes; into a folder; or with neither to the top level outside folders.",
			ordered{{"page_id", pageID}, {"parent_id", map[string]any{"type": "string", "description": "The page to put it inside."}},
				{"folder_id", map[string]any{"type": "string", "description": "The folder to put it in, at the top level; not with parent_id."}}}, "page_id"),
		tool("rename_page", "Change a page's title.", ordered{{"page_id", pageID}, {"title", map[string]any{"type": "string"}}}, "page_id", "title"),
		tool("set_page_tags", "Set a page's tags, replacing the ones it has: an empty list takes them all off. Tags are short words or phrases, without #; reuse the user's where they fit.",
			ordered{{"page_id", pageID}, {"tags", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}, "page_id", "tags"),
		tool("create_folder", "Create a folder for top-level pages, last among the user's folders.", map[string]any{"name": map[string]any{"type": "string"}}, "name"))
}

// workspace runs a workspace agent's tool.
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
		// The titles it lists are read too.
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
		// More would be more than a model reads at once: it finds the rest.
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
		// What people wrote in a page shared with them is not read: it
		// is found, but none of its text shown; nor of one the run does
		// not know, as one made since it began.
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
		// Only the user names their folders: as on a page, listing them
		// reads nothing someone else wrote.
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
	case "web_search":
		return x.searchWeb(args)
	case "read_web_page":
		return x.readWeb(args)
	}
	return nil, terr("Unknown tool %q.", name)
}

// pageView is a page as list_pages lists it.
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

// workspacePage is the page args names at key, one of the user's.
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

// changeable is p, refused when someone else reads or writes it.
func changeable(p WorkspacePage) error {
	if p.Shared {
		return terr("“%s” is shared with others: the agent here leaves it as it is, and puts nothing in it. To change it, ask the agent on that page.", p.Title)
	}
	return nil
}

// place is where args puts a page: inside a page of the user's that no one
// else shares, or at the top level, in a folder or none.
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
	// Without the spaces around it, as a new page's.
	title = strings.TrimSpace(title)
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
	f, err := x.Ctx.Workspace.CreateFolder(name)
	if err != nil {
		return nil, err
	}
	x.record(doc.Op{Op: "create_folder", Folder: &f}, locales.AgentEditCreatedFolder(name))
	return map[string]any{"folder_id": f.ID}, nil
}

// workspaceTools are the tools of a workspace run on this device, over
// the store: the user's own space, its templates left out. What changes a
// page as the app has it, as a subpage's card in its parent, runs on the
// main thread through the host's Pages.
func (h *Hub) workspaceTools(ctx context.Context, agent string, settings store.Settings, online bool) ToolContext {
	st := h.st
	space := st.Session().SpaceID
	mine := func(p store.Page) bool { return p.SpaceID == space && !p.Template && p.DeletedAt.IsZero() }
	// A page with a public link another device serves, as the server says
	// as the run starts, is read by others too.
	elsewhere := h.publicElsewhereIn(ctx, space, online)
	read := func(p store.Page, private func(store.Page) bool) bool { return !private(p) || elsewhere[p.ID] }
	// Those whose titles someone else reads, as their cards are on a page
	// they read: read once a run, of all the pages they read, and only for
	// a page no one reads itself. Unread, every page is taken for one.
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
		// Their subpages, whose cards they may have, and the cards they have.
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
	// A recording's transcript only on the page whose block names it, as
	// on a page (PageTools).
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
			// Content this device cannot read, as a newer app's, is not
			// read as an empty page.
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
			// Among the user's own pages alone, so only theirs count toward
			// the most it finds, as the server's search keeps to them.
			hits, err := st.SearchPages(ctx, query, 20, mine)
			if err != nil {
				return nil, err
			}
			out := []map[string]any{}
			for _, hit := range hits {
				r := map[string]any{"page_id": hit.PageID, "title": hit.Title}
				if hit.Snippet != "" {
					r["snippet"] = hit.Snippet
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
			// As a page made from the sidebar, it shows in the page it is
			// inside. A card that could not go in leaves the page made, as
			// a page moved is (core.Pages.MovePage): the run keeps it, to
			// undo.
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
			// Where it is already it stays, as it is among the pages there,
			// as the server leaves it: inside the page, or at the top level
			// in the folder.
			if parentID == p.ParentID && (parentID != "" || folderID == p.FolderID) {
				return from, from, nil
			}
			err = h.onMain(ctx, func() error {
				left, err := h.pages().MovePageAt(id, parentID, nil, func() error { return st.MovePage(id, parentID, folderID, "") })
				// Where its card was, for Undo to put it back there.
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
		CreateFolder: func(name string) (doc.NewFolder, error) {
			f, err := st.CreateFolder(name)
			return doc.NewFolder{ID: f.ID, SpaceID: f.SpaceID, Name: f.Name, SortKey: f.SortKey}, err
		},
	}
	return ToolContext{Content: doc.Empty(), PageID: WorkspaceID, AgentName: agent, Workspace: w}
}

// onMain runs fn on the main thread, where pages change as the app has
// them, and waits for it, or for ctx to end. Given up on before its turn
// came, fn never runs: the run that asked for it no longer keeps what it
// does, to undo. Once it runs, it is waited for.
func (h *Hub) onMain(ctx context.Context, fn func() error) error {
	// 0 waits its turn, 1 runs, 2 was given up on.
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

// ChatWorkspace sends a message to the workspace agent. openPageID is the
// page open as the user sends it, which the agent is told of, "" for none.
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

// openPage is page id as a workspace run's agent is told of it: nil for
// none, or one not the user's own.
func (h *Hub) openPage(id string) *OpenPage {
	p, err := h.st.Page(id)
	if id == "" || err != nil || p.SpaceID != h.st.Session().SpaceID || p.Template || !p.DeletedAt.IsZero() {
		return nil
	}
	return &OpenPage{ID: p.ID, Title: p.DisplayTitle(), Shared: !h.private(p)}
}
