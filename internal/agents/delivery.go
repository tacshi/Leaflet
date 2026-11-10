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

const streamEvery = 50 * time.Millisecond

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

func (st *replyStream) clear() {
	st.mu.Lock()
	st.text = ""
	st.mu.Unlock()
}

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

type delivery struct {
	sent      int
	ed        LiveDoc
	read      uint64
	conflict  func(*doc.Content) error
	chatSaved time.Time
	failed    error
	run       *Run
}

func (h *Hub) newDelivery(pageID string, run *Run, option Option) *delivery {
	return &delivery{run: run, conflict: func(c *doc.Content) error {
		return h.st.AddHistory(pageID, c, "conflict", run.ID, "")
	}}
}

func (d *delivery) to(ed LiveDoc, ops []doc.Op) error {
	for i, op := range ops[d.sent:] {
		if op.IsPageOp() {
			continue
		}
		if err := d.apply(ed, op, d.sent+i); err != nil {
			d.sent += i
			return err
		}
	}
	d.sent = len(ops)
	return nil
}

func (d *delivery) apply(ed LiveDoc, op doc.Op, at int) error {
	if d.conflict != nil {
		if c := ed.Peek(); overwrites(c, []doc.Op{op}) {
			if err := d.conflict(c); err != nil {
				return err
			}
		}
	}
	if err := ed.ApplyOpFrom(op, ed != d.ed, d.read); err != nil {
		log.Printf("agent op on the open page: %v", err)
		d.skip(at)
	}
	d.ed = ed
	return nil
}

func (d *delivery) skip(at int) {
	if d.run != nil {
		d.run.skip(at)
	}
}

func (d *delivery) missed(ops []doc.Op) ([]doc.Op, []int) {
	var out []doc.Op
	var at []int
	for i, op := range ops[d.sent:] {
		if !op.IsPageOp() {
			out, at = append(out, op), append(at, d.sent+i)
		}
	}
	return out, at
}

func (d *delivery) save(st *store.Store, pageID string, ops []doc.Op, jobID string) error {
	if missed, at := d.missed(ops); len(missed) > 0 {
		skipped, err := storeOps(st, pageID, missed, true, false, jobID, "agent op on a closed page")
		if err != nil {
			return err
		}
		for _, i := range skipped {
			d.skip(at[i])
		}
	}
	d.sent = len(ops)
	return nil
}

func applyAll(c *doc.Content, ops []doc.Op) error {
	for _, op := range ops {
		if err := c.Apply(op); err != nil {
			return err
		}
	}
	return nil
}

var errKeptChanging error = errPageKeptChanging{}

type errPageKeptChanging struct{}

func (errPageKeptChanging) Error() string { return locales.AgentPageKeptChanging() }

func storeOps(st *store.Store, pageID string, ops []doc.Op, orEmpty, strict bool, jobID, what string) (skipped []int, err error) {
	inHistory := ""
	err = st.UpdateContent(pageID, func(c *doc.Content, kept bool) (bool, error) {
		skipped = nil
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
			for i, op := range ops {
				if err := c.Apply(op); err != nil {
					log.Printf("%s: %v", what, err)
					skipped = append(skipped, i)
				}
			}
		}
		beforeStore(pageID)
		return true, nil
	})
	switch {
	case err == nil:
		return skipped, nil
	case errors.Is(err, store.ErrContentChanged):
		return nil, errKeptChanging
	case errors.Is(err, store.ErrUnreadable):
		return nil, errUnreadable
	}
	return nil, err
}

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

func encodeNode(n *doc.Node) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func walk(n *doc.Node, fn func(*doc.Node)) {
	fn(n)
	for _, k := range n.Children {
		walk(k, fn)
	}
}

var errUnreadable error = errPageUnreadable{}

type errPageUnreadable struct{}

func (errPageUnreadable) Error() string { return locales.AgentPageUnreadable() }

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

var beforeStore = func(pageID string) {}
