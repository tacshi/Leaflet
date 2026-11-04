package web

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"leaflet/internal/doc"
)

// shortLinkClient asks a site where a share link leads, without going
// there, to public addresses only, as Fetch does. Tests replace it.
var shortLinkClient = &http.Client{
	Transport: publicTransport{},
	Timeout:   10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// VideoLink is link, or, for a share link doc.ShortVideoLink tells, the
// watch link of the video it leads to, without what the share added to
// it. It asks the site, so it runs off the main thread. A share link that
// leads to no video, or whose site can't be asked, stays as it is.
func VideoLink(link string) string {
	link = strings.TrimSpace(link)
	if !doc.ShortVideoLink(link) {
		return link
	}
	req, err := http.NewRequest("GET", link, nil)
	if err != nil {
		return link
	}
	req.Header.Set("User-Agent", fetchAgent)
	res, err := shortLinkClient.Do(req)
	if err != nil {
		return link
	}
	_ = res.Body.Close()
	to, err := res.Location()
	if err != nil {
		return link
	}
	if v, ok := doc.ParseVideoURL(to.String()); ok && v.Provider != "file" {
		return v.WatchURL()
	}
	return link
}

// bilibiliParts are the parts of Bilibili videos asked of Bilibili this
// run, by video and part, and when Bilibili last could not tell of one.
var bilibiliParts = struct {
	sync.Mutex
	m      map[string]doc.BilibiliPart
	failed map[string]time.Time
}{m: map[string]doc.BilibiliPart{}, failed: map[string]time.Time{}}

// partsRetry is how long a part Bilibili could not tell of waits to be
// asked again: a public link shows its videos each time it is opened.
// Tests change it.
var partsRetry = 5 * time.Minute

// partsFetch reads a video's parts' list. Tests replace it.
var partsFetch = Fetch

// KnownVideoPart is v with its part's cid where VideoPart has asked
// Bilibili for it this run, without asking: for the main thread.
func KnownVideoPart(v doc.Video) doc.Video {
	if v.Provider != "bilibili" || v.CID > 0 {
		return v
	}
	bilibiliParts.Lock()
	defer bilibiliParts.Unlock()
	v.CID = bilibiliParts.m[v.ID+"/"+strconv.Itoa(v.Part)].CID
	return v
}

// VideoPart is v with its part's cid, where it is a Bilibili video's, as
// its players need it to play it in another site's page (doc.Video.CID),
// and the part as Bilibili lists it, asked of Bilibili once a run. Any
// other video, or one Bilibili can't tell of now, is as it is; that one is
// asked again once partsRetry has passed. It asks the site, so it runs off
// the main thread.
func VideoPart(v doc.Video) (doc.Video, doc.BilibiliPart) {
	if v.Provider != "bilibili" {
		return v, doc.BilibiliPart{}
	}
	key := v.ID + "/" + strconv.Itoa(v.Part)
	bilibiliParts.Lock()
	p, ok := bilibiliParts.m[key]
	at, failed := bilibiliParts.failed[key]
	bilibiliParts.Unlock()
	if !ok {
		if failed && time.Since(at) < partsRetry {
			return v, doc.BilibiliPart{}
		}
		data, err := partsFetch(v.InfoURL())
		if err == nil {
			p, err = v.ReadPart(data)
		}
		bilibiliParts.Lock()
		defer bilibiliParts.Unlock()
		if err != nil {
			bilibiliParts.failed[key] = time.Now()
			return v, doc.BilibiliPart{}
		}
		delete(bilibiliParts.failed, key)
		bilibiliParts.m[key] = p
	}
	if v.CID == 0 {
		v.CID = p.CID
	}
	return v, p
}
