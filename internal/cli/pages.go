package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type listing struct {
	Pages   []listedPage   `json:"pages"`
	Folders []listedFolder `json:"folders"`
}

type listedPage struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Tags     []string `json:"tags,omitempty"`
	ParentID string   `json:"parent_id,omitempty"`
	FolderID string   `json:"folder_id,omitempty"`
	Shared   bool     `json:"shared,omitzero"`
}

type listedFolder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_folder_id,omitempty"`
}

func (r *runner) listing() (*listing, error) {
	if r.list != nil {
		return r.list, nil
	}
	out, err := r.call("list_pages", map[string]any{})
	if err != nil {
		return nil, err
	}
	l := &listing{}
	if err := decode(out, l); err != nil {
		return nil, err
	}
	r.list = l
	return l, nil
}

const idLen = 8

const minPrefix = 4

func shortID(id string) string { return id[:min(idLen, len(id))] }

func shown(s string) string { return strings.Map(plainRune(false), s) }

func shownLines(s string) string {
	return strings.Map(plainRune(true), strings.ReplaceAll(s, "\r\n", "\n"))
}

func plainRune(lines bool) func(rune) rune {
	return func(c rune) rune {
		switch {
		case lines && (c == '\n' || c == '\t'):
			return c
		case c < 0x20 || c >= 0x7f && c <= 0x9f:
			return ' '
		}
		return c
	}
}

func (r *runner) page(ref string) (listedPage, error) {
	l, err := r.listing()
	if err != nil {
		return listedPage{}, err
	}
	p, err := find(l.Pages, ref, func(p listedPage) (string, string) { return p.ID, p.Title }, "page", "titled", func(p listedPage) string { return r.where(l, p) })
	if err != nil {
		return listedPage{}, fmt.Errorf("%w: %s ls lists them, and %s search finds them", err, r.o.Name, r.o.Name)
	}
	return p, nil
}

func (r *runner) folder(ref string) (listedFolder, error) {
	l, err := r.listing()
	if err != nil {
		return listedFolder{}, err
	}
	f, err := find(l.Folders, ref, func(f listedFolder) (string, string) { return f.ID, f.Name }, "folder", "named", func(listedFolder) string { return "" })
	if err != nil {
		return listedFolder{}, fmt.Errorf("%w: %s ls lists them", err, r.o.Name)
	}
	return f, nil
}

func find[T any](items []T, ref string, key func(T) (id, name string), kind, named string, where func(T) string) (T, error) {
	var none T
	for _, it := range items {
		if id, _ := key(it); id == ref {
			return it, nil
		}
	}
	matches := []func(T) bool{
		func(it T) bool { _, name := key(it); return name == ref },
		func(it T) bool { _, name := key(it); return strings.EqualFold(name, ref) },
		func(it T) bool {
			id, _ := key(it)
			return len(ref) >= minPrefix && strings.HasPrefix(id, strings.ToLower(ref))
		},
	}
	for i, match := range matches {
		var found []T
		for _, it := range items {
			if match(it) {
				found = append(found, it)
			}
		}
		if len(found) == 1 {
			return found[0], nil
		}
		if len(found) == 0 {
			continue
		}
		var each []string
		for _, it := range found {
			id, name := key(it)
			s := shortID(id) + " " + fmt.Sprintf("%q", name)
			if w := where(it); w != "" {
				s += " " + w
			}
			each = append(each, s)
		}
		what := fmt.Sprintf("%d %ss are %s %q", len(found), kind, named, ref)
		if i == len(matches)-1 {
			what = fmt.Sprintf("%d %ss' IDs start with %q", len(found), kind, ref)
		}
		return none, fmt.Errorf("%s (%s); name one by its ID", what, strings.Join(each, ", "))
	}
	return none, fmt.Errorf("no %s %q", kind, ref)
}

func (r *runner) where(l *listing, p listedPage) string {
	for _, q := range l.Pages {
		if q.ID == p.ParentID && p.ParentID != "" {
			return fmt.Sprintf("in %q", q.Title)
		}
	}
	for _, f := range l.Folders {
		if f.ID == p.FolderID && p.FolderID != "" {
			return fmt.Sprintf("in %s/", shown(f.Name))
		}
	}
	if p.Shared {
		return "shared with you"
	}
	return "at the top level"
}

func (r *runner) completePage(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return r.completeInto(cmd, args, toComplete)
}

func (r *runner) completeInto(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	l, err := r.completing()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var out []cobra.Completion
	for _, p := range l.Pages {
		if strings.HasPrefix(strings.ToLower(p.Title), strings.ToLower(toComplete)) {
			out = append(out, cobra.CompletionWithDesc(shown(p.Title), r.where(l, p)))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func (r *runner) completeFolder(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	l, err := r.completing()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var out []cobra.Completion
	for _, f := range l.Folders {
		if strings.HasPrefix(strings.ToLower(f.Name), strings.ToLower(toComplete)) {
			out = append(out, shown(f.Name))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func (r *runner) completing() (*listing, error) {
	if err := r.open(); err != nil {
		return nil, err
	}
	return r.listing()
}
