package doc

import "testing"

func TestFilePreviewKind(t *testing.T) {
	for _, c := range []struct{ name, mime, want string }{
		{"a.PNG", "", FileImage}, {"a.jpeg", "", FileImage},
		{"a.pdf", "", FilePDF}, {"a.md", "", FileMarkdown}, {"a.Markdown", "", FileMarkdown},
		{"a.csv", "", FileTable}, {"a.tsv", "", FileTable},
		{"a.mp3", "", FileAudio}, {"a.M4A", "", FileAudio}, {"a.flac", "", FileAudio}, {"a.ogg", "", FileAudio},
		{"a.mp4", "", FileVideo}, {"a.mov", "", FileVideo}, {"a.webm", "video/webm", FileVideo},
		{"a.mp4", "audio/mp4", FileAudio}, {"a.webm", "Audio/WebM", FileAudio},
		{"a.go", "", FileText}, {"a.html", "text/html", FileText}, {"a.svg", "", FileText}, {"Makefile", "", FileText},
		{"notes", "text/plain", FileText},
		{"a.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ""},
		{"a.exe", "", ""}, {"a.zip", "text/plain", ""}, {"", "", ""}, {"archive", "", ""},
	} {
		if got := FilePreviewKind(c.name, c.mime); got != c.want {
			t.Errorf("FilePreviewKind(%q, %q) = %q, want %q", c.name, c.mime, got, c.want)
		}
	}
}

func TestFileMediaType(t *testing.T) {
	for _, c := range []struct{ name, mime, want string }{
		{"a.mp3", "", "audio/mpeg"}, {"a.m4a", "", "audio/mp4"}, {"a.mp4", "", "video/mp4"},
		{"a.mp4", "audio/mp4", "audio/mp4"}, {"a.webm", "audio/webm", "audio/webm"}, {"a.pdf", "", "application/pdf"},
		{"a.html", "text/html", ""}, {"a.svg", "image/svg+xml", ""}, {"a.mov", "text/html", "video/quicktime"},
	} {
		if got := FileMediaType(c.name, c.mime); got != c.want {
			t.Errorf("FileMediaType(%q, %q) = %q, want %q", c.name, c.mime, got, c.want)
		}
	}
}

func TestTranscribable(t *testing.T) {
	if !Transcribable("call.m4a", "") || !Transcribable("call.mp4", "audio/mp4") || Transcribable("film.mp4", "") || Transcribable("a.txt", "") {
		t.Error("wrong files transcribable")
	}
}
