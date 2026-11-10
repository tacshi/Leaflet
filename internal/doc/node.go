package doc

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"time"
)

type Node struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Data     map[string]any `json:"data,omitempty"`
	Children []*Node        `json:"children,omitempty"`
}

var TextTypes = map[string]bool{
	"paragraph":     true,
	"heading":       true,
	"bulleted_list": true,
	"numbered_list": true,
	"todo_list":     true,
	"quote":         true,
	"code":          true,
	"callout":       true,
	"toggle_list":   true,
}

var NestableTypes = map[string]bool{
	"bulleted_list": true,
	"numbered_list": true,
	"todo_list":     true,
	"toggle_list":   true,
}

var TypedKeys = []string{"checked", "due", "level", "language", "tone", "collapsed"}

func TypeData(typ string, level int) map[string]any {
	switch typ {
	case "heading":
		return map[string]any{"level": level}
	case "todo_list":
		return map[string]any{"checked": false}
	case "code":
		return map[string]any{"language": ""}
	case "callout":
		return map[string]any{"tone": "info"}
	case "toggle_list":
		return map[string]any{"collapsed": false}
	}
	return map[string]any{}
}

func (n *Node) IsText() bool { return TextTypes[n.Type] }

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

func (n *Node) Delta() Delta {
	if n == nil || n.Data == nil {
		return nil
	}
	return DeltaFromJSON(n.Data["delta"])
}

func (n *Node) SetDelta(d Delta) {
	if n.Data == nil {
		n.Data = map[string]any{}
	}
	n.Data["delta"] = d.JSON()
}

func (n *Node) Str(key string) string {
	if n == nil || n.Data == nil {
		return ""
	}
	s, _ := n.Data[key].(string)
	return s
}

func (n *Node) Int(key string, def int) int {
	if n == nil || n.Data == nil {
		return def
	}
	return AsInt(n.Data[key], def)
}

func (n *Node) Bool(key string) bool {
	if n == nil || n.Data == nil {
		return false
	}
	b, _ := n.Data[key].(bool)
	return b
}

func (n *Node) Set(key string, v any) {
	if n.Data == nil {
		n.Data = map[string]any{}
	}
	n.Data[key] = normalize(v)
}

func AsInt(v any, def int) int {
	var i int64
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.Abs(x) > maxExactInt {
			return def
		}
		i = int64(x)
	case int:
		i = int64(x)
	case int64:
		i = x
	case json.Number:
		var err error
		if i, err = x.Int64(); err != nil {
			return def
		}
	default:
		return def
	}
	if i > maxExactInt || i < -maxExactInt {
		return def
	}
	return int(i)
}

const maxExactInt = 1 << 53

func AsFloat(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int:
		f = float64(x)
	case int64:
		f = float64(x)
	case string:
		if _, err := fmt.Sscan(x, &f); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

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

func NewRowID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func NowMs() int64 { return time.Now().UnixMilli() }

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

func canonical(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
