package web

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"leaflet/internal/doc"
)

var shortLinkClient = &http.Client{
	Transport: publicTransport{},
	Timeout:   10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

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

var bilibiliParts = struct {
	sync.Mutex
	m      map[string]doc.BilibiliPart
	failed map[string]time.Time
}{m: map[string]doc.BilibiliPart{}, failed: map[string]time.Time{}}

var partsRetry = 5 * time.Minute

var partsFetch = Fetch

func KnownVideoPart(v doc.Video) doc.Video {
	if v.Provider != "bilibili" || v.CID > 0 {
		return v
	}
	bilibiliParts.Lock()
	defer bilibiliParts.Unlock()
	v.CID = bilibiliParts.m[v.ID+"/"+strconv.Itoa(v.Part)].CID
	return v
}

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
