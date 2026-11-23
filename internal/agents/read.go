package agents

import (
	"encoding/json"
	"maps"
	"math"
	"slices"

	"leaflet/internal/doc"
)

const (
	readBudget = toolResultLimit - 2_000
	// The most of a part its subpages and its comments may each take, so the blocks keep the rest.
	readListShare = readBudget / 4
)

var partSchema = []prop{
	{"offset", map[string]any{"type": "integer", "minimum": 0, "description": "The block to start from, as next_offset gives it; the first when not given."}},
	{"limit", map[string]any{"type": "integer", "minimum": 1, "description": "The most blocks to read; as many as fit when not given."}},
}

const partsNote = "A long page comes in parts: total_blocks says how many blocks it has, and next_offset, while there are more, is the offset to read on from; more_subpages and more_comments count the subpages and comment threads a very long list leaves out."

func PartArgs(args map[string]any) (offset, limit int, err error) {
	count := func(key string, least int) (int, error) {
		v, ok := args[key]
		if !ok || v == nil {
			return 0, nil
		}
		f, ok := v.(float64)
		if !ok || f != math.Trunc(f) || f < float64(least) || f > math.MaxInt32 {
			return 0, terr("%s must be a whole number, %d or more.", key, least)
		}
		return int(f), nil
	}
	if offset, err = count("offset", 0); err != nil {
		return 0, 0, err
	}
	limit, err = count("limit", 1)
	return offset, limit, err
}

// ReadPart reads the blocks of c from offset on that fit one tool result, with the page's
// subpages and comments in the first part.
func (tc ToolContext) ReadPart(pageID string, page map[string]any, c *doc.Content, offset, limit int) map[string]any {
	blocks := doc.BlockViews(c)
	// The part without its blocks, next_offset as long as it can be, is sized
	// whole, keys and all, so the blocks that fit keep the part to readBudget.
	out := map[string]any{"page": page, "total_blocks": len(blocks), "next_offset": len(blocks), "blocks": []map[string]any{}}
	if offset == 0 {
		for key, list := range map[string]func() []map[string]any{"subpages": tc.Subpages, "comments": tc.Comments} {
			if list == nil {
				continue
			}
			all := list()
			kept := fitting(all, readListShare)
			out[key] = kept
			if more := len(all) - len(kept); more > 0 {
				out["more_"+key] = more
			}
		}
	}
	size := jsonSize(out)
	start := min(offset, len(blocks))
	end := start
	for end < len(blocks) && (limit == 0 || end-start < limit) {
		tc.Finish(pageID, blocks[end])
		cost := jsonSize(blocks[end]) + 1
		if end > start && size+cost > readBudget {
			break
		}
		size += cost
		end++
	}
	out["blocks"] = blocks[start:end]
	if end < len(blocks) {
		out["next_offset"] = end
	} else {
		delete(out, "next_offset")
	}
	return out
}

func fitting(list []map[string]any, budget int) []map[string]any {
	kept, cost := []map[string]any{}, 0
	for _, v := range list {
		n := jsonSize(v) + 1
		if cost+n > budget {
			break
		}
		kept, cost = append(kept, v), cost+n
	}
	return kept
}

func jsonSize(v any) int {
	raw, _ := json.Marshal(v)
	return len(raw)
}

// resultJSON writes a tool's result with its blocks last, so that a result cut for its
// length loses blocks and never what says how to read on.
func resultJSON(out map[string]any) string {
	o := make(ordered, 0, len(out))
	for _, k := range slices.Sorted(maps.Keys(out)) {
		if k != "blocks" {
			o = append(o, prop{k, out[k]})
		}
	}
	if blocks, ok := out["blocks"]; ok {
		o = append(o, prop{"blocks", blocks})
	}
	raw, _ := json.Marshal(o)
	return string(raw)
}
