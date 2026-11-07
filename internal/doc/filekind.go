package doc

import (
	"path"
	"strings"
)

// The kinds of file the app shows or plays itself, as FilePreviewKind says.
const (
	FileImage    = "image"
	FileText     = "text"
	FileMarkdown = "markdown"
	FileTable    = "table"
	FilePDF      = "pdf"
	FileAudio    = "audio"
	FileVideo    = "video"
)

// mediaTypes are the types the app's players are told a file has, by
// extension: nothing else is served to one.
var mediaTypes = map[string]string{
	".aac": "audio/aac", ".aif": "audio/aiff", ".aiff": "audio/aiff", ".caf": "audio/x-caf", ".flac": "audio/flac",
	".m4a": "audio/mp4", ".mp3": "audio/mpeg", ".oga": "audio/ogg", ".ogg": "audio/ogg", ".opus": "audio/ogg",
	".wav": "audio/wav", ".weba": "audio/webm",
	".m4v": "video/mp4", ".mov": "video/quicktime", ".mp4": "video/mp4", ".ogv": "video/ogg", ".webm": "video/webm",
	".pdf": "application/pdf",
}

// textExts are the kinds of file that are text to read, as source or data.
var textExts = map[string]bool{
	".bash": true, ".c": true, ".cc": true, ".cfg": true, ".conf": true, ".cpp": true, ".cs": true, ".css": true,
	".env": true, ".go": true, ".h": true, ".hpp": true, ".htm": true, ".html": true, ".ini": true, ".java": true,
	".js": true, ".json": true, ".jsx": true, ".kt": true, ".log": true, ".lua": true, ".mjs": true, ".php": true,
	".pl": true, ".py": true, ".r": true, ".rb": true, ".rs": true, ".sh": true, ".sql": true, ".svg": true,
	".swift": true, ".toml": true, ".ts": true, ".tsx": true, ".txt": true, ".xml": true, ".yaml": true, ".yml": true,
	".zsh": true,
}

// bareTexts are files that are text by a name with no extension.
var bareTexts = map[string]bool{"dockerfile": true, "license": true, "makefile": true, "readme": true}

// FilePreviewKind says how the app shows a file of name and mime type,
// which is often empty: as one of the kinds above, or "" for a file it
// leaves to other apps. The extension decides, as mime may only be a guess
// from it, except that an .mp4 or .webm that says it is audio is.
func FilePreviewKind(name, mime string) string {
	ext := strings.ToLower(path.Ext(name))
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch ext {
	case ".mp4", ".webm":
		if strings.HasPrefix(mime, "audio/") {
			return FileAudio
		}
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return FileImage
	case ".md", ".markdown":
		return FileMarkdown
	case ".csv", ".tsv":
		return FileTable
	}
	switch t := mediaTypes[ext]; {
	case t == "application/pdf":
		return FilePDF
	case strings.HasPrefix(t, "audio/"):
		return FileAudio
	case strings.HasPrefix(t, "video/"):
		return FileVideo
	}
	if textExts[ext] || ext == "" && bareTexts[strings.ToLower(name)] {
		return FileText
	}
	if ext == "" && strings.HasPrefix(mime, "text/") {
		return FileText
	}
	return ""
}

// FileMediaType is the type a player or the PDF viewer is told a file of
// name and mime type has, or "" for a file neither takes.
func FileMediaType(name, mime string) string {
	ext := strings.ToLower(path.Ext(name))
	if FilePreviewKind(name, mime) == FileAudio {
		switch ext {
		case ".mp4":
			return "audio/mp4"
		case ".webm":
			return "audio/webm"
		}
	}
	return mediaTypes[ext]
}

// Transcribable reports whether a file of name and mime type is audio to
// transcribe.
func Transcribable(name, mime string) bool { return FilePreviewKind(name, mime) == FileAudio }

// FileWithNotes is file block prev with the notes a replace_block's spec
// gives it, its file's own fields as they are: an agent can't name a file,
// and can only write what shows under it.
func FileWithNotes(prev *Node, spec map[string]any) (*Node, error) {
	f := asMap(spec["file"])
	if f == nil {
		f = spec
	}
	raw, _ := f["notes"].(string)
	notes, err := checkedNotes(raw, "file")
	if err != nil {
		return nil, err
	}
	n := prev.Clone()
	if notes == "" {
		delete(n.Data, "notes")
	} else {
		n.Set("notes", notes)
	}
	return n, nil
}
