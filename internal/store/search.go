package store

import (
	"database/sql/driver"
	"slices"
	"sort"
	"strings"

	"modernc.org/sqlite"

	"leaflet/locales"
)

// lowerFunc is SQL's name for strings.ToLower, which search matches text
// with as it does titles: SQLite's lower() lowers ASCII alone.
const lowerFunc = "leaflet_lower"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(lowerFunc, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return strings.ToLower(v), nil
		case []byte:
			return strings.ToLower(string(v)), nil
		}
		return args[0], nil
	})
}

// SearchHit is one search result.
type SearchHit struct {
	Kind    string // page | template | recording | comment
	Title   string
	PageID  string
	Snippet string
	BlockID string
}

// Search finds pages and templates by title, tag or text, recordings by
// transcript, and comments. Words starting with # keep to the pages with
// those tags, which alone lists them, newest first. A blank query returns
// the most recently edited pages.
func (s *Store) Search(query string, limit int) ([]SearchHit, error) {
	filters, rest := SplitTagQuery(query)
	q := strings.ToLower(rest)
	pages, err := s.Pages()
	if err != nil {
		return nil, err
	}
	templates, err := s.Templates()
	if err != nil {
		return nil, err
	}
	// Text, transcripts and comments are read for every page there is,
	// the pages with the tags among them, while tags filter: a limit
	// before the filter would let other pages' matches crowd theirs out.
	textLimit, otherLimit := limit, 10
	if len(filters) > 0 {
		match := tagMatcher(filters, append(append([]Page{}, pages...), templates...))
		pages, templates = slices.DeleteFunc(pages, func(p Page) bool { return !match(p) }),
			slices.DeleteFunc(templates, func(p Page) bool { return !match(p) })
		textLimit, otherLimit = -1, -1
	}
	titles := map[string]string{}
	for _, p := range pages {
		titles[p.ID] = p.Title
	}
	templateTitles := map[string]string{}
	for _, t := range templates {
		templateTitles[t.ID] = t.Title
	}
	var hits []SearchHit
	if q == "" {
		recent := append([]Page{}, pages...)
		sort.Slice(recent, func(i, j int) bool { return recent[i].UpdatedAt.After(recent[j].UpdatedAt) })
		for i, p := range recent {
			if i == 8 && len(filters) == 0 || i == limit {
				break
			}
			hits = append(hits, SearchHit{Kind: "page", Title: p.DisplayTitle(), PageID: p.ID})
		}
		return hits, nil
	}
	seen := map[string]bool{}
	for _, p := range pages {
		if strings.Contains(strings.ToLower(p.Title), q) || tagContains(p, q) {
			hits = append(hits, SearchHit{Kind: "page", Title: p.DisplayTitle(), PageID: p.ID})
			seen[p.ID] = true
		}
	}
	for _, t := range templates {
		if strings.Contains(strings.ToLower(t.Title), q) {
			hits = append(hits, SearchHit{Kind: "template", Title: t.DisplayTitle(), PageID: t.ID})
			seen[t.ID] = true
		}
	}
	// Text matches as the titles do, lowered as Go lowers it, and with %
	// and _ the characters they are: SQLite's lower() and LIKE fold ASCII
	// alone, and LIKE reads those two as wildcards. Only the hits listed
	// count toward the limits, so pages deleted, found by their titles
	// already, or left out by the tags do not crowd the others out; the
	// most recently changed come first.
	rows, err := s.db.Query(`SELECT c.page_id, c.plain_text FROM page_contents c JOIN pages p ON p.id = c.page_id
		WHERE p.deleted_at IS NULL AND instr(`+lowerFunc+`(c.plain_text), ?) > 0 ORDER BY p.updated_at DESC, p.id`, q)
	n := 0
	err = each(rows, err, func(sc scanner) (bool, error) {
		var id, text string
		if err := sc.Scan(&id, &text); err != nil {
			return false, err
		}
		if seen[id] {
			return true, nil
		}
		if title, ok := templateTitles[id]; ok {
			hits = append(hits, SearchHit{Kind: "template", Title: TitleOr(title), PageID: id, Snippet: snippet(text, q)})
			n++
		} else if title, ok := titles[id]; ok {
			hits = append(hits, SearchHit{Kind: "page", Title: TitleOr(title), PageID: id, Snippet: snippet(text, q)})
			n++
		}
		return n != textLimit, nil
	})
	if err != nil {
		return nil, err
	}
	// Recordings and comments, as the text, of the pages listed alone.
	rows, err = s.db.Query(`SELECT r.page_id, r.block_id FROM recordings r JOIN pages p ON p.id = r.page_id
		WHERE p.deleted_at IS NULL AND p.is_template = 0 AND instr(`+lowerFunc+`(r.transcript), ?) > 0
		ORDER BY r.created_at DESC, r.id`, q)
	n = 0
	err = each(rows, err, func(sc scanner) (bool, error) {
		var id, block string
		if err := sc.Scan(&id, &block); err != nil {
			return false, err
		}
		if title, ok := titles[id]; ok {
			hits = append(hits, SearchHit{Kind: "recording", Title: locales.StoreSearchRecording(TitleOr(title)), PageID: id, BlockID: block})
			n++
		}
		return n != otherLimit, nil
	})
	if err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT c.page_id, c.block_id, c.body FROM comments c JOIN pages p ON p.id = c.page_id
		WHERE p.deleted_at IS NULL AND p.is_template = 0 AND instr(`+lowerFunc+`(c.body), ?) > 0 AND c.deleted_at IS NULL
		ORDER BY c.created_at DESC, c.id`, q)
	n = 0
	err = each(rows, err, func(sc scanner) (bool, error) {
		var id, block, body string
		if err := sc.Scan(&id, &block, &body); err != nil {
			return false, err
		}
		if title, ok := titles[id]; ok {
			hits = append(hits, SearchHit{Kind: "comment", Title: body, PageID: id, BlockID: block, Snippet: locales.StoreSearchCommentOn(TitleOr(title))})
			n++
		}
		return n != otherLimit, nil
	})
	if err != nil {
		return nil, err
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// SplitTagQuery splits a search into its tag filters, the words starting
// with #, without it, and the rest of its words.
func SplitTagQuery(query string) (tags []string, rest string) {
	var words []string
	for _, w := range strings.Fields(query) {
		if t, ok := strings.CutPrefix(w, "#"); ok {
			if t = strings.TrimLeft(t, "#"); t != "" {
				tags = append(tags, t)
			}
			continue
		}
		words = append(words, w)
	}
	return tags, strings.Join(words, " ")
}

// tagMatcher reports whether a page has every tag of filters: a tag of
// that name, in any case, or while no page has one, a tag starting with
// it, as one is typed.
func tagMatcher(filters []string, pages []Page) func(Page) bool {
	known := map[string]bool{}
	for _, p := range pages {
		for _, t := range p.Tags {
			known[strings.ToLower(t)] = true
		}
	}
	return func(p Page) bool {
		for _, f := range filters {
			f = strings.ToLower(f)
			if !slices.ContainsFunc(p.Tags, func(t string) bool {
				t = strings.ToLower(t)
				return t == f || !known[f] && strings.HasPrefix(t, f)
			}) {
				return false
			}
		}
		return true
	}
}

// tagContains reports whether a tag of p contains q, lower case.
func tagContains(p Page, q string) bool {
	return slices.ContainsFunc(p.Tags, func(t string) bool { return strings.Contains(strings.ToLower(t), q) })
}

func snippet(text, q string) string {
	// Blocks read as a line of their own, set apart by a dot.
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	text = strings.Join(lines, " · ")
	lower := strings.ToLower(text)
	i, n := strings.Index(lower, q), len(q)
	if i < 0 {
		return ""
	}
	if len(lower) != len(text) {
		// Lowering changed the byte length, so the match is not at the
		// same place in text: show its start.
		i, n = 0, 0
	}
	start, end := max(i-30, 0), min(i+n+50, len(text))
	// Start and end at whole words, where one is near.
	if start > 0 {
		if j := strings.IndexByte(text[start:i], ' '); j >= 0 {
			start += j + 1
		}
	}
	if end < len(text) {
		if j := strings.LastIndexByte(text[i+n:end], ' '); j >= 0 {
			end = i + n + j
		}
	}
	for start > 0 && !utf8Start(text[start]) {
		start--
	}
	for end < len(text) && !utf8Start(text[end]) {
		end++
	}
	out := strings.Trim(text[start:end], " ·")
	if start > 0 {
		out = "…" + out
	}
	if end < len(text) {
		out += "…"
	}
	return out
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
