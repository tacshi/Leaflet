package store

import (
	"encoding/json"
	"strings"
)

// SttSettings are the speech-to-text choice and recording options, which
// the server mirrors in stt_settings.
type SttSettings struct {
	// Provider is soniox | openai-compatible (on-device engines come later).
	Provider       string   `json:"provider"`
	ModelSize      string   `json:"model_size"`
	RunOn          string   `json:"run_on"`   // auto | gpu | cpu
	Language       string   `json:"language"` // auto, or an ISO 639-1 code
	LiveTranscript bool     `json:"live_transcript"`
	KeepAudio      bool     `json:"keep_audio"`
	SpeakerLabels  bool     `json:"speaker_labels"`
	BaseURL        string   `json:"base_url"`
	Model          string   `json:"model"`
	ProviderID     string   `json:"provider_id,omitempty"`
	SonioxModel    string   `json:"soniox_model"`
	LanguageHints  []string `json:"language_hints"`
}

// ModelChoice is a provider and one of its models.
type ModelChoice struct {
	ProviderID string
	Model      string
}

// Encode writes "providerId|model".
func (m ModelChoice) Encode() string { return m.ProviderID + "|" + m.Model }

// DecodeModelChoice reads "providerId|model".
func DecodeModelChoice(raw string) (ModelChoice, bool) {
	p, m, ok := strings.Cut(raw, "|")
	return ModelChoice{p, m}, ok
}

// Settings are the app's settings on this device.
type Settings struct {
	Theme string `json:"theme"` // system | light | sepia | dark
	// Accent is a name of theme.Accents, "system" or "" for Indigo.
	Accent   string  `json:"accent,omitempty"`
	TextSize float64 `json:"text_size"`
	// Language is the tag of the language the app shows, as App.SetLocale
	// keeps it, or "" to follow the system's.
	Language     string `json:"language,omitempty"`
	DefaultModel string `json:"default_model,omitempty"`
	// LocalWhenOffline switches to a local model when offline.
	LocalWhenOffline     bool `json:"local_when_offline"`
	AgentsReadOtherPages bool `json:"agents_read_other_pages"`
	AllPagesOffline      bool `json:"all_pages_offline"`
	// WorkOffline treats the app as offline even with a connection.
	WorkOffline bool `json:"work_offline"`
	// ICloudSync syncs pages through the Leaflet folder in iCloud Drive.
	ICloudSync              bool        `json:"icloud_sync,omitempty"`
	KeepAudioOnDevice       string      `json:"keep_audio_on_device"` // until_uploaded | 30_days | always
	UploadAudioOverCellular bool        `json:"upload_audio_over_cellular"`
	Stt                     SttSettings `json:"stt"`
	SidebarOpen             bool        `json:"sidebar_open"`
	SidebarWidth            float32     `json:"sidebar_width,omitempty"`
	PanelWidth              float32     `json:"panel_width,omitempty"`
}

// DefaultSettings are the settings of a new install.
func DefaultSettings() Settings {
	return Settings{
		Theme: "system", TextSize: 17, LocalWhenOffline: true, AgentsReadOtherPages: true,
		AllPagesOffline: true, KeepAudioOnDevice: "until_uploaded", SidebarOpen: true,
		SidebarWidth: 264, PanelWidth: 340,
		Stt: SttSettings{
			Provider: "openai-compatible", ModelSize: "qwen3-asr-0.6b", RunOn: "auto", Language: "auto",
			LiveTranscript: true, KeepAudio: true, SonioxModel: "stt-rt-v5", LanguageHints: []string{"en", "zh"},
		},
	}
}

// Default returns the default model choice, if one is set.
func (s Settings) Default() (ModelChoice, bool) {
	if s.DefaultModel == "" {
		return ModelChoice{}, false
	}
	return DecodeModelChoice(s.DefaultModel)
}

// Settings reads the settings, filling in defaults.
func (s *Store) Settings() Settings {
	out := DefaultSettings()
	if raw, ok := s.Value("settings"); ok {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out.SidebarWidth == 0 {
		out.SidebarWidth = 264
	}
	if out.PanelWidth == 0 {
		out.PanelWidth = 340
	}
	if out.Stt.Provider != "soniox" && out.Stt.Provider != "openai-compatible" {
		out.Stt.Provider = "openai-compatible"
	}
	return out
}

// SaveSettings writes the settings.
func (s *Store) SaveSettings(v Settings) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetValue("settings", string(raw))
}
