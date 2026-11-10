package doc

import (
	"regexp"
	"time"
)

const (
	DueLayout     = "2006-01-02"
	DueTimeLayout = "2006-01-02T15:04"
)

func ParseDue(s string) (t time.Time, timed, ok bool) {
	if t, err := time.ParseInLocation(DueTimeLayout, s, time.Local); err == nil {
		return t, true, true
	}
	t, err := time.ParseInLocation(DueLayout, s, time.Local)
	return t, false, err == nil
}

var dueForm = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2})?$`)

func validDue(s string) bool {
	_, _, ok := ParseDue(s)
	return ok && dueForm.MatchString(s)
}

func (n *Node) Due() (t time.Time, timed, ok bool) {
	return ParseDue(n.Str("due"))
}

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

type Todo struct {
	BlockID string
	Text    string
	Due     time.Time
	Timed   bool
	Owner   string
	OwnerID string
}

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
				t.Owner, t.OwnerID = m.Name, m.ID
				break
			}
		}
		if !t.Due.IsZero() || t.Owner != "" {
			out = append(out, t)
		}
	})
	return out
}
