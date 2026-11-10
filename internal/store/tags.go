package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	maxTags   = 50
	maxTagLen = 64
)

func NormalizeTags(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.Join(strings.Fields(strings.TrimLeft(strings.TrimSpace(t), "#")), " ")
		if r := []rune(t); len(r) > maxTagLen {
			t = strings.TrimSpace(string(r[:maxTagLen]))
		}
		k := strings.ToLower(t)
		if t == "" || seen[k] || len(out) == maxTags {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

func decodeTags(s string) []string {
	var tags []string
	_ = json.Unmarshal([]byte(s), &tags)
	if tags == nil {
		tags = []string{}
	}
	return tags
}

func encodeTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func (p Page) HasTag(tag string) bool {
	for _, t := range p.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

func (s *Store) SetPageTags(id string, tags []string) error {
	tags = NormalizeTags(tags)
	return s.changePage(id, "tagged", func(_ *sql.Tx, p *Page) (bool, error) {
		if slices.Equal(p.Tags, tags) {
			return false, nil
		}
		p.Tags = tags
		return true, nil
	})
}

type TagCount struct {
	Name  string
	Count int
}

func TagsOf(pages []Page) []TagCount {
	at := map[string]int{}
	var out []TagCount
	for _, p := range pages {
		for _, t := range p.Tags {
			k := strings.ToLower(t)
			if i, ok := at[k]; ok {
				out[i].Count++
				continue
			}
			at[k] = len(out)
			out = append(out, TagCount{Name: t, Count: 1})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Store) RenameTag(old, name string) (map[string][]string, error) {
	return s.retag(old, func(t string) string { return name })
}

func (s *Store) DeleteTag(tag string) (map[string][]string, error) {
	return s.retag(tag, func(string) string { return "" })
}

func (s *Store) retag(tag string, to func(string) string) (map[string][]string, error) {
	pages, err := s.queryPages(`deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, p := range pages {
		if p.HasTag(tag) && s.CanEdit(p) {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	before := map[string][]string{}
	now := time.Now()
	err = s.inTx(func(tx *sql.Tx) error {
		for _, id := range ids {
			p, err := pageIn(tx, id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return err
			}
			if !p.DeletedAt.IsZero() || !p.HasTag(tag) {
				continue
			}
			tags := make([]string, 0, len(p.Tags))
			for _, t := range p.Tags {
				if strings.EqualFold(t, tag) {
					t = to(t)
				}
				tags = append(tags, t)
			}
			if tags = NormalizeTags(tags); slices.Equal(tags, p.Tags) {
				continue
			}
			before[p.ID] = p.Tags
			p.Tags, p.UpdatedAt = tags, now
			if err := savePage(tx, p, "tagged"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || len(before) == 0 {
		return nil, err
	}
	for id := range before {
		s.Notify("outbox", id)
	}
	s.Notify("pages", "")
	return before, nil
}
