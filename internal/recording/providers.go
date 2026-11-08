package recording

import (
	"strings"

	"leaflet/internal/store"
	"leaflet/locales"
)

// Settings › Speech to text lists the speech providers as Agent providers
// lists the agents': those set up, each with its own settings, the one
// that transcribes chosen among them, and the others to add.

// Endpoint is the provider of an OpenAI-compatible endpoint.
const Endpoint = "openai-compatible"

// SpeechProvider is a speech provider as Settings lists it.
type SpeechProvider struct {
	// ID is the provider, as the speech settings name it.
	ID string `json:"id"`
	// Name is what the list calls it, with its model where it has one.
	Name string `json:"name"`
	// Place says where it runs and where its key is kept.
	Place string `json:"place"`
	// Removable says it can be taken off the list: the device's own
	// engine can't, nor a Soniox key kept only on the server.
	Removable bool `json:"removable"`
}

// Keys says which keys are kept, for SpeechProviders: a Soniox key in
// this device's keychain or on the server, and a MOSS key in the
// keychain, and whether the device has an engine of its own.
type Keys struct {
	SonioxHere, SonioxOnServer, MossHere, OnDevice bool
}

// providerOrder is the order the list has them in.
var providerOrder = []string{OnDevice, "soniox", Moss, Endpoint}

// SetUp reports whether provider id can transcribe with stt and keys:
// the device's engine where it has one, a key for Soniox or MOSS, an
// address for an endpoint.
func SetUp(id string, stt store.SttSettings, k Keys) bool {
	switch id {
	case OnDevice:
		return k.OnDevice
	case "soniox":
		return k.SonioxHere || k.SonioxOnServer
	case Moss:
		return k.MossHere
	case Endpoint:
		return strings.TrimSpace(stt.BaseURL) != ""
	}
	return false
}

// SpeechProviders are the providers set up, in the list's order, and
// those that can be added.
func SpeechProviders(stt store.SttSettings, k Keys) (set []SpeechProvider, addable []SpeechProvider) {
	for _, id := range providerOrder {
		if SetUp(id, stt, k) {
			set = append(set, describe(id, stt, k))
		} else if id != OnDevice {
			addable = append(addable, SpeechProvider{ID: id, Name: ProviderName(id)})
		}
	}
	return set, addable
}

// ProviderName is a provider's name, without its model.
func ProviderName(id string) string {
	switch id {
	case OnDevice:
		return locales.RecSttOnDevice()
	case "soniox":
		return "Soniox"
	case Moss:
		return "MOSS"
	}
	return locales.CommonEndpoint()
}

// describe is provider id as the list shows it.
func describe(id string, stt store.SttSettings, k Keys) SpeechProvider {
	p := SpeechProvider{ID: id, Name: ProviderName(id), Removable: id != OnDevice}
	switch id {
	case OnDevice:
		p.Place = locales.RecSttOnDevicePlace()
	case "soniox":
		if m := strings.TrimSpace(stt.SonioxModel); m != "" {
			p.Name += " · " + m
		}
		p.Place = locales.RecSttSonioxOnDevice()
		if !k.SonioxHere {
			p.Place, p.Removable = locales.RecSttSonioxOnServer(), false
		}
	case Moss:
		p.Place = locales.RecSttMossDesc()
	case Endpoint:
		if m := strings.TrimSpace(stt.Model); m != "" {
			p.Name += " · " + m
		}
		url := strings.TrimSpace(stt.BaseURL)
		if IsLocalEndpoint(url) {
			p.Place = locales.RecEngineLocal(url)
		} else {
			p.Place = locales.RecEngineCloud(url)
		}
	}
	return p
}

// PreferSetUp has stt transcribe with the first provider set up, when the
// one it names is not: adding the first provider chooses it, as adding
// the first agent provider makes it the default. It reports whether stt
// changed, to be saved.
func PreferSetUp(stt *store.SttSettings, k Keys) bool {
	if SetUp(stt.Provider, *stt, k) {
		return false
	}
	for _, id := range providerOrder {
		if SetUp(id, *stt, k) {
			stt.Provider = id
			return true
		}
	}
	return false
}

// RemoveProvider takes provider id off the list: its key goes from this
// device's keychain, or an endpoint's address and key from the settings,
// and another provider set up transcribes when it was the one. The caller
// saves stt; keys are what is kept once the provider is gone.
func RemoveProvider(id string, stt *store.SttSettings, keys Keys) (Keys, error) {
	switch id {
	case "soniox":
		if err := SetLocalSonioxKey(""); err != nil {
			return keys, err
		}
		keys.SonioxHere = false
	case Moss:
		if err := SetMossKey(""); err != nil {
			return keys, err
		}
		keys.MossHere = false
	case Endpoint:
		if err := SetEndpointKey(""); err != nil {
			return keys, err
		}
		stt.BaseURL, stt.Model, stt.ProviderID = "", "", ""
	}
	PreferSetUp(stt, keys)
	return keys, nil
}

// Choice is the provider a transcription on page pageID takes unless
// another is picked, among those set up: the one last picked there, else
// the one last picked anywhere, as stt names it, else the first. There
// is no default: each transcription is picked, and this is the one its
// list marks.
func (r *Controller) Choice(pageID string, stt store.SttSettings, k Keys) string {
	r.pickMu.Lock()
	here := r.picked[pageID]
	r.pickMu.Unlock()
	for _, id := range []string{here, stt.Provider} {
		if id != "" && SetUp(id, stt, k) {
			return id
		}
	}
	for _, id := range providerOrder {
		if SetUp(id, stt, k) {
			return id
		}
	}
	return stt.Provider
}

// Pick keeps provider id as the one picked on page pageID, for Choice to
// mark there next.
func (r *Controller) Pick(pageID, id string) {
	r.pickMu.Lock()
	defer r.pickMu.Unlock()
	if r.picked == nil {
		r.picked = map[string]string{}
	}
	r.picked[pageID] = id
}

// Choices are the providers a transcription can be picked from: those set
// up, in the list's order.
func Choices(stt store.SttSettings, k Keys) []SpeechProvider {
	set, _ := SpeechProviders(stt, k)
	return set
}

// For is stt as a transcription on page pageID takes it: with provider,
// picked for it, or else the one Choice marks. A provider picked is kept
// as the page's; last reports whether it differs from the one stt last
// picked anywhere, for the caller to keep it there.
func (r *Controller) For(pageID, provider string, stt store.SttSettings, k Keys) (out store.SttSettings, last bool) {
	out = stt
	if provider == "" {
		out.Provider = r.Choice(pageID, stt, k)
		return out, false
	}
	r.Pick(pageID, provider)
	out.Provider = provider
	return out, provider != stt.Provider
}
