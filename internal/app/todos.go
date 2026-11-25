package app

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/core"
	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

type todoDay = doc.TodoDay[store.PageTodo]

// todoDays lists the to-dos of every page a day at a time.
func todoDays(todos []store.PageTodo, now time.Time) []todoDay {
	return doc.TodoDays(todos, func(t store.PageTodo) doc.Todo { return t.Todo }, now)
}

// todoDayCache is the to-dos a day at a time as last made, and the date they were made on, as
// 2026-11-20: empty until they are made, and again once the to-dos change.
type todoDayCache struct {
	on   string
	days []todoDay
}

// todoDayTitle names a day of to-dos, as Today, Tomorrow or Nov 20.
func todoDayTitle(d todoDay, now time.Time) string {
	if d.Day.IsZero() {
		return locales.TodosSectionNoDate()
	}
	return editor.DueLabel(d.Day, false, now)
}

func (a *App) todoList() []store.PageTodo {
	if a.todosStale || a.todos == nil {
		reread(&a.todos, a.st.Todos)
		if a.todos == nil {
			a.todos = []store.PageTodo{}
		}
		a.todosStale = false
		a.dayCache = todoDayCache{}
	}
	return a.todos
}

// todoDayList is the to-dos of every page a day at a time, made again only once the to-dos
// change or the day turns, so the To-dos tab, a day's view and the window's title share them.
func (a *App) todoDayList(now time.Time) []todoDay {
	todos := a.todoList()
	if on := now.Format(time.DateOnly); a.dayCache.on != on {
		a.dayCache = todoDayCache{on: on, days: todoDays(todos, now)}
	}
	return a.dayCache.days
}

// todoDay is the day key names, with the to-dos left on it, if any.
func (a *App) todoDay(key string, now time.Time) todoDay {
	return doc.TodoDayAt(a.todoDayList(now), key, now)
}

// openDay shows the to-dos due on the day key names, from every page.
func (a *App) openDay(key string) {
	a.closePreview()
	if a.route.Kind == "day" && a.route.ID == key {
		a.closeSettings()
		return
	}
	a.navigate(Route{Kind: "day", ID: key})
}

// todosList is the To-dos tab: the days with to-dos left on them, with how many, a click on one
// showing its to-dos.
func (a *App) todosList(c *ui.Context) {
	pal := theme.Of(c)
	now := time.Now()
	days := a.todoDayList(now)
	if len(days) == 0 {
		ui.Column(c).Grow(1).Padding(2, 18).Gap(4).Children(func() {
			ui.Text(c, locales.TodosEmptyTitle()).FontSize(13).FontWeight(600).TextColor(pal.Text)
			ui.Text(c, locales.TodosEmptyBody()).FontSize(13).TextColor(pal.TextMuted).Pretty()
		})
		return
	}
	if a.todoIn.follows(a.route.Kind + ":" + a.route.ID) {
		a.todoRow = -1
		for i, d := range days {
			if a.route.Kind == "day" && d.Key() == a.route.ID {
				a.todoRow = i
			}
		}
	}
	a.todoState.Selected = &a.todoRow
	a.todoState.Key = func(i int) any { return days[i].Key() }
	a.todoState.Label = func(i int) string { return todoDayTitle(days[i], now) }
	list := ui.List(c, &a.todoState, len(days), func(i int) {
		a.todoDayItem(c, days[i], i == a.todoRow, now, pal)
	}).Grow(1).Padding(0, 8, 12)
	a.todoIn.after(list)
	if (list.Changed() || list.Submitted()) && a.todoRow >= 0 && a.todoRow < len(days) {
		a.openDay(days[a.todoRow].Key())
	}
}

// todoDayItem is a day in the To-dos tab, with how many to-dos are left on it, in red for a day
// gone by.
func (a *App) todoDayItem(c *ui.Context, d todoDay, chosen bool, now time.Time, pal *theme.Palette) {
	title := todoDayTitle(d, now)
	ui.Row(c.Key("day:" + d.Key())).Grow(1).Height(26).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, theme.Icon("calendar")).FontSize(15)
		ui.Text(c, title).FontSize(13.5).Grow(1).SingleLine().Tooltip(title)
		count := ui.Text(c, strconv.Itoa(len(d.Todos))).FontSize(12).FontWeight(600).Shrink(0).
			Label(locales.TodosDayLeft(len(d.Todos)))
		switch {
		case chosen:
		case d.Section == doc.TodoOverdue:
			count.TextColor(pal.DangerText)
		default:
			count.TextColor(pal.TextMuted)
		}
	})
}

// dayView is a day's to-dos, from every page, under their pages: a click on a page opens it, and
// one on a to-do opens its page at it.
func (a *App) dayView(c *ui.Context) {
	pal := theme.Of(c)
	now := time.Now()
	key := a.route.ID
	d := a.todoDay(key, now)
	title := todoDayTitle(d, now)
	a.titleBar(c, func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Margin(0, 0, 0, 6).Shrink(1).ClipX().Children(func() {
			ui.Icon(c, theme.Icon("calendar")).FontSize(14).TextColor(pal.TextMuted).Shrink(0)
			ui.Text(c, title).FontSize(13).TextColor(pal.Text).SingleLine().Shrink(1)
		})
		ui.Spacer(c)
		a.syncStatus(c)
		if a.rec.Active() {
			a.recordingPill(c)
		}
		tools(c, func() { a.panelToggle(c) })
	})
	size := float32(a.settings.TextSize)
	ui.Scroll(c.Key("day-scroll:" + key)).Fill().Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(40, pagePadX, 160).Children(func() {
			ui.Column(c).Width(a.contentWidth(c)).Children(func() {
				ui.Text(c, title).Font(theme.PageFamily(title)).FontSize(size*2.15).FontWeight(700).TextColor(pal.Text).Margin(0, 0, 6)
				ui.Row(c).Gap(12).AlignItems(ui.Center).Margin(0, 0, 18).Children(func() {
					if !d.Day.IsZero() {
						ui.Text(c, locales.TodosDayDate(d.Day)).FontSize(13).TextColor(pal.TextMuted)
					}
					if len(d.Todos) > 0 {
						left := ui.Text(c, locales.TodosDayLeft(len(d.Todos))).FontSize(13).TextColor(pal.TextMuted)
						if d.Section == doc.TodoOverdue {
							left.TextColor(pal.DangerText)
						}
					}
				})
				if len(d.Todos) == 0 {
					ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(48, 0).Gap(10).Children(func() {
						ui.Icon(c, theme.Icon("square-check")).FontSize(32).TextColor(pal.ChartMuted)
						ui.Text(c, locales.TodosDayDoneTitle()).FontSize(16).FontWeight(650).TextColor(pal.Text)
						ui.Text(c, locales.TodosDayDoneBody()).TextColor(pal.TextMuted).Balance()
					})
					return
				}
				for _, todos := range d.ByPage(func(t store.PageTodo) string { return t.PageID }) {
					a.dayPageHeader(c, todos[0].PageID, todos[0].PageTitle, size, pal)
					for _, t := range todos {
						a.dayTodoRow(c, t, size, now, pal)
					}
				}
			})
		})
	})
}

// dayPageHeader names a page over its to-dos on a day: a click on it opens the page.
func (a *App) dayPageHeader(c *ui.Context, id, title string, size float32, pal *theme.Palette) {
	if strings.TrimSpace(title) == "" {
		title = locales.CommonUntitled()
	}
	h := ui.ButtonBase(c.Key("day-page:"+id)).Label(title).Tooltip(title).Padding(4, 6).Margin(14, 0, 2).Gap(6).Radius(6).
		AlignItems(ui.Center).Justify(ui.Start).TextColor(pal.TextMuted)
	if h.Hovered() {
		h.Background(pal.Hover).TextColor(pal.Text)
	}
	h.Children(func() {
		ui.Icon(c, theme.Icon("file-text")).FontSize(14).Shrink(0)
		ui.Text(c, title).FontSize(size * 0.8).FontWeight(650).SingleLine().Shrink(1)
	})
	h.OnClick(func() { a.openPage(id) })
}

// dayTodoRow is a to-do on its day's page of to-dos, with its owner and its time of day if it
// has them.
func (a *App) dayTodoRow(c *ui.Context, t store.PageTodo, size float32, now time.Time, pal *theme.Palette) {
	row := ui.ButtonBase(c.Key("day-todo:"+t.PageID+"#"+t.BlockID)).Padding(6, 6).Gap(10).Radius(6).AlignItems(ui.Start).Justify(ui.Start).FillWidth()
	if row.Hovered() {
		row.Background(pal.Hover)
	}
	row.Children(func() {
		done := false
		if ui.Checkbox(c, &done, "").Label(locales.CommonDone()).Margin(size*0.15, 0, 0).Disabled(!a.canEdit(t.PageID)).Changed() {
			a.finishTodo(t)
		}
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, todoText(t)).FontSize(size).TextColor(pal.Text)
			if t.Owner != "" {
				ui.Text(c, t.Owner).FontSize(12).TextColor(pal.TextMuted).SingleLine()
			}
		})
		if t.Timed && !t.Due.IsZero() {
			due := ui.Badge(c, locales.TodosDueTime(t.Due)).FontSize(11).Shrink(0)
			if editor.Overdue(t.Due, t.Timed, now) {
				due.Background(pal.DangerBg).TextColor(pal.DangerText)
			}
		}
	})
	if row.Clicked() {
		a.openBlock(t.PageID, t.BlockID)
	}
}

// todoText is a to-do's text as the app shows it, an empty one's saying so.
func todoText(t store.PageTodo) string {
	if strings.TrimSpace(t.Text) == "" {
		return locales.TodosUntitled()
	}
	return t.Text
}

func (a *App) finishTodo(t store.PageTodo) {
	if !a.canEdit(t.PageID) {
		return
	}
	set := func(checked bool) {
		if ed := a.openEditor(t.PageID); ed != nil {
			if n := ed.Content.Find(t.BlockID); n != nil && n.Bool("checked") != checked {
				ed.ToggleChecked(t.BlockID)
				a.savePage()
			}
			return
		}
		err := a.pageCore().EditPage(t.PageID, func(c *doc.Content) bool {
			n := c.Find(t.BlockID)
			if n == nil {
				return false
			}
			n.Set("checked", checked)
			return true
		})
		if err != nil {
			a.toast(locales.SidebarCouldNotSave(err.Error()))
		}
	}
	set(true)
	a.todosStale = true
	a.toastAction(locales.TodosFinished(todoText(t)), locales.CommonUndo(), func() {
		set(false)
		a.todosStale = true
	})
}

func (a *App) remindsOf(t store.PageTodo) bool {
	return core.RemindsOf(t, a.session.UserID, a.ownsPage(t.PageID))
}

func (a *App) reminders(now time.Time) []store.PageTodo {
	var todos []store.PageTodo
	for _, t := range a.todoList() {
		if a.remindsOf(t) {
			todos = append(todos, t)
		}
	}
	return core.DueReminders(todos, now, func(t store.PageTodo) bool {
		v, ok := a.st.Value(core.ReminderKey(t.PageID, t.BlockID))
		return ok && v == core.ReminderValue(t)
	})
}

func (a *App) remind() {
	if !mygo.NotificationsSupported() {
		return
	}
	now := time.Now()
	for _, t := range a.reminders(now) {
		n := mygo.NewNotification(mygo.NotificationOptions{ID: core.ReminderID(t.PageID, t.BlockID),
			Title: core.ReminderTitle(locales.In(i18n.Locale()), t, now), Subtitle: t.PageTitle, Body: todoText(t), Group: "todos"})
		if err := n.Show(); err == nil {
			_ = a.st.SetValue(core.ReminderKey(t.PageID, t.BlockID), core.ReminderValue(t))
		}
	}
}

func (a *App) notificationClicked(id string) {
	target, ok := strings.CutPrefix(id, "todo:")
	if !ok {
		return
	}
	page, block, _ := strings.Cut(target, "#")
	open := func() {
		a.showWindow()
		a.openBlock(page, block)
	}
	a.winMu.Lock()
	if a.win == nil {
		a.early = append(a.early, open)
		a.winMu.Unlock()
		return
	}
	a.winMu.Unlock()
	a.update(open)
}

func (a *App) startReminders() {
	var tick func()
	tick = func() {
		a.update(a.remind)
		time.AfterFunc(time.Minute, tick)
	}
	time.AfterFunc(5*time.Second, tick)
}
