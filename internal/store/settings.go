package store

import (
	"encoding/json"
	"slices"
	"strings"
)

type SttSettings struct {
	Provider       string   `json:"provider"`
	ModelSize      string   `json:"model_size"`
	RunOn          string   `json:"run_on"`
	Language       string   `json:"language"`
	LiveTranscript bool     `json:"live_transcript"`
	SpeakerLabels  bool     `json:"speaker_labels"`
	BaseURL        string   `json:"base_url"`
	Model          string   `json:"model"`
	SonioxModel    string   `json:"soniox_model"`
	LanguageHints  []string `json:"language_hints"`
}

// SonioxModel is Soniox's real-time model. Settings naming none, or one
// Soniox retired or deprecated, which it routes to this one, move to it.
const SonioxModel = "stt-rt-v6"

var oldSonioxModels = []string{"stt-rt-preview", "stt-rt-preview-v2", "stt-rt-v3", "stt-rt-v4", "stt-rt-v5"}

type ModelChoice struct {
	ProviderID string
	Model      string
}

func (m ModelChoice) Encode() string { return m.ProviderID + "|" + m.Model }

func DecodeModelChoice(raw string) (ModelChoice, bool) {
	p, m, ok := strings.Cut(raw, "|")
	return ModelChoice{p, m}, ok
}

type Fonts struct {
	Latin    string `json:"latin,omitempty"`
	Chinese  string `json:"chinese,omitempty"`
	Japanese string `json:"japanese,omitempty"`
	Code     string `json:"code,omitempty"`
}

type Settings struct {
	Theme                   string      `json:"theme"`
	Accent                  string      `json:"accent,omitempty"`
	TextSize                float64     `json:"text_size"`
	Fonts                   Fonts       `json:"fonts"`
	Language                string      `json:"language,omitempty"`
	DefaultModel            string      `json:"default_model,omitempty"`
	AgentsReadOtherPages    bool        `json:"agents_read_other_pages"`
	AgentsUseWeb            bool        `json:"agents_use_web"`
	AllPagesOffline         bool        `json:"all_pages_offline"`
	WorkOffline             bool        `json:"work_offline"`
	ICloudSync              bool        `json:"icloud_sync,omitempty"`
	KeepAudioOnDevice       string      `json:"keep_audio_on_device"`
	UploadAudioOverCellular bool        `json:"upload_audio_over_cellular"`
	Stt                     SttSettings `json:"stt"`
	SidebarOpen             bool        `json:"sidebar_open"`
	SidebarTab              int         `json:"sidebar_tab,omitempty"`
	SidebarWidth            float32     `json:"sidebar_width,omitempty"`
	PanelWidth              float32     `json:"panel_width,omitempty"`
}

func DefaultSettings() Settings {
	return Settings{
		Theme: "system", TextSize: 17, AgentsReadOtherPages: true, AgentsUseWeb: true,
		AllPagesOffline: true, KeepAudioOnDevice: "until_uploaded", SidebarOpen: true,
		SidebarWidth: 264, PanelWidth: 360,
		Stt: SttSettings{
			Provider: "openai-compatible", ModelSize: "qwen3-asr-0.6b", RunOn: "auto", Language: "auto",
			LiveTranscript: true, SonioxModel: SonioxModel, LanguageHints: []string{"en", "zh"},
		},
	}
}

func (s Settings) Default() (ModelChoice, bool) {
	if s.DefaultModel == "" {
		return ModelChoice{}, false
	}
	return DecodeModelChoice(s.DefaultModel)
}

func (s *Store) Settings() Settings {
	out := DefaultSettings()
	if raw, ok := s.Value("settings"); ok {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out.SidebarWidth == 0 {
		out.SidebarWidth = 264
	}
	if out.PanelWidth == 0 {
		out.PanelWidth = 360
	}
	if !slices.Contains([]string{"soniox", "moss", "openai-compatible", "on-device"}, out.Stt.Provider) {
		out.Stt.Provider = "openai-compatible"
	}
	if m := strings.TrimSpace(out.Stt.SonioxModel); m == "" || slices.Contains(oldSonioxModels, m) {
		out.Stt.SonioxModel = SonioxModel
	}
	if def, ok := out.Default(); ok {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM model_providers WHERE id = ?`, def.ProviderID).Scan(&n); err == nil && n == 0 {
			out.DefaultModel = ""
		}
	}
	return out
}

func (s *Store) SaveSettings(v Settings) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetValue("settings", string(raw))
}
