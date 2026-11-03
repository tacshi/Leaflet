package agents

import (
	"encoding/json"
	"errors"
	"strings"

	"leaflet/internal/doc"
	"leaflet/locales"
)

// draft is a call that writes blocks going into the page while its
// arguments stream, write_markdown's or insert_blocks': each block as it
// comes, the last one as far as it has come. The run keeps the call's
// ops as if it had run whole, revised as more comes: an insert of the
// blocks, after a replace of the empty block the first took the place of.
type draft struct {
	// verb says what the call did, in its edits.
	verb func(what string) string
	pos  doc.Position
	// into is the empty block the first block takes the place of, and
	// prev that block as it was.
	into string
	prev *doc.Node
	// keys are the blocks as last put in, without their IDs, and ids
	// theirs.
	keys []string
	ids  []string
	// replaceAt and insertAt are the call's ops among the run's, -1
	// before it has them.
	replaceAt, insertAt int
	// frozen is a draft a block of which could not go in, or of more
	// blocks than may go in (fits): the rest wait for the whole call,
	// which then fails.
	frozen bool
}

// drafts are the tools whose calls go in the page as they stream.
var drafts = map[string]bool{"write_markdown": true, "insert_blocks": true}

// Draft puts a call's blocks in the page as its arguments stream, raw
// being them so far. A block of insert_blocks that is not text waits
// until it has all come.
func (x *Executor) Draft(name, raw string) {
	if !drafts[name] || x.Ctx.ReadOnly || x.draft != nil && x.draft.frozen {
		return
	}
	// A drafted call runs no dispatch, which keeps the page's URLs as the
	// run began, before the draft puts its own in.
	x.begin()
	closed, ok := closeJSON(raw)
	if !ok {
		return
	}
	var args map[string]any
	if json.Unmarshal([]byte(closed), &args) != nil {
		return
	}
	body := "blocks"
	if name == "write_markdown" {
		body = "markdown"
	}
	if x.draft == nil {
		// The position has all come once what goes there begins.
		p, b := strings.Index(raw, `"position"`), strings.Index(raw, `"`+body+`"`)
		if p < 0 || b < p {
			return
		}
		d, err := x.newDraft(args["position"], name == "write_markdown")
		if err != nil {
			return
		}
		d.verb = locales.AgentEditInserted
		if name == "write_markdown" {
			d.verb = locales.AgentEditWrote
		}
		x.draft = d
	}
	if name == "write_markdown" {
		md, _ := args["markdown"].(string)
		nodes := doc.MarkdownBlocks(md)
		if !x.fits(nodes) {
			return
		}
		_ = x.syncDraft(nodes, len(nodes)-1)
		return
	}
	list, _ := args["blocks"].([]any)
	var nodes []*doc.Node
	for i, s := range list {
		m, _ := s.(map[string]any)
		// The last block is still coming: a text one shows as far as it
		// has come, another once it has all come.
		if t, _ := m["type"].(string); i == len(list)-1 && !doc.TextTypes[t] {
			break
		}
		n, err := doc.SpecToNode(m, "", x.Ctx.AgentName)
		if err != nil {
			if i < len(list)-1 {
				x.draft.frozen = true
			}
			break
		}
		nodes = append(nodes, n)
	}
	if !x.fits(nodes) {
		return
	}
	_ = x.syncDraft(nodes, len(list)-1)
}

// fits reports whether a draft's blocks are within the most a call writes
// and a run does. One past them freezes the draft: the page gets no more
// of it, and the whole call, refused once it has come, takes out what it
// put in.
func (x *Executor) fits(nodes []*doc.Node) bool {
	if doc.CallNodes(nodes) != nil || x.room(nodes) != nil {
		x.draft.frozen = true
		return false
	}
	return true
}

// newDraft is a draft of blocks going where raw says: insert_blocks'
// positions, or with replace, as write_markdown takes it, an empty block
// to write in place of too.
func (x *Executor) newDraft(raw any, replace bool) (*draft, error) {
	d := &draft{replaceAt: -1, insertAt: -1}
	// An empty block to write in place of says where, whatever else the
	// position has.
	if m := given(raw); replace && m["replace"] != nil {
		id, _ := m["replace"].(string)
		n := x.block(id)
		switch {
		case n == nil:
			return nil, noBlock(id)
		case !n.IsText() || n.Delta().Len() > 0 || len(n.Children) > 0:
			return nil, terr("position.replace takes an empty block only; use replace_block to rewrite one.")
		}
		d.into, d.prev = id, n.Clone()
		return d, nil
	}
	pos, err := x.position(raw)
	if err != nil {
		return nil, err
	}
	d.pos = pos
	return d, nil
}

// FinishDraft ends the draft with the call's whole arguments, as running
// the call would: its result, and false when there is no draft. A call
// that fails takes what it put in the page out again.
func (x *Executor) FinishDraft(name, raw string) (map[string]any, bool) {
	if x.draft == nil {
		return nil, false
	}
	fail := func(err error) (map[string]any, bool) {
		x.DropDraft()
		return map[string]any{"error": toolMessage(err)}, true
	}
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return fail(terr("Arguments were not valid JSON."))
	}
	var nodes []*doc.Node
	if name == "write_markdown" {
		md, _ := args["markdown"].(string)
		if nodes = doc.MarkdownBlocks(md); len(nodes) == 0 {
			return fail(terr("markdown is empty."))
		}
		if err := doc.CallNodes(nodes); err != nil {
			return fail(err)
		}
	} else {
		var err error
		if nodes, err = x.specs(args["blocks"]); err != nil {
			return fail(err)
		}
	}
	if err := x.room(nodes); err != nil {
		return fail(err)
	}
	x.draft.frozen = false
	if err := x.syncDraft(nodes, len(nodes)); err != nil {
		return fail(err)
	}
	ids := x.draft.ids
	x.draft = nil
	x.written += countNodes(nodes)
	return map[string]any{"inserted_ids": ids}, true
}

// DropDraft takes a draft's blocks out of the page again, as for a call
// that was cut off.
func (x *Executor) DropDraft() {
	d := x.draft
	x.draft = nil
	if d == nil {
		return
	}
	rest := d.ids
	if d.into != "" && len(rest) > 0 {
		rest = rest[1:]
	}
	if d.insertAt >= 0 {
		del := x.Ctx.Content.DeleteOp(rest)
		if x.Ctx.Content.Apply(del) == nil {
			x.dropOp(d.insertAt, []doc.Op{del})
		}
	}
	if d.replaceAt >= 0 {
		back := x.Ctx.Content.ReplaceOp(d.into, d.prev.Clone())
		if x.Ctx.Content.Apply(back) == nil {
			x.dropOp(d.replaceAt, []doc.Op{back})
		}
	}
}

// dropOp takes away a draft's op, the run's last, delta being what that
// changes in the page.
func (x *Executor) dropOp(at int, delta []doc.Op) {
	x.Ops, x.Edits = x.Ops[:at], x.Edits[:at]
	if x.OnRevise != nil {
		x.OnRevise(at, nil, "", delta)
	}
}

// keyOf is a block as a draft compares it: without its IDs, which each
// reading of the arguments makes anew.
func keyOf(n *doc.Node) string {
	c := n.Clone()
	var strip func(*doc.Node)
	strip = func(k *doc.Node) {
		k.ID = ""
		for _, ch := range k.Children {
			strip(ch)
		}
	}
	strip(c)
	raw, _ := json.Marshal(c)
	return string(raw)
}

// syncDraft puts nodes in the page, those before whole having all come:
// a block that changed is rewritten, a new one goes after the one before
// it, and one no longer there once all has come is taken out.
func (x *Executor) syncDraft(nodes []*doc.Node, whole int) error {
	d := x.draft
	// The changes in the page, of the replaced block and of the rest.
	var first, rest []doc.Op
	apply := func(i int, op doc.Op) error {
		if err := x.Ctx.Content.Apply(op); err != nil {
			return err
		}
		if i == 0 && d.into != "" {
			first = append(first, op)
		} else {
			rest = append(rest, op)
		}
		return nil
	}
	var err error
	for i, n := range nodes {
		key := keyOf(n)
		if i < len(d.keys) && d.keys[i] == key {
			continue
		}
		if e := x.checkURLs([]*doc.Node{n}); e != nil {
			if i < whole {
				d.frozen, err = true, e
			}
			break
		}
		var op doc.Op
		switch {
		case i < len(d.ids):
			op = x.Ctx.Content.ReplaceOp(d.ids[i], n)
		case i == 0 && d.into != "":
			op = x.Ctx.Content.ReplaceOp(d.into, n)
		case i == 0:
			op = doc.InsertOp(d.pos, n)
		default:
			op = doc.InsertOp(doc.Position{After: d.ids[i-1]}, n)
		}
		if e := apply(i, op); e != nil {
			d.frozen, err = true, e
			break
		}
		if i < len(d.ids) {
			d.keys[i] = key
		} else {
			d.keys, d.ids = append(d.keys, key), append(d.ids, n.ID)
		}
	}
	if err == nil && whole >= len(nodes) && len(d.ids) > len(nodes) {
		if apply(len(nodes), x.Ctx.Content.DeleteOp(d.ids[len(nodes):])) == nil {
			d.keys, d.ids = d.keys[:len(nodes)], d.ids[:len(nodes)]
		}
	}
	ids := d.ids
	if d.into != "" && len(ids) > 0 {
		if len(first) > 0 {
			n := x.Ctx.Content.Find(d.into).Clone()
			op := doc.Op{Op: "replace", ID: d.into, Node: n, PrevNode: d.prev.Clone()}
			x.putOp(&d.replaceAt, op, summaryFor(d.verb, []*doc.Node{n}), []string{d.into}, first)
		}
		ids = ids[1:]
	}
	switch {
	case len(rest) == 0:
	case len(ids) > 0:
		var blocks []*doc.Node
		for _, id := range ids {
			blocks = append(blocks, x.Ctx.Content.Find(id).Clone())
		}
		pos := d.pos
		if d.into != "" {
			pos = doc.Position{After: d.into}
		}
		x.putOp(&d.insertAt, doc.InsertOp(pos, blocks...), summaryFor(d.verb, blocks), append([]string(nil), ids...), rest)
	case d.insertAt >= 0:
		// The blocks after the first were all taken out.
		x.dropOp(d.insertAt, rest)
		d.insertAt = -1
	}
	return err
}

// putOp makes a draft's op the run's, at *at: a new one when it has none
// yet, which the page gets whole, else a revision of it, of which the
// page gets delta.
func (x *Executor) putOp(at *int, op doc.Op, summary string, ids []string, delta []doc.Op) {
	edit := Edit{Summary: summary, BlockIDs: ids}
	if *at < 0 {
		x.Ops, x.Edits = append(x.Ops, op), append(x.Edits, edit)
		*at = len(x.Ops) - 1
		if x.OnOp != nil {
			x.OnOp(op, summary)
		}
		return
	}
	x.Ops[*at], x.Edits[*at] = op, edit
	if x.OnRevise != nil {
		x.OnRevise(*at, &op, summary, delta)
	}
}

// toolMessage is an error as a tool's result says it.
func toolMessage(err error) string {
	var te *doc.ToolError
	if errors.As(err, &te) {
		return te.Message
	}
	return err.Error()
}
