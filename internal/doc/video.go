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

// Video is the video a link to a video site points to, or the video file.
type Video struct {
	// Provider is the site: "youtube", "vimeo", "bilibili", "dailymotion"
	// or "niconico"; or "file".
	Provider string
	// ID is the video's ID on its site, or a file's link without its time.
	// A Bilibili video's is its BV ID, or its av number as av170001.
	ID string
	// Hash is the key in the link of an unlisted Vimeo video.
	Hash string
	// Part is which of a Bilibili video's parts the link is to, from the
	// second; 0 is its first.
	Part int
	// CID is a Bilibili part's own ID, which its players need beside the
	// video's to play it in another site's page, without asking Bilibili
	// for it, which turns such a page away: as the embed code Bilibili
	// gives names it, or as its parts list it (ReadPart); 0 until known.
	CID int64
	// Start is how many seconds in the link says to start.
	Start int
}

var (
	youtubeID     = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoID       = regexp.MustCompile(`^[0-9]+$`)
	vimeoHash     = regexp.MustCompile(`^[0-9a-f]{6,}$`)
	bilibiliID    = regexp.MustCompile(`^(?:BV[1-9A-HJ-NP-Za-km-z]{10}|av[1-9][0-9]*)$`)
	dailymotionID = regexp.MustCompile(`^[xk][0-9A-Za-z]+$`)
	niconicoID    = regexp.MustCompile(`^(?:sm|so|nm)[1-9][0-9]*$`)
	// A time as the sites' links give it: 90, 90s or 1h2m3s.
	linkTime = regexp.MustCompile(`^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s?)?$`)
)

// VideoPlayers are the origins of the sites' players, which a page that
// plays videos frames. Bilibili's sends a phone's or a tablet's browser,
// as the iOS app's web views are, on to its mobile player on
// www.bilibili.com.
const VideoPlayers = "https://www.youtube-nocookie.com https://player.vimeo.com https://player.bilibili.com " +
	"https://www.bilibili.com https://geo.dailymotion.com https://embed.nicovideo.jp"

// ShortVideoLink reports whether link is a share link that names a video
// only once followed, as Bilibili's app shares b23.tv/<code>; web.VideoLink
// follows it.
func ShortVideoLink(link string) bool {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	_, video := ParseVideoURL(link)
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") == "b23.tv" && len(parts) == 1 && !video
}

// VideoExts are the kinds of video file a video block plays, by extension.
var VideoExts = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".ogv": true}

// ParseVideoURL reads the video a link to YouTube, Vimeo, Bilibili,
// Dailymotion or Niconico points to: a watch page, a short link, a Short,
// a live stream or a player's own link. Any other web link to a video
// file, as its extension says, is that file.
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
	case "bilibili.com", "m.bilibili.com", "player.bilibili.com", "b23.tv":
		// A video's page is video/ and its ID, after a language or s/ on
		// some, as bilibili.com/zh-Hans/video/BV1GJ411x7h7; a short link
		// is b23.tv and the ID; the player takes the ID, or the av number
		// alone, in its query.
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
		// Bilibili takes a BV ID's prefix in any case, sending on to BV.
		if len(id) > 2 && strings.EqualFold(id[:2], "bv") {
			id = "BV" + id[2:]
		}
		if bilibiliID.MatchString(id) {
			v := Video{Provider: "bilibili", ID: id}
			// Older players' links name the part page.
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
			// A time shared from the player has a fraction of a second.
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
		// An old link has the title after the ID, as x7tgad0_a-title.
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

// startOf reads a link's time in seconds, or 0.
func startOf(t string) int {
	s, _ := linkSeconds(t)
	return s
}

// linkSeconds reads a link's time in seconds, and whether it is one: a
// time past what an int32 holds, which no video lasts, is none.
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

// At is the video from seconds into it.
func (v Video) At(sec int) Video {
	v.Start = max(sec, 0)
	return v
}

// WatchURL is the video's page on its site, or the file, from its start.
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

// PlayerURL is the site's player for the video, to frame in a page, or the
// file. With autoplay, the player starts playing as it loads, where its
// site lets it, as when the user asked for it by clicking play.
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
		// Bilibili's player plays as it loads unless told not to.
		q := url.Values{"autoplay": {"0"}, "high_quality": {"1"}}
		if autoplay {
			q.Set("autoplay", "1")
		}
		if n, ok := strings.CutPrefix(v.ID, "av"); ok {
			q.Set("aid", n)
		} else {
			q.Set("bvid", v.ID)
		}
		// Given its part, the player needs to ask for nothing but what it
		// plays, as Bilibili's own embed code has it, which a phone's
		// player, framed, can't otherwise.
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
		// Niconico's player waits for its own play button, whatever it is
		// told.
		u := "https://embed.nicovideo.jp/watch/" + v.ID
		if v.Start > 0 {
			u += "?from=" + strconv.Itoa(v.Start)
		}
		return u
	}
	// The page around the player seeks it by message, which YouTube's
	// takes only with its API on.
	u := "https://www.youtube-nocookie.com/embed/" + v.ID + "?" + play + "playsinline=1&rel=0&enablejsapi=1"
	if v.Start > 0 {
		u += "&start=" + strconv.Itoa(v.Start)
	}
	return u
}

// InfoURL is where the site tells the video's title and picture, as
// ReadInfo reads them, or "" for a file, which has no site to ask.
func (v Video) InfoURL() string {
	v = v.At(0)
	switch v.Provider {
	case "file":
		return ""
	case "vimeo":
		return "https://vimeo.com/api/oembed.json?width=1280&url=" + url.QueryEscape(v.WatchURL())
	case "bilibili":
		// Its parts' list, as ReadPart reads it: its view of the video
		// turns away who has not been to the site first.
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

// ReadInfo reads the video's title and picture from what its InfoURL
// answered: oEmbed's JSON, or Bilibili's or Niconico's own.
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

// FileName is a video file's name, as its link ends, or "".
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

// BilibiliPart is a part of a Bilibili video, as its site lists it: its
// cid, its title, which a video of one part has as its own, and a picture
// of its first frame.
type BilibiliPart struct {
	CID     int64
	Title   string
	Picture string
}

// ReadPart reads the part of a Bilibili video v is of from its parts'
// list, as InfoURL answers.
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
			// Its pictures' links are http, which their servers answer as
			// https too.
			pic := p.FirstFrame
			if rest, ok := strings.CutPrefix(pic, "http://"); ok {
				pic = "https://" + rest
			}
			return BilibiliPart{CID: p.CID, Title: strings.TrimSpace(p.Part), Picture: pic}, nil
		}
	}
	return BilibiliPart{}, fmt.Errorf("bilibili: no part %d", max(v.Part, 1))
}

// bilibiliAID is the av number of a Bilibili video's ID: an av ID's own,
// or what a BV ID encodes, as Bilibili's own pages decode it.
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

// Thumbnail is the video's picture where it follows from the ID alone, as
// a YouTube video's does, or "".
func (v Video) Thumbnail() string {
	if v.Provider == "youtube" {
		return "https://i.ytimg.com/vi/" + v.ID + "/hqdefault.jpg"
	}
	return ""
}

// VideoNode is a video block for a link to a video on a site Leaflet
// plays, or to a video file.
func VideoNode(link, title string) (*Node, error) {
	link = strings.TrimSpace(link)
	if _, ok := ParseVideoURL(link); !ok {
		return nil, toolErr("A video block needs video.url, a link to a YouTube, Vimeo, Bilibili, Dailymotion or Niconico video, or to a video file (.mp4, .webm, .mov).")
	}
	return &Node{ID: NewBlockID(), Type: "video", Data: map[string]any{"url": link, "title": strings.TrimSpace(title)}}, nil
}
