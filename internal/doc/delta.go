package doc

import (
	"strings"
	"unicode/utf8"
)

// Mention is a person or an agent named inline.
type Mention struct {
	Kind string `json:"kind"` // "user" or "agent"
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Attrs are the inline formats of a run of text.
type Attrs struct {
	Bold    bool
	Italic  bool
	Under   bool
	Strike  bool
	Code    bool
	Href    string
	Mention *Mention
	// Color colors the text and Background highlights it, as hex
	// ("#rrggbbaa"), as MyGo's rich text does; "" for none.
	Color, Background string
}

// ValidColor reports whether s is a color as runs keep one: "#" and 6 or
// 8 hex digits.
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

// Zero reports whether the run is plain text.
func (a Attrs) Zero() bool { return a == Attrs{} }

// Equal compares two sets of formats.
func (a Attrs) Equal(b Attrs) bool {
	if a.Bold != b.Bold || a.Italic != b.Italic || a.Under != b.Under ||
		a.Strike != b.Strike || a.Code != b.Code || a.Href != b.Href ||
		a.Color != b.Color || a.Background != b.Background {
		return false
	}
	if (a.Mention == nil) != (b.Mention == nil) {
		return false
	}
	return a.Mention == nil || *a.Mention == *b.Mention
}

// Run is text in one set of formats: one insert of an appflowy delta.
type Run struct {
	Text  string
	Attrs Attrs
}

// Delta is a block's rich text, runs in order.
type Delta []Run

// DeltaFromJSON reads an appflowy delta: [{insert, attributes?}].
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

// JSON returns the delta as plain JSON values.
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

// Compact drops empty runs and joins neighbours with equal formats.
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

// Plain returns the text without formats.
func (d Delta) Plain() string {
	var b strings.Builder
	for _, r := range d {
		b.WriteString(r.Text)
	}
	return b.String()
}

// Len is the length of the text in runes.
func (d Delta) Len() int {
	n := 0
	for _, r := range d {
		n += utf8.RuneCountInString(r.Text)
	}
	return n
}

// Slice returns the runes from start to end, with their formats.
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

// Concat returns d followed by e.
func (d Delta) Concat(e Delta) Delta {
	out := make(Delta, 0, len(d)+len(e))
	out = append(out, d...)
	out = append(out, e...)
	return out.Compact()
}

// MentionAround returns where the mention that a rune offset is inside
// starts and ends; ok is false when the offset is not inside one.
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

// Widen widens a range of runes to the whole of each mention it cuts: a
// mention is one unit, taken out or formatted whole.
func (d Delta) Widen(start, end int) (int, int) {
	if s, _, ok := d.MentionAround(start); ok {
		start = s
	}
	if _, e, ok := d.MentionAround(end); ok {
		end = e
	}
	return start, end
}

// Insert inserts text in the given formats at a rune offset; inside a
// mention, after it.
func (d Delta) Insert(at int, text string, attrs Attrs) Delta {
	if _, end, ok := d.MentionAround(at); ok {
		at = end
	}
	out := d.Slice(0, at)
	out = append(out, Run{Text: text, Attrs: attrs})
	out = append(out, d.Slice(at, d.Len())...)
	return out.Compact()
}

// InsertDelta inserts rich text at a rune offset; inside a mention,
// after it.
func (d Delta) InsertDelta(at int, e Delta) Delta {
	if _, end, ok := d.MentionAround(at); ok {
		at = end
	}
	return d.Slice(0, at).Concat(e).Concat(d.Slice(at, d.Len()))
}

// Delete removes the runes from start to end, and the whole of a mention
// they are part of.
func (d Delta) Delete(start, end int) Delta {
	if start < end {
		start, end = d.Widen(start, end)
	}
	return d.Slice(0, start).Concat(d.Slice(end, d.Len()))
}

// Format changes the formats of the runes from start to end, and of the
// whole of a mention they are part of.
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

// AttrsAt returns the formats typing at a rune offset continues: those of
// the rune before it, without links or mentions.
func (d Delta) AttrsAt(at int) Attrs {
	pos := 0
	var last Attrs
	for _, r := range d {
		n := utf8.RuneCountInString(r.Text)
		if at <= pos+n && at > pos {
			last = r.Attrs
			break
		}
		if at == 0 {
			last = r.Attrs
			break
		}
		pos += n
	}
	last.Mention = nil
	last.Href = ""
	return last
}

// All reports whether every rune from start to end has a format.
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
