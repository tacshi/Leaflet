package doc

import (
	"strings"
	"unicode/utf8"
)

type Mention struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Attrs struct {
	Bold   bool
	Italic bool
	Under  bool
	Strike bool
	Code   bool
	// Math marks a formula in the line: the run's text is its TeX source.
	Math              bool
	Href              string
	Mention           *Mention
	Color, Background string
}

func ValidColor(s string) bool {
	if len(s) != 7 && len(s) != 9 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func (a Attrs) Zero() bool { return a == Attrs{} }

func (a Attrs) Equal(b Attrs) bool {
	if a.Bold != b.Bold || a.Italic != b.Italic || a.Under != b.Under ||
		a.Strike != b.Strike || a.Code != b.Code || a.Math != b.Math || a.Href != b.Href ||
		a.Color != b.Color || a.Background != b.Background {
		return false
	}
	if (a.Mention == nil) != (b.Mention == nil) {
		return false
	}
	return a.Mention == nil || *a.Mention == *b.Mention
}

type Run struct {
	Text  string
	Attrs Attrs
}

type Delta []Run

func DeltaFromJSON(v any) Delta {
	var out Delta
	switch ops := v.(type) {
	case []any:
		for _, raw := range ops {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			s, ok := m["insert"].(string)
			if !ok {
				continue
			}
			out = append(out, Run{Text: s, Attrs: attrsFromJSON(m["attributes"])})
		}
	case Delta:
		out = append(out, ops...)
	}
	return out.Compact()
}

func attrsFromJSON(v any) Attrs {
	m, _ := v.(map[string]any)
	var a Attrs
	if m == nil {
		return a
	}
	a.Bold, _ = m["bold"].(bool)
	a.Italic, _ = m["italic"].(bool)
	a.Under, _ = m["underline"].(bool)
	a.Strike, _ = m["strikethrough"].(bool)
	a.Code, _ = m["code"].(bool)
	a.Math, _ = m["math"].(bool)
	a.Href, _ = m["href"].(string)
	if s, _ := m["color"].(string); ValidColor(s) {
		a.Color = s
	}
	if s, _ := m["background"].(string); ValidColor(s) {
		a.Background = s
	}
	if mm, ok := m["mention"].(map[string]any); ok {
		men := &Mention{}
		men.Kind, _ = mm["kind"].(string)
		men.ID, _ = mm["id"].(string)
		men.Name, _ = mm["name"].(string)
		a.Mention = men
	}
	return a
}

func (a Attrs) json() map[string]any {
	m := map[string]any{}
	if a.Bold {
		m["bold"] = true
	}
	if a.Italic {
		m["italic"] = true
	}
	if a.Under {
		m["underline"] = true
	}
	if a.Strike {
		m["strikethrough"] = true
	}
	if a.Code {
		m["code"] = true
	}
	if a.Math {
		m["math"] = true
	}
	if a.Href != "" {
		m["href"] = a.Href
	}
	if a.Color != "" {
		m["color"] = a.Color
	}
	if a.Background != "" {
		m["background"] = a.Background
	}
	if a.Mention != nil {
		m["mention"] = map[string]any{"kind": a.Mention.Kind, "id": a.Mention.ID, "name": a.Mention.Name}
	}
	return m
}

func (d Delta) JSON() []any {
	out := make([]any, 0, len(d))
	for _, r := range d {
		if r.Text == "" {
			continue
		}
		op := map[string]any{"insert": r.Text}
		if !r.Attrs.Zero() {
			op["attributes"] = r.Attrs.json()
		}
		out = append(out, op)
	}
	return out
}

func (d Delta) Compact() Delta {
	out := make(Delta, 0, len(d))
	for _, r := range d {
		if r.Text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Attrs.Equal(r.Attrs) && r.Attrs.Mention == nil {
			out[n-1].Text += r.Text
			continue
		}
		out = append(out, r)
	}
	return out
}

func (d Delta) Plain() string {
	var b strings.Builder
	for _, r := range d {
		b.WriteString(r.Text)
	}
	return b.String()
}

func (d Delta) Len() int {
	n := 0
	for _, r := range d {
		n += utf8.RuneCountInString(r.Text)
	}
	return n
}

func (d Delta) Slice(start, end int) Delta {
	var out Delta
	at := 0
	for _, r := range d {
		rs := []rune(r.Text)
		a, b := at, at+len(rs)
		at = b
		lo, hi := max(a, start), min(b, end)
		if lo >= hi {
			continue
		}
		out = append(out, Run{Text: string(rs[lo-a : hi-a]), Attrs: r.Attrs})
	}
	return out
}

func (d Delta) Concat(e Delta) Delta {
	out := make(Delta, 0, len(d)+len(e))
	out = append(out, d...)
	out = append(out, e...)
	return out.Compact()
}

func (d Delta) MentionAround(off int) (start, end int, ok bool) {
	at := 0
	for _, r := range d {
		n := utf8.RuneCountInString(r.Text)
		if off <= at {
			break
		}
		if r.Attrs.Mention != nil && off < at+n {
			return at, at + n, true
		}
		at += n
	}
	return 0, 0, false
}

func (d Delta) Widen(start, end int) (int, int) {
	if s, _, ok := d.MentionAround(start); ok {
		start = s
	}
	if _, e, ok := d.MentionAround(end); ok {
		end = e
	}
	return start, end
}

func (d Delta) Insert(at int, text string, attrs Attrs) Delta {
	if _, end, ok := d.MentionAround(at); ok {
		at = end
	}
	out := d.Slice(0, at)
	out = append(out, Run{Text: text, Attrs: attrs})
	out = append(out, d.Slice(at, d.Len())...)
	return out.Compact()
}

func (d Delta) Delete(start, end int) Delta {
	if start < end {
		start, end = d.Widen(start, end)
	}
	return d.Slice(0, start).Concat(d.Slice(end, d.Len()))
}

func (d Delta) Format(start, end int, fn func(*Attrs)) Delta {
	if start < end {
		start, end = d.Widen(start, end)
	}
	var out Delta
	out = append(out, d.Slice(0, start)...)
	for _, r := range d.Slice(start, end) {
		fn(&r.Attrs)
		out = append(out, r)
	}
	out = append(out, d.Slice(end, d.Len())...)
	return out.Compact()
}

func (d Delta) AttrsAt(at int) Attrs {
	pos := 0
	var last Attrs
	inLink := false
	for i, r := range d {
		n := utf8.RuneCountInString(r.Text)
		if at <= pos+n && at > pos {
			last = r.Attrs
			next := r.Attrs
			if at == pos+n {
				next = Attrs{}
				if i+1 < len(d) {
					next = d[i+1].Attrs
				}
			}
			inLink = last.Mention == nil && next.Mention == nil && next.Href == last.Href
			break
		}
		if at == 0 {
			last = r.Attrs
			break
		}
		pos += n
	}
	last.Mention = nil
	if !inLink {
		last.Href = ""
	}
	return last
}

func (d Delta) All(start, end int, has func(Attrs) bool) bool {
	if start >= end {
		return false
	}
	for _, r := range d.Slice(start, end) {
		if !has(r.Attrs) {
			return false
		}
	}
	return true
}
