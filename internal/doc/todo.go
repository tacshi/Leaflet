package doc

import "time"

// DueLayout is how a to-do keeps its due date: a day, in the user's own
// time zone; DueTimeLayout is a day with its time of day.
const (
	DueLayout     = "2006-01-02"
	DueTimeLayout = "2006-01-02T15:04"
)

// ParseDue reads a due date, a day or a day with its time, here: a day is
// at its start, and timed reports a time.
func ParseDue(s string) (t time.Time, timed, ok bool) {
	if t, err := time.ParseInLocation(DueTimeLayout, s, time.Local); err == nil {
		return t, true, true
	}
	t, err := time.ParseInLocation(DueLayout, s, time.Local)
	return t, false, err == nil
}

// Due returns a to-do's due date, at the start of that day here or at its
// time of day, which timed reports.
func (n *Node) Due() (t time.Time, timed, ok bool) {
	return ParseDue(n.Str("due"))
}

// SetDue gives a to-do a due date, with its time of day when timed, or
// takes it away for a zero time.
func (n *Node) SetDue(t time.Time, timed bool) {
	if t.IsZero() {
		delete(n.Data, "due")
		return
	}
	layout := DueLayout
	if timed {
		layout = DueTimeLayout
	}
	n.Set("due", t.In(time.Local).Format(layout))
}

// Todo is an open to-do of a page that has a due date, or someone to do
// it: the first person mentioned in it.
type Todo struct {
	BlockID string
	Text    string
	Due     time.Time // zero for none
	Timed   bool      // whether Due has a time of day
	Owner   string
}

// Todos returns a page's open to-dos with a due date or an owner, in
// document order.
func (c *Content) Todos() []Todo {
	var out []Todo
	c.Walk(func(n, _ *Node) {
		if n.Type != "todo_list" || n.Bool("checked") {
			return
		}
		t := Todo{BlockID: n.ID, Text: n.Delta().Plain()}
		t.Due, t.Timed, _ = n.Due()
		for _, r := range n.Delta() {
			if m := r.Attrs.Mention; m != nil && m.Kind == "user" {
				t.Owner = m.Name
				break
			}
		}
		if !t.Due.IsZero() || t.Owner != "" {
			out = append(out, t)
		}
	})
	return out
}
