package starters

import (
	"leaflet/internal/doc"
	"leaflet/locales"
)

type Starter struct {
	Key, Icon   string
	Title, Hint func() string
	Blocks      func() []map[string]any
}

func All() []Starter {
	heading := func(s string) map[string]any { return map[string]any{"type": "heading", "level": 2.0, "text": s} }
	empty := func(typ string) map[string]any { return map[string]any{"type": typ, "text": ""} }
	return []Starter{
		{"meeting", "calendar", locales.TemplateStarterMeeting, locales.TemplateStarterMeetingHint, func() []map[string]any {
			return []map[string]any{
				heading(locales.TemplateStarterAgenda()), empty("numbered_list"),
				heading(locales.TemplateStarterNotes()), empty("bulleted_list"),
				heading(locales.TemplateStarterActionItems()), empty("todo_list"),
			}
		}},
		{"todo", "square-check", locales.TemplateStarterTodo, locales.TemplateStarterTodoHint, func() []map[string]any {
			return []map[string]any{
				heading(locales.TemplateStarterToday()), empty("todo_list"),
				heading(locales.TemplateStarterThisWeek()), empty("todo_list"),
				heading(locales.TemplateStarterLater()), empty("todo_list"),
			}
		}},
		{"project", "lightbulb", locales.TemplateStarterProject, locales.TemplateStarterProjectHint, func() []map[string]any {
			return []map[string]any{
				heading(locales.TemplateStarterOverview()), empty("paragraph"),
				heading(locales.TemplateStarterGoals()), empty("bulleted_list"),
				heading(locales.TemplateStarterMilestones()),
				{"type": "table", "rows": []any{
					[]any{locales.TemplateStarterMilestone(), locales.TemplateStarterOwner(), locales.TemplateStarterDue()},
					[]any{"", "", ""},
				}},
				heading(locales.TemplateStarterQuestions()), empty("bulleted_list"),
			}
		}},
		{"journal", "square-pen", locales.TemplateStarterJournal, locales.TemplateStarterJournalHint, func() []map[string]any {
			return []map[string]any{
				heading(locales.TemplateStarterHighlights()), empty("bulleted_list"),
				heading(locales.TemplateStarterGrateful()), empty("numbered_list"),
				heading(locales.TemplateStarterTomorrow()), empty("todo_list"),
			}
		}},
	}
}

func (s Starter) Nodes() []*doc.Node {
	var out []*doc.Node
	for _, spec := range s.Blocks() {
		if n, err := doc.SpecToNode(spec, "", "Leaflet"); err == nil {
			out = append(out, n)
		}
	}
	return out
}
