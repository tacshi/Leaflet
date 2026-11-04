package agents

import (
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"leaflet/internal/doc"
	"leaflet/internal/store"
	"leaflet/internal/web"
	"leaflet/locales"
)

// How a run's answer and ops reach its page: open, as they come, or
// stored, over what else changed it, keeping what they take away.

// streamEvery spaces the showings of a reply as it streams: each is a
// redraw, and on iOS a fetch of the page's agent state.
const streamEvery = 50 * time.Millisecond

// replyStream shows a run's answer as it streams in, in its chat reply
// when it has one, set from the run and shown on the main thread.
type replyStream struct {
	h      *Hub
	pageID string
	run    *Run
	reply  *Message
	mu     sync.Mutex
	text   string
	shown  time.Time
	timer  *time.Timer
}

// set takes the answer so far, to show it streamEvery after the last
// showing at the soonest.
func (st *replyStream) set(text string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.text = text
	if st.timer != nil {
		return
	}
	st.timer = time.AfterFunc(max(0, streamEvery-time.Since(st.shown)), func() {
		st.mu.Lock()
		st.timer, st.shown = nil, time.Now()
		st.mu.Unlock()
		st.h.host.Update(st.pageID, st.show)
	})
}

// clear drops an answer an edit of the page goes on from: the reply says
// what the run did instead.
func (st *replyStream) clear() {
	st.mu.Lock()
	st.text = ""
	st.mu.Unlock()
}

// show puts the answer in the reply while the run works, its thinking
// left out; an answer still thinking leaves the reply as it was.
func (st *replyStream) show() {
	st.mu.Lock()
	text := shownText(st.text)
	st.mu.Unlock()
	if text == "" || st.run.Status == "done" || st.run.Status == "failed" {
		return
	}
	st.run.Answer, st.run.Activity = text, ""
	if st.reply != nil && st.reply.Pending {
		st.reply.Text, st.reply.Streamed = text, true
	}
}

// delivery is how far a run's ops have reached the page: those before
// sent are in an open editor, or were saved with it when it closed.
type delivery struct {
	sent int
	ed   LiveDoc
	// conflict keeps an open page's content in history before an op takes
	// a change of it away; nil keeps none.
	conflict func(*doc.Content) error
	// chatSaved is when the page's chat last kept the run's ops.
	chatSaved time.Time
}

// newDelivery is a run's delivery to its page, which keeps in the page's
// history what the run's ops take away of the open page, under its job.
func (h *Hub) newDelivery(pageID string, run *Run, option Option) *delivery {
	return &delivery{conflict: func(c *doc.Content) error {
		return h.st.AddHistory(pageID, c, "conflict", runJobID(run, option), "")
	}}
}

// to applies the ops ed does not have yet, in order, so an op never comes
// before the blocks it needs.
func (d *delivery) to(ed LiveDoc, ops []doc.Op) {
	for _, op := range ops[d.sent:] {
		if op.Op == "create_page" || op.Op == "delete_page" {
			continue
		}
		d.apply(ed, op)
	}
	d.sent = len(ops)
}

// apply applies one op, or one change of an op as its call streams, to
// ed. The first op an editor gets starts an undo step that the rest join.
// An op that replaces or deletes a block the user changed since the agent
// read it keeps the page as it is in history first (conflict), as
// storeOps does on a closed page. Each op is checked against the page
// just before it, which has the run's earlier ops by then, so the agent's
// own edits are no change.
func (d *delivery) apply(ed LiveDoc, op doc.Op) {
	if d.conflict != nil && overwrites(ed.Doc(), []doc.Op{op}) {
		if err := d.conflict(ed.Doc()); err != nil {
			log.Printf("agent op on the open page: %v", err)
		}
	}
	if err := ed.ApplyOp(op, ed != d.ed); err != nil {
		log.Printf("agent op on the open page: %v", err)
	}
	d.ed = ed
}

// missed are the block ops the page does not have yet.
func (d *delivery) missed(ops []doc.Op) []doc.Op {
	var out []doc.Op
	for _, op := range ops[d.sent:] {
		if op.Op != "create_page" && op.Op != "delete_page" {
			out = append(out, op)
		}
	}
	return out
}

// save applies the ops the page does not have yet to its stored content,
// which keeps whatever else changed there during the run, and in history
// what they would take away of it (storeOps), under the run jobID. They
// count as delivered once stored, so a save that fails can be tried again.
func (d *delivery) save(st *store.Store, pageID string, ops []doc.Op, jobID string) error {
	if missed := d.missed(ops); len(missed) > 0 {
		if err := storeOps(st, pageID, missed, true, false, jobID, "agent op on a closed page"); err != nil {
			return err
		}
	}
	d.sent = len(ops)
	return nil
}

// applyAll applies ops to c, stopping at the first that does not apply.
func applyAll(c *doc.Content, ops []doc.Op) error {
	for _, op := range ops {
		if err := c.Apply(op); err != nil {
			return err
		}
	}
	return nil
}

// errKeptChanging is a closed page stored over each time the ops were
// applied to it, said in the app's language.
var errKeptChanging error = errPageKeptChanging{}

type errPageKeptChanging struct{}

func (errPageKeptChanging) Error() string { return locales.AgentPageKeptChanging() }

// storeOps applies ops to a page's stored content, an empty page when
// there is none and orEmpty, and saves it only over the content it read:
// what MCP or a sync stores in between is read again and gets the ops,
// rather than being saved over. Ops that replace or delete a block
// changed since they were made take that change away, so the content
// they find is kept in history first (reason conflict, under jobID), as
// the agent function does; one that could not be kept is not saved over.
// An op that does not apply is passed over, as the agent's other edits
// still go; with strict, it stops them all, and nothing is saved.
func storeOps(st *store.Store, pageID string, ops []doc.Op, orEmpty, strict bool, jobID, what string) error {
	inHistory := "" // the stored content history has already
	err := st.UpdateContent(pageID, func(c *doc.Content, kept bool) (bool, error) {
		if !kept && !orEmpty {
			return false, nil
		}
		if read := c.Encode(); kept && read != inHistory && overwrites(c, ops) {
			if err := st.AddHistory(pageID, c, "conflict", jobID, ""); err != nil {
				return false, err
			}
			inHistory = read
		}
		if strict {
			if err := applyAll(c, ops); err != nil {
				return false, err
			}
		} else {
			for _, op := range ops {
				if err := c.Apply(op); err != nil {
					log.Printf("%s: %v", what, err)
				}
			}
		}
		beforeStore(pageID)
		return true, nil
	})
	switch {
	case errors.Is(err, store.ErrContentChanged):
		return errKeptChanging
	case errors.Is(err, store.ErrUnreadable):
		return errUnreadable
	}
	return err
}

// overwrites reports whether applying ops to c takes away a change made
// since they were made: a block that a replace or a delete finds other
// than the agent read it, as the agent function's overwrites has it. The
// first op on a block has it as read; a replace that keeps the block's
// children looks at the block alone, as Apply keeps the children found.
// A table's cell, and a block in one, which Apply passes over, take
// nothing away.
func overwrites(c *doc.Content, ops []doc.Op) bool {
	seen := map[string]bool{}
	changed := func(read *doc.Node, own bool) bool {
		if read == nil || seen[read.ID] {
			return false
		}
		seen[read.ID] = true
		at := c.Addressable(read.ID)
		if at == nil {
			return false
		}
		at = asRead(at, read)
		if own {
			return encodeNode(&doc.Node{ID: at.ID, Type: at.Type, Data: at.Data}) != encodeNode(&doc.Node{ID: read.ID, Type: read.Type, Data: read.Data})
		}
		return encodeNode(at) != encodeNode(read)
	}
	found := false
	for _, op := range ops {
		switch op.Op {
		case "insert":
			for _, n := range op.Nodes {
				walk(n, func(k *doc.Node) { seen[k.ID] = true })
			}
		case "replace":
			if changed(op.PrevNode, keepsChildren(op)) {
				found = true
			}
		case "delete":
			for _, r := range op.Removed {
				if changed(r.Node, false) {
					found = true
				}
			}
		}
	}
	return found
}

// asRead is a block as found, with the info the app looks up for a link
// card that lacked it as read put back as read: filling a card in
// (web.LinkInfo.Fill) is no change of the page's.
func asRead(at, read *doc.Node) *doc.Node {
	link := read.Str("url")
	if link == "" || !web.Lacking(read, link) || at.Type != read.Type || at.Str("url") != link {
		return at
	}
	out := at.Clone()
	for k := range (web.LinkInfo{}).Data() {
		if v, ok := read.Data[k]; ok {
			out.Data[k] = v
		} else {
			delete(out.Data, k)
		}
	}
	return out
}

// keepsChildren reports whether a replace left the block's children as
// they were, as doc's keptChildren: its node has the children its
// previous had, as replace_block without children writes it.
func keepsChildren(op doc.Op) bool {
	if op.PrevNode == nil || op.Node == nil || op.PrevNode.Type == "table" || len(op.Node.Children) != len(op.PrevNode.Children) {
		return false
	}
	for i, k := range op.Node.Children {
		if k.ID != op.PrevNode.Children[i].ID {
			return false
		}
	}
	return true
}

// encodeNode is a block as JSON, alike for equal blocks: Data holds only
// JSON values, and maps encode with their keys in order.
func encodeNode(n *doc.Node) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// walk calls fn on n and every block inside it.
func walk(n *doc.Node, fn func(*doc.Node)) {
	fn(n)
	for _, k := range n.Children {
		walk(k, fn)
	}
}

// errUnreadable is a page whose stored content cannot be read
// (store.ErrUnreadable), said in the app's language.
var errUnreadable error = errPageUnreadable{}

type errPageUnreadable struct{}

func (errPageUnreadable) Error() string { return locales.AgentPageUnreadable() }

// stored is a page's stored content, an empty page when it is not kept
// here. What cannot be read is an error rather than an empty page, which
// saving would put in its place.
func stored(st *store.Store, pageID string) (*doc.Content, error) {
	row, err := st.ContentRow(pageID)
	switch {
	case store.IsNotFound(err):
		return doc.Empty(), nil
	case err != nil:
		return nil, err
	}
	c, err := doc.Decode(row.Content)
	if err != nil {
		return nil, errUnreadable
	}
	return c, nil
}

// beforeStore runs between applying ops to a page's stored content and
// saving it; tests save there, standing in for MCP or a sync.
var beforeStore = func(pageID string) {}
