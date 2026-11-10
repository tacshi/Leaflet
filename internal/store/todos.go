package store

import (
	"leaflet/internal/doc"
)

type PageTodo struct {
	doc.Todo
	PageID    string
	PageTitle string
}

func (s *Store) Todos() ([]PageTodo, error) {
	pages, err := s.Pages()
	if err != nil {
		return nil, err
	}
	keys := map[string]int64{}
	rows, err := s.db.Query(`SELECT id, write_id FROM text_writes WHERE kind = 'page'`)
	if err := each(rows, err, func(sc scanner) (bool, error) {
		var id string
		var written int64
		err := sc.Scan(&id, &written)
		keys[id] = written
		return true, err
	}); err != nil {
		return nil, err
	}
	s.todosMu.Lock()
	defer s.todosMu.Unlock()
	cache := make(map[string]pageTodos, len(pages))
	var out []PageTodo
	for _, p := range pages {
		k, ok := keys[p.ID]
		if !ok {
			continue
		}
		have, ok := s.todos[p.ID]
		if !ok || have.key != k {
			have = pageTodos{key: k}
			if c := s.Content(p.ID); c != nil {
				have.todos = c.Todos()
			}
		}
		cache[p.ID] = have
		for _, t := range have.todos {
			out = append(out, PageTodo{Todo: t, PageID: p.ID, PageTitle: p.DisplayTitle()})
		}
	}
	s.todos = cache
	return out, nil
}

type pageTodos struct {
	key   int64
	todos []doc.Todo
}
