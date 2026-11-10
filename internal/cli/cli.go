package cli

import (
	"bytes"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/spf13/cobra"

	"leaflet/internal/doc"
	"leaflet/internal/export"
	"leaflet/internal/mcp"
	"leaflet/internal/store"
)

const Agent = "Command line"

type Options struct {
	Name    string
	Dir     string
	Version string
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
}

func CommandName(app string) string {
	return strings.ToLower(strings.Join(strings.Fields(app), "-"))
}

func Invoked(args []string, name string) bool {
	if len(args) == 0 {
		return false
	}
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

type runner struct {
	o     Options
	root  *cobra.Command
	json  bool
	stdin bool
	st    *store.Store
	srv   *mcp.Server
	list  *listing
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
			"  " + name + ` new "Meeting notes" --folder Work --stdin < notes.md`,
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
	current := &cobra.Command{
		Use:   "current",
		Short: "Show the page open in the app, with its selection and text color",
		Long: `Show the page open in the app, with the selected text and its text color and
highlight. With the app closed, or no page open, it names the page opened
last, if any.`,
		Args: cobra.NoArgs,
		RunE: r.with(r.current),
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
		Short: "Make a page, with the Markdown piped in as its content if asked",
		Long: `Make a page, and print its ID. --stdin takes its content from the Markdown
piped in; a title alone leaves what is piped in to the commands after it, as
in a loop reading a file.
Without a title, the Markdown piped in is its content, and its first line,
a # heading, names it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: r.with(func(args []string) error { return r.newPage(args, into, folder) }),
	}
	r.placeFlags(newPage, &into, &folder, nil)
	newPage.Flags().BoolVar(&r.stdin, "stdin", false, "take its content from what is piped in")
	appendTo := &cobra.Command{
		Use:   "append PAGE [TEXT...]",
		Short: "Add Markdown to the end of a page: the text given, or what is piped in",
		Long: `Add Markdown to the end of a page: the text given, or what is piped in.
Each line is a block: a paragraph, # to ### headings, - or 1. list items,
- [ ] to-dos, > quotes, fenced code, | tables and --- dividers. With no text
what is piped in is added, as --stdin asks for by name. After the page, --json,
--stdin and --help still count; a -- before the text makes them text too.`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: r.completePage,
		RunE: func(cmd *cobra.Command, args []string) error {
			text, help := r.appendText(args[1:])
			if help {
				return cmd.Help()
			}
			return r.with(func([]string) error { return r.appendTo(args[0], text) })(cmd, args)
		},
	}
	appendTo.Flags().SetInterspersed(false)
	appendTo.Flags().BoolVar(&r.stdin, "stdin", false, "add what is piped in")
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
		Short: "Make, rename and move folders",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	var folderIn string
	var folderTop bool
	newFolder := &cobra.Command{
		Use:   "new NAME [--in FOLDER]",
		Short: "Make a folder, last at the top level or in a folder, and print its ID",
		Args:  cobra.ExactArgs(1),
		RunE:  r.with(func(args []string) error { return r.newFolder(args, folderIn) }),
	}
	newFolder.Flags().StringVar(&folderIn, "in", "", "make it in this folder")
	_ = newFolder.RegisterFlagCompletionFunc("in", r.completeFolder)
	mvFolder := &cobra.Command{
		Use:   "mv FOLDER (--in FOLDER | --top)",
		Short: "Move a folder, with what is in it, into another folder or to the top level",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return r.completeFolder(cmd, args, toComplete)
		},
		RunE: r.with(func(args []string) error { return r.mvFolder(args, folderIn) }),
	}
	mvFolder.Flags().StringVar(&folderIn, "in", "", "put it in this folder, last")
	mvFolder.Flags().BoolVar(&folderTop, "top", false, "put it at the top level")
	_ = mvFolder.RegisterFlagCompletionFunc("in", r.completeFolder)
	mvFolder.MarkFlagsOneRequired("in", "top")
	mvFolder.MarkFlagsMutuallyExclusive("in", "top")
	folders.AddCommand(newFolder, mvFolder, &cobra.Command{
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
	for _, c := range []*cobra.Command{ls, search, current, cat, newPage, appendTo, rename, mv, tag, folders} {
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

func (r *runner) placeFlags(cmd *cobra.Command, into, folder *string, top *bool) {
	cmd.Flags().StringVar(into, "in", "", "put it inside this page, which links to it at its end")
	cmd.Flags().StringVar(folder, "folder", "", "put it in this folder, at the top level")
	_ = cmd.RegisterFlagCompletionFunc("in", r.completeInto)
	_ = cmd.RegisterFlagCompletionFunc("folder", r.completeFolder)
	if top != nil {
		cmd.Flags().BoolVar(top, "top", false, "put it at the top level, outside folders")
		cmd.MarkFlagsOneRequired("in", "folder", "top")
		cmd.MarkFlagsMutuallyExclusive("in", "folder", "top")
	} else {
		cmd.MarkFlagsMutuallyExclusive("in", "folder")
	}
	cmd.PreRunE = func(*cobra.Command, []string) error {
		switch {
		case cmd.Flags().Changed("in") && strings.TrimSpace(*into) == "":
			return errors.New("--in names no page")
		case cmd.Flags().Changed("folder") && strings.TrimSpace(*folder) == "":
			return errors.New("--folder names no folder")
		}
		return nil
	}
}

func (r *runner) with(run func(args []string) error) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, args []string) error {
		if err := r.open(); err != nil {
			return err
		}
		return run(args)
	}
}

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

func (r *runner) call(name string, args map[string]any) (map[string]any, error) {
	out := r.srv.Call(name, args)
	if msg, ok := out["error"]; ok {
		return nil, fmt.Errorf("%v", msg)
	}
	return out, nil
}

func (r *runner) printJSON(v any) error {
	raw, err := json.Marshal(v, json.Deterministic(true), jsontext.WithIndent("  "), jsontext.AllowInvalidUTF8(true))
	if err != nil {
		return err
	}
	_, err = r.o.Out.Write(append(raw, '\n'))
	return err
}

func (r *runner) print(s string) error {
	_, err := io.WriteString(r.o.Out, s)
	return err
}

func decode(result map[string]any, v any) error {
	raw, err := json.Marshal(result, jsontext.AllowInvalidUTF8(true))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (r *runner) changed(out map[string]any, err error) error {
	if err != nil || !r.json {
		return err
	}
	return r.printJSON(out)
}

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
	return strings.ReplaceAll(utf8Text(raw), "\r\n", "\n"), err
}

func (r *runner) inputWaits() bool {
	f, ok := r.o.In.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	switch {
	case err != nil:
		return false
	case info.Mode().IsRegular():
		at, err := f.Seek(0, io.SeekCurrent)
		return err == nil && at < info.Size()
	case info.Mode()&os.ModeNamedPipe != 0:
		return pipeHolds(f)
	}
	return false
}

func utf8Text(raw []byte) string {
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		order = binary.LittleEndian
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		order = binary.BigEndian
	default:
		return string(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}))
	}
	units := make([]uint16, (len(raw)-2)/2)
	for i := range units {
		units[i] = order.Uint16(raw[2+2*i:])
	}
	return string(utf16.Decode(units))
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
		line := shortID(p.ID) + "  " + strings.Repeat("  ", depth) + shown(p.Title)
		for _, t := range p.Tags {
			line += "  #" + shown(t)
		}
		if p.Shared && depth == 0 {
			line += "  (shared with you)"
		}
		b.WriteString(line + "\n")
		for _, c := range children[p.ID] {
			tree(c, depth+1)
		}
	}
	folderListed := map[string]bool{}
	for _, f := range l.Folders {
		folderListed[f.ID] = true
	}
	subFolders := map[string][]listedFolder{}
	var rootFolders []listedFolder
	for _, f := range l.Folders {
		if folderListed[f.ParentID] {
			subFolders[f.ParentID] = append(subFolders[f.ParentID], f)
		} else {
			rootFolders = append(rootFolders, f)
		}
	}
	done := map[string]bool{}
	var folderTree func(f listedFolder, depth int)
	folderTree = func(f listedFolder, depth int) {
		if done[f.ID] {
			return
		}
		done[f.ID] = true
		b.WriteString(shortID(f.ID) + "  " + strings.Repeat("  ", depth) + shown(f.Name) + "/\n")
		for _, sub := range subFolders[f.ID] {
			folderTree(sub, depth+1)
		}
		for _, p := range inFolder[f.ID] {
			tree(p, depth+1)
		}
	}
	for _, f := range rootFolders {
		folderTree(f, 0)
	}
	for _, f := range l.Folders {
		folderTree(f, 0)
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
		b.WriteString(shortID(h.PageID) + "  " + shown(h.Title))
		if h.Kind != "page" {
			b.WriteString("  (" + shown(h.Kind) + ")")
		}
		b.WriteString("\n")
		if h.Snippet != "" {
			b.WriteString(strings.Repeat(" ", idLen+2) + strings.Join(strings.Fields(shown(h.Snippet)), " ") + "\n")
		}
	}
	return r.print(b.String())
}

func (r *runner) current(_ []string) error {
	out, err := r.call("current_page", map[string]any{})
	if err != nil {
		return err
	}
	if r.json {
		return r.printJSON(out)
	}
	var at struct {
		Open      bool   `json:"open"`
		PageID    string `json:"page_id"`
		Title     string `json:"title"`
		Selection struct {
			Text        string `json:"text"`
			Truncated   bool   `json:"truncated"`
			Color       string `json:"color"`
			Highlight   string `json:"highlight"`
			MixedColors bool   `json:"mixed_colors"`
		} `json:"selection"`
	}
	if err := decode(out, &at); err != nil {
		return err
	}
	if at.PageID == "" {
		return errors.New("no page is open")
	}
	s := at.Selection
	var b strings.Builder
	b.WriteString(shortID(at.PageID) + "  " + shown(at.Title))
	if !at.Open {
		b.WriteString("  (opened last, not open now)")
	}
	b.WriteString("\n")
	if s.Color != "" {
		b.WriteString("Color: " + shown(s.Color) + "\n")
	}
	if s.Highlight != "" {
		b.WriteString("Highlight: " + shown(s.Highlight) + "\n")
	}
	if s.MixedColors {
		b.WriteString("Colors differ across the selection\n")
	}
	if s.Text != "" {
		b.WriteString("Selected:\n" + shownLines(s.Text) + "\n")
	}
	if s.Truncated {
		b.WriteString("(the selection goes on)\n")
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
	return r.print(doc.PageMarkdown(p.Title, export.WithTranscripts(r.st, p.ID, c), doc.MarkdownOptions{PageTitle: func(id string) string { return titles[id] }}))
}

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
	var md string
	if r.stdin || len(args) == 0 {
		var err error
		if md, err = r.input(); err != nil {
			return err
		}
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
	rest, err := parts(md)
	if err != nil {
		return err
	}
	a := map[string]any{"title": title}
	if len(rest) > 0 {
		a["markdown"], rest = rest[0], rest[1:]
	}
	if err := r.place(a, into, folder); err != nil {
		return err
	}
	out, err := r.call("create_page", a)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		id, _ := out["page_id"].(string)
		if _, err := r.write(id, rest); err != nil {
			return fmt.Errorf("page %s made without the end of its content: %w", id, err)
		}
	}
	if !r.stdin && r.inputWaits() {
		_, _ = fmt.Fprintf(r.o.Err, "%s: what is piped in was not read: --stdin reads it\n", r.o.Name)
	}
	if r.json {
		return r.printJSON(out)
	}
	return r.print(fmt.Sprintln(out["page_id"]))
}

func headingTitle(md string) (title, rest string) {
	trimmed := strings.TrimLeft(md, "\n")
	line, rest, _ := strings.Cut(trimmed, "\n")
	if t, ok := strings.CutPrefix(line, "# "); ok && strings.TrimSpace(t) != "" {
		return strings.TrimSpace(t), rest
	}
	return "", md
}

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

func (r *runner) appendText(words []string) (text []string, help bool) {
	for i, w := range words {
		switch w {
		case "--":
			return append(text, words[i+1:]...), help
		case "--json":
			r.json = true
		case "--stdin":
			r.stdin = true
		case "--help", "-h":
			help = true
		default:
			text = append(text, w)
		}
	}
	return text, help
}

func (r *runner) appendTo(ref string, text []string) error {
	p, err := r.page(ref)
	if err != nil {
		return err
	}
	md := strings.Join(text, " ")
	if r.stdin && md != "" {
		return errors.New("give the text or --stdin, not both")
	}
	if md == "" {
		if md, err = r.input(); err != nil {
			return err
		}
	}
	todo, err := parts(md)
	if err != nil {
		return err
	}
	if len(todo) == 0 {
		return errors.New("nothing to add: give the text after the page, or pipe it in")
	}
	return r.changed(r.write(p.ID, todo))
}

func (r *runner) write(pageID string, parts []string) (map[string]any, error) {
	ids := []string{}
	for i, md := range parts {
		out, err := r.call("write_markdown", map[string]any{"page_id": pageID, "position": map[string]any{"at": "end"}, "markdown": md})
		if err != nil {
			if i > 0 {
				err = fmt.Errorf("%d of %d parts added: %w", i, len(parts), err)
			}
			return nil, err
		}
		var added struct {
			IDs []string `json:"inserted_ids"`
		}
		if err := decode(out, &added); err != nil {
			return nil, err
		}
		ids = append(ids, added.IDs...)
	}
	return map[string]any{"inserted_ids": ids}, nil
}

func parts(md string) ([]string, error) {
	blocks := doc.MarkdownBlocks(md)
	if len(blocks) == 0 {
		return nil, nil
	}
	err := doc.CallNodes(blocks)
	if err == nil {
		return []string{md}, nil
	}
	cut, inList := middleLine(md)
	if cut == 0 {
		return nil, fmt.Errorf("a block too long to cut, so nothing was written: %w", err)
	}
	head, err := parts(md[:cut])
	if err != nil {
		return nil, err
	}
	rest := md[cut:]
	if inList {
		rest = renumbered(rest)
	}
	tail, err := parts(rest)
	if err != nil {
		return nil, err
	}
	return append(head, tail...), nil
}

func middleLine(md string) (cut int, inList bool) {
	var at, toggles, number int
	var fence string
	var row, quote, hard bool
	nearer := func(a, b int) bool { return max(a, len(md)-a) < max(b, len(md)-b) }
	for line := range strings.Lines(md) {
		trimmed := strings.TrimSpace(line)
		top := trimmed != "" && !indented(line)
		isRow, isQuote := strings.HasPrefix(trimmed, "|"), strings.HasPrefix(trimmed, ">")
		goesOn := row && isRow || quote && isQuote || hard
		n, digits := itemNumber(trimmed)
		counted := digits > 0 && (digits <= 3 || n == number+1)
		if at > 0 && top && fence == "" && toggles == 0 && !goesOn && nearer(at, cut) {
			cut, inList = at, digits > 3 && counted
		}
		at += len(line)
		row, quote, hard = isRow, isQuote, hardBreak(line)
		switch {
		case !top:
		case counted:
			number = n
		default:
			number = 0
		}
		lower := strings.ToLower(trimmed)
		_, code := doc.CodeFence(trimmed)
		switch {
		case fence == "$$" && trimmed == "$$", fence != "" && fence != "$$" && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, "` ") == "":
			fence = ""
		case fence != "":
		case trimmed == "$$":
			fence = "$$"
		case code:
			fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "`"))]
		case strings.HasPrefix(lower, "<details>"), strings.HasPrefix(lower, "<details open>"):
			toggles++
		case lower == "</details>" && toggles > 0:
			toggles--
		}
	}
	return cut, inList
}

func indented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

func hardBreak(line string) bool {
	line = strings.TrimRight(line, "\r\n")
	return (len(line)-len(strings.TrimRight(line, `\`)))%2 == 1
}

func itemNumber(line string) (n, digits int) {
	digits = len(line) - len(strings.TrimLeft(line, "0123456789"))
	if digits == 0 || digits > 9 || digits == len(line) || !strings.ContainsRune(".)", rune(line[digits])) {
		return 0, 0
	}
	if rest := line[digits+1:]; rest != "" && rest[0] != ' ' {
		return 0, 0
	}
	n, _ = strconv.Atoi(line[:digits])
	return n, digits
}

func renumbered(md string) string {
	var b strings.Builder
	at, count, last := 0, 0, 0
	for line := range strings.Lines(md) {
		trimmed, rest := strings.TrimSpace(line), line
		if trimmed != "" && !indented(line) {
			n, digits := itemNumber(trimmed)
			if digits == 0 || count > 0 && n != last+1 {
				break
			}
			count++
			last = n
			b.WriteString(strconv.Itoa(count))
			rest = line[digits:]
		}
		b.WriteString(rest)
		at += len(line)
	}
	return b.String() + md[at:]
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
	case len(tags) > 0 && len(store.NormalizeTags(tags)) == 0:
		return errors.New("no tag given: --clear takes them all off")
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

func orEmpty(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}

func (r *runner) newFolder(args []string, in string) error {
	a := map[string]any{"name": args[0]}
	if strings.TrimSpace(in) != "" {
		f, err := r.folder(in)
		if err != nil {
			return err
		}
		a["parent_folder_id"] = f.ID
	}
	out, err := r.call("create_folder", a)
	if err != nil {
		return err
	}
	if r.json {
		return r.printJSON(out)
	}
	return r.print(fmt.Sprintln(out["folder_id"]))
}

func (r *runner) mvFolder(args []string, in string) error {
	f, err := r.folder(args[0])
	if err != nil {
		return err
	}
	a := map[string]any{"folder_id": f.ID}
	if strings.TrimSpace(in) != "" {
		into, err := r.folder(in)
		if err != nil {
			return err
		}
		a["parent_folder_id"] = into.ID
	}
	out, err := r.call("move_folder", a)
	return r.changed(out, err)
}

func (r *runner) renameFolder(args []string) error {
	f, err := r.folder(args[0])
	if err != nil {
		return err
	}
	out, err := r.call("rename_folder", map[string]any{"folder_id": f.ID, "name": args[1]})
	return r.changed(out, err)
}
