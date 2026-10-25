// Package doc is the page document model shared with the backend: blocks
// with stable IDs in appflowy's node JSON, per-block edit times and
// tombstones, inline markdown for agents, block ops and their inverses, and
// the per-block merge used by sync. See docs/protocol.md §1–3.
package doc

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"time"
)

// Node is one block: appflowy_editor's node JSON plus a stable ID. Data
// holds only JSON values (maps, slices, strings, numbers, booleans), so two
// equal blocks always encode alike.
type Node struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Data     map[string]any `json:"data,omitempty"`
	Children []*Node        `json:"children,omitempty"`
}

// Block types that hold rich text in data.delta.
var TextTypes = map[string]bool{
	"paragraph":     true,
	"heading":       true,
	"bulleted_list": true,
	"numbered_list": true,
	"todo_list":     true,
	"quote":         true,
}

// Block types that may hold nested blocks in children.
var NestableTypes = map[string]bool{
	"bulleted_list": true,
	"numbered_list": true,
	"todo_list":     true,
}

// IsText reports whether the block holds rich text.
func (n *Node) IsText() bool { return TextTypes[n.Type] }

// Clone returns a deep copy of the block and its subtree.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	out := &Node{ID: n.ID, Type: n.Type, Data: cloneMap(n.Data)}
	if len(n.Children) > 0 {
		out.Children = make([]*Node, len(n.Children))
		for i, k := range n.Children {
			out.Children[i] = k.Clone()
		}
	}
	return out
}

// Delta returns the block's rich text.
func (n *Node) Delta() Delta {
	if n == nil || n.Data == nil {
		return nil
	}
	return DeltaFromJSON(n.Data["delta"])
}

// SetDelta stores rich text in the block.
func (n *Node) SetDelta(d Delta) {
	if n.Data == nil {
		n.Data = map[string]any{}
	}
	n.Data["delta"] = d.JSON()
}

// Str returns a string field of the block's data.
func (n *Node) Str(key string) string {
	if n == nil || n.Data == nil {
		return ""
	}
	s, _ := n.Data[key].(string)
	return s
}

// Int returns a number field of the block's data, or def.
func (n *Node) Int(key string, def int) int {
	if n == nil || n.Data == nil {
		return def
	}
	return AsInt(n.Data[key], def)
}

// Bool returns a boolean field of the block's data.
func (n *Node) Bool(key string) bool {
	if n == nil || n.Data == nil {
		return false
	}
	b, _ := n.Data[key].(bool)
	return b
}

// Set sets a field of the block's data.
func (n *Node) Set(key string, v any) {
	if n.Data == nil {
		n.Data = map[string]any{}
	}
	n.Data[key] = normalize(v)
}

// AsInt reads a JSON number as an int.
func AsInt(v any, def int) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i)
		}
	}
	return def
}

// AsFloat reads a JSON number as a float64.
func AsFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		var f float64
		if _, err := fmt.Sscan(x, &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// NewBlockID returns a short random block ID, unique within a page, which
// is all block IDs need to be.
func NewBlockID() string {
	b := make([]byte, 8)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// NewRowID returns a UUID v4 for rows that also live in Postgres.
func NewRowID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NowMs is the time in milliseconds since the epoch.
func NowMs() int64 { return time.Now().UnixMilli() }

// normalize turns a Go value into plain JSON values through an encoding
// round trip, so typed slices and structs stored in Data encode the same as
// decoded JSON does.
func normalize(v any) any {
	switch v.(type) {
	case nil, string, bool, float64, int, int64, map[string]any, []any:
		return v
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return v
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneValue(e)
		}
		return out
	default:
		return normalize(v)
	}
}

// canonical encodes v with map keys sorted, as signatures compare.
func canonical(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
