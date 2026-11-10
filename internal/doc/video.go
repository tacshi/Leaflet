package doc

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Video struct {
	Provider string
	ID       string
	Hash     string
	Part     int
	CID      int64
	Start    int
}

var (
	youtubeID     = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoID       = regexp.MustCompile(`^[0-9]+$`)
	vimeoHash     = regexp.MustCompile(`^[0-9a-f]{6,}$`)
	bilibiliID    = regexp.MustCompile(`^(?:BV[1-9A-HJ-NP-Za-km-z]{10}|av[1-9][0-9]*)$`)
	dailymotionID = regexp.MustCompile(`^[xk][0-9A-Za-z]+$`)
	niconicoID    = regexp.MustCompile(`^(?:sm|so|nm)[1-9][0-9]*$`)
	linkTime      = regexp.MustCompile(`^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s?)?$`)
)

const VideoPlayers = "https://www.youtube-nocookie.com https://player.vimeo.com https://player.bilibili.com " +
	"https://www.bilibili.com https://geo.dailymotion.com https://embed.nicovideo.jp"

func ShortVideoLink(link string) bool {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	_, video := ParseVideoURL(link)
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") == "b23.tv" && len(parts) == 1 && !video
}

var VideoExts = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".ogv": true}

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
	case "bilibili.com", "m.bilibili.com", "player.bilibili.com", "b23.tv":
		id := ""
		switch i := slices.Index(parts, "video"); {
		case host == "b23.tv":
			if len(parts) == 1 {
				id = parts[0]
			}
		case len(parts) == 1 && parts[0] == "player.html":
			id = q.Get("bvid")
			if id == "" && q.Get("aid") != "" {
				id = "av" + q.Get("aid")
			}
		case i >= 0 && i+1 < len(parts):
			id = parts[i+1]
		}
		if len(id) > 2 && strings.EqualFold(id[:2], "bv") {
			id = "BV" + id[2:]
		}
		if bilibiliID.MatchString(id) {
			v := Video{Provider: "bilibili", ID: id}
			part := q.Get("p")
			if part == "" {
				part = q.Get("page")
			}
			if p, _ := strconv.Atoi(part); p > 1 {
				v.Part = p
			}
			if cid, _ := strconv.ParseInt(q.Get("cid"), 10, 64); cid > 0 {
				v.CID = cid
			}
			t, _, _ := strings.Cut(q.Get("t"), ".")
			v.Start = startOf(t)
			return v, true
		}
	case "dailymotion.com", "geo.dailymotion.com", "dai.ly":
		id := ""
		switch {
		case host == "dai.ly":
			if len(parts) == 1 {
				id = parts[0]
			}
		case len(parts) == 1 && parts[0] == "player.html":
			id = q.Get("video")
		case len(parts) == 2 && parts[0] == "video":
			id = parts[1]
		case len(parts) == 3 && parts[0] == "embed" && parts[1] == "video":
			id = parts[2]
		}
		id, _, _ = strings.Cut(id, "_")
		if dailymotionID.MatchString(id) {
			t := q.Get("start")
			if t == "" {
				t = q.Get("startTime")
			}
			return Video{Provider: "dailymotion", ID: id, Start: startOf(t)}, true
		}
	case "nicovideo.jp", "sp.nicovideo.jp", "embed.nicovideo.jp", "nico.ms":
		id := ""
		switch {
		case host == "nico.ms":
			if len(parts) == 1 {
				id = parts[0]
			}
		case len(parts) == 2 && parts[0] == "watch":
			id = parts[1]
		}
		if niconicoID.MatchString(id) {
			return Video{Provider: "niconico", ID: id, Start: startOf(q.Get("from"))}, true
		}
	}
	if VideoExts[strings.ToLower(path.Ext(u.Path))] && u.Host != "" {
		t, _ := strings.CutPrefix(u.Fragment, "t=")
		u.Fragment, u.RawFragment = "", ""
		return Video{Provider: "file", ID: u.String(), Start: startOf(t)}, true
	}
	return Video{}, false
}

func startOf(t string) int {
	s, _ := linkSeconds(t)
	return s
}

func linkSeconds(t string) (int, bool) {
	m := linkTime.FindStringSubmatch(strings.ToLower(t))
	if m == nil {
		return 0, false
	}
	var s int64
	for i, k := range []int64{3600, 60, 1} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.ParseInt(m[i+1], 10, 32)
		if err != nil {
			return 0, false
		}
		if s += n * k; s > math.MaxInt32 {
			return 0, false
		}
	}
	return int(s), true
}

func (v Video) At(sec int) Video {
	v.Start = max(sec, 0)
	return v
}

func (v Video) WatchURL() string {
	switch v.Provider {
	case "file":
		return v.PlayerURL(false)
	case "vimeo":
		u := "https://vimeo.com/" + v.ID
		if v.Hash != "" {
			u += "/" + v.Hash
		}
		if v.Start > 0 {
			u += "#t=" + strconv.Itoa(v.Start) + "s"
		}
		return u
	case "bilibili":
		q := url.Values{}
		if v.Part > 0 {
			q.Set("p", strconv.Itoa(v.Part))
		}
		if v.Start > 0 {
			q.Set("t", strconv.Itoa(v.Start))
		}
		u := "https://www.bilibili.com/video/" + v.ID
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		return u
	case "dailymotion":
		u := "https://www.dailymotion.com/video/" + v.ID
		if v.Start > 0 {
			u += "?start=" + strconv.Itoa(v.Start)
		}
		return u
	case "niconico":
		u := "https://www.nicovideo.jp/watch/" + v.ID
		if v.Start > 0 {
			u += "?from=" + strconv.Itoa(v.Start)
		}
		return u
	}
	u := "https://www.youtube.com/watch?v=" + v.ID
	if v.Start > 0 {
		u += "&t=" + strconv.Itoa(v.Start) + "s"
	}
	return u
}

func (v Video) PlayerURL(autoplay bool) string {
	play := ""
	if autoplay {
		play = "autoplay=1&"
	}
	switch v.Provider {
	case "file":
		if v.Start > 0 {
			return v.ID + "#t=" + strconv.Itoa(v.Start)
		}
		return v.ID
	case "vimeo":
		u := "https://player.vimeo.com/video/" + v.ID + "?" + play + "dnt=1"
		if v.Hash != "" {
			u += "&h=" + v.Hash
		}
		if v.Start > 0 {
			u += "#t=" + strconv.Itoa(v.Start) + "s"
		}
		return u
	case "bilibili":
		q := url.Values{"autoplay": {"0"}, "high_quality": {"1"}}
		if autoplay {
			q.Set("autoplay", "1")
		}
		if n, ok := strings.CutPrefix(v.ID, "av"); ok {
			q.Set("aid", n)
		} else {
			q.Set("bvid", v.ID)
		}
		if aid, ok := bilibiliAID(v.ID); ok && v.CID > 0 {
			q.Set("aid", strconv.FormatInt(aid, 10))
			q.Set("cid", strconv.FormatInt(v.CID, 10))
		}
		if v.Part > 0 {
			q.Set("p", strconv.Itoa(v.Part))
		}
		if v.Start > 0 {
			q.Set("t", strconv.Itoa(v.Start))
		}
		return "https://player.bilibili.com/player.html?" + q.Encode()
	case "dailymotion":
		u := "https://geo.dailymotion.com/player.html?video=" + v.ID
		if v.Start > 0 {
			u += "&startTime=" + strconv.Itoa(v.Start)
		}
		return u
	case "niconico":
		u := "https://embed.nicovideo.jp/watch/" + v.ID
		if v.Start > 0 {
			u += "?from=" + strconv.Itoa(v.Start)
		}
		return u
	}
	u := "https://www.youtube-nocookie.com/embed/" + v.ID + "?" + play + "playsinline=1&rel=0&enablejsapi=1"
	if v.Start > 0 {
		u += "&start=" + strconv.Itoa(v.Start)
	}
	return u
}

func (v Video) InfoURL() string {
	v = v.At(0)
	switch v.Provider {
	case "file":
		return ""
	case "vimeo":
		return "https://vimeo.com/api/oembed.json?width=1280&url=" + url.QueryEscape(v.WatchURL())
	case "bilibili":
		if n, ok := strings.CutPrefix(v.ID, "av"); ok {
			return "https://api.bilibili.com/x/player/pagelist?aid=" + n
		}
		return "https://api.bilibili.com/x/player/pagelist?bvid=" + v.ID
	case "dailymotion":
		return "https://www.dailymotion.com/services/oembed?url=" + url.QueryEscape(v.WatchURL())
	case "niconico":
		return "https://ext.nicovideo.jp/api/getthumbinfo/" + v.ID
	}
	return "https://www.youtube.com/oembed?format=json&url=" + url.QueryEscape(v.WatchURL())
}

func (v Video) ReadInfo(data []byte) (title, thumbnail string, err error) {
	switch v.Provider {
	case "bilibili":
		p, err := v.ReadPart(data)
		return p.Title, p.Picture, err
	case "niconico":
		var r struct {
			Status    string `xml:"status,attr"`
			Title     string `xml:"thumb>title"`
			Thumbnail string `xml:"thumb>thumbnail_url"`
			Error     string `xml:"error>description"`
		}
		if err := xml.Unmarshal(data, &r); err != nil {
			return "", "", err
		}
		if r.Status != "ok" {
			return "", "", fmt.Errorf("niconico: %s", r.Error)
		}
		return strings.TrimSpace(r.Title), r.Thumbnail, nil
	}
	var o struct {
		Title     string `json:"title"`
		Thumbnail string `json:"thumbnail_url"`
	}
	if err := json.Unmarshal(data, &o); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(o.Title), o.Thumbnail, nil
}

func (v Video) FileName() string {
	if v.Provider != "file" {
		return ""
	}
	u, err := url.Parse(v.ID)
	if err != nil {
		return ""
	}
	return path.Base(u.Path)
}

type BilibiliPart struct {
	CID     int64
	Title   string
	Picture string
}

func (v Video) ReadPart(data []byte) (BilibiliPart, error) {
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    []struct {
			CID        int64  `json:"cid"`
			Page       int    `json:"page"`
			Part       string `json:"part"`
			FirstFrame string `json:"first_frame"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return BilibiliPart{}, err
	}
	if r.Code != 0 {
		return BilibiliPart{}, fmt.Errorf("bilibili: %d %s", r.Code, r.Message)
	}
	for _, p := range r.Data {
		if p.Page == max(v.Part, 1) && p.CID > 0 {
			pic := p.FirstFrame
			if rest, ok := strings.CutPrefix(pic, "http://"); ok {
				pic = "https://" + rest
			}
			return BilibiliPart{CID: p.CID, Title: strings.TrimSpace(p.Part), Picture: pic}, nil
		}
	}
	return BilibiliPart{}, fmt.Errorf("bilibili: no part %d", max(v.Part, 1))
}

func bilibiliAID(id string) (int64, bool) {
	if n, ok := strings.CutPrefix(id, "av"); ok {
		aid, err := strconv.ParseInt(n, 10, 64)
		return aid, err == nil && aid > 0
	}
	const (
		alphabet = "FcwAPNKTMug3GV5Lj7EJnHpWsx4tb8haYeviqBz6rkCy12mUSDQX9RdoZf"
		xorCode  = 23442827791579
		maskCode = 1<<51 - 1
	)
	if len(id) != 12 || !strings.HasPrefix(id, "BV1") {
		return 0, false
	}
	var n int64
	for _, i := range []int{6, 4, 2, 3, 1, 5, 0, 7, 8} {
		d := strings.IndexByte(alphabet, id[3+i])
		if d < 0 {
			return 0, false
		}
		n = n*int64(len(alphabet)) + int64(d)
	}
	return n&maskCode ^ xorCode, true
}

func (v Video) Thumbnail() string {
	if v.Provider == "youtube" {
		return "https://i.ytimg.com/vi/" + v.ID + "/hqdefault.jpg"
	}
	return ""
}

func VideoNode(link, title string) (*Node, error) {
	link = strings.TrimSpace(link)
	if _, ok := ParseVideoURL(link); !ok {
		return nil, toolErr("A video block needs video.url, a link to a YouTube, Vimeo, Bilibili, Dailymotion or Niconico video, or to a video file (.mp4, .webm, .mov).")
	}
	return &Node{ID: NewBlockID(), Type: "video", Data: map[string]any{"url": link, "title": strings.TrimSpace(title)}}, nil
}
