package app

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/mcp"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// agentWatch is the small window in a corner of the screen that shows the
// page an agent outside the app is changing, as it changes it, while the app
// runs, in front or not: the part of the page it is changing, with a few
// lines before and after, where the blocks it changes light up and those it
// adds come in one by one.
type agentWatch struct {
	// seen is when each agent's last change taken in was, by key, so that
	// only what comes after shows; nil until the app first reads them.
	seen map[string]int64
	win  *mygo.Window
	// shown is whether the window shows, or would without one, as in tests.
	shown bool
	idle  *time.Timer

	act    mcp.Activity
	at     time.Time    // when act came in
	target *doc.Content // the page as act left it
	ed     *editor.Editor
	tinted map[string]bool
	// lit is when each tinted block was lit, zero until it is drawn.
	lit map[string]time.Time
	// pending is the blocks the agent added that don't show yet, in the
	// page's order, the first at next, each after it step later.
	pending []string
	next    time.Time
	step    time.Duration
	// follow is the block to show, as it shows; jump is whether to go there
	// at once, as for a page the window takes up, which shows no page until.
	// The editor guesses where a block it has not laid out is: settle is how
	// many frames more to go on placing it, as it lays it out, and aim where
	// it was last placed.
	follow     string
	followWait int
	jump       bool
	settle     int
	aim        float32
	scroll     ui.ScrollState
	view       ui.Handle
	hovered    bool
	// hushed is whether the user closed the window, which then stays away
	// until the agents pause for watchHush.
	hushed bool
}

const (
	watchStale    = 30 * time.Second // an activity older than this as it is read doesn't show
	watchBusy     = 3 * time.Second  // after an edit, the agent shows as editing this long
	watchIdle     = 12 * time.Second // after an edit, the window goes this long after, unless the pointer is on it
	watchHush     = 90 * time.Second // closed, the window shows again after the agents pause this long
	watchLit      = 2 * time.Second  // how long a changed block stays lit
	watchReveal   = 1500 * time.Millisecond
	watchStepMax  = 150 * time.Millisecond
	watchW        = 360
	watchH        = 300
	watchGap      = 16
	watchTextSize = 13
	watchPadX     = 16
)

// appInFront is whether the app's window is the one the user works in.
var appInFront = func(a *App) bool {
	return a.win != nil && mygo.App.IsActive() && a.win.IsVisible() && a.win.IsFocused()
}

// startWatch takes in the agents' last changes as the app starts, which
// were before it and don't show.
func (a *App) startWatch() {
	a.watch.seen = map[string]int64{}
	a.newAgentEdits()
}

// agentsEdited shows what agents outside the app changed since the app last
// looked, as another process wrote to the database.
func (a *App) agentsEdited() {
	if a.watch.seen == nil {
		a.startWatch()
		return
	}
	for _, act := range a.newAgentEdits() {
		a.agentEdited(act)
	}
}

// newAgentEdits is the agents' changes since those taken in, the oldest
// first, but those from too long ago to show.
func (a *App) newAgentEdits() []mcp.Activity {
	w := &a.watch
	var fresh []mcp.Activity
	now := time.Now()
	for key, raw := range mcp.Activities(a.st) {
		last := w.seen[key]
		if time.UnixMilli(last).Sub(now) > watchStale {
			last = 0 // taken in before the clock went back
		}
		for _, act := range mcp.ParseActivities(raw) {
			if act.At <= last {
				continue
			}
			last, w.seen[key] = act.At, act.At
			if now.Sub(time.UnixMilli(act.At)).Abs() < watchStale {
				fresh = append(fresh, act)
			}
		}
	}
	slices.SortStableFunc(fresh, func(x, y mcp.Activity) int { return cmp.Compare(x.At, y.At) })
	return fresh
}

// agentEdited shows an agent's edit: it lights up the blocks it changed in
// the open page, and shows the page in the corner window unless the page is
// in front already.
func (a *App) agentEdited(act mcp.Activity) {
	open := a.page != nil && a.page.id == act.PageID
	if open {
		a.lightBlocks(slices.Concat(act.Added, act.Changed))
	}
	w := &a.watch
	if !a.settings.LiveAgentEdits || a.pageByID(act.PageID) == nil {
		return
	}
	if open && appInFront(a) {
		if w.act.PageID == act.PageID {
			a.hideWatch()
		}
		return
	}
	c := a.st.Content(act.PageID)
	if c == nil {
		return
	}
	now := time.Now()
	if w.hushed && now.Sub(w.at) < watchHush {
		w.at = now
		return
	}
	w.hushed = false
	w.take(act, c, now, watchHost{a})
	a.showWatch()
}

// lightBlocks lights the blocks of the open page up for a moment, as a block
// gone to does.
func (a *App) lightBlocks(ids []string) {
	ps := a.page
	if len(ids) == 0 {
		return
	}
	if ps.lit == nil {
		ps.lit = map[string]time.Time{}
	}
	for _, id := range ids {
		ps.tinted[id], ps.lit[id] = true, time.Time{}
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// take shows the page as act left it: what it changed lit, and what it added
// to come in one by one, after those of an edit before still to come.
func (w *agentWatch) take(act mcp.Activity, c *doc.Content, now time.Time, host editor.Host) {
	same := w.shown && w.ed != nil && w.act.PageID == act.PageID
	wait := map[string]bool{}
	if same {
		for _, id := range w.pending {
			wait[id] = true
		}
	}
	for _, id := range act.Added {
		wait[id] = true
	}
	var pending []string
	c.Walk(func(n, _ *doc.Node) {
		if wait[n.ID] {
			pending = append(pending, n.ID)
		}
	})
	if same {
		w.ed.Replace(without(c, pending))
	} else {
		w.tinted, w.lit = map[string]bool{}, map[string]time.Time{}
		w.ed = editor.New(without(c, pending), host)
		w.ed.ReadOnly = true
		w.ed.Tinted = w.tinted
		w.scroll, w.follow, w.jump = ui.ScrollState{}, "", true
	}
	for _, id := range act.Changed {
		w.tinted[id], w.lit[id] = true, time.Time{}
	}
	if len(w.pending) == 0 || !same || w.next.Before(now) {
		w.next = now
	}
	if n := len(pending); n > 0 {
		w.step = min(watchReveal/time.Duration(n), watchStepMax)
	}
	switch {
	case len(act.Changed) > 0:
		w.followTo(act.Changed[0])
	case len(act.Added) == 0 && act.Near != "":
		w.followTo(act.Near)
	}
	w.pending, w.act, w.target, w.at = pending, act, c, now
}

// without is c without the blocks hidden names, each with what is in it.
func without(c *doc.Content, hidden []string) *doc.Content {
	out := c.Copy()
	for _, id := range hidden {
		out.Remove(id)
	}
	return out
}

// advance shows the added blocks whose time has come, and puts out the lights
// that were on long enough, and returns how soon it has more to do, or zero.
func (w *agentWatch) advance(now time.Time) time.Duration {
	var wake time.Duration
	soon := func(d time.Duration) {
		if d > 0 && (wake == 0 || d < wake) {
			wake = d
		}
	}
	shown := 0
	for shown < len(w.pending) && !now.Before(w.next) {
		id := w.pending[shown]
		w.tinted[id], w.lit[id] = true, now
		w.followTo(id)
		w.next = w.next.Add(w.step)
		shown++
	}
	if shown > 0 {
		w.pending = w.pending[shown:]
		w.ed.Replace(without(w.target, w.pending))
	}
	if len(w.pending) > 0 {
		soon(max(w.next.Sub(now), time.Millisecond))
	}
	for id, at := range w.lit {
		if at.IsZero() {
			w.lit[id], at = now, now
		}
		left := watchLit - now.Sub(at)
		if left <= 0 {
			delete(w.lit, id)
			delete(w.tinted, id)
			continue
		}
		soon(left)
	}
	return wake
}

// left is how long the added blocks still to come take to show.
func (w *agentWatch) left() time.Duration {
	return time.Duration(len(w.pending)) * w.step
}

// firstEdit is the first block of the page the last edit added or changed,
// or where it took blocks out.
func (w *agentWatch) firstEdit() string {
	edited := map[string]bool{}
	for _, id := range slices.Concat(w.act.Added, w.act.Changed) {
		edited[id] = true
	}
	first := ""
	if w.target != nil {
		w.target.Walk(func(n, _ *doc.Node) {
			if first == "" && edited[n.ID] {
				first = n.ID
			}
		})
	}
	if first == "" {
		return w.act.Near
	}
	return first
}

func (a *App) showWatch() {
	w := &a.watch
	w.shown = true
	if a.headless() || a.quitting {
		return
	}
	a.watchIdleIn(watchIdle + w.left())
	if w.win == nil || w.win.IsDestroyed() {
		w.win = a.newWatchWindow()
	}
	if !w.win.IsVisible() {
		w.win.ShowInactive()
	}
	w.win.Invalidate()
}

func (a *App) newWatchWindow() *mygo.Window {
	wa := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint()).WorkArea
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:           locales.ExternalLiveEditsTitle(),
		Panel:           true,
		Hidden:          true,
		Frameless:       true,
		CornerRadius:    12,
		Width:           watchW,
		Height:          watchH,
		MinWidth:        280,
		MinHeight:       200,
		X:               wa.X + wa.Width - watchW - watchGap,
		Y:               wa.Y + wa.Height - watchH - watchGap,
		BackgroundColor: "light-dark(#ffffff, #1e1e1e)",
		StateKey:        "live-edits",
		Content:         ui.View(a.watchView),
	})
	win.OnClose(func(e *mygo.CloseEvent) {
		if a.quitting {
			return
		}
		// Escape, or the system's close, closes it as its close button does.
		e.PreventDefault()
		a.update(a.hushWatch)
	})
	return win
}

// watchIdleIn hides the window in d, unless the pointer is on it or an agent
// edited again since.
func (a *App) watchIdleIn(d time.Duration) {
	w := &a.watch
	if w.idle != nil {
		w.idle.Stop()
	}
	w.idle = time.AfterFunc(d, func() { a.update(a.watchIdled) })
}

func (a *App) watchIdled() {
	w := &a.watch
	if !w.shown {
		return
	}
	if left := watchIdle + w.left() - time.Since(w.at); left > 0 || w.hovered {
		a.watchIdleIn(max(left, 2*time.Second))
		return
	}
	a.hideWatch()
}

func (a *App) hideWatch() {
	w := &a.watch
	w.shown = false
	w.pending = nil
	if w.idle != nil {
		w.idle.Stop()
		w.idle = nil
	}
	if w.win != nil && !w.win.IsDestroyed() {
		w.win.Hide()
	}
}

// resetWatch hides the corner window and drops what it held, as when leaving
// or switching accounts.
func (a *App) resetWatch() {
	a.hideWatch()
	w := &a.watch
	w.act, w.at, w.target, w.ed = mcp.Activity{}, time.Time{}, nil, nil
	w.tinted, w.lit, w.pending = nil, nil, nil
	w.follow, w.jump, w.hushed = "", false, false
	w.seen = map[string]int64{}
}

// hushWatch hides the window as the user closed it: it stays away until the
// agents pause.
func (a *App) hushWatch() {
	a.watch.hushed, a.watch.at = true, time.Now()
	a.hideWatch()
}

// closeWatch closes the window for good, as the app's window closes, so the
// app quits as it would without it.
func (a *App) closeWatch() {
	w := &a.watch
	a.hideWatch()
	if w.win != nil {
		win := w.win
		w.win = nil
		win.Destroy()
	}
}

// watchFront hides the window as the app's window comes in front with the
// page it shows.
func (a *App) watchFront() {
	if a.watch.shown && a.page != nil && a.page.id == a.watch.act.PageID {
		a.hideWatch()
	}
}

// openWatched opens the page in the app's window, at its first edit.
func (a *App) openWatched() {
	w := &a.watch
	id, block := w.act.PageID, w.firstEdit()
	a.hideWatch()
	a.showWindow()
	a.openLink(id, block, -1)
}

func (a *App) redrawWatch() {
	if w := a.watch.win; w != nil {
		w.Invalidate()
	}
}

func (a *App) watchView(c *ui.Context) {
	theme.Apply(c)
	pal := theme.Of(c)
	w := &a.watch
	root := ui.Column(c.Key("live-edits")).Fill().Background(pal.Surface)
	if w.ed == nil {
		return
	}
	w.hovered = root.Hovered()
	now := c.Now()
	if d := w.advance(now); d > 0 {
		c.After(d)
	}
	busy := len(w.pending) > 0 || now.Sub(w.at) < watchBusy
	if busy && len(w.pending) == 0 {
		c.After(watchBusy - now.Sub(w.at))
	}
	title := a.pageTitleOf(w.act.PageID)
	width, _ := c.Size()
	root.Children(func() {
		ui.Row(c.Key("head")).FillWidth().Padding(10, 8, 10, 14).Gap(10).AlignItems(ui.Center).DragWindow().Children(func() {
			avatar(c, w.act.Agent, 26, true)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				if busy {
					shimmer(c, locales.ExternalLiveEditsEditing(w.act.Agent), 12)
				} else {
					ui.Text(c, locales.ExternalLiveEditsEdited(w.act.Agent)).FontSize(12).TextColor(pal.TextMuted).SingleLine()
				}
				ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(pal.Text).SingleLine()
			})
			if iconButton(c, "open", "external-link", locales.ExternalLiveEditsOpen()).Clicked() {
				a.update(a.openWatched)
			}
			if iconButton(c, "close", "x", locales.CommonClose()).Clicked() {
				a.update(a.hushWatch)
			}
		})
		ui.Box(c).FillWidth().Height(1).Background(pal.Divider)
		w.followEdit(c)
		// The part being changed, without a scroll bar, its edges fading
		// into what is above and below.
		body := ui.Box(c.Key("body")).Grow(1).FillWidth().Clip()
		jumping := w.jump
		body.DrawOver(func(p *ui.Painter, r ui.Rect) {
			if jumping {
				// Covered rather than hidden: the editor places its blocks
				// where it last drew them.
				p.Fill(r, pal.Surface, 0)
				return
			}
			const fade = 20
			if w.scroll.Y > 1 {
				p.FillGradient(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: fade}, ui.LinearGradient{From: pal.Surface, To: pal.Surface.Alpha(0), Angle: 180}, 0)
			}
			if w.scroll.Y < w.scroll.MaxY-1 {
				p.FillGradient(ui.Rect{X: r.X, Y: r.Y + r.H - fade, W: r.W, H: fade}, ui.LinearGradient{From: pal.Surface.Alpha(0), To: pal.Surface, Angle: 180}, 0)
			}
		})
		body.Children(func() {
			// Room after the page, for its last block to show where any other
			// would, with what comes before it.
			tail := max(w.view.Bounds(c).H*(1-watchAt), 28)
			ui.Scroll(c.Key("body:" + w.act.PageID)).TrackScroll(&w.scroll).Scrollbars(false).Fill().Bind(&w.view).Children(func() {
				ui.Column(c).FillWidth().Padding(12, watchPadX, tail).Children(func() {
					w.ed.SetWidth(max(width-2*watchPadX, 160))
					w.ed.Size = watchTextSize
					w.ed.View(c)
				})
			})
		})
		ui.Row(c.Key("foot")).FillWidth().Padding(8, 14).BorderWidth(1, 0, 0, 0).BorderColor(pal.Divider).Children(func() {
			ui.Text(c, editSummary(w.act, title)).FontSize(12).TextColor(pal.TextMuted).SingleLine()
		})
	})
}

// watchAt is how far down the window's view the block being changed shows,
// with the lines before it above and those after it below.
const watchAt = 0.4

const watchSettle = 4

func (w *agentWatch) followTo(id string) {
	w.follow, w.followWait, w.settle = id, 0, watchSettle
}

func (w *agentWatch) reveal(id string) {
	if w.ed == nil || id == "" {
		return
	}
	for loc := w.ed.Content.Locate(id); loc != nil && loc.Parent != nil; loc = w.ed.Content.Locate(loc.Parent.ID) {
		if p := loc.Parent; p.Type == "toggle_list" && p.Bool("collapsed") {
			w.ed.ToggleCollapsed(p.ID)
		}
	}
	w.ed.Reveal(id)
}

// followEdit scrolls the block to follow to watchAt of the view: at once for
// a page the window has just taken up, smoothly after, and again as the
// editor lays it out and knows better where it is.
func (w *agentWatch) followEdit(c *ui.Context) {
	if w.follow == "" {
		if len(w.pending) == 0 {
			w.jump = false
		}
		return
	}
	w.reveal(w.follow)
	top, ok := w.ed.BlockTop(w.follow)
	view := w.view.Bounds(c)
	if !ok || view.H == 0 {
		if w.followWait++; w.followWait > 30 {
			w.follow, w.jump = "", false
		} else {
			c.After(16 * time.Millisecond)
		}
		return
	}
	y := max(w.scroll.Y+top-view.Y-view.H*watchAt, 0)
	switch {
	case w.jump:
		w.scroll.Y = y
	case w.settle == watchSettle || math.Abs(float64(y-w.aim)) > 2:
		w.scroll.ScrollTo(0, y, ui.Smooth)
	}
	w.aim = y
	if !w.jump && w.scroll.Scrolling() {
		return // the glide draws the frames that follow it
	}
	if w.settle--; w.settle > 0 {
		c.After(16 * time.Millisecond)
		return
	}
	w.follow, w.jump = "", false
}

// editSummary says what an agent's edit did, as an in-app agent's edits say.
func editSummary(act mcp.Activity, title string) string {
	n := len(act.Added) + len(act.Changed)
	switch {
	case act.Tool == "create_page":
		return locales.AgentEditCreatedPage(title)
	case act.Tool == "move_block":
		return locales.AgentEditMoved()
	case act.Tool == "delete_blocks" || n == 0 && act.Removed > 0:
		return locales.AgentEditDeleted(max(act.Removed, 1))
	case act.Tool == "replace_block":
		return locales.AgentEditRewrote(locales.AgentEditBlocks(max(n, 1)))
	case act.Tool == "write_markdown":
		return locales.AgentEditWrote(locales.AgentEditBlocks(max(n, 1)))
	}
	return locales.AgentEditInserted(locales.AgentEditBlocks(max(n, 1)))
}

// watchHost shows a page in the corner window, read-only: what plays or runs
// in a web view in the app's window, as a video or an embed, shows as a card
// naming it, and a link opens in the app's window.
type watchHost struct{ a *App }

func (h watchHost) PageTitle(id string) string { return h.a.pageTitleOf(id) }

func (h watchHost) OpenPage(id string) {
	h.a.update(func() {
		h.a.showWindow()
		h.a.openPage(id)
	})
}

func (h watchHost) Image(c *ui.Context, n *doc.Node) (ui.ImageSource, *ui.VideoPlayer, bool) {
	pic, _, failed := h.a.blockImage(c, n, h.a.redrawWatch)
	return pic, nil, failed
}

func (h watchHost) Embed(c *ui.Context, n *doc.Node, _ float32) {
	_, source := doc.BlockSource(n)
	switch n.Type {
	case "math_equation":
		blockCard(c, n, "sigma", locales.HostSlashMath(), source)
	case "diagram":
		blockCard(c, n, "workflow", locales.HostSlashDiagram(), source)
	default:
		blockCard(c, n, "code", cmp.Or(n.Str("title"), locales.HostSlashEmbed()), "")
	}
}

func (h watchHost) Video(c *ui.Context, n *doc.Node, _ float32) {
	blockCard(c, n, "play", cmp.Or(n.Str("title"), locales.HostSlashVideo()), n.Str("url"))
}

func (h watchHost) Card(c *ui.Context, n *doc.Node) {
	h.a.linkPreviewBlockFor(c, n, h.a.watch.act.PageID, h.a.redrawWatch)
}

func (h watchHost) File(c *ui.Context, n *doc.Node, _ float32) {
	blockCard(c, n, "file", cmp.Or(n.Str("name"), locales.HostSlashFile()), "")
}

func (h watchHost) Recording(c *ui.Context, n *doc.Node) {
	blockCard(c, n, "mic", cmp.Or(n.Str("title"), locales.CommonRecording()), "")
}

func (watchHost) CommentCount(string) int                    { return 0 }
func (watchHost) OpenComments(string)                        {}
func (watchHost) CommentPreview(*ui.Context, string)         {}
func (watchHost) SlashItems(string) []editor.MenuItem        { return nil }
func (watchHost) MentionItems(string) []editor.MenuItem      { return nil }
func (watchHost) Pick(editor.Trigger, editor.MenuItem)       {}
func (watchHost) Comment(string, string)                     {}
func (watchHost) AskAgent(string, string)                    {}
func (watchHost) EditBlock(string)                           {}
func (watchHost) VideoMenu(*ui.Menu, *doc.Node)              {}
func (watchHost) FileMenu(*ui.Context, *ui.Menu, *doc.Node)  {}
func (watchHost) FollowVideoLink(string, func(video string)) {}
func (h watchHost) BlockLink(blockID string) string {
	return doc.BlockLink(h.a.watch.act.PageID, blockID)
}
func (h watchHost) OpenBlock(pageID, blockID string, at int) {
	h.a.update(func() {
		h.a.showWindow()
		h.a.openLink(pageID, blockID, at)
	})
}

// blockCard is a block the corner window names rather than shows: its kind
// or title, and the first line of what it holds.
func blockCard(c *ui.Context, n *doc.Node, icon, name, detail string) {
	pal := theme.Of(c)
	detail, _, _ = strings.Cut(strings.TrimSpace(detail), "\n")
	ui.Row(c.Key("card:"+n.ID)).FillWidth().Padding(10, 12).Gap(10).Radius(8).Border(1, pal.Divider).Background(pal.Ground).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, theme.Icon(icon)).FontSize(15).TextColor(pal.TextMuted)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, name).FontSize(13).FontWeight(600).TextColor(pal.Text).SingleLine()
			if detail != "" {
				ui.Text(c, detail).FontSize(12).TextColor(pal.TextMuted).SingleLine()
			}
		})
	})
}
