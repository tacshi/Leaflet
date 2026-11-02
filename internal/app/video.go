package app

import (
	"encoding/json"
	"log"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/rendered"
	"leaflet/internal/theme"
	"leaflet/internal/web"
	"leaflet/locales"
)

// Video blocks show the video's picture until play is clicked, and then
// its site's player, or a player of the file, in a web view of its own.

// videoSession is the website data of the video players, kept in memory
// only, which goes as the last player closes.
var videoSession = sync.OnceValue(mygo.NewEphemeralSession)

// videoInfo is what a video's site tells about it: its title and picture.
type videoInfo struct{ title, thumbnail string }

// videoKey names a video whatever link to it a block has.
func videoKey(v doc.Video) string { return v.Provider + ":" + v.ID }

// videoInfoOf returns what the video's site said about it, asking it the
// first time. Until it answers, or when it cannot, the info is empty; a
// file, with no site to ask, goes by its name.
func (a *App) videoInfoOf(v doc.Video) videoInfo {
	if v.Provider == "file" {
		return videoInfo{title: v.FileName()}
	}
	key := videoKey(v)
	if info, ok := a.videoInfo[key]; ok {
		return info
	}
	if !a.videoAsked[key] {
		a.videoAsked[key] = true
		go func() {
			var info videoInfo
			data, err := web.Fetch(v.OEmbedURL())
			if err == nil {
				var o struct {
					Title     string `json:"title"`
					Thumbnail string `json:"thumbnail_url"`
				}
				err = json.Unmarshal(data, &o)
				info = videoInfo{strings.TrimSpace(o.Title), o.Thumbnail}
			}
			if err != nil {
				log.Printf("video %s: %v", v.WatchURL(), err)
			}
			a.update(func() {
				a.videoInfo[key] = info
				a.nameVideos(key, info.title)
			})
		}()
	}
	return videoInfo{}
}

// nameVideos gives the open page's blocks of a video without a title the
// one its site said, for search and exports to have it.
func (a *App) nameVideos(key, title string) {
	if title == "" || a.page == nil || a.page.editor.ReadOnly {
		return
	}
	var ids []string
	a.page.editor.Content.Walk(func(n, _ *doc.Node) {
		if v, ok := doc.ParseVideoURL(n.Str("url")); ok && n.Type == "video" && n.Str("title") == "" && videoKey(v) == key {
			ids = append(ids, n.ID)
		}
	})
	for _, id := range ids {
		a.page.editor.SetData(id, "title", title)
	}
}

func (a *App) videoBlock(c *ui.Context, n *doc.Node, width float32) {
	pal := theme.Of(c)
	v, ok := doc.ParseVideoURL(n.Str("url"))
	if !ok {
		ui.Text(c, locales.VideoNotALink(n.Str("url"))).TextColor(pal.TextMuted)
		return
	}
	height := width * 9 / 16
	if wv := a.videos[n.ID]; wv != nil {
		ui.WebView(c, wv).Height(height).FillWidth().Radius(8)
		return
	}
	info := a.videoInfoOf(v)
	title := n.Str("title")
	if title == "" {
		title = info.title
	}
	if _, answered := a.videoInfo[videoKey(v)]; answered && title == "" {
		// The site does not know the video, or could not be reached: the
		// link at least says what it was.
		title = n.Str("url")
	}
	thumb := v.Thumbnail()
	if thumb == "" {
		thumb = info.thumbnail
	}
	white := ui.RGB(255, 255, 255)
	poster := ui.ButtonBase(c.Key("video:" + n.ID)).Height(height).FillWidth().Radius(8).Clip().
		Background(ui.RGB(0, 0, 0)).Cursor(ui.CursorPointer).Label(locales.VideoPlay(title))
	hovered := poster.Hovered()
	poster.Children(func() {
		if bmp := a.images.get(thumb, func() { a.update(func() {}) }); bmp != nil {
			ui.Image(c, bmp).Absolute().Top(0).Left(0).Size(width, height).Fit(ui.Cover)
		}
		if title != "" {
			ui.Row(c).Absolute().Top(0).Left(0).Width(width).Padding(14, 16, 32).
				LinearGradient(ui.LinearGradient{From: ui.RGBA(0, 0, 0, 0.6), To: ui.RGBA(0, 0, 0, 0), Angle: 180}).Children(func() {
				ui.Text(c, title).FontSize(15).FontWeight(600).TextColor(white).SingleLine().Grow(1)
			})
		}
		const play = 64
		ui.Row(c).Absolute().Top((height-play)/2).Left((width-play)/2).Size(play, play).Radius(play / 2).
			Background(ui.RGBA(0, 0, 0, map[bool]float32{true: 0.8, false: 0.55}[hovered])).Center().Children(func() {
			ui.Icon(c, theme.Icon("play")).FontSize(26).TextColor(white).Margin(0, 0, 0, 3)
		})
	})
	if poster.Clicked() {
		a.playVideo(n.ID, v)
	}
}

// playVideo puts the video's player in its block, playing.
func (a *App) playVideo(id string, v doc.Video) {
	if a.win == nil {
		return
	}
	// The player's cookies and storage stay in memory, apart from the
	// other web views'. Its videos may float in picture in picture, which
	// macOS otherwise keeps to Safari. The wheel over the player, which
	// does not scroll, scrolls the page around it.
	wv, err := a.win.NewWebView(mygo.WebViewOptions{BackgroundColor: "#000000", ScrollToContainer: true,
		Page: mygo.PageOptions{Session: videoSession(), PictureInPicture: true}})
	if err != nil {
		a.toast(locales.VideoCannotPlay(err.Error()))
		return
	}
	page := wv.Page()
	// The player's links, as to the video on its site, open in the
	// browser rather than in the block; what its script opens on its own,
	// as an ad's pop-up, opens nowhere.
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		e.PreventDefault()
		if e.UserInitiated {
			openLink(e.URL)
		}
	})
	page.SetWindowOpenHandler(func(r mygo.WindowOpenRequest) *mygo.WindowOptions {
		if r.UserInitiated {
			openLink(r.URL)
		}
		return nil
	})
	page.LoadHTML(rendered.VideoPage(v), rendered.VideoOrigin)
	a.videos[id] = wv
}

// dropVideos stops the players of videos no longer in the open page.
func (a *App) dropVideos() {
	for id, v := range a.videos {
		if a.page == nil || a.page.editor.Content.Find(id) == nil {
			v.Destroy()
			delete(a.videos, id)
		}
	}
}

// openLink opens a web link in the browser.
func openLink(u string) {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		go func() { _ = mygo.Shell.OpenExternal(u) }()
	}
}
