package agents

import (
	"strings"

	"leaflet/internal/skills"
)

// SkillToolDefinition is read_skill, which reads one of Leaflet's skills:
// a guide to doing something well in a page, read when the task calls
// for it rather than carried in every prompt. external offers the skills
// for agents outside the app too.
func SkillToolDefinition(external bool) map[string]any {
	return tool("read_skill", "Read one of Leaflet's skills: a guide to doing something well in a page. The skills:\n"+skills.Index(external),
		map[string]any{"name": map[string]any{"type": "string", "enum": skills.Names(external), "description": "The skill's name."}}, "name")
}

// SkillPrompt is the line of a prompt that says when to read the skills.
var SkillPrompt = "- Before you write or reshape more than a few lines of a page, or make a page with content, read the skill " + skills.Expressive +
	" with read_skill, once in a run: it says which blocks show which content best (tables, charts, cards, maps, diagrams, callouts, toggles, embeds) and how to use emphasis, text color and highlight. Read another skill when your task is what it guides. The skills:\n  " + strings.ReplaceAll(skills.Index(false), "\n", "\n  ") + "\n"

// ReadSkill answers read_skill: the skill's guide, by its name. With
// external false, skills for agents outside the app are refused.
func ReadSkill(args map[string]any, external bool) (map[string]any, error) {
	name, err := str(args, "name")
	if err != nil {
		return nil, err
	}
	s, ok := skills.Get(name)
	if !ok || s.External && !external {
		return nil, terr("No skill %q. The skills are %s.", name, strings.Join(skills.Names(external), ", "))
	}
	return map[string]any{"name": s.Name, "description": s.Description, "guide": s.Body}, nil
}
