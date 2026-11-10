package app

import (
	"log"
	"time"

	"leaflet/internal/store"
)

const viewDelay = 200 * time.Millisecond

const maxViewText = 20_000

func (a *App) viewChanged() {
	if a.viewTimer != nil {
		a.viewTimer.Stop()
	}
	a.viewTimer = a.later(viewDelay, a.publishView)
}

func (a *App) publishView() {
	var v store.OpenView
	if a.page != nil {
		s := a.page.editor.SelectionView()
		text := []rune(store.CleanText(s.Text))
		long := len(text) > maxViewText
		if long {
			text = text[:maxViewText]
		}
		v = store.OpenView{PageID: a.page.id, Selection: store.ViewSelection{
			StartBlock: s.Start.ID, StartOffset: s.Start.Off, EndBlock: s.End.ID, EndOffset: s.End.Off,
			Text: string(text), Truncated: long, Color: s.Color, Highlight: s.Highlight, MixedColors: s.Mixed,
		}}
	}
	if v == a.openView {
		return
	}
	a.openView = v
	var err error
	if v.PageID == "" {
		err = a.st.SetOpenView(nil)
	} else {
		v.At = time.Now()
		err = a.st.SetOpenView(&v)
	}
	if err != nil {
		log.Printf("open view: %v", err)
	}
}

func (a *App) endView() {
	if a.viewTimer != nil {
		a.viewTimer.Stop()
	}
	a.openView = store.OpenView{}
	if err := a.st.SetOpenView(nil); err != nil {
		log.Printf("open view: %v", err)
	}
}
