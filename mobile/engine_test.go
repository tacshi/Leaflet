package mobile

import (
	"testing"

	"leaflet/internal/recording"
	"leaflet/locales"
)

func TestEngineNameIsAsTheTranscriptSays(t *testing.T) {
	for provider, want := range map[string]string{
		"": "", "soniox": "Soniox", recording.Moss: "MOSS", recording.OnDevice: locales.RecEngineOnDevice(),
		"openai-compatible": locales.CommonEndpoint(),
	} {
		if got := EngineName(provider); got != want {
			t.Errorf("EngineName(%q) = %q, want %q", provider, got, want)
		}
	}
}
