package doc

import "testing"

func TestSubpagePlace(t *testing.T) {
	p := &Node{ID: "p", Type: "paragraph"}
	card := SubpageNode("page")
	c := FromBlocks([]*Node{p, card})
	at, ok := c.SubpagePlace("page")
	if !ok || at.After != "p" {
		t.Fatalf("place %+v, %v", at, ok)
	}
	c.Remove(card.ID)
	if err := c.Insert(at, SubpageNode("page")); err != nil || len(c.Children) != 2 || c.Children[1].Str("page_id") != "page" {
		t.Fatalf("put back: %v", err)
	}
	if _, ok := FromBlocks([]*Node{p}).SubpagePlace("page"); ok {
		t.Fatal("a place for no card")
	}
}
