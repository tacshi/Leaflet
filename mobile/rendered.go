package mobile

import (
	"leaflet/internal/doc"
	"leaflet/internal/rendered"
	"leaflet/internal/web"
)

func MathPage(formula string) string {
	defer catch(nil)
	return rendered.MathPage(formula)
}

func DiagramPage(source, bg string) string {
	defer catch(nil)
	return rendered.DiagramPage(source, bg, false)
}

type videoPlayer struct {
	Page      string `json:"page"`
	Origin    string `json:"origin"`
	Thumbnail string `json:"thumbnail"`
	WatchURL  string `json:"watch_url"`
}

func VideoPlayer(link string) string {
	defer catch(nil)
	return VideoPlayerAt(link, -1)
}

func VideoPlayerAt(link string, at int) string {
	defer catch(nil)
	v, ok := doc.ParseVideoURL(link)
	if !ok {
		return ""
	}
	if at >= 0 {
		v = v.At(at)
	}
	thumbnail := v.Thumbnail()
	v, part := web.VideoPart(v)
	if thumbnail == "" {
		thumbnail = part.Picture
	}
	out, _ := encode(videoPlayer{
		Page: rendered.VideoPage(v), Origin: rendered.VideoOrigin,
		Thumbnail: thumbnail, WatchURL: v.WatchURL(),
	})
	return out
}

func VideoSeek(link string, at int) string {
	defer catch(nil)
	v, ok := doc.ParseVideoURL(link)
	if !ok {
		return ""
	}
	return rendered.VideoSeek(v, at)
}

type noteLine struct {
	At      int        `json:"at"`
	Time    string     `json:"time,omitempty"`
	Parts   []notePart `json:"parts"`
	Line    int        `json:"line"`
	Speaker string     `json:"speaker,omitempty"`
}

type notePart struct {
	Text string `json:"text"`
	At   int    `json:"at"`
}

func FileKind(name, mime string) string {
	defer catch(nil)
	return doc.FilePreviewKind(name, mime)
}

func FileSpeakers(notes, names string) string {
	defer catch(nil)
	raw, _ := encode(doc.NoteSpeakers(notes, doc.SpeakerNamesFrom(names)))
	return raw
}

type speakersEdit struct {
	Notes string   `json:"notes"`
	Names []string `json:"names"`
}

func encodeSpeakers(notes string, names []string) string {
	if names == nil {
		names = []string{}
	}
	raw, _ := encode(speakersEdit{Notes: notes, Names: names})
	return raw
}

func RenameSpeaker(notes, names, from, to string) string {
	defer catch(nil)
	return encodeSpeakers(doc.RenameSpeaker(notes, doc.SpeakerNamesFrom(names), from, to))
}

func SetLineSpeaker(notes, names string, line int, to string) string {
	defer catch(nil)
	return encodeSpeakers(doc.SetLineSpeaker(notes, doc.SpeakerNamesFrom(names), line, to))
}

func FileNotes(notes, names string, hidden bool) string {
	defer catch(nil)
	return encodeLines(doc.SpokenLines(notes, doc.SpeakerNamesFrom(names), hidden))
}

func FileTranscriptText(notes, names string, hidden bool) string {
	defer catch(nil)
	return doc.NotesText(notes, doc.SpeakerNamesFrom(names), hidden)
}

func FileTranscriptBlocks(notes, names string, hidden bool) string {
	defer catch(nil)
	raw, _ := encode(transcript{Blocks: doc.NotesBlocks(notes, doc.SpeakerNamesFrom(names), hidden)})
	return raw
}

func VideoNotes(notes string) string {
	defer catch(nil)
	return encodeLines(doc.NoteLines(notes))
}

func encodeLines(read []doc.NoteLine) string {
	lines := []noteLine{}
	for _, l := range read {
		nl := noteLine{At: l.At, Line: l.Line, Speaker: l.Speaker, Parts: []notePart{}}
		if l.At >= 0 {
			nl.Time = doc.Clock(l.At)
		}
		for _, p := range l.Parts {
			nl.Parts = append(nl.Parts, notePart{Text: p.Text, At: p.At})
		}
		lines = append(lines, nl)
	}
	out, _ := encode(lines)
	return out
}

const MaxNotes = doc.MaxNotes

const FoldedLines = doc.FoldedLines

const MaxPixels = doc.MaxPixels

func FoldsTranscript(n int) bool {
	defer catch(nil)
	return doc.FoldsTranscript(n)
}

func VideoHasCaptions(link string) bool {
	defer catch(nil)
	v, ok := doc.ParseVideoURL(link)
	return ok && web.HasCaptions(v)
}

func NormalizeNotes(notes string) string {
	defer catch(nil)
	return doc.NormalizeNotes(notes)
}

func Clock(sec int) string {
	defer catch(nil)
	return doc.Clock(sec)
}

func MomentLink(pageID, blockID string, at int) string {
	defer catch(nil)
	return doc.MomentLink(pageID, blockID, at)
}

func BlockLink(pageID, blockID string) string {
	defer catch(nil)
	return doc.BlockLink(pageID, blockID)
}

func LinkedBlock(href string) string {
	defer catch(nil)
	return doc.LinkBlock(href)
}

func LinkedMoment(href string) int {
	defer catch(nil)
	at, ok := doc.LinkMoment(href)
	if !ok {
		return -1
	}
	return at
}

func EmbedPage(src, bg string, dark bool) string {
	defer catch(nil)
	return rendered.EmbedPage("", src, bg, dark, true)
}

func NoRTCScript() string {
	defer catch(nil)
	return rendered.NoRTC
}
