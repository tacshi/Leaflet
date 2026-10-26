package doc

import "time"

// DueLayout is how a to-do keeps its due date: a day, in the user's own
// time zone.
const DueLayout = "2006-01-02"

// Due returns a to-do's due date, at the start of that day here.
func (n *Node) Due() (time.Time, bool) {
	t, err := time.ParseInLocation(DueLayout, n.Str("due"), time.Local)
	return t, err == nil
}

// SetDue gives a to-do a due date, or takes it away for a zero time.
func (n *Node) SetDue(t time.Time) {
	if t.IsZero() {
		delete(n.Data, "due")
		return
	}
	n.Set("due", t.Format(DueLayout))
}

// Todo is an open to-do of a page that has a due date, or someone to do
// it: the first person mentioned in it.
type Todo struct {
	BlockID string
	Text    string
	Due     time.Time // zero for none
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
		t.Due, _ = n.Due()
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
