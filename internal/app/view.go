package app

import (
	"log"
	"time"

	"leaflet/internal/store"
)

// viewDelay is how long the view rests before it is published: a write a
// keystroke would be too many.
const viewDelay = 200 * time.Millisecond

// maxViewText is how much of the selected text is published, in
// characters: selecting all of a long page writes it each time it changes.
const maxViewText = 20_000

// viewChanged publishes the open page and its selection, shortly after
// they last changed.
func (a *App) viewChanged() {
	if a.viewTimer != nil {
		a.viewTimer.Stop()
	}
	a.viewTimer = a.later(viewDelay, a.publishView)
}

// publishView stores the open page and its selection for those outside
// the app (store.OpenView), when they changed.
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

// endView takes the view away as the app ends.
func (a *App) endView() {
	if a.viewTimer != nil {
		a.viewTimer.Stop()
	}
	a.openView = store.OpenView{}
	if err := a.st.SetOpenView(nil); err != nil {
		log.Printf("open view: %v", err)
	}
}
