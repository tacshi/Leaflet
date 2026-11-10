package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"leaflet/internal/doc"
	"leaflet/internal/remote"
)

func HasCaptions(v doc.Video) bool { return v.Provider == "youtube" || v.Provider == "vimeo" }

type Captions struct {
	Language string
	Auto     bool
	Lines    []Caption
}

type Caption struct {
	StartMs int    `json:"start_ms"`
	Text    string `json:"text"`
}

var (
	ErrNoCaptions = errors.New("the video has no captions")
	ErrRefused    = errors.New("the site refused to give the captions")
)

type RestrictedError struct{ Reason string }

func (e *RestrictedError) Error() string {
	if e.Reason == "" {
		return "the video can't be watched here"
	}
	return "the video can't be watched here: " + e.Reason
}

const maxCaptionBytes = 8 << 20

var captionsKept = struct {
	sync.Mutex
	m map[string]keptCaptions
}{m: map[string]keptCaptions{}}

type keptCaptions struct {
	c  Captions
	at time.Time
}

func ReadCaptions(ctx context.Context, v doc.Video) (Captions, error) {
	key := v.Provider + ":" + v.ID + ":" + v.Hash
	captionsKept.Lock()
	k, ok := captionsKept.m[key]
	captionsKept.Unlock()
	if ok && time.Since(k.at) < time.Hour {
		return k.c, nil
	}
	var c Captions
	var err error
	switch v.Provider {
	case "youtube":
		c, err = youtubeCaptions(ctx, v.ID)
	case "vimeo":
		c, err = vimeoCaptions(ctx, v.ID, v.Hash)
	default:
		return Captions{}, ErrNoCaptions
	}
	if err != nil {
		return Captions{}, err
	}
	captionsKept.Lock()
	defer captionsKept.Unlock()
	for k, kept := range captionsKept.m {
		if time.Since(kept.at) >= time.Hour {
			delete(captionsKept.m, k)
		}
	}
	captionsKept.m[key] = keptCaptions{c: c, at: time.Now()}
	return c, nil
}

func ask(ctx context.Context, method, link string, body []byte, cookie string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, link, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fetchAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	res, err := publicClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, ErrRefused
	case res.StatusCode >= 400:
		return nil, &remote.Error{Status: res.StatusCode, Message: res.Status}
	}
	return io.ReadAll(io.LimitReader(res.Body, maxCaptionBytes))
}

var (
	youtubeKey     = regexp.MustCompile(`"INNERTUBE_API_KEY":\s*"([a-zA-Z0-9_-]+)"`)
	youtubeConsent = regexp.MustCompile(`name="v" value="(.*?)"`)
)

func youtubeCaptions(ctx context.Context, id string) (Captions, error) {
	watch := "https://www.youtube.com/watch?v=" + url.QueryEscape(id)
	page, err := ask(ctx, "GET", watch, nil, "")
	if err != nil {
		return Captions{}, err
	}
	if bytes.Contains(page, []byte(`action="https://consent.youtube.com/s"`)) {
		m := youtubeConsent.FindSubmatch(page)
		if m == nil {
			return Captions{}, ErrRefused
		}
		if page, err = ask(ctx, "GET", watch, nil, "CONSENT=YES+"+string(m[1])); err != nil {
			return Captions{}, err
		}
	}
	m := youtubeKey.FindSubmatch(page)
	if m == nil {
		return Captions{}, ErrRefused
	}
	body, _ := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]any{"clientName": "ANDROID", "clientVersion": "20.10.38"}},
		"videoId": id,
	})
	data, err := ask(ctx, "POST", "https://www.youtube.com/youtubei/v1/player?key="+string(m[1]), body, "")
	if err != nil {
		return Captions{}, err
	}
	track, err := YouTubeTrack(data)
	if err != nil {
		return Captions{}, err
	}
	xmlData, err := ask(ctx, "GET", track.URL, nil, "")
	if err != nil {
		return Captions{}, err
	}
	return Captions{Language: track.Language, Auto: track.Auto, Lines: YouTubeLines(xmlData)}, nil
}

type Track struct {
	URL      string `json:"url"`
	Language string `json:"language"`
	Auto     bool   `json:"auto"`
}

func YouTubeTrack(player []byte) (Track, error) {
	var p any
	if err := json.Unmarshal(player, &p); err != nil {
		return Track{}, fmt.Errorf("youtube: %w", err)
	}
	status, reason := text(at(p, "playabilityStatus", "status")), text(at(p, "playabilityStatus", "reason"))
	switch {
	case status == "OK":
	case status == "LOGIN_REQUIRED" && strings.Contains(strings.ToLower(reason), "bot"):
		return Track{}, ErrRefused
	default:
		return Track{}, &RestrictedError{Reason: reason}
	}
	list := at(p, "captions", "playerCaptionsTracklistRenderer")
	var tracks []Track
	for _, t := range items(at(list, "captionTracks")) {
		if link := text(at(t, "baseUrl")); link != "" {
			tracks = append(tracks, Track{URL: link, Language: text(at(t, "languageCode")), Auto: at(t, "kind") == "asr"})
		}
	}
	first := -1
	if audio := items(at(list, "audioTracks")); len(audio) > 0 {
		if n, ok := at(audio[0], "defaultCaptionTrackIndex").(float64); ok && n == float64(int(n)) {
			first = int(n)
		}
	}
	t, ok := pickTrack(tracks, first, -1)
	if !ok {
		return Track{}, ErrNoCaptions
	}
	if strings.Contains(t.URL, "&exp=xpe") {
		return Track{}, ErrRefused
	}
	return t, nil
}

func pickTrack(tracks []Track, first, then int) (Track, bool) {
	if len(tracks) == 0 {
		return Track{}, false
	}
	base := func(lang string) string { b, _, _ := strings.Cut(strings.ToLower(lang), "-"); return b }
	if i := slices.IndexFunc(tracks, func(t Track) bool { return t.Auto }); i >= 0 {
		spoken := base(tracks[i].Language)
		if j := slices.IndexFunc(tracks, func(t Track) bool { return !t.Auto && base(t.Language) == spoken }); j >= 0 {
			return tracks[j], true
		}
		return tracks[i], true
	}
	if first >= 0 && first < len(tracks) {
		return tracks[first], true
	}
	if i := slices.IndexFunc(tracks, func(t Track) bool { return base(t.Language) == "en" }); i >= 0 {
		return tracks[i], true
	}
	if then >= 0 && then < len(tracks) {
		return tracks[then], true
	}
	return tracks[0], true
}

func YouTubeLines(data []byte) []Caption {
	var lines []Caption
	for _, m := range youtubeLine.FindAllSubmatch(data, -1) {
		start := 0
		if t := youtubeStart.FindSubmatch(m[2]); t != nil && string(t[1]) == "t" {
			start, _ = strconv.Atoi(string(t[2]))
		} else if t != nil {
			f, _ := strconv.ParseFloat(string(t[2]), 64)
			start = int(f*1000 + 0.5)
		}
		if s := captionText(string(m[3])); s != "" {
			lines = append(lines, Caption{StartMs: start, Text: s})
		}
	}
	return lines
}

var (
	youtubeLine  = regexp.MustCompile(`(?s)<(p|text)\b([^>]*)>(.*?)</(?:p|text)>`)
	youtubeStart = regexp.MustCompile(`(?:^|\s)(t|start)="([0-9.]+)"`)
)

func captionText(s string) string {
	for range 2 {
		s = unescapeCaption(tagRe.ReplaceAllString(s, ""))
	}
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

var (
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	entityRe = regexp.MustCompile(`&(#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[a-zA-Z]+);`)
	entities = map[string]string{"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'", "nbsp": "\u00a0"}
)

func unescapeCaption(s string) string {
	return entityRe.ReplaceAllStringFunc(s, func(e string) string {
		name := e[1 : len(e)-1]
		if r, ok := entities[name]; ok {
			return r
		}
		if num, ok := strings.CutPrefix(name, "#"); ok {
			base := 10
			if hex, ok := strings.CutPrefix(strings.ToLower(num), "x"); ok {
				num, base = hex, 16
			}
			if n, err := strconv.ParseInt(num, base, 32); err == nil && n > 0 && n <= unicode.MaxRune && utf8.ValidRune(rune(n)) {
				return string(rune(n))
			}
		}
		return e
	})
}

func vimeoCaptions(ctx context.Context, id, hash string) (Captions, error) {
	link := "https://player.vimeo.com/video/" + url.PathEscape(id) + "/config"
	if hash != "" {
		link += "?h=" + url.QueryEscape(hash)
	}
	data, err := ask(ctx, "GET", link, nil, "")
	var re *remote.Error
	if errors.As(err, &re) && (re.Status == http.StatusForbidden || re.Status == http.StatusNotFound) {
		return Captions{}, &RestrictedError{}
	}
	if err != nil {
		return Captions{}, err
	}
	track, err := VimeoTrack(data)
	if err != nil {
		return Captions{}, err
	}
	vtt, err := ask(ctx, "GET", track.URL, nil, "")
	if err != nil {
		return Captions{}, err
	}
	return Captions{Language: track.Language, Auto: track.Auto, Lines: VTTLines(string(vtt))}, nil
}

func VimeoTrack(config []byte) (Track, error) {
	var c any
	if err := json.Unmarshal(config, &c); err != nil {
		return Track{}, fmt.Errorf("vimeo: %w", err)
	}
	var tracks []Track
	captions, chosen := -1, -1
	for _, t := range items(at(c, "request", "text_tracks")) {
		link := text(at(t, "url"))
		if strings.HasPrefix(link, "/") {
			link = "https://player.vimeo.com" + link
		}
		if !WebLink(link) {
			continue
		}
		lang, auto := strings.CutSuffix(text(at(t, "lang")), "-x-autogen")
		if at(t, "kind") == "captions" && captions < 0 {
			captions = len(tracks)
		}
		if at(t, "default") == true && chosen < 0 {
			chosen = len(tracks)
		}
		tracks = append(tracks, Track{URL: link, Language: lang, Auto: auto})
	}
	t, ok := pickTrack(tracks, captions, chosen)
	if !ok {
		return Track{}, ErrNoCaptions
	}
	return t, nil
}

func at(v any, keys ...string) any {
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	return v
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

func items(v any) []any {
	l, _ := v.([]any)
	return l
}

func WebLink(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func VTTLines(vtt string) []Caption {
	var lines []Caption
	vtt = strings.ReplaceAll(strings.ReplaceAll(vtt, "\r\n", "\n"), "\r", "\n")
	for block := range strings.SplitSeq(vtt, "\n\n") {
		rows := strings.Split(strings.Trim(block, "\n"), "\n")
		at := slices.IndexFunc(rows, func(r string) bool { return strings.Contains(r, "-->") })
		if at < 0 {
			continue
		}
		start, ok := vttTime(strings.TrimSpace(strings.SplitN(rows[at], "-->", 2)[0]))
		if !ok {
			continue
		}
		if s := captionText(strings.Join(rows[at+1:], " ")); s != "" {
			lines = append(lines, Caption{StartMs: start, Text: s})
		}
	}
	return lines
}

func vttTime(s string) (int, bool) {
	whole, frac, _ := strings.Cut(s, ".")
	parts := strings.Split(whole, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	digits := func(s string) bool {
		return s != "" && strings.Trim(s, "0123456789") == ""
	}
	ms := 0
	for _, p := range parts {
		if !digits(p) {
			return 0, false
		}
		n, _ := strconv.Atoi(p)
		ms = ms*60 + n
	}
	ms *= 1000
	if frac != "" {
		if frac = (frac + "000")[:3]; !digits(frac) {
			return 0, false
		}
		n, _ := strconv.Atoi(frac)
		ms += n
	}
	return ms, true
}

func Passages(lines []Caption) []Caption {
	var out []Caption
	for _, l := range lines {
		if n := len(out); n > 0 {
			cur := &out[n-1]
			if long := l.StartMs - cur.StartMs; long < 20000 && (long < 6000 || !endsSentence(cur.Text)) {
				cur.Text += " " + l.Text
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

func endsSentence(text string) bool {
	text = strings.TrimRight(text, `"'”’)]）」』`)
	r, _ := utf8.DecodeLastRuneInString(text)
	return strings.ContainsRune(".!?。！？…", r)
}
