package mobile

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"

	"leaflet/internal/core"
	"leaflet/internal/store"
	"leaflet/locales"
)

type reminder struct {
	ID       string `json:"id"`
	PageID   string `json:"page_id"`
	BlockID  string `json:"block_id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Body     string `json:"body"`
	At       int64  `json:"at"`
	Due      string `json:"due"`
}

func (c *Core) Reminders(now int64, limit int) (_ string, err error) {
	defer catch(&err)
	out := []reminder{}
	c.q.do(func() {
		st := c.core.Store
		var todos []store.PageTodo
		if todos, err = st.Todos(); err != nil {
			return
		}
		var pages []store.Page
		if pages, err = st.Pages(); err != nil {
			return
		}
		session := st.Session()
		own := make(map[string]bool, len(pages))
		for _, p := range pages {
			own[p.ID] = p.SpaceID == session.SpaceID
		}
		at := time.UnixMilli(now)
		var coming, mine []store.PageTodo
		for _, t := range todos {
			if t.Due.IsZero() || !core.RemindsOf(t, session.UserID, own[t.PageID]) {
				continue
			}
			mine = append(mine, t)
			if core.ReminderAt(t).After(at) {
				coming = append(coming, t)
			}
		}
		due := core.DueReminders(mine, at, func(t store.PageTodo) bool {
			v, ok := st.Value(core.ReminderKey(t.PageID, t.BlockID))
			return ok && v == core.ReminderValue(t)
		})
		slices.SortStableFunc(coming, func(a, b store.PageTodo) int {
			return core.ReminderAt(a).Compare(core.ReminderAt(b))
		})
		for _, t := range slices.Concat(due, coming) {
			if len(out) == limit {
				break
			}
			shown, when := at, int64(0)
			if r := core.ReminderAt(t); r.After(at) {
				shown, when = r, r.UnixMilli()
			}
			out = append(out, reminderOf(t, shown, when))
		}
	})
	if err != nil {
		return "", err
	}
	return encode(out)
}

func reminderOf(t store.PageTodo, at time.Time, when int64) reminder {
	body, page := t.Text, t.PageTitle
	if strings.TrimSpace(body) == "" {
		body = locales.TodosUntitled()
	}
	if strings.TrimSpace(page) == "" {
		page = locales.CommonUntitled()
	}
	return reminder{
		ID: core.ReminderID(t.PageID, t.BlockID), PageID: t.PageID, BlockID: t.BlockID,
		Title: core.ReminderTitle(locales.In(i18n.Locale()), t, at), Subtitle: page, Body: body,
		At: when, Due: core.ReminderValue(t),
	}
}

func (c *Core) RemindedOf(reminders string) (err error) {
	defer catch(&err)
	var list []reminder
	if err := json.Unmarshal([]byte(reminders), &list); err != nil {
		return err
	}
	c.q.do(func() {
		for _, r := range list {
			if err = c.core.Store.SetValue(core.ReminderKey(r.PageID, r.BlockID), r.Due); err != nil {
				return
			}
		}
	})
	return err
}
