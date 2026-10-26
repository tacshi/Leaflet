package agents

import (
	"encoding/json"
	"strings"
	"time"
)

// Preset is a built-in agent provider (Settings › Agent providers).
type Preset struct {
	ID      string
	Name    string
	BaseURL string
	// Auth is "key" (an API key), "account" (signing in to a
	// subscription) or "none".
	Auth  string
	Group string // Sign in | Built-in | Local | Custom
	// Local providers run on this device, not through the server.
	Local bool
	// Suggested models show until the provider's own list is fetched;
	// presets whose names change often have none and fetch the list.
	Suggested   []string
	Placeholder string
}

// Presets are the providers the app knows.
var Presets = []Preset{
	{ID: "chatgpt", Name: "ChatGPT", BaseURL: "https://api.openai.com/v1", Auth: "account", Group: "Sign in", Local: true},
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Auth: "key", Group: "Built-in", Suggested: []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-luna"}},
	{ID: "claude", Name: "Claude", BaseURL: "https://api.anthropic.com/v1", Auth: "key", Group: "Built-in", Suggested: []string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}},
	{ID: "xai", Name: "xAI", BaseURL: "https://api.x.ai/v1", Auth: "key", Group: "Built-in"},
	{ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", Auth: "key", Group: "Built-in"},
	{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Auth: "key", Group: "Built-in", Suggested: []string{"deepseek-v4-pro", "deepseek-v4-flash"}},
	{ID: "glm", Name: "GLM", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Auth: "key", Group: "Built-in"},
	{ID: "ollama", Name: "Ollama", BaseURL: "http://localhost:11434/v1", Auth: "none", Group: "Local", Local: true},
	{ID: "lmstudio", Name: "LM Studio", BaseURL: "http://localhost:1234/v1", Auth: "none", Group: "Local", Local: true},
	{ID: "local", Name: "Local server", BaseURL: "http://localhost:8080/v1", Auth: "none", Group: "Local", Local: true},
	{ID: "custom", Name: "Custom endpoint", Auth: "key", Group: "Custom", Placeholder: "https://your-host/v1"},
}

// notChat are words in the IDs of models that do not chat: embeddings,
// speech, images and the like, which /models lists too.
var notChat = []string{"embed", "tts", "whisper", "transcribe", "dall-e", "image", "moderation", "realtime", "audio", "search", "speech", "rerank", "babbage", "davinci"}

// ChatModels keeps the models that chat, the preset's suggestions first.
func ChatModels(ids []string, suggested []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range suggested {
		for _, id := range ids {
			if id == s && !seen[id] {
				out = append(out, id)
				seen[id] = true
			}
		}
	}
next:
	for _, id := range ids {
		lower := strings.ToLower(id)
		for _, w := range notChat {
			if strings.Contains(lower, w) {
				continue next
			}
		}
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}

// PresetByID returns a preset, or the custom one.
func PresetByID(id string) Preset {
	for _, p := range Presets {
		if p.ID == id {
			return p
		}
	}
	return Presets[len(Presets)-1]
}

// Today is the date for the prompt.
func Today() string { return time.Now().Format("2006-01-02") }

// SystemPrompt is the prompt of every agent run, kept in step with
// supabase/functions/_shared/prompt.ts.
func SystemPrompt(agentName, pageTitle, surface string, canReadOtherPages, supportsTools bool) string {
	focus := "The user is chatting with you beside the page. The whole page is your focus."
	switch surface {
	case "inline":
		focus = "The user mentioned you inside a block. That block is your focus: write or change it in place."
	case "selection":
		focus = "The user selected some text and asked about it. The selected blocks are your focus."
	case "comment":
		focus = "The user mentioned you in a comment thread anchored to a block. That block is your focus; your reply is posted in the thread."
	}
	var b strings.Builder
	b.WriteString("You are " + agentName + ", an AI collaborator in Leaflet, an app where people and AI co-write living documents called pages. Today is " + Today() + ". The current page is \"" + pageTitle + "\".\n\n")
	b.WriteString(focus + " Focus is where to start, not a boundary: you may read every block and edit anywhere on the page when the request calls for it.\n\n")
	if supportsTools {
		b.WriteString(`How to work:
- Call read_page first unless the page is already in this conversation.
- Make every change with the tools (insert_blocks, replace_block, move_block, delete_blocks, create_subpage). Never paste page content into your reply instead of editing.
- Address blocks by their IDs. Keep edits minimal and in the page's existing style and language.
- Text fields take inline markdown only (**bold**, *italic*, ` + "`code`" + `, [link](url)). Use block types for structure: heading, bulleted_list, numbered_list, todo_list, quote, table.
- For numbers worth seeing, insert a chart block (bar, line, or pie) with labels and series.
- For anything interactive or animated (a simulation such as orbiting planets, an instrument, a game, a calculator, a visual explainer), insert an embed block: one complete HTML document with inline CSS and JS, drawing with canvas, SVG or the DOM and playing sound with the Web Audio API. It runs sandboxed with no network, so it loads nothing from outside. Give it a title and a height that fits it.
- To show a YouTube or Vimeo video, insert a video block with its link, rather than the link as text.
- For code, insert a code block with the code in text, verbatim, and its language; never a fence in a paragraph.
- For a note, tip or warning to stand out, insert a callout with a tone. To keep long detail out of the way, put it in a toggle_list's children, the summary in its text.
- For a formula, insert a math_equation with its TeX; for a flowchart, sequence or other diagram, a diagram with its Mermaid source. To point at a web page, insert a link_preview with its url.
- Recording blocks carry their transcript. To summarize or use a recording, read it there and write the result into the page, for example a heading with key points below the recording.
- If a tool returns an error, fix the call and try again.
`)
		if canReadOtherPages {
			b.WriteString("- You may read the user's other pages with list_pages and read_other_page, but you only edit this page.\n")
		}
	} else {
		b.WriteString("This model cannot call tools, so you cannot edit the page. Answer in the reply, and say what you would change.\n")
	}
	b.WriteString("\nWhen you are done, reply in one to three short sentences: what you changed, or your answer. Do not repeat the content you wrote.")
	return b.String()
}

// FocusNote is the context about the focus blocks sent with the request.
func FocusNote(focusBlocks []map[string]any, selection string) string {
	if len(focusBlocks) == 0 && selection == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Focus:\n")
	if selection != "" {
		b.WriteString("Selected text: \"" + strings.TrimSpace(selection) + "\"\n")
	}
	if len(focusBlocks) > 0 {
		raw, _ := json.Marshal(focusBlocks)
		b.WriteString("Blocks: " + string(raw) + "\n")
	}
	return b.String()
}
