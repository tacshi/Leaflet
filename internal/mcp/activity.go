package mcp

import (
	"encoding/json"
	"log"
	"slices"
	"strings"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
)

// ActivityPrefix keys, by agent, the last changes an agent outside the app
// made to pages, which the app, while it runs, shows as they happen.
const ActivityPrefix = "mcp_activity:"

const (
	// keptActivity is how many changes of an agent's are kept at most, and
	// activityAge how long, for the app, which looks every second, to see
	// each of a quick run of them.
	keptActivity = 20
	activityAge  = time.Minute
)

// Activity is a change an agent outside the app made to a page's content.
type Activity struct {
	Agent  string `json:"agent"`
	PageID string `json:"page_id"`
	Tool   string `json:"tool"`
	// At is when it was saved, in Unix milliseconds, after the agent's change
	// before it.
	At int64 `json:"at"`
	// Added and Changed are the blocks it made, and those it changed or
	// moved, in the page's order, a table as one block; Removed is how many
	// it took out, each with what was in it.
	Added   []string `json:"added,omitempty"`
	Changed []string `json:"changed,omitempty"`
	Removed int      `json:"removed,omitempty"`
	// Near is, for an edit that only took blocks out, the block now where
	// the first of them was: the one after it, or else the one before.
	Near string `json:"near,omitempty"`
}

// ParseActivities reads an agent's last changes as Activities keeps them,
// the oldest first.
func ParseActivities(raw string) []Activity {
	var list []Activity
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil
	}
	return slices.DeleteFunc(list, func(a Activity) bool { return a.PageID == "" })
}

// Activities is each agent's last changes, as kept, by agent.
func Activities(st *store.Store) map[string]string { return st.ValuesWithPrefix(ActivityPrefix) }

// noteActivity keeps what a tool changed on a page, from the content before
// and after it, and its ops, for the app to show, after the agent's other
// changes of the last minute.
func (s *Server) noteActivity(tool, pageID string, before, after *doc.Content, ops []doc.Op) {
	now, age := doc.NowMs(), activityAge.Milliseconds()
	a := Activity{Agent: s.agent, PageID: pageID, Tool: tool, At: now}
	a.Added, a.Changed, a.Removed = blockChanges(before, after, ops)
	if len(a.Added)+len(a.Changed) == 0 && a.Removed > 0 {
		a.Near = nearRemoved(before, after)
	}
	key := ActivityPrefix + strings.ToLower(s.agent)
	kept, _ := s.st.Value(key)
	list := slices.DeleteFunc(ParseActivities(kept), func(k Activity) bool { return k.At < now-age || k.At > now+age })
	// Each comes after the one before, in the same millisecond too, as the
	// app takes in those after the last it took.
	if n := len(list); n > 0 {
		a.At = max(a.At, list[n-1].At+1)
	}
	list = append(list, a)
	list = list[max(len(list)-keptActivity, 0):]
	raw, err := json.Marshal(list)
	if err == nil {
		err = s.st.SetValue(key, string(raw))
	}
	if err != nil {
		log.Printf("mcp: %v", err)
	}
}

// blockChanges is the blocks after holds that before did not, those it
// holds changed or moved, and how many of before's it lost, not counting
// those in a block it lost too.
func blockChanges(before, after *doc.Content, ops []doc.Op) (added, changed []string, removed int) {
	was, in := map[string]string{}, map[string]string{}
	units(before, func(n, parent *doc.Node) {
		was[n.ID] = own(n)
		if parent != nil {
			in[n.ID] = parent.ID
		}
	})
	moved := map[string]bool{}
	for _, op := range ops {
		if op.Op == "move" {
			moved[op.ID] = true
		}
	}
	units(after, func(n, _ *doc.Node) {
		w, ok := was[n.ID]
		switch {
		case !ok:
			added = append(added, n.ID)
		case w != own(n) || moved[n.ID]:
			changed = append(changed, n.ID)
		}
		delete(was, n.ID)
	})
	for id := range was {
		if _, lost := was[in[id]]; !lost {
			removed++
		}
	}
	return added, changed, removed
}

// nearRemoved is the block of after where the first block before held and
// after lost was: the next of before's that after keeps, or else the one
// before it that it keeps.
func nearRemoved(before, after *doc.Content) string {
	kept := map[string]bool{}
	units(after, func(n, _ *doc.Node) { kept[n.ID] = true })
	var order []string
	units(before, func(n, _ *doc.Node) { order = append(order, n.ID) })
	for i, id := range order {
		if kept[id] {
			continue
		}
		for _, next := range order[i+1:] {
			if kept[next] {
				return next
			}
		}
		for j := i - 1; j >= 0; j-- {
			if kept[order[j]] {
				return order[j]
			}
		}
		return ""
	}
	return ""
}

// units visits c's blocks in order, each with the block it is in, nested
// ones after it, a table as one block, with its rows and cells, as own sees
// it.
func units(c *doc.Content, fn func(n, parent *doc.Node)) {
	if c == nil {
		return
	}
	var visit func([]*doc.Node, *doc.Node)
	visit = func(nodes []*doc.Node, parent *doc.Node) {
		for _, n := range nodes {
			fn(n, parent)
			if n.Type != "table" {
				visit(n.Children, n)
			}
		}
	}
	visit(c.Children, nil)
}
