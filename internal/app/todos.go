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

// todoRow is a row of the To-dos tab: a day's header, or a to-do.
type todoRow struct {
	day  *todoDay
	open bool
	todo store.PageTodo
}

// todoRows lists the to-dos a day at a time, each day under its header with only its header
// shown while it is folded; open tells whether a day shows its to-dos. To-dos all with no date
// are listed with no header.
func todoRows(todos []store.PageTodo, now time.Time, open func(todoDay) bool) []todoRow {
	days := doc.TodoDays(todos, func(t store.PageTodo) doc.Todo { return t.Todo }, now)
	var out []todoRow
	for i := range days {
		d := &days[i]
		shown := true
		if len(days) > 1 || d.Section != doc.TodoNoDate {
			shown = open(*d)
			out = append(out, todoRow{day: d, open: shown})
		}
		if shown {
			for _, t := range d.Todos {
				out = append(out, todoRow{todo: t})
			}
		}
	}
	return out
}

// todoDayTitle names a day of to-dos, as Today, Tomorrow or Nov 20.
func todoDayTitle(d todoDay, now time.Time) string {
	if d.Day.IsZero() {
		return locales.TodosSectionNoDate()
	}
	return editor.DueLabel(d.Day, false, now)
}

// todoDayOpen tells whether a day shows its to-dos: as it was last left, or else open unless it
// has gone by.
func (a *App) todoDayOpen(d todoDay) bool {
	if open, ok := a.settings.TodoDaysOpen[d.Key()]; ok {
		return open
	}
	return !d.Folded()
}

// foldTodoDay opens or folds a day of to-dos and keeps that for the next launch, forgetting the
// days no longer listed.
func (a *App) foldTodoDay(key string, open bool) {
	kept := map[string]bool{}
	for _, d := range doc.TodoDays(a.todoList(), func(t store.PageTodo) doc.Todo { return t.Todo }, time.Now()) {
		k := d.Key()
		if k == key {
			if open == d.Folded() {
				kept[k] = open
			}
			continue
		}
		if v, ok := a.settings.TodoDaysOpen[k]; ok {
			kept[k] = v
		}
	}
	a.settings.TodoDaysOpen = kept
	a.saveSettings()
}

func (a *App) todoList() []store.PageTodo {
	if a.todosStale || a.todos == nil {
		reread(&a.todos, a.st.Todos)
		if a.todos == nil {
			a.todos = []store.PageTodo{}
		}
		a.todosStale = false
	}
	return a.todos
}

func (a *App) todosList(c *ui.Context) {
	pal := theme.Of(c)
	now := time.Now()
	rows := todoRows(a.todoList(), now, a.todoDayOpen)
	if len(rows) == 0 {
		ui.Column(c).Grow(1).Padding(2, 18).Gap(4).Children(func() {
			ui.Text(c, locales.TodosEmptyTitle()).FontSize(13).FontWeight(600).TextColor(pal.Text)
			ui.Text(c, locales.TodosEmptyBody()).FontSize(13).TextColor(pal.TextMuted).Pretty()
		})
		return
	}
	opaque := opaqueSidebar(c, pal)
	a.todoState.Header = nil
	if opaque {
		a.todoState.Header = func(i int) bool { return rows[i].day != nil }
	}
	a.todoState.Key = func(i int) any {
		if d := rows[i].day; d != nil {
			return "day:" + d.Key()
		}
		return rows[i].todo.PageID + "#" + rows[i].todo.BlockID
	}
	ui.List(c, &a.todoState, len(rows), func(i int) {
		r := rows[i]
		if r.day != nil {
			a.todoDayHeader(c, *r.day, r.open, opaque, now, pal)
			return
		}
		a.todoRowView(c, r.todo, now, pal)
	}).Grow(1).Padding(0, 8, 12)
}

// todoDayHeader is a day's header, with how many to-dos are left on it: clicking it opens or
// folds the day.
func (a *App) todoDayHeader(c *ui.Context, d todoDay, open, opaque bool, now time.Time, pal *theme.Palette) {
	title := todoDayTitle(d, now)
	h := ui.ButtonBase(c.Key("day:"+d.Key())).Label(title).Expanded(open).Padding(6, 8, 4).Gap(6).Radius(6).
		AlignItems(ui.Center).Justify(ui.Start).FillWidth()
	switch {
	case opaque && h.Hovered():
		h.Background(pal.Sidebar.Mix(pal.Text, 0.05))
	case h.Hovered():
		h.Background(pal.Hover)
	case opaque:
		h.Background(pal.Sidebar)
	}
	h.Children(func() {
		turn := float32(0)
		if open {
			turn = 90
		}
		ui.Icon(c, theme.Icon("chevron-right")).FontSize(11).TextColor(pal.TextMuted).Rotate(turn).Shrink(0)
		ui.Text(c, title).FontSize(11.5).FontWeight(650).TextColor(pal.TextMuted).LetterSpacing(0.2).SingleLine().Grow(1).Shrink(1)
		count := ui.Text(c, strconv.Itoa(len(d.Todos))).FontSize(11.5).FontWeight(600).TextColor(pal.TextMuted).Shrink(0)
		if d.Section == doc.TodoOverdue {
			count.TextColor(pal.DangerText)
		}
	})
	key := d.Key()
	h.OnClick(func() { a.foldTodoDay(key, !open) })
}

// todoRowView is a to-do under its day, with its time of day if it has one.
func (a *App) todoRowView(c *ui.Context, t store.PageTodo, now time.Time, pal *theme.Palette) {
	row := ui.ButtonBase(c.Key("todo:"+t.PageID+"#"+t.BlockID)).Padding(5, 8).Gap(8).Radius(6).AlignItems(ui.Start).Justify(ui.Start).FillWidth()
	if row.Hovered() {
		row.Background(pal.Hover)
	}
	row.Children(func() {
		done := false
		if ui.Checkbox(c, &done, "").Label(locales.CommonDone()).Margin(1, 0, 0).Disabled(!a.canEdit(t.PageID)).Changed() {
			a.finishTodo(t)
		}
		ui.Column(c).Grow(1).Shrink(1).Gap(1).Children(func() {
			text := t.Text
			if strings.TrimSpace(text) == "" {
				text = locales.TodosUntitled()
			}
			ui.Text(c, text).FontSize(13.5).TextColor(pal.Text).SingleLine().Tooltip(text)
			sub := t.PageTitle
			if t.Owner != "" {
				sub = locales.TodosPageAndOwner(sub, t.Owner)
			}
			ui.Text(c, sub).FontSize(11.5).TextColor(pal.TextMuted).SingleLine()
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
	a.toastAction(locales.TodosFinished(t.Text), locales.CommonUndo(), func() {
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
			Title: core.ReminderTitle(locales.In(i18n.Locale()), t, now), Subtitle: t.PageTitle, Body: t.Text, Group: "todos"})
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
