package golden

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "write what the app makes now over the golden files' values that differ")

const (
	most     = 10
	mostShow = 2000
)

type File struct {
	t      testing.TB
	path   string
	raw    []byte
	failed int
	puts   []put
}

type put struct {
	start, end int
	value      []byte
	key        string
}

func Open(t testing.TB, name string) *File {
	t.Helper()
	return OpenIn(t, ".", name)
}

func OpenIn(t testing.TB, dir, name string) *File {
	t.Helper()
	path := filepath.Join(dir, "testdata", "golden", name)
	raw, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("%s is not JSON", path)
	}
	var cases []json.RawMessage
	if json.Unmarshal(raw, &cases) == nil && len(cases) == 0 {
		t.Fatalf("%s has no cases", path)
	}
	f := &File{t: t, path: path, raw: raw}
	t.Cleanup(f.close)
	return f
}

func (f *File) Decode(v any) {
	f.t.Helper()
	if err := json.Unmarshal(f.raw, v); err != nil {
		f.t.Fatalf("%s: %v", f.path, err)
	}
}

func (f *File) Raw(path ...any) json.RawMessage {
	f.t.Helper()
	start, end, ok := span(f.raw, path)
	if !ok {
		f.t.Fatalf("%s has no %s", f.path, where(path))
	}
	return f.raw[start:end]
}

func (f *File) Check(got any, path ...any) {
	f.t.Helper()
	start, end, ok := span(f.raw, path)
	if ok {
		var want any
		if err := json.Unmarshal(f.raw[start:end], &want); err != nil {
			f.t.Fatalf("%s %s: %v", f.path, where(path), err)
		}
		if reflect.DeepEqual(JSON(f.t, got), want) {
			return
		}
	}
	f.Mismatch(got, path...)
}

func (f *File) Mismatch(got any, path ...any) {
	f.t.Helper()
	start, end, ok := span(f.raw, path)
	if *update {
		value, err := encode(got)
		if err != nil {
			f.t.Errorf("%s %s: %v", f.path, where(path), err)
			return
		}
		if ok {
			f.puts = append(f.puts, put{start: start, end: end, value: value})
			return
		}
		key, named := "", false
		if len(path) > 0 {
			key, named = path[len(path)-1].(string)
		}
		start, end, ok = span(f.raw, path[:max(len(path)-1, 0)])
		if !named || !ok || f.raw[start] != '{' {
			f.t.Errorf("%s has no %s to update: write the case by hand", f.path, where(path))
			return
		}
		at := len(bytes.TrimRight(f.raw[:end-1], " \t\r\n"))
		f.puts = append(f.puts, put{start: at, end: at, value: value, key: key})
		return
	}
	if f.failed++; f.failed > most {
		return
	}
	want := "nothing"
	if ok {
		want = string(f.raw[start:end])
	}
	msg := fmt.Sprintf("%s %s", f.path, where(path))
	if in, ok := f.input(path); ok {
		msg += "\ninput: " + short(in)
	}
	f.t.Errorf("%s\ngot:   %s\nwant:  %s", msg, short(Show(got)), short(compact(want)))
}

func (f *File) input(path []any) (string, bool) {
	last := len(path) - 1
	for last >= 0 {
		if _, ok := path[last].(string); ok {
			break
		}
		last--
	}
	if last < 0 {
		return "", false
	}
	start, end, ok := span(f.raw, path[:last])
	if !ok {
		return "", false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(f.raw[start:end], &obj) != nil {
		return "", false
	}
	delete(obj, path[last].(string))
	if len(obj) == 0 {
		return "", false
	}
	return Show(obj), true
}

func (f *File) close() {
	if !*update {
		if f.failed > most {
			f.t.Errorf("%s: %d values differ in all", f.path, f.failed)
		}
		return
	}
	if len(f.puts) == 0 || f.t.Failed() {
		return
	}
	slices.SortFunc(f.puts, func(a, b put) int { return b.start - a.start })
	out := slices.Clone(f.raw)
	for i, p := range f.puts {
		if i > 0 && p.end > f.puts[i-1].start {
			f.t.Errorf("%s: two updates of one value", f.path)
			return
		}
		// Keys added at one place are written last to first, so in an
		// object that was empty the one written first ends it and each
		// other is followed by a comma.
		after := i > 0 && f.puts[i-1].start == p.start
		value, err := f.write(p, after)
		if err != nil {
			f.t.Error(err)
			return
		}
		out = slices.Concat(out[:p.start], value, out[p.end:])
	}
	if err := os.WriteFile(f.path, out, 0o644); err != nil { // #nosec G306
		f.t.Error(err)
		return
	}
	f.t.Logf("%s: updated %d values", f.path, len(f.puts))
}

func (f *File) write(p put, after bool) ([]byte, error) {
	unit := f.indent()
	lead := func(at int) string {
		line := f.raw[bytes.LastIndexByte(f.raw[:at], '\n')+1 : at]
		return string(line[:len(line)-len(bytes.TrimLeft(line, " "))])
	}
	indented := func(prefix string) ([]byte, error) {
		var buf bytes.Buffer
		err := json.Indent(&buf, p.value, prefix, unit)
		return buf.Bytes(), err
	}
	if p.key == "" {
		if unit == "" {
			return p.value, nil
		}
		return indented(lead(p.start))
	}
	key, err := encode(p.key)
	if err != nil {
		return nil, err
	}
	first := f.raw[p.start-1] == '{'
	var out []byte
	if !first {
		out = append(out, ',')
	}
	if unit == "" {
		out = slices.Concat(out, key, []byte(":"), p.value)
		if first && after {
			out = append(out, ',')
		}
		return out, nil
	}
	brace := p.start + bytes.IndexByte(f.raw[p.start:], '}')
	outer := lead(brace)
	if first {
		outer = lead(p.start)
	}
	value, err := indented(outer + unit)
	if err != nil {
		return nil, err
	}
	out = slices.Concat(out, []byte("\n"+outer+unit), key, []byte(": "), value)
	switch {
	case first && after:
		out = append(out, ',')
	case first:
		out = append(out, "\n"+outer...)
	}
	return out, nil
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			out = append(out, raw[i])
			continue
		}
		switch rest := raw[i:]; {
		case bytes.HasPrefix(rest, []byte(`\u2028`)):
			out, i = append(out, "\u2028"...), i+5
		case bytes.HasPrefix(rest, []byte(`\u2029`)):
			out, i = append(out, "\u2029"...), i+5
		default:
			out, i = append(out, raw[i], raw[i+1]), i+1
		}
	}
	return out, nil
}

func (f *File) indent() string {
	_, rest, ok := bytes.Cut(f.raw, []byte("\n"))
	if !ok {
		return ""
	}
	return string(rest[:len(rest)-len(bytes.TrimLeft(rest, " "))])
}

func span(raw []byte, path []any) (start, end int, ok bool) {
	i := space(raw, 0)
	for _, p := range path {
		switch p := p.(type) {
		case int:
			if raw[i] != '[' {
				return 0, 0, false
			}
			i = space(raw, i+1)
			for n := 0; ; n++ {
				if raw[i] == ']' {
					return 0, 0, false
				}
				if n == p {
					break
				}
				i = space(raw, skip(raw, i))
				if raw[i] == ',' {
					i = space(raw, i+1)
				}
			}
		case string:
			if raw[i] != '{' {
				return 0, 0, false
			}
			i = space(raw, i+1)
			for {
				if raw[i] == '}' {
					return 0, 0, false
				}
				end := skip(raw, i)
				var key string
				if json.Unmarshal(raw[i:end], &key) != nil {
					return 0, 0, false
				}
				i = space(raw, space(raw, end)+1)
				if key == p {
					break
				}
				i = space(raw, skip(raw, i))
				if raw[i] == ',' {
					i = space(raw, i+1)
				}
			}
		default:
			return 0, 0, false
		}
	}
	return i, skip(raw, i), true
}

func space(raw []byte, i int) int {
	for i < len(raw) && strings.IndexByte(" \t\r\n", raw[i]) >= 0 {
		i++
	}
	return i
}

func skip(raw []byte, i int) int {
	switch raw[i] {
	case '"':
		for i++; raw[i] != '"'; i++ {
			if raw[i] == '\\' {
				i++
			}
		}
		return i + 1
	case '{', '[':
		depth := 0
		for {
			switch raw[i] {
			case '"':
				i = skip(raw, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
			i++
		}
	}
	for i < len(raw) && strings.IndexByte(",]} \t\r\n", raw[i]) < 0 {
		i++
	}
	return i
}

func where(path []any) string {
	var b strings.Builder
	for _, p := range path {
		switch p := p.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(p) + "]")
		case string:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(p)
		}
	}
	if b.Len() == 0 {
		return "(the whole file)"
	}
	return b.String()
}

func short(s string) string {
	if len(s) <= mostShow {
		return s
	}
	return strings.ToValidUTF8(s[:mostShow], "") + fmt.Sprintf("… (%d bytes in all)", len(s))
}

func compact(s string) string {
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(s)) != nil {
		return s
	}
	return buf.String()
}

func JSON(t testing.TB, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func Show(v any) string {
	raw, err := encode(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

func Plain(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if (k == "data" || k == "children") && empty(e) {
				continue
			}
			out[k] = Plain(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Plain(e)
		}
		return out
	}
	return v
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

func Steady(known map[string]bool, vs ...any) []any {
	names := map[string]string{}
	var learn func(any)
	learn = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				learn(x[k])
			}
			if id, ok := x["id"].(string); ok && x["type"] != nil && !known[id] && names[id] == "" {
				names[id] = fmt.Sprintf("new-%d", len(names)+1)
			}
		case []any:
			for _, e := range x {
				learn(e)
			}
		}
	}
	var rename func(any) any
	rename = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, e := range x {
				out[k] = rename(e)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = rename(e)
			}
			return out
		case string:
			if name, ok := names[x]; ok {
				return name
			}
		}
		return v
	}
	for _, v := range vs {
		learn(v)
	}
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = rename(v)
	}
	return out
}

func WithoutIDs(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k != "id" {
				out[k] = WithoutIDs(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = WithoutIDs(e)
		}
		return out
	}
	return v
}
