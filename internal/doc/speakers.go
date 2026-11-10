package doc

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"leaflet/locales"
)

const (
	SpeakersHidden = "speakers_hidden"
	SpeakerNames   = "speaker_names"
	TranscribedBy  = "transcribed_by"
)

const maxSpeakerName = 64

var speakerLabel = sync.OnceValue(func() *regexp.Regexp {
	const name, text = "", ""
	var forms []string
	for _, lang := range locales.Catalog.Languages() {
		before, rest, ok := strings.Cut(locales.In(lang).RecSpeakerLine(name, text), name)
		if !ok {
			continue
		}
		after, _, _ := strings.Cut(rest, text)
		forms = append(forms, `(`+regexp.QuoteMeta(before)+`[^\s:：]{1,32})`+regexp.QuoteMeta(strings.TrimSpace(after))+`\s*`)
	}
	return regexp.MustCompile(`^(?:` + strings.Join(forms, "|") + `)`)
})

func speakerAt(text string, names []string) (string, int) {
	for _, name := range names {
		after, ok := strings.CutPrefix(text, name)
		if r, size := utf8.DecodeRuneInString(after); ok && (r == ':' || r == '：') {
			return name, len(text) - len(strings.TrimLeftFunc(after[size:], unicode.IsSpace))
		}
	}
	m := speakerLabel().FindStringSubmatchIndex(text)
	for i := 2; i+1 < len(m); i += 2 {
		if m[i] >= 0 {
			return text[m[i]:m[i+1]], m[1]
		}
	}
	return "", 0
}

type spokenLine struct {
	head, speaker, sep, words string
}

func splitSpoken(line string, names []string) spokenLine {
	_, rest := leadingTime(line)
	s := spokenLine{head: line[:len(line)-len(rest)], words: rest}
	if speaker, end := speakerAt(rest, names); speaker != "" {
		s.speaker, s.sep, s.words = speaker, rest[len(speaker):end], rest[end:]
	}
	return s
}

func (s spokenLine) String() string { return s.head + s.speaker + s.sep + s.words }

func SpeakerNamesOf(n *Node) []string {
	if n == nil {
		return nil
	}
	list, _ := n.Data[SpeakerNames].([]any)
	var names []string
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" {
			names = append(names, s)
		}
	}
	return names
}

func SetSpeakerNames(n *Node, names []string) {
	if len(names) == 0 {
		delete(n.Data, SpeakerNames)
		return
	}
	list := make([]any, len(names))
	for i, name := range names {
		list[i] = name
	}
	n.Set(SpeakerNames, list)
}

func NoteSpeakers(notes string, names []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for line := range strings.SplitSeq(notes, "\n") {
		s := splitSpoken(strings.TrimSpace(line), names)
		if s.speaker != "" && !seen[s.speaker] {
			seen[s.speaker] = true
			out = append(out, s.speaker)
		}
	}
	return out
}

func HasSpeakers(notes string, names []string) bool {
	for line := range strings.SplitSeq(notes, "\n") {
		if splitSpoken(strings.TrimSpace(line), names).speaker != "" {
			return true
		}
	}
	return false
}

func WithoutSpeakers(notes string, names []string) string {
	lines := strings.Split(notes, "\n")
	for i, line := range lines {
		s := splitSpoken(strings.TrimSpace(line), names)
		if s.speaker != "" {
			lines[i] = s.head + s.words
		}
	}
	return strings.Join(lines, "\n")
}

func ShownNotes(n *Node) string {
	if n.Bool(SpeakersHidden) {
		return WithoutSpeakers(n.Str("notes"), SpeakerNamesOf(n))
	}
	return n.Str("notes")
}

func SpokenLines(notes string, names []string, hidden bool) []NoteLine {
	var out []NoteLine
	for i, line := range strings.Split(NormalizeNotes(notes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		at, rest := leadingTime(line)
		speaker, end := speakerAt(rest, names)
		if hidden {
			rest = rest[end:]
		}
		out = append(out, NoteLine{At: at, Parts: noteParts(rest), Line: i, Speaker: speaker})
	}
	return out
}

func NotesText(notes string, names []string, hidden bool) string {
	var lines []string
	for _, l := range SpokenLines(notes, names, hidden) {
		lines = append(lines, NoteText(l))
	}
	return strings.Join(lines, "\n")
}

func NotesBlocks(notes string, names []string, hidden bool) []*Node {
	nodes := []*Node{}
	for _, l := range SpokenLines(notes, names, hidden) {
		n := &Node{ID: NewBlockID(), Type: "paragraph"}
		d := Delta{}
		if l.At >= 0 {
			d = append(d, Run{Text: Clock(l.At) + "  ", Attrs: Attrs{Bold: true}})
		}
		n.SetDelta(append(d, Run{Text: NoteText(l)}))
		nodes = append(nodes, n)
	}
	return nodes
}

func CleanSpeakerName(name string) string {
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || r == ':' || r == '：'
	}), " ")
	if r := []rune(name); len(r) > maxSpeakerName {
		name = strings.TrimSpace(string(r[:maxSpeakerName]))
	}
	return name
}

func RenameSpeaker(notes string, names []string, from, to string) (string, []string) {
	to = CleanSpeakerName(to)
	if to == "" || to == from {
		return notes, names
	}
	lines := strings.Split(NormalizeNotes(notes), "\n")
	for i, line := range lines {
		if s := splitSpoken(strings.TrimSpace(line), names); s.speaker == from {
			s.speaker = to
			lines[i] = s.String()
		}
	}
	notes = strings.Join(lines, "\n")
	return notes, usedNames(notes, names, to)
}

func SetLineSpeaker(notes string, names []string, line int, to string) (string, []string) {
	to = CleanSpeakerName(to)
	lines := strings.Split(NormalizeNotes(notes), "\n")
	if to == "" || line < 0 || line >= len(lines) {
		return notes, names
	}
	s := splitSpoken(strings.TrimSpace(lines[line]), names)
	if s.speaker == "" {
		s.sep = speakerSep(lines, names)
	}
	s.speaker = to
	lines[line] = s.String()
	notes = strings.Join(lines, "\n")
	return notes, usedNames(notes, names, to)
}

func speakerSep(lines, names []string) string {
	for _, line := range lines {
		if s := splitSpoken(strings.TrimSpace(line), names); s.speaker != "" {
			return s.sep
		}
	}
	return ": "
}

func usedNames(notes string, names []string, to string) []string {
	all := names
	if !strings.Contains("\n"+strings.Join(names, "\n")+"\n", "\n"+to+"\n") {
		all = append(append([]string{}, names...), to)
	}
	used := map[string]bool{}
	for _, s := range NoteSpeakers(notes, all) {
		used[s] = true
	}
	kept := []string{}
	for _, name := range all {
		if used[name] {
			kept = append(kept, name)
		}
	}
	return kept
}

func SpeakerNamesJSON(names []string) string {
	if names == nil {
		names = []string{}
	}
	raw, _ := json.Marshal(names)
	return string(raw)
}

func SpeakerNamesFrom(raw string) []string {
	var names []string
	_ = json.Unmarshal([]byte(raw), &names)
	return names
}
