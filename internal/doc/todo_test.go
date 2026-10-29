package doc

import (
	"testing"
	"time"
)

func TestTodos(t *testing.T) {
	todo := func(id, text string, checked bool) *Node {
		n := &Node{ID: id, Type: "todo_list"}
		n.SetDelta(Delta{{Text: text}})
		n.Set("checked", checked)
		return n
	}
	dated := todo("a", "Send the draft", false)
	dated.SetDue(time.Date(2026, 10, 30, 15, 0, 0, 0, time.Local), false)
	owned := todo("b", "", false)
	owned.SetDelta(Delta{{Text: "Review it "}, {Text: "Sam", Attrs: Attrs{Mention: &Mention{Kind: "user", ID: "u1", Name: "Sam"}}}})
	done := todo("c", "Old", true)
	done.SetDue(time.Now(), false)
	plain := todo("d", "No date", false)
	c := FromBlocks([]*Node{dated, owned, done, plain})
	got := c.Todos()
	if len(got) != 2 || got[0].BlockID != "a" || got[1].Owner != "Sam" {
		t.Fatalf("todos %+v", got)
	}
	if d := got[0].Due; d.Year() != 2026 || d.Month() != 10 || d.Day() != 30 || d.Hour() != 0 || got[0].Timed {
		t.Errorf("due %v, timed %v", d, got[0].Timed)
	}
	// With a time of day, it keeps the hour and minute.
	dated.SetDue(time.Date(2026, 10, 30, 15, 30, 45, 0, time.Local), true)
	if dated.Str("due") != "2026-10-30T15:30" {
		t.Errorf("timed due %q", dated.Str("due"))
	}
	if d, timed, ok := dated.Due(); !ok || !timed || d.Hour() != 15 || d.Minute() != 30 {
		t.Errorf("timed due %v %v %v", d, timed, ok)
	}
	dated.SetDue(time.Time{}, false)
	if _, _, ok := dated.Due(); ok {
		t.Error("the date is still there")
	}
	// Agents write a due date as YYYY-MM-DD, and read it back.
	n, err := SpecToNode(map[string]any{"type": "todo_list", "text": "Ship", "due": "2026-11-02"}, "", "Agent")
	if err != nil || n.Str("due") != "2026-11-02" {
		t.Fatalf("spec %+v %v", n, err)
	}
	if v := BlockViews(FromBlocks([]*Node{n}))[0]; v["due"] != "2026-11-02" {
		t.Errorf("view %+v", v)
	}
	// Or as YYYY-MM-DDTHH:MM, with a time of day.
	n, err = SpecToNode(map[string]any{"type": "todo_list", "text": "Call", "due": "2026-11-02T09:15"}, "", "Agent")
	if err != nil || n.Str("due") != "2026-11-02T09:15" {
		t.Fatalf("timed spec %+v %v", n, err)
	}
}
