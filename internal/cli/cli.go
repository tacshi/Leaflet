// Package cli is `leaflet` in a terminal: commands that find, read, make
// and file the user's pages, and add to them, through the tools `leaflet
// mcp` serves AI apps (internal/mcp), on the same database as the app. The
// open app picks up what they change within a second, and sync sends it
// on as the app runs.
package cli

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/mcp"
	"leaflet/internal/store"
)

// Agent names the command line on what it writes, as an AI app's name
// does: "Before Command line" in a page's history.
const Agent = "Command line"

// Options are what a run of the command line needs.
type Options struct {
	// Name is the command's name, as its help shows it (CommandName).
	Name string
	// Dir is the data folder.
	Dir     string
	Version string
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
}

// CommandName is the command of the app named app: "leaflet", or
// "leaflet-debug" for Leaflet Debug, so the two can be installed side by
// side.
func CommandName(app string) string {
	return strings.ToLower(strings.Join(strings.Fields(app), "-"))
}

// Invoked reports whether the app's executable, run with args (the
// command's name first), runs as the command line rather than the app:
// through a link named name, as Settings installs, or with one of the
// command line's commands.
func Invoked(args []string, name string) bool {
	if len(args) == 0 {
		return false
	}
	// A link: the executable itself named so, as go build names one,
	// is the app.
	if base := filepath.Base(args[0]); base == name {
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err == nil && filepath.Base(exe) != base {
			return true
		}
	}
	if len(args) < 2 {
		return false
	}
	r := newRunner(Options{Name: name})
	if slices.Contains([]string{"-h", "--help", "-v", "--version", "--json", "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd}, args[1]) {
		return true
	}
	cmd, _, err := r.root.Find(args[1:2])
	return err == nil && cmd != r.root
}

// Main runs the command line with args, those after the command's name,
// and returns its exit status.
func Main(args []string, o Options) int {
	r := newRunner(o)
	defer r.close()
	r.root.SetArgs(args)
	if err := r.root.Execute(); err != nil {
		_, _ = fmt.Fprintf(o.Err, "%s: %v\n", o.Name, err)
		return 1
	}
	return 0
}

// runner is a run of the command line: its commands, and the pages once a
// command opens them.
type runner struct {
	o    Options
	root *cobra.Command
	json bool
	st   *store.Store
	srv  *mcp.Server
	list *listing
}

func newRunner(o Options) *runner {
	r := &runner{o: o}
	name := o.Name
	r.root = &cobra.Command{
		Use:   name,
		Short: "Find, read, make and file your Leaflet pages, and add to them",
		Long: `Find, read, make and file your Leaflet pages from a terminal, and add to them.
What a command changes shows in the open app within a second, and syncs
while the app runs. Each page keeps a version from before the changes in
its history.

A page is named by its title, its ID, or the start of its ID as ls shows
it; a folder by its name or ID.`,
		Example: strings.Join([]string{
			"  " + name + " ls",
			"  " + name + ` search "launch #work"`,
			"  " + name + " cat Roadmap > roadmap.md",
			"  " + name + ` new "Meeting notes" --folder Work < notes.md`,
			"  make test 2>&1 | " + name + ` append "CI log"`,
			"  " + name + " append Inbox call the dentist",
			"  " + name + " tag Roadmap work q4",
		}, "\n"),
		Version:       o.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	r.root.SetIn(o.In)
	r.root.SetOut(o.Out)
	r.root.SetErr(o.Err)
	r.root.PersistentFlags().BoolVar(&r.json, "json", false, "print the result as JSON, as the MCP tools give it")
	r.root.AddGroup(&cobra.Group{ID: "pages", Title: "Pages:"}, &cobra.Group{ID: "agents", Title: "AI apps:"})

	ls := &cobra.Command{
		Use:   "ls",
		Short: "List your folders and pages, with their IDs and tags",
		Args:  cobra.NoArgs,
		RunE:  r.with(r.ls),
	}
	search := &cobra.Command{
		Use:   "search QUERY",
		Short: "Find pages by title, tag or text, recordings by transcript, and comments",
		Long: `Find pages by title, tag or text, recordings by transcript, and comments.
Words starting with # (as #work) keep to the pages with those tags; alone
they list them. Quote them: a shell takes a word starting with # for a
comment.`,
		Args: cobra.MinimumNArgs(1),
		RunE: r.with(r.search),
	}
	cat := &cobra.Command{
		Use:               "cat PAGE",
		Short:             "Print a page as Markdown",
		Long:              "Print a page as Markdown, with its recordings' transcripts.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: r.completePage,
		RunE:              r.with(r.cat),
	}
	var into, folder string
	var top bool
	newPage := &cobra.Command{
		Use:   "new [TITLE]",
		Short: "Make a page, with the Markdown piped in as its content",
		Long: `Make a page, with the Markdown piped in as its content, and print its ID.
Without a title, the piped Markdown's first line names it, when it is a
# heading.`,
		Args: cobra.MaximumNArgs(1),
		RunE: r.with(func(args []string) error { return r.newPage(args, into, folder) }),
	}
	r.placeFlags(newPage, &into, &folder, nil)
	appendTo := &cobra.Command{
		Use:   "append PAGE [TEXT...]",
		Short: "Add Markdown to the end of a page: the text given, or what is piped in",
		Long: `Add Markdown to the end of a page: the text given, or what is piped in.
Each line is a block: a paragraph, # to ### headings, - or 1. list items,
- [ ] to-dos, > quotes, fenced code, | tables and --- dividers.`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: r.completePage,
		RunE:              r.with(r.appendTo),
	}
	rename := &cobra.Command{
		Use:               "rename PAGE TITLE",
		Short:             "Change a page's title",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: r.completePage,
		RunE:              r.with(r.rename),
	}
	mv := &cobra.Command{
		Use:   "mv PAGE (--in PAGE | --folder FOLDER | --top)",
		Short: "Move a page, with its subpages, inside another page, into a folder, or to the top level",
		Long: `Move a page, with its subpages: inside another page, which links to it at
its end; into a folder; or to the top level, outside folders.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: r.completePage,
		RunE:              r.with(func(args []string) error { return r.mv(args, into, folder) }),
	}
	r.placeFlags(mv, &into, &folder, &top)
	var clearTags bool
	tag := &cobra.Command{
		Use:   "tag PAGE [TAG...]",
		Short: "Set a page's tags, or print them",
		Long: `Set a page's tags, replacing the ones it has, or print them when none are
given. --clear takes them all off.`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: r.completePage,
		RunE:              r.with(func(args []string) error { return r.tag(args, clearTags) }),
	}
	tag.Flags().BoolVar(&clearTags, "clear", false, "take all the page's tags off")
	folders := &cobra.Command{
		Use:   "folder",
		Short: "Make and rename folders",
		Args:  cobra.NoArgs,
	}
	folders.AddCommand(&cobra.Command{
		Use:   "new NAME",
		Short: "Make a folder, last in the sidebar, and print its ID",
		Args:  cobra.ExactArgs(1),
		RunE:  r.with(r.newFolder),
	}, &cobra.Command{
		Use:   "rename FOLDER NAME",
		Short: "Change a folder's name",
		Args:  cobra.ExactArgs(2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return r.completeFolder(cmd, args, toComplete)
		},
		RunE: r.with(r.renameFolder),
	})
	for _, c := range []*cobra.Command{ls, search, cat, newPage, appendTo, rename, mv, tag, folders} {
		c.GroupID = "pages"
		r.root.AddCommand(c)
	}
	r.root.AddCommand(&cobra.Command{
		Use:     "mcp",
		Short:   "Serve your pages to AI apps over MCP, on stdin and stdout",
		Long:    "Serve your pages to AI apps, such as Claude Code, Codex and Cursor, as a Model Context Protocol server on stdin and stdout. Settings › External agents sets them up.",
		GroupID: "agents",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return mcp.Main(r.o.Dir, r.o.Version, r.o.In, r.o.Out)
		},
	})
	return r
}

// placeFlags gives cmd the flags that say where a page goes.
func (r *runner) placeFlags(cmd *cobra.Command, into, folder *string, top *bool) {
	cmd.Flags().StringVar(into, "in", "", "put it inside this page, which links to it at its end")
	cmd.Flags().StringVar(folder, "folder", "", "put it in this folder, at the top level")
	_ = cmd.RegisterFlagCompletionFunc("in", r.completePage)
	_ = cmd.RegisterFlagCompletionFunc("folder", r.completeFolder)
	if top != nil {
		cmd.Flags().BoolVar(top, "top", false, "put it at the top level, outside folders")
		cmd.MarkFlagsOneRequired("in", "folder", "top")
		cmd.MarkFlagsMutuallyExclusive("in", "folder", "top")
	} else {
		cmd.MarkFlagsMutuallyExclusive("in", "folder")
	}
}

// with is a command's run, given the pages open.
func (r *runner) with(run func(args []string) error) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, args []string) error {
		if err := r.open(); err != nil {
			return err
		}
		return run(args)
	}
}

// open opens the pages, as `leaflet mcp` does, unless they are open. A
// data folder without them is one the app has never run with, which the
// command line leaves for it to set up: its first run writes the welcome
// guide, say.
func (r *runner) open() error {
	if r.st != nil {
		return nil
	}
	if _, err := os.Stat(store.Path(r.o.Dir)); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no pages in %s: open Leaflet once first, or set LEAFLET_DATA_DIR to its data folder", r.o.Dir)
	} else if err != nil {
		return err
	}
	st, err := mcp.Open(r.o.Dir)
	if err != nil {
		return err
	}
	r.st, r.srv = st, mcp.NewServer(st, r.o.Version)
	r.srv.SetAgent(Agent)
	return nil
}

func (r *runner) close() {
	if r.st != nil {
		_ = r.st.Close()
	}
}

// call runs one of the MCP tools, and returns its result, or its error.
func (r *runner) call(name string, args map[string]any) (map[string]any, error) {
	out := r.srv.Call(name, args)
	if msg, ok := out["error"]; ok {
		return nil, fmt.Errorf("%v", msg)
	}
	return out, nil
}

// printJSON prints v as JSON, for --json.
func (r *runner) printJSON(v any) error {
	raw, err := json.Marshal(v, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	_, err = r.o.Out.Write(append(raw, '\n'))
	return err
}

// print prints s, all at once.
func (r *runner) print(s string) error {
	_, err := io.WriteString(r.o.Out, s)
	return err
}

// decode reads a tool's result into v, as a client reads its JSON.
func decode(result map[string]any, v any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// changed ends a command that changes the pages, with the tool's result
// out or its error: out is printed for --json, and otherwise nothing is,
// as a shell's commands say nothing when they work.
func (r *runner) changed(out map[string]any, err error) error {
	if err != nil || !r.json {
		return err
	}
	return r.printJSON(out)
}

// input is what is piped in, with Windows' line ends as Markdown has
// them: "" from a terminal, which would wait for typing.
func (r *runner) input() (string, error) {
	if f, ok := r.o.In.(*os.File); ok {
		if info, err := f.Stat(); err != nil || info.Mode()&os.ModeCharDevice != 0 {
			return "", nil
		}
	}
	if r.o.In == nil {
		return "", nil
	}
	raw, err := io.ReadAll(r.o.In)
	return strings.ReplaceAll(string(raw), "\r\n", "\n"), err
}

func (r *runner) ls(_ []string) error {
	l, err := r.listing()
	if err != nil {
		return err
	}
	if r.json {
		return r.printJSON(l)
	}
	listed := map[string]bool{}
	for _, p := range l.Pages {
		listed[p.ID] = true
	}
	children := map[string][]listedPage{}
	inFolder := map[string][]listedPage{}
	var top, shared []listedPage
	for _, p := range l.Pages {
		switch {
		case p.ParentID != "" && listed[p.ParentID]:
			children[p.ParentID] = append(children[p.ParentID], p)
		case p.FolderID != "":
			inFolder[p.FolderID] = append(inFolder[p.FolderID], p)
		case p.Shared:
			shared = append(shared, p)
		default:
			top = append(top, p)
		}
	}
	var b strings.Builder
	var tree func(p listedPage, depth int)
	tree = func(p listedPage, depth int) {
		line := shortID(p.ID) + "  " + strings.Repeat("  ", depth) + p.Title
		for _, t := range p.Tags {
			line += "  #" + t
		}
		if p.Shared && depth == 0 {
			line += "  (shared with you)"
		}
		b.WriteString(line + "\n")
		for _, c := range children[p.ID] {
			tree(c, depth+1)
		}
	}
	// As the sidebar has them: the folders with their pages, the pages
	// outside folders, then the pages shared with the user.
	for _, f := range l.Folders {
		b.WriteString(shortID(f.ID) + "  " + f.Name + "/\n")
		for _, p := range inFolder[f.ID] {
			tree(p, 1)
		}
	}
	for _, p := range slices.Concat(top, shared) {
		tree(p, 0)
	}
	return r.print(b.String())
}

func (r *runner) search(args []string) error {
	out, err := r.call("search_pages", map[string]any{"query": strings.Join(args, " ")})
	if err != nil {
		return err
	}
	if r.json {
		return r.printJSON(out)
	}
	var found struct {
		Results []struct {
			Kind    string `json:"kind"`
			Title   string `json:"title"`
			PageID  string `json:"page_id"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := decode(out, &found); err != nil {
		return err
	}
	var b strings.Builder
	for _, h := range found.Results {
		b.WriteString(shortID(h.PageID) + "  " + h.Title)
		if h.Kind != "page" {
			b.WriteString("  (" + h.Kind + ")")
		}
		b.WriteString("\n")
		if h.Snippet != "" {
			b.WriteString(strings.Repeat(" ", idLen+2) + strings.Join(strings.Fields(h.Snippet), " ") + "\n")
		}
	}
	return r.print(b.String())
}

func (r *runner) cat(args []string) error {
	p, err := r.page(args[0])
	if err != nil {
		return err
	}
	if r.json {
		return r.readWhole(p)
	}
	c := r.st.Content(p.ID)
	if c == nil {
		// Not kept on this device, or not readable: read_page says which.
		if _, err := r.call("read_page", map[string]any{"page_id": p.ID}); err != nil {
			return err
		}
		return fmt.Errorf("the content of %q could not be read", p.Title)
	}
	titles := map[string]string{}
	if l, err := r.listing(); err == nil {
		for _, q := range l.Pages {
			titles[q.ID] = q.Title
		}
	}
	return r.print(doc.PageMarkdown(p.Title, export.WithTranscripts(r.st, c), doc.MarkdownOptions{PageTitle: func(id string) string { return titles[id] }}))
}

// readWhole prints read_page's answer for the whole page, its parts in
// one.
func (r *runner) readWhole(p listedPage) error {
	var whole map[string]any
	for offset := 0.0; ; {
		out, err := r.call("read_page", map[string]any{"page_id": p.ID, "offset": offset})
		if err != nil {
			return err
		}
		var part map[string]any
		if err := decode(out, &part); err != nil {
			return err
		}
		if whole == nil {
			whole = part
		} else {
			blocks, _ := part["blocks"].([]any)
			whole["blocks"] = append(whole["blocks"].([]any), blocks...)
		}
		next, more := part["next_offset"].(float64)
		if !more {
			break
		}
		offset = next
	}
	delete(whole, "next_offset")
	return r.printJSON(whole)
}

func (r *runner) newPage(args []string, into, folder string) error {
	md, err := r.input()
	if err != nil {
		return err
	}
	var title string
	if len(args) > 0 {
		title = args[0]
	} else {
		title, md = headingTitle(md)
	}
	if strings.TrimSpace(title) == "" {
		return errors.New("give the page a title, or start what is piped in with a # heading")
	}
	a := map[string]any{"title": title}
	if strings.TrimSpace(md) != "" {
		a["markdown"] = md
	}
	if err := r.place(a, into, folder); err != nil {
		return err
	}
	out, err := r.call("create_page", a)
	if err != nil {
		return err
	}
	if r.json {
		return r.printJSON(out)
	}
	return r.print(fmt.Sprintln(out["page_id"]))
}

// headingTitle is md's title, when its first line is a # heading, and md
// without it.
func headingTitle(md string) (title, rest string) {
	trimmed := strings.TrimLeft(md, "\n")
	line, rest, _ := strings.Cut(trimmed, "\n")
	if t, ok := strings.CutPrefix(line, "# "); ok && strings.TrimSpace(t) != "" {
		return strings.TrimSpace(t), rest
	}
	return "", md
}

// place puts in a where --in or --folder say a page goes.
func (r *runner) place(a map[string]any, into, folder string) error {
	if into != "" {
		p, err := r.page(into)
		if err != nil {
			return err
		}
		a["parent_id"] = p.ID
	}
	if folder != "" {
		f, err := r.folder(folder)
		if err != nil {
			return err
		}
		a["folder_id"] = f.ID
	}
	return nil
}

func (r *runner) appendTo(args []string) error {
	p, err := r.page(args[0])
	if err != nil {
		return err
	}
	md := strings.Join(args[1:], " ")
	if md == "" {
		if md, err = r.input(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(md) == "" {
		return errors.New("nothing to add: give the text after the page, or pipe it in")
	}
	out, err := r.call("write_markdown", map[string]any{"page_id": p.ID, "position": map[string]any{"at": "end"}, "markdown": md})
	return r.changed(out, err)
}

func (r *runner) rename(args []string) error {
	p, err := r.page(args[0])
	if err != nil {
		return err
	}
	out, err := r.call("rename_page", map[string]any{"page_id": p.ID, "title": args[1]})
	return r.changed(out, err)
}

func (r *runner) mv(args []string, into, folder string) error {
	p, err := r.page(args[0])
	if err != nil {
		return err
	}
	a := map[string]any{"page_id": p.ID}
	if err := r.place(a, into, folder); err != nil {
		return err
	}
	out, err := r.call("move_page", a)
	return r.changed(out, err)
}

func (r *runner) tag(args []string, clear bool) error {
	p, err := r.page(args[0])
	if err != nil {
		return err
	}
	tags := args[1:]
	switch {
	case clear && len(tags) > 0:
		return errors.New("give tags or --clear, not both")
	case !clear && len(tags) == 0:
		if r.json {
			return r.printJSON(map[string]any{"page_id": p.ID, "tags": orEmpty(p.Tags)})
		}
		var b strings.Builder
		for _, t := range p.Tags {
			b.WriteString(t + "\n")
		}
		return r.print(b.String())
	}
	list := []any{}
	for _, t := range tags {
		list = append(list, t)
	}
	out, err := r.call("set_page_tags", map[string]any{"page_id": p.ID, "tags": list})
	return r.changed(out, err)
}

// orEmpty is tags, or an empty list for none, as JSON has them.
func orEmpty(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

func (r *runner) newFolder(args []string) error {
	out, err := r.call("create_folder", map[string]any{"name": args[0]})
	if err != nil {
		return err
	}
	if r.json {
		return r.printJSON(out)
	}
	return r.print(fmt.Sprintln(out["folder_id"]))
}

func (r *runner) renameFolder(args []string) error {
	f, err := r.folder(args[0])
	if err != nil {
		return err
	}
	out, err := r.call("rename_folder", map[string]any{"folder_id": f.ID, "name": args[1]})
	return r.changed(out, err)
}
