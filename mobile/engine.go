package mobile

import "leaflet/internal/recording"

// EngineName is how a transcript names what made it, for the provider kept
// as its transcribed_by: "" for none.
func EngineName(provider string) string {
	defer catch(nil)
	return recording.EngineName(provider)
}
