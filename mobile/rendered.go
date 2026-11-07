package mobile

import (
	"leaflet/internal/doc"
	"leaflet/internal/rendered"
	"leaflet/internal/web"
)

// MathPage returns the page that draws a math block's TeX, as the
// desktop's: KaTeX's MathML, which a web view shows.
func MathPage(formula string) string {
	defer catch(nil)
	return rendered.MathPage(formula)
}

// DiagramPage returns the page that draws a diagram block's Mermaid source
// on a background of bg, a hex color, as the desktop's: Pele's SVG, which
// zooms with a pinch.
func DiagramPage(source, bg string) string {
	defer catch(nil)
	return rendered.DiagramPage(source, bg, false)
}

// videoPlayer is a video block's player, as VideoPlayer returns it.
type videoPlayer struct {
	// Page plays the video, loaded at Origin, which YouTube's player
	// wants as the page that frames it.
	Page   string `json:"page"`
	Origin string `json:"origin"`
	// Thumbnail is the video's picture, where its site has one at a
	// known address, or "".
	Thumbnail string `json:"thumbnail"`
	WatchURL  string `json:"watch_url"`
}

// VideoPlayer returns the player of a video block's link, as JSON, or ""
// for a link that is no video Leaflet plays, as VideoPlayerAt does, off
// the main thread.
func VideoPlayer(link string) string {
	defer catch(nil)
	return VideoPlayerAt(link, -1)
}

// VideoPlayerAt is VideoPlayer playing from at seconds in, as a time in
// the block's notes or a link to a moment of it asks; at -1 plays from
// where the link says. A Bilibili video's part, which its player needs,
// and its picture are asked of Bilibili the first time, so the app calls
// it off the main thread.
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

// VideoSeek returns the script that plays a video block's player, its page
// as VideoPlayer made it, from at seconds in, for the web view to run; ""
// for a player whose site can't be told to, which loads again there
// instead, VideoPlayerAt's page.
func VideoSeek(link string, at int) string {
	defer catch(nil)
	v, ok := doc.ParseVideoURL(link)
	if !ok {
		return ""
	}
	return rendered.VideoSeek(v, at)
}

// noteLine is a line of a video's notes, as VideoNotes returns it: its
// time in seconds, or -1, as it shows, and its text in parts, a time in it
// at its seconds, text at -1.
type noteLine struct {
	At    int        `json:"at"`
	Time  string     `json:"time,omitempty"`
	Parts []notePart `json:"parts"`
	// Line is its place among the notes' lines, and Speaker who it names,
	// for a file's.
	Line    int    `json:"line"`
	Speaker string `json:"speaker,omitempty"`
}

type notePart struct {
	Text string `json:"text"`
	At   int    `json:"at"`
}

// FileKind says how the app shows a file of name and mime type, as
// doc.FilePreviewKind.
func FileKind(name, mime string) string {
	defer catch(nil)
	return doc.FilePreviewKind(name, mime)
}

// FileSpeakers returns the speakers notes name, as JSON, for names, the
// names the user gave them, as JSON (doc.NoteSpeakers).
func FileSpeakers(notes, names string) string {
	defer catch(nil)
	raw, _ := encode(doc.NoteSpeakers(notes, doc.SpeakerNamesFrom(names)))
	return raw
}

// speakersEdit is notes after a change of speakers, and the names they use.
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

// RenameSpeaker has every line of from name to, and returns a speakersEdit
// in JSON (doc.RenameSpeaker).
func RenameSpeaker(notes, names, from, to string) string {
	defer catch(nil)
	return encodeSpeakers(doc.RenameSpeaker(notes, doc.SpeakerNamesFrom(names), from, to))
}

// SetLineSpeaker has line number line name to, and returns a speakersEdit
// in JSON (doc.SetLineSpeaker).
func SetLineSpeaker(notes, names string, line int, to string) string {
	defer catch(nil)
	return encodeSpeakers(doc.SetLineSpeaker(notes, doc.SpeakerNamesFrom(names), line, to))
}

// FileNotes returns a file block's notes in lines, as its block shows them,
// each with its speaker and its place, as JSON (doc.SpokenLines).
func FileNotes(notes, names string, hidden bool) string {
	defer catch(nil)
	return encodeLines(doc.SpokenLines(notes, doc.SpeakerNamesFrom(names), hidden))
}

// FileTranscriptText returns a file's notes as text to copy
// (doc.NotesText).
func FileTranscriptText(notes, names string, hidden bool) string {
	defer catch(nil)
	return doc.NotesText(notes, doc.SpeakerNamesFrom(names), hidden)
}

// FileTranscriptBlocks returns a file's notes as a transcript in JSON, as
// blocks to put in its page (doc.NotesBlocks).
func FileTranscriptBlocks(notes, names string, hidden bool) string {
	defer catch(nil)
	raw, _ := encode(transcript{Blocks: doc.NotesBlocks(notes, doc.SpeakerNamesFrom(names), hidden)})
	return raw
}

// VideoNotes returns a video block's notes in lines, as its block shows
// them, as JSON (doc.NoteLines).
func VideoNotes(notes string) string {
	defer catch(nil)
	return encodeLines(doc.NoteLines(notes))
}

// encodeLines is lines of notes as JSON.
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

// MaxNotes is the most characters a video's notes hold.
const MaxNotes = doc.MaxNotes

// FoldedLines are the lines long notes or a long transcript show until
// they are opened, and FoldsTranscript says n lines are that long.
const FoldedLines = doc.FoldedLines

// MaxPixels is the most pixels a picture may have to be shown, which the
// app checks before it decodes one.
const MaxPixels = doc.MaxPixels

// FoldsTranscript says n lines of notes or a transcript show only their
// first FoldedLines until they are opened.
func FoldsTranscript(n int) bool {
	defer catch(nil)
	return doc.FoldsTranscript(n)
}

// VideoHasCaptions says the site of a video block's link gives its
// videos' captions, which agents read for what is said in one.
func VideoHasCaptions(link string) bool {
	defer catch(nil)
	v, ok := doc.ParseVideoURL(link)
	return ok && web.HasCaptions(v)
}

// NormalizeNotes returns a video's notes as its block keeps them: a line
// break for each of any kind, and no space around them.
func NormalizeNotes(notes string) string {
	defer catch(nil)
	return doc.NormalizeNotes(notes)
}

// Clock returns seconds as a video or a recording shows a time: 3:05, or
// 1:02:05.
func Clock(sec int) string {
	defer catch(nil)
	return doc.Clock(sec)
}

// MomentLink returns the link to a moment of a video or a recording, block
// blockID of page pageID, at seconds in: it opens the page there and plays
// the block from that time.
func MomentLink(pageID, blockID string, at int) string {
	defer catch(nil)
	return doc.MomentLink(pageID, blockID, at)
}

// BlockLink returns the link to block blockID of page pageID, which opens
// the page there.
func BlockLink(pageID, blockID string) string {
	defer catch(nil)
	return doc.BlockLink(pageID, blockID)
}

// LinkedBlock returns the block a link to a page points to, "" for the
// page itself, without the time a link to a moment names.
func LinkedBlock(href string) string {
	defer catch(nil)
	return doc.LinkBlock(href)
}

// LinkedMoment returns the seconds a link to a block plays it from, or -1
// for a link that names no moment.
func LinkedMoment(href string) int {
	defer catch(nil)
	at, ok := doc.LinkMoment(href)
	if !ok {
		return -1
	}
	return at
}

// EmbedPage returns the page that runs an interactive embed's HTML, as the
// desktop's: in a sandboxed frame off the network, on a background of bg,
// a hex color, dark or not.
func EmbedPage(src, bg string, dark bool) string {
	defer catch(nil)
	return rendered.EmbedPage("", src, bg, dark, true)
}

// NoRTCScript returns the script that takes WebRTC away from the window it
// runs in, as an embed's frame runs it before the embed (rendered.NoRTC):
// the app's web views of embeds, formulas and diagrams run it first in
// each of their frames, those a page makes itself among them, which would
// otherwise have WebRTC to reach the network around the page's policy.
func NoRTCScript() string {
	defer catch(nil)
	return rendered.NoRTC
}
