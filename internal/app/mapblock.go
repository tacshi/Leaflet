package app

import (
	"context"
	"hash/fnv"
	"log"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/maps"
	"leaflet/internal/rendered"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// mapHeight is how tall a map block's map is, in a page or a chat.
const mapHeight = 300

// mapViews are the web views that show map blocks, by their keys: a
// block's (blockView), the map of a chat's answer ("chat:" and its hash),
// and the one filling the page (fullView). Each is made as its map comes
// near the window's view, let go once far, and loaded again when its map
// or the theme changes.
type mapViews struct {
	views  map[string]*mygo.WebView
	loaded map[string]uint64
	built  map[string]bool
	failed map[string]string
}

// mapService takes what a map's page asks of the app: to leave the map
// that fills the page, on Escape.
type mapService struct{ a *App }

func (s *mapService) Exit(key string) {
	s.a.update(func() {
		if ps := s.a.page; ps != nil && ps.fullMap != "" && fullView(ps.fullMap) == key {
			s.a.leaveFullMap()
		}
	})
}

// mapBlock is a map block of the open page: its map, its title and its
// route in words, and while the pointer is on it, buttons to fill the page
// with it, open it in a maps app and edit it.
func (a *App) mapBlock(c *ui.Context, n *doc.Node, width float32) {
	m := doc.MapOf(n)
	editable := a.page != nil && !a.page.editor.ReadOnly
	box := ui.Column(c.Key("mapblock:" + n.ID)).FillWidth()
	hovered := box.Hovered()
	box.Children(func() {
		a.mapCard(c, m, blockView(n.ID), width, false, func() {
			shown := map[bool]float32{true: 1, false: 0}[hovered]
			ui.Row(c).Absolute().Top(8).Left(8).Gap(2).Padding(2).Radius(8).Background(theme.Of(c).Surface).Opacity(shown).
				IgnoreInput(shown == 0).Children(func() {
				if barButton(c, "full", "maximize-2", locales.RenderedFullScreen(), shown).Clicked() && a.page != nil {
					a.page.fullMap = n.ID
				}
				a.openMapButton(c, m, shown)
				if editable && barButton(c, "edit", "pencil", locales.EditorEditMap(), shown).Clicked() {
					a.dialogs.openMap(n.ID, a.page.editor)
				}
			})
		})
	})
}

// openMapButton opens m in Apple's Maps on a Mac, and on openstreetmap.org
// elsewhere.
func (a *App) openMapButton(c *ui.Context, m doc.Map, shown float32) {
	link, label := maps.OSMURL(m), locales.MapOpenInOSM()
	if runtime.GOOS == "darwin" {
		link, label = maps.AppleMapsURL(m), locales.MapOpenInMaps()
	}
	if link != "" && barButton(c, "open", "external-link", label, shown).Clicked() {
		openLink(link)
	}
}

// mapCard draws a map: on its tiles in a web view keyed key, width wide,
// then its title and its route in words; tools, when given, float over
// the map's corner.
func (a *App) mapCard(c *ui.Context, m doc.Map, key string, width float32, full bool, tools func()) {
	pal := theme.Of(c)
	card := ui.Column(c).FillWidth().Padding(5).Radius(10).Border(1, pal.Divider).Background(pal.Surface).Role(ui.RoleGroup).Label(m.Title)
	card.Children(func() {
		ui.Box(c).FillWidth().Height(mapHeight).Radius(7).Clip().Children(func() {
			a.mapView(c, m, key, full)
			if tools != nil {
				tools()
			}
		})
		a.mapCaption(c, m)
	})
	_ = width
}

// mapCaption is a map's title, its route in a line and its legs in
// another, in the app's language, then whose data it shows, a link to
// the page that credits it.
func (a *App) mapCaption(c *ui.Context, m doc.Map) {
	pal := theme.Of(c)
	msgs := locales.In(i18n.Locale())
	ui.Column(c).FillWidth().Padding(9, 9, 6).Gap(3).Children(func() {
		if m.Title != "" {
			ui.Text(c, m.Title).FontSize(14.5).FontWeight(600).TextColor(pal.Text)
		}
		if r := m.Route; r != nil {
			ui.Text(c, doc.RouteSummary(msgs, r)).FontSize(13).FontWeight(500).TextColor(pal.TextSecondary)
			ui.Text(c, doc.RouteSteps(msgs, r)).FontSize(12.5).TextColor(pal.TextMuted)
		}
		credit := ui.ButtonBase(c.Key("credit")).AlignSelf(ui.Start).Cursor(ui.CursorPointer).Label(doc.MapCredit(m)).
			Tooltip(doc.MapCreditLink(m))
		credit.Children(func() {
			ui.Text(c, doc.MapCredit(m)).FontSize(11).TextColor(pal.TextMuted)
		})
		if credit.Clicked() {
			openLink(doc.MapCreditLink(m))
		}
	})
}

// mapView is the web view of the map keyed key, made while it is near the
// window's view.
func (a *App) mapView(c *ui.Context, m doc.Map, key string, full bool) {
	pal := theme.Of(c)
	mv := &a.mapViews
	if mv.views == nil {
		mv.views, mv.loaded, mv.built, mv.failed = map[string]*mygo.WebView{}, map[string]uint64{}, map[string]bool{}, map[string]string{}
	}
	// The same map twice in a frame, as an answer repeated, takes a view
	// each: the second's key numbered, as it comes second in every frame.
	for base, n := key, 2; mv.built[key]; n++ {
		key = base + "#" + strconv.Itoa(n)
	}
	mv.built[key] = true
	holder := ui.Box(c.Key("map:" + key)).Fill()
	near, far := embedNear(c, holder.Bounds())
	if full {
		near, far = true, false
	}
	v := mv.views[key]
	page := rendered.MapPage(m, key, pal.Dark, full, hexOf(pal.Ground), hexOf(pal.Accent))
	if v != nil && (far || page == "") {
		a.letGoMap(key)
		v = nil
	}
	if page == "" {
		holder.Children(func() { ui.Box(c).Fill().Background(pal.Ground) })
		return
	}
	if _, failed := mv.failed[key]; v == nil && !failed && near && a.win != nil {
		v = a.newMapView(key, full, pal)
	}
	if why, failed := mv.failed[key]; v == nil && failed {
		holder.Children(func() {
			ui.Box(c).Fill().Center().Background(pal.Ground).Children(func() {
				ui.Text(c, locales.HostEmbedError(why)).FontSize(12.5).TextColor(pal.TextMuted)
			})
		})
		return
	}
	if v != nil {
		if sum := pageSum(page); mv.loaded[key] != sum {
			v.Page().LoadHTML(page, rendered.MapOrigin)
			mv.loaded[key] = sum
		}
	}
	holder.Children(func() {
		if v == nil {
			ui.Box(c).Fill().Background(pal.Ground)
			return
		}
		ui.WebView(c, v).Fill().Radius(7)
	})
}

// newMapView makes the web view of the map keyed key, or nil, keeping
// why, when it cannot be made.
func (a *App) newMapView(key string, full bool, pal *theme.Palette) *mygo.WebView {
	mv := &a.mapViews
	wv, err := a.win.NewWebView(mygo.WebViewOptions{BackgroundColor: hexOf(pal.Ground), ScrollToContainer: !full,
		Page: mygo.PageOptions{TrustedOrigins: []string{strings.TrimSuffix(rendered.MapOrigin, "/")}}})
	if err != nil {
		log.Printf("map %s: %v", key, err)
		mv.failed[key] = err.Error()
		return nil
	}
	p := wv.Page()
	p.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if strings.HasPrefix(e.URL, rendered.MapOrigin) || strings.HasPrefix(e.URL, "about:") {
			return
		}
		e.PreventDefault()
		if e.UserInitiated && (strings.HasPrefix(e.URL, "https://") || strings.HasPrefix(e.URL, "http://")) {
			openLink(e.URL)
		}
	})
	p.SetWindowOpenHandler(func(r mygo.WindowOpenRequest) *mygo.WindowOptions {
		if r.UserInitiated && (strings.HasPrefix(r.URL, "https://") || strings.HasPrefix(r.URL, "http://")) {
			openLink(r.URL)
		}
		return nil
	})
	mv.views[key] = wv
	return wv
}

func pageSum(page string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(page))
	return h.Sum64()
}

func (a *App) letGoMap(key string) {
	mv := &a.mapViews
	if v := mv.views[key]; v != nil {
		v.Destroy()
	}
	delete(mv.views, key)
	delete(mv.loaded, key)
}

// dropMaps lets go of the map views no frame built: of maps scrolled out
// of the page, deleted, or of a page or chat closed.
func (a *App) dropMaps() {
	mv := &a.mapViews
	for key := range mv.views {
		if !mv.built[key] {
			a.letGoMap(key)
		}
	}
	for key := range mv.failed {
		if !mv.built[key] {
			delete(mv.failed, key)
		}
	}
	clear(mv.built)
}

// fullMap fills the page with a map block's map, which zooms and pans
// freely there, until its button or Escape leaves it.
func (a *App) fullMap(c *ui.Context, n *doc.Node) {
	pal := theme.Of(c)
	m := doc.MapOf(n)
	key := fullView(n.ID)
	ui.Column(c.Key(key)).Fill().Padding(0, pagePadX, 16).Children(func() {
		ui.Row(c).Height(40).Gap(2).AlignItems(ui.Center).Children(func() {
			title := m.Title
			if title == "" {
				title = locales.HostSlashMap()
			}
			ui.Text(c, title).FontSize(12.5).TextColor(pal.TextMuted).Grow(1).SingleLine()
			a.openMapButton(c, m, 1)
			if barButton(c, "exit-full", "minimize-2", locales.RenderedExitFullScreen(), 1).Clicked() {
				a.leaveFullMap()
			}
		})
		ui.Box(c).Grow(1).FillWidth().Radius(8).Clip().Children(func() { a.mapView(c, m, key, true) })
		a.mapCaption(c, m)
	})
	c.OnShortcut(0, ui.KeyEscape, a.leaveFullMap)
}

func (a *App) leaveFullMap() {
	ps := a.page
	if ps == nil || ps.fullMap == "" {
		return
	}
	key := fullView(ps.fullMap)
	ps.fullMap = ""
	a.letGoMap(key)
}

// chatMap draws a ```map fence of an agent's answer or a comment as the
// map it holds, once its fence closes; one that holds none shows as the
// code it is.
func (a *App) chatMap(c *ui.Context, b *markdown.CodeBlockView) {
	if b.Open {
		shimmer(c, locales.HostSlashMap(), 13)
		return
	}
	code := b.Code()
	n, ok := doc.MapNodeFromJSON(code)
	if !ok {
		markdown.DefaultCodeBlock(c, b)
		return
	}
	m := doc.MapOf(n)
	key := "chat:" + strconv.FormatUint(pageSum(code), 36)
	a.mapCard(c, m, key, a.panelW, false, func() {
		ui.Row(c).Absolute().Top(8).Left(8).Gap(2).Padding(2).Radius(8).Background(theme.Of(c).Surface).Children(func() {
			a.openMapButton(c, m, 1)
		})
	})
}

func (d *dialogs) openMap(id string, ed *editor.Editor) {
	d.stopMap()
	d.mapOpen, d.mapID, d.mapEditor, d.mapError = true, id, ed, ""
	d.mapTitle, d.mapPlaces, d.mapMode, d.mapWas = "", "", 0, doc.Map{}
	if id == "" {
		return
	}
	n := ed.Content.Find(id)
	if n == nil {
		d.mapOpen = false
		return
	}
	m := doc.MapOf(n)
	d.mapWas, d.mapTitle, d.mapPlaces = m, m.Title, maps.PlacesText(m.Places)
	for i, mode := range mapModes {
		if mode == maps.RouteMode(m) {
			d.mapMode = i
		}
	}
}

// mapModes are the map dialog's ways a route goes, "" for none.
var mapModes = []string{"", doc.RouteWalk, doc.RouteCycle, doc.RouteDrive, doc.RouteTransit}

func (a *App) mapDialog(c *ui.Context) {
	pal := theme.Of(c)
	d := &a.dialogs
	dialog(c, &d.mapOpen, 560, func() {
		dialogHeader(c, locales.DialogMapTitle(), locales.DialogMapSubtitle(), &d.mapOpen)
		ui.Column(c).Padding(0, 20, 20).Gap(12).Children(func() {
			field(c, "title", &d.mapTitle, locales.DialogFieldTitle())
			ui.TextArea(c.Key("places"), &d.mapPlaces).Height(130).FontSize(13).Placeholder(locales.DialogMapPlaces())
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.DialogMapRoute()).FontSize(13).TextColor(pal.TextSecondary)
				ui.Segmented(c.Key("mode"), &d.mapMode, locales.DialogMapNoRoute(), locales.MapModeWalk(), locales.MapModeCycle(),
					locales.MapModeDrive(), locales.MapModeTransit())
			})
			ui.Text(c, locales.DialogMapPrivacy()).FontSize(12).TextColor(pal.TextMuted)
			switch {
			case d.mapBusy:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(13, 13)
					ui.Text(c, locales.DialogMapFinding()).FontSize(12.5).TextColor(pal.TextMuted)
				})
			case d.mapError != "":
				ui.Text(c, d.mapError).FontSize(12.5).TextColor(pal.DangerText)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if button(c, "cancel", "", locales.CommonCancel()).Clicked() {
					d.mapOpen = false
					d.stopMap()
				}
				if primary(c, "save", "", locales.DialogMapSave()).Disabled(d.mapBusy).Clicked() && !d.mapBusy {
					a.saveMap()
				}
			})
		})
	})
}

// saveMap finds the map dialog's places and route, off the main thread,
// then puts the map in the page: in place of the map it edits, or where
// the caret is. A search the dialog let go of, closing or opening again,
// is stopped, and what it finds goes nowhere.
func (a *App) saveMap() {
	d := &a.dialogs
	d.stopMap()
	title, places, mode, was, id, ed := d.mapTitle, d.mapPlaces, mapModes[d.mapMode], d.mapWas, d.mapID, d.mapEditor
	ctx, cancel := context.WithCancel(context.Background())
	run := d.mapRun
	d.mapBusy, d.mapError, d.mapCancel = true, "", cancel
	var made doc.Map
	var err error
	a.offMain(func() { made, err = maps.Build(ctx, title, places, mode, was) }, func() {
		if run != d.mapRun {
			return
		}
		cancel()
		d.mapBusy, d.mapCancel = false, nil
		switch {
		case !d.mapOpen:
			return
		case err != nil:
			d.mapError = mapError(err)
			return
		}
		data := made.Data()
		if id == "" {
			data["author"] = a.session.DisplayName
			ed.InsertBlocks(&doc.Node{ID: doc.NewBlockID(), Type: "map", Data: data})
		} else {
			ed.Edit(func(content *doc.Content) {
				if n := content.Find(id); n != nil {
					if author, ok := n.Data["author"]; ok {
						data["author"] = author
					}
					n.Data = data
				}
			})
		}
		d.mapOpen = false
	})
}

// stopMap lets go of the map dialog's search, if one goes on.
func (d *dialogs) stopMap() {
	d.mapRun++
	if d.mapCancel != nil {
		d.mapCancel()
	}
	d.mapBusy, d.mapCancel = false, nil
}

// mapError is why a map could not be made, for a person to read.
func mapError(err error) string { return maps.Message(locales.In(i18n.Locale()), err) }
