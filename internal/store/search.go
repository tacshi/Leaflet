package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"leaflet/internal/guard"
	"leaflet/locales"
)

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
	return s.SearchContext(context.Background(), query, limit)
}

// SearchContext is Search, which stops, with ctx's error, once ctx is
// done, as when the user typed on and a newer search replaces it.
//
// Text matches as titles do: lowered as Go lowers it, and with % and _ the
// characters they are. The texts are found by the trigrams of what is
// searched for (search_index), then read and matched: only the hits listed
// count toward the limits, so pages deleted, found by their titles
// already, or left out by the tags do not crowd the others out; the most
// recently changed come first.
func (s *Store) SearchContext(ctx context.Context, query string, limit int) ([]SearchHit, error) {
	return s.search(ctx, query, limit, nil)
}

// SearchPages finds pages as the agent function's search does
// (agent_search_pages), the pages keep keeps alone: no templates,
// recordings or comments, so that only those pages count toward the
// limit. Titles and tags that match come first, then text, each the most
// recently changed page first, a match in text with the 40 characters
// either side of it; tags keep to the pages with each of them, in any
// case, and a blank query lists the most recently edited, up to limit.
func (s *Store) SearchPages(ctx context.Context, query string, limit int, keep func(Page) bool) ([]SearchHit, error) {
	return s.search(ctx, query, limit, keep)
}

// newestFirst orders pages the most recently changed first, then by ID.
func newestFirst(a, b Page) int {
	if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

func (s *Store) search(ctx context.Context, query string, limit int, keep func(Page) bool) ([]SearchHit, error) {
	filters, rest := SplitTagQuery(query)
	q := strings.ToLower(rest)
	pages, err := s.Pages()
	if err != nil {
		return nil, err
	}
	var templates []Page
	if keep != nil {
		pages = slices.DeleteFunc(pages, func(p Page) bool { return !keep(p) })
		slices.SortStableFunc(pages, newestFirst)
	} else if templates, err = s.Templates(); err != nil {
		return nil, err
	}
	// Text, transcripts and comments are read for every page there is,
	// the pages with the tags among them, while tags filter: a limit
	// before the filter would let other pages' matches crowd theirs out.
	textLimit, otherLimit := limit, 10
	if len(filters) > 0 {
		match := tagMatcher(filters, append(append([]Page{}, pages...), templates...))
		if keep != nil {
			match = hasTags(filters)
		}
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
		slices.SortStableFunc(recent, newestFirst)
		for i, p := range recent {
			if i == 8 && len(filters) == 0 && keep == nil || i == limit {
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
	// What the index lacks is read as it is: a search does not fail for
	// it.
	if err := s.indexSearch(ctx, indexBudget); ctx.Err() != nil {
		return nil, ctx.Err()
	} else if err != nil {
		log.Printf("search index: %v", err)
	}
	// The pages' text, the most recently changed first.
	found, err := s.searchCandidates(ctx, "page", q)
	if err != nil {
		return nil, err
	}
	listed := slices.Concat(pages, templates)
	slices.SortFunc(listed, newestFirst)
	// Of a page found by its text alone, the words around the match.
	cut := func(text string) string { return snippet(text, q) }
	if keep != nil {
		at := regexp.MustCompile("(?i)" + regexp.QuoteMeta(rest))
		cut = func(text string) string { return around(text, at, rest) }
	}
	n := 0
	for _, p := range listed {
		if n == textLimit {
			break
		}
		if seen[p.ID] || !found.has(p.ID) {
			continue
		}
		text, ok, err := s.matchText(ctx, "page", `SELECT plain_text FROM page_contents WHERE page_id = ?`, p.ID, q)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		kind := "page"
		if p.Template {
			kind = "template"
		}
		hits = append(hits, SearchHit{Kind: kind, Title: TitleOr(p.Title), PageID: p.ID, Snippet: cut(text)})
		n++
	}
	// SearchPages finds pages alone.
	if keep != nil {
		return hits[:min(len(hits), limit)], nil
	}
	// Recordings and comments, as the text, of the pages listed alone.
	if err := s.searchRows(ctx, "recording", q, otherLimit, `SELECT r.id, r.page_id, r.block_id FROM recordings r JOIN pages p ON p.id = r.page_id
		WHERE p.deleted_at IS NULL AND p.is_template = 0 ORDER BY r.created_at DESC, r.id`, `SELECT transcript FROM recordings WHERE id = ?`,
		titles, func(id, block, _, title string) {
			hits = append(hits, SearchHit{Kind: "recording", Title: locales.StoreSearchRecording(TitleOr(title)), PageID: id, BlockID: block})
		}); err != nil {
		return nil, err
	}
	if err := s.searchRows(ctx, "comment", q, otherLimit, `SELECT c.id, c.page_id, c.block_id FROM comments c JOIN pages p ON p.id = c.page_id
		WHERE p.deleted_at IS NULL AND p.is_template = 0 AND c.deleted_at IS NULL ORDER BY c.created_at DESC, c.id`, `SELECT body FROM comments WHERE id = ?`,
		titles, func(id, block, body, title string) {
			hits = append(hits, SearchHit{Kind: "comment", Title: body, PageID: id, BlockID: block, Snippet: locales.StoreSearchCommentOn(TitleOr(title))})
		}); err != nil {
		return nil, err
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// searchRows lists the recordings or comments (kind) whose text, read by
// id with text, contains q, lowered, up to limit, in the order list lists
// them as their id, page and block: those of the pages in titles alone.
func (s *Store) searchRows(ctx context.Context, kind, q string, limit int, list, text string, titles map[string]string, hit func(pageID, block, text, title string)) error {
	found, err := s.searchCandidates(ctx, kind, q)
	if err != nil {
		return err
	}
	type row struct{ id, page, block string }
	rows, err := s.db.QueryContext(ctx, list)
	listed, err := collect(rows, err, func(sc scanner) (row, error) {
		var r row
		err := sc.Scan(&r.id, &r.page, &r.block)
		return r, err
	})
	if err != nil {
		return err
	}
	n := 0
	for _, r := range listed {
		if n == limit {
			break
		}
		title, ok := titles[r.page]
		if !ok || !found.has(r.id) {
			continue
		}
		body, ok, err := s.matchText(ctx, kind, text, r.id, q)
		if err != nil {
			return err
		}
		if ok {
			hit(r.page, r.block, body, title)
			n++
		}
	}
	return nil
}

// matchText reads a text of kind by id with query, and reports whether it
// holds q, lowered as Go lowers it. One gone meanwhile holds nothing.
func (s *Store) matchText(ctx context.Context, kind, query, id, q string) (string, bool, error) {
	var text string
	err := s.db.QueryRowContext(ctx, query, id).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	text = searchedText(kind, text)
	return text, strings.Contains(strings.ToLower(text), q), nil
}

// searchedText is a text of kind as search reads it. A recording's is its
// transcript, kept as its lines in JSON: what was said is searched, a line
// each, not the JSON, whose names and times every recording has, and which
// writes a quote or an & another way.
func searchedText(kind, text string) string {
	if kind != "recording" {
		return text
	}
	var lines []Segment
	if json.Unmarshal([]byte(text), &lines) != nil {
		return text
	}
	said := make([]string, len(lines))
	for i, l := range lines {
		said[i] = l.Text
	}
	return strings.Join(said, "\n")
}

// candidates are the texts of a kind that may hold what is searched for,
// by ID: those whose trigrams search_index has, and those it does not
// have as they are now. all is set when every one may, as for a search of
// fewer than three characters, which has no trigram.
type candidates struct {
	ids map[string]bool
	all bool
}

func (c candidates) has(id string) bool { return c.all || c.ids[id] }

// searchCandidates finds the texts of a kind that may hold q, lowered, in
// search_index: those with each of its trigrams. Search reads and matches
// each, so what the index cannot tell apart, as the trigrams in another
// order, is never a hit.
func (s *Store) searchCandidates(ctx context.Context, kind, q string) (candidates, error) {
	grams := trigrams(q)
	if len(grams) == 0 {
		return candidates{all: true}, nil
	}
	quoted := make([]string, len(grams))
	for i, g := range grams {
		quoted[i] = `"` + strings.ReplaceAll(g, `"`, `""`) + `"`
	}
	out := candidates{ids: map[string]bool{}}
	// Those written since they were indexed, or never, as while the
	// index is made, are read as they are. It is one statement, which
	// sees the index and the writes as they were at one moment: indexing
	// in the background between two would take a text out of what is
	// left to index after the index was asked, and it would be in neither.
	rows, err := s.db.QueryContext(ctx, `SELECT w.id FROM text_writes w
		WHERE w.kind = ? AND (w.indexed IS NOT w.write_id
			OR w.n IN (SELECT rowid FROM search_index WHERE search_index MATCH ?))`, kind, strings.Join(quoted, " AND "))
	if err := each(rows, err, func(sc scanner) (bool, error) {
		var id string
		err := sc.Scan(&id)
		out.ids[id] = true
		return true, err
	}); err != nil {
		return candidates{}, err
	}
	return out, nil
}

// maxTrigrams is how many of a search's trigrams the index is asked for:
// a long search has more than it takes to tell texts apart, and each is
// one more list for the index to read.
const maxTrigrams = 24

// trigrams are the distinct runs of three characters in q, as
// search_index's tokenizer makes them, at most maxTrigrams of them spread
// over q: none for fewer than three characters.
func trigrams(q string) []string {
	runes := []rune(q)
	if len(runes) < 3 {
		return nil
	}
	var all []string
	for i := 0; i+3 <= len(runes); i++ {
		if g := string(runes[i : i+3]); !slices.Contains(all, g) {
			all = append(all, g)
		}
	}
	if len(all) <= maxTrigrams {
		return all
	}
	out := make([]string, 0, maxTrigrams)
	for i := range maxTrigrams {
		out = append(out, all[i*(len(all)-1)/(maxTrigrams-1)])
	}
	return out
}

// indexBudget is how long a search indexes the texts written since the
// last, before it searches: those it leaves are read as they are, and
// indexed in the background.
const indexBudget = 50 * time.Millisecond

// indexSearch brings search_index up to date with the texts written since
// it last was, for up to budget, a few at a time in a transaction, which
// reads each text with its write: the write it records is the text it
// indexed. With texts still to index, as a database from before the index
// has, it goes on in the background.
func (s *Store) indexSearch(ctx context.Context, budget time.Duration) error {
	done, err := s.indexFor(ctx, budget)
	if err == nil && !done && s.indexing.CompareAndSwap(false, true) {
		guard.Go(func() {
			defer s.indexing.Store(false)
			for {
				// A little at a time, so the app's own reads and writes
				// wait on it no longer than a search does.
				if done, err := s.indexFor(context.Background(), indexBudget); err != nil || done {
					return
				}
				time.Sleep(indexBudget)
			}
		})
	}
	return err
}

// indexFor indexes the texts written since they were, for up to budget,
// and reports whether it indexed all of them.
func (s *Store) indexFor(ctx context.Context, budget time.Duration) (bool, error) {
	until := time.Now().Add(budget)
	for time.Now().Before(until) {
		rows, err := s.db.QueryContext(ctx, `SELECT n FROM text_writes WHERE indexed IS NOT write_id LIMIT 25`)
		stale, err := collect(rows, err, func(sc scanner) (int64, error) {
			var n int64
			err := sc.Scan(&n)
			return n, err
		})
		if err != nil || len(stale) == 0 {
			return err == nil, err
		}
		if err := s.inTx(func(tx *sql.Tx) error {
			for _, n := range stale {
				if err := indexText(tx, n); err != nil {
					return err
				}
				if time.Now().After(until) {
					break
				}
			}
			return nil
		}); err != nil {
			return false, err
		}
	}
	return false, nil
}

// searchTexts are where each kind of text search_index holds is: its
// table, key and column.
var searchTexts = map[string][3]string{
	"page":      {"page_contents", "page_id", "plain_text"},
	"recording": {"recordings", "id", "transcript"},
	"comment":   {"comments", "id", "body"},
}

// indexText puts text_writes' row n's text in search_index, lowered, in
// place of what it had of it.
func indexText(tx *sql.Tx, n int64) error {
	var kind, id string
	var written int64
	var indexed sql.NullInt64
	switch err := tx.QueryRow(`SELECT kind, id, write_id, indexed FROM text_writes WHERE n = ?`, n).Scan(&kind, &id, &written, &indexed); {
	case errors.Is(err, sql.ErrNoRows):
		return nil // its text went meanwhile
	case err != nil:
		return err
	}
	// Indexed meanwhile, as by the search running beside the indexing in
	// the background.
	src, ok := searchTexts[kind]
	if !ok || indexed.Valid && indexed.Int64 == written {
		return nil
	}
	var text string
	switch err := tx.QueryRow(`SELECT `+src[2]+` FROM `+src[0]+` WHERE `+src[1]+` = ?`, id).Scan(&text); { // #nosec G202 -- searchTexts' own names.
	case errors.Is(err, sql.ErrNoRows):
		return nil // gone, and its row with it
	case err != nil:
		return err
	}
	if indexed.Valid {
		if _, err := tx.Exec(`DELETE FROM search_index WHERE rowid = ?`, n); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO search_index (rowid, text) VALUES (?, ?)`, n, strings.ToLower(searchedText(kind, text))); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE text_writes SET indexed = ? WHERE n = ?`, written, n)
	return err
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

// hasTags reports whether a page has every tag of filters, in any case,
// lowered as the server lowers them (lower(t) = lower(want)) rather than
// folded.
func hasTags(filters []string) func(Page) bool {
	want := make([]string, len(filters))
	for i, f := range filters {
		want[i] = strings.ToLower(f)
	}
	return func(p Page) bool {
		for _, f := range want {
			if !slices.ContainsFunc(p.Tags, func(t string) bool { return strings.ToLower(t) == f }) {
				return false
			}
		}
		return true
	}
}

// around is the words around the first match of at, of what, in text, as
// the agent function's search cuts them: from 40 characters before it,
// as long as what and 80 more.
func around(text string, at *regexp.Regexp, what string) string {
	from := 0
	if loc := at.FindStringIndex(text); loc != nil {
		from = max(utf8.RuneCountInString(text[:loc[0]])-40, 0)
	}
	runes := []rune(text)
	return string(runes[min(from, len(runes)):min(from+utf8.RuneCountInString(what)+80, len(runes))])
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
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
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
