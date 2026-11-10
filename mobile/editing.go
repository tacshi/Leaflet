package mobile

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"leaflet/internal/doc"
	"leaflet/internal/rules"
	"leaflet/locales"
)

type blockTypes struct {
	Text     []string `json:"text"`
	Nestable []string `json:"nestable"`
}

func BlockTypes() string {
	defer catch(nil)
	keys := func(m map[string]bool) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	raw, _ := json.Marshal(blockTypes{Text: keys(doc.TextTypes), Nestable: keys(doc.NestableTypes)})
	return string(raw)
}

func NewBlockID() string {
	defer catch(nil)
	return doc.NewBlockID()
}

type newBlock struct {
	Type string         `json:"type,omitempty"`
	Data map[string]any `json:"data,omitempty"`
}

func NewLine(typ string) string {
	defer catch(nil)
	t, data := rules.NewLine(typ)
	raw, _ := encode(newBlock{Type: t, Data: data})
	return raw
}

type enter struct {
	Action string `json:"action"`
	newBlock
	Unfold bool `json:"unfold,omitempty"`
}

func Enter(typ string, collapsed, empty, atStart, nested, last, holds bool) string {
	defer catch(nil)
	line := rules.Enter(rules.Block{Type: typ, Collapsed: collapsed, Empty: empty, Nested: nested, Last: last, Holds: holds}, atStart)
	raw, _ := encode(enter{Action: line.Action.String(), newBlock: newBlock{Type: line.Type, Data: line.Data}, Unfold: line.Unfold})
	return raw
}

func Backspace(typ string, empty, nested, last bool, prev string) string {
	defer catch(nil)
	b := rules.Block{Type: typ, Empty: empty, Nested: nested, Last: last}
	return rules.Backspace(b, rules.Block{Type: prev}, prev != "").String()
}

func Delete(typ, next string) string {
	defer catch(nil)
	return rules.Delete(rules.Block{Type: typ}, rules.Block{Type: next}).String()
}

func Nests(typ, under string) bool {
	defer catch(nil)
	return rules.Nests(typ, under)
}

type codeEnter struct {
	Leave  bool   `json:"leave,omitempty"`
	Keep   int    `json:"keep,omitempty"`
	Insert string `json:"insert,omitempty"`
}

func CodeEnter(code string, caret int) string {
	defer catch(nil)
	leave, keep, insert := rules.CodeEnter(code, runeOffset(code, caret))
	raw, _ := encode(codeEnter{Leave: leave, Keep: unitOffset(code, keep), Insert: insert})
	return raw
}

type indented struct {
	Code   string `json:"code"`
	Anchor int    `json:"anchor"`
	Focus  int    `json:"focus"`
}

func IndentCode(code string, anchor, focus int, out bool) string {
	defer catch(nil)
	made, a, f, ok := rules.IndentCode(code, runeOffset(code, anchor), runeOffset(code, focus), out)
	if !ok {
		return ""
	}
	raw, _ := encode(indented{Code: made, Anchor: unitOffset(made, a), Focus: unitOffset(made, f)})
	return raw
}

func Folded(blocks string) string {
	defer catch(nil)
	var folds []rules.Fold
	for line := range strings.Lines(blocks) {
		f := strings.Split(strings.TrimSuffix(line, "\n"), ",")
		if len(f) != 4 {
			continue
		}
		depth, _ := strconv.Atoi(f[1])
		level, _ := strconv.Atoi(f[2])
		folds = append(folds, rules.Fold{Type: f[0], Depth: depth, Level: level, Collapsed: f[3] == "1"})
	}
	var b strings.Builder
	for _, h := range rules.Hidden(folds) {
		if h {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

func MostlyCJK(text string) bool {
	defer catch(nil)
	return rules.MostlyCJK(text)
}

func NextTone(tone string) string {
	defer catch(nil)
	return doc.NextTone(tone)
}

func SourceKey(typ string) string {
	defer catch(nil)
	return doc.SourceKey(typ)
}

func LinkTarget(href string) string {
	defer catch(nil)
	href = strings.TrimSpace(href)
	if _, ok := doc.PageLinkID(href); ok {
		return href
	}
	return doc.SafeHref(href)
}

func TableText(node string) (_ string, err error) {
	defer catch(&err)
	var n doc.Node
	if err := json.Unmarshal([]byte(node), &n); err != nil {
		return "", err
	}
	return encode(doc.PlainTableRows(&n))
}

type due struct {
	Label   string `json:"label"`
	Overdue bool   `json:"overdue,omitempty"`
	Today   bool   `json:"today,omitempty"`
}

func Due(locale, date string, now int64) string {
	defer catch(nil)
	t, timed, ok := doc.ParseDue(date)
	if !ok {
		return ""
	}
	at := time.UnixMilli(now)
	raw, _ := encode(due{
		Label:   doc.DueLabel(locales.In(locale), t, timed, at),
		Overdue: doc.Overdue(t, timed, at),
		Today:   doc.DueToday(t, at),
	})
	return raw
}

func runeOffset(s string, off int) int {
	units, runes := 0, 0
	for _, r := range s {
		if units >= off {
			break
		}
		units += utf16.RuneLen(r)
		runes++
	}
	return runes
}

func unitOffset(s string, off int) int {
	units, runes := 0, 0
	for _, r := range s {
		if runes >= off {
			break
		}
		units += utf16.RuneLen(r)
		runes++
	}
	return units
}
