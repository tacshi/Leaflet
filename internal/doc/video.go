package doc

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Video is the YouTube or Vimeo video a link points to.
type Video struct {
	Provider string // "youtube" or "vimeo"
	ID       string
	// Hash is the key in the link of an unlisted Vimeo video.
	Hash string
	// Start is how many seconds in the link says to start.
	Start int
}

var (
	youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoID   = regexp.MustCompile(`^[0-9]+$`)
	vimeoHash = regexp.MustCompile(`^[0-9a-f]{6,}$`)
	// A time as YouTube and Vimeo links give it: 90, 90s or 1h2m3s.
	linkTime = regexp.MustCompile(`^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s?)?$`)
)

// ParseVideoURL reads the video a YouTube or Vimeo link points to: a watch
// page, a short link, a Short, a live stream or a player's own link.
func ParseVideoURL(s string) (Video, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Video{}, false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	q := u.Query()
	switch host {
	case "youtu.be":
		if len(parts) == 1 && youtubeID.MatchString(parts[0]) {
			return Video{Provider: "youtube", ID: parts[0], Start: startOf(q.Get("t"))}, true
		}
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
		id := ""
		switch {
		case len(parts) == 1 && parts[0] == "watch":
			id = q.Get("v")
		case len(parts) == 2 && (parts[0] == "embed" || parts[0] == "shorts" || parts[0] == "live" || parts[0] == "v"):
			id = parts[1]
		}
		if youtubeID.MatchString(id) {
			t := q.Get("t")
			if t == "" {
				t = q.Get("start")
			}
			return Video{Provider: "youtube", ID: id, Start: startOf(t)}, true
		}
	case "vimeo.com", "player.vimeo.com":
		// The ID is the last number in the path, as in vimeo.com/76979871,
		// vimeo.com/channels/staffpicks/76979871 and
		// player.vimeo.com/video/76979871; an unlisted video's key follows it.
		for i := len(parts) - 1; i >= 0; i-- {
			if !vimeoID.MatchString(parts[i]) {
				continue
			}
			v := Video{Provider: "vimeo", ID: parts[i], Hash: q.Get("h")}
			if i+1 < len(parts) && vimeoHash.MatchString(parts[i+1]) {
				v.Hash = parts[i+1]
			}
			if !vimeoHash.MatchString(v.Hash) {
				v.Hash = ""
			}
			t := q.Get("t")
			if f, ok := strings.CutPrefix(u.Fragment, "t="); ok {
				t = f
			}
			v.Start = startOf(t)
			return v, true
		}
	}
	return Video{}, false
}

// startOf reads a link's time in seconds, or 0.
func startOf(t string) int {
	m := linkTime.FindStringSubmatch(strings.ToLower(t))
	if m == nil {
		return 0
	}
	s := 0
	for i, k := range []int{3600, 60, 1} {
		n, _ := strconv.Atoi(m[i+1])
		s += n * k
	}
	return s
}

// WatchURL is the video's page on its site.
func (v Video) WatchURL() string {
	if v.Provider == "vimeo" {
		u := "https://vimeo.com/" + v.ID
		if v.Hash != "" {
			u += "/" + v.Hash
		}
		return u
	}
	u := "https://www.youtube.com/watch?v=" + v.ID
	if v.Start > 0 {
		u += "&t=" + strconv.Itoa(v.Start) + "s"
	}
	return u
}

// PlayerURL is the site's player for the video, to frame in a page. It
// starts playing as it loads, as the user asked for it by clicking play.
func (v Video) PlayerURL() string {
	if v.Provider == "vimeo" {
		u := "https://player.vimeo.com/video/" + v.ID + "?autoplay=1&dnt=1"
		if v.Hash != "" {
			u += "&h=" + v.Hash
		}
		if v.Start > 0 {
			u += "#t=" + strconv.Itoa(v.Start) + "s"
		}
		return u
	}
	u := "https://www.youtube-nocookie.com/embed/" + v.ID + "?autoplay=1&playsinline=1&rel=0"
	if v.Start > 0 {
		u += "&start=" + strconv.Itoa(v.Start)
	}
	return u
}

// OEmbedURL is where the site tells the video's title and picture.
func (v Video) OEmbedURL() string {
	if v.Provider == "vimeo" {
		return "https://vimeo.com/api/oembed.json?width=1280&url=" + url.QueryEscape(v.WatchURL())
	}
	return "https://www.youtube.com/oembed?format=json&url=" + url.QueryEscape(v.WatchURL())
}

// Thumbnail is the video's picture where it follows from the ID alone, as
// a YouTube video's does, or "".
func (v Video) Thumbnail() string {
	if v.Provider == "youtube" {
		return "https://i.ytimg.com/vi/" + v.ID + "/hqdefault.jpg"
	}
	return ""
}

// VideoNode is a video block for a YouTube or Vimeo link.
func VideoNode(link, title string) (*Node, error) {
	link = strings.TrimSpace(link)
	if _, ok := ParseVideoURL(link); !ok {
		return nil, toolErr("A video block needs video.url, a YouTube or Vimeo link.")
	}
	return &Node{ID: NewBlockID(), Type: "video", Data: map[string]any{"url": link, "title": strings.TrimSpace(title)}}, nil
}
