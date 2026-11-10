package agents

import (
	"cmp"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"leaflet/internal/doc"
	"leaflet/locales"
)

type draft struct {
	verb                func(what string) string
	pos                 doc.Position
	into                string
	prev                *doc.Node
	nodes               []*doc.Node
	ids                 []string
	replaceAt, insertAt int
	frozen              bool
}

var drafts = map[string]bool{"write_markdown": true, "insert_blocks": true}

func (x *Executor) Draft(name, raw string) {
	if !drafts[name] || x.Ctx.ReadOnly || x.Ctx.Workspace != nil || x.draft != nil && x.draft.frozen {
		return
	}
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

func (x *Executor) fits(nodes []*doc.Node) bool {
	if doc.CallNodes(nodes) != nil || x.room(nodes) != nil {
		x.draft.frozen = true
		return false
	}
	return true
}

func (x *Executor) newDraft(raw any, replace bool) (*draft, error) {
	d := &draft{replaceAt: -1, insertAt: -1}
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

func (x *Executor) FinishDraft(name, raw string) (map[string]any, bool) {
	if x.draft == nil {
		return nil, false
	}
	fail := func(err error) (map[string]any, bool) {
		x.DropDraft()
		return x.refuse(err), true
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
	x.Refused = ""
	return map[string]any{"inserted_ids": ids}, true
}

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

func (x *Executor) dropOp(at int, delta []doc.Op) {
	x.Ops, x.Edits = x.Ops[:at], x.Edits[:at]
	if x.OnRevise != nil {
		x.OnRevise(at, nil, "", delta)
	}
}

func sameBlock(a, b *doc.Node) bool {
	if a.Type != b.Type || len(a.Children) != len(b.Children) || !reflect.DeepEqual(a.Data, b.Data) {
		return false
	}
	for i, k := range a.Children {
		if !sameBlock(k, b.Children[i]) {
			return false
		}
	}
	return true
}

func (x *Executor) syncDraft(nodes []*doc.Node, whole int) error {
	d := x.draft
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
	var fresh []*doc.Node
	for i, n := range nodes {
		if i < len(d.nodes) && sameBlock(d.nodes[i], n) {
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
			n.ID = d.ids[i]
			op = doc.Op{Op: "replace", ID: n.ID, Node: n, PrevNode: d.nodes[i].Clone()}
		case i == 0 && d.into != "":
			n.ID = d.into
			op = doc.Op{Op: "replace", ID: n.ID, Node: n, PrevNode: d.prev.Clone()}
		default:
			fresh = append(fresh, n)
			continue
		}
		if e := apply(i, op); e != nil {
			d.frozen, err = true, e
			break
		}
		if i < len(d.ids) {
			d.nodes[i] = n
		} else {
			d.nodes, d.ids = append(d.nodes, n), append(d.ids, n.ID)
		}
	}
	if len(fresh) > 0 {
		pos := d.pos
		if len(d.ids) > 0 {
			pos = doc.Position{After: d.ids[len(d.ids)-1]}
		}
		if e := apply(len(d.ids), doc.InsertOp(pos, fresh...)); e != nil {
			d.frozen, err = true, cmp.Or(err, e)
		} else {
			for _, n := range fresh {
				d.nodes, d.ids = append(d.nodes, n), append(d.ids, n.ID)
			}
		}
	}
	if err == nil && whole >= len(nodes) && len(d.ids) > len(nodes) {
		if apply(len(nodes), x.Ctx.Content.DeleteOp(d.ids[len(nodes):])) == nil {
			d.nodes, d.ids = d.nodes[:len(nodes)], d.ids[:len(nodes)]
		}
	}
	ids, kept := d.ids, d.nodes
	if d.into != "" && len(ids) > 0 {
		if len(first) > 0 {
			n := kept[0].Clone()
			op := doc.Op{Op: "replace", ID: d.into, Node: n, PrevNode: d.prev.Clone()}
			x.putOp(&d.replaceAt, op, summaryFor(d.verb, []*doc.Node{n}), []string{d.into}, first)
		}
		ids, kept = ids[1:], kept[1:]
	}
	switch {
	case len(rest) == 0:
	case len(ids) > 0:
		blocks := make([]*doc.Node, len(kept))
		for i, n := range kept {
			blocks[i] = n.Clone()
		}
		pos := d.pos
		if d.into != "" {
			pos = doc.Position{After: d.into}
		}
		x.putOp(&d.insertAt, doc.InsertOp(pos, blocks...), summaryFor(d.verb, blocks), append([]string(nil), ids...), rest)
	case d.insertAt >= 0:
		x.dropOp(d.insertAt, rest)
		d.insertAt = -1
	}
	return err
}

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

func toolMessage(err error) string {
	var te *doc.ToolError
	if errors.As(err, &te) {
		return te.Message
	}
	return err.Error()
}
