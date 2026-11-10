package recording

import (
	"strings"

	"leaflet/internal/store"
	"leaflet/locales"
)

const Endpoint = "openai-compatible"

type SpeechProvider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Place     string `json:"place"`
	Removable bool   `json:"removable"`
}

type Keys struct {
	SonioxHere, MossHere, OnDevice bool
}

var providerOrder = []string{OnDevice, "soniox", Moss, Endpoint}

func SetUp(id string, stt store.SttSettings, k Keys) bool {
	switch id {
	case OnDevice:
		return k.OnDevice
	case "soniox":
		return k.SonioxHere
	case Moss:
		return k.MossHere
	case Endpoint:
		return strings.TrimSpace(stt.BaseURL) != ""
	}
	return false
}

func SpeechProviders(stt store.SttSettings, k Keys) (set []SpeechProvider, addable []SpeechProvider) {
	for _, id := range providerOrder {
		if SetUp(id, stt, k) {
			set = append(set, describe(id, stt))
		} else if id != OnDevice {
			addable = append(addable, SpeechProvider{ID: id, Name: ProviderName(id)})
		}
	}
	return set, addable
}

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

func describe(id string, stt store.SttSettings) SpeechProvider {
	p := SpeechProvider{ID: id, Name: ProviderName(id), Removable: id != OnDevice}
	switch id {
	case OnDevice:
		p.Place = locales.RecSttOnDevicePlace()
	case "soniox":
		if m := strings.TrimSpace(stt.SonioxModel); m != "" {
			p.Name += " · " + m
		}
		p.Place = locales.RecSttSonioxOnDevice()
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
		if err := SetEndpointKey("", ""); err != nil {
			return keys, err
		}
		stt.BaseURL, stt.Model = "", ""
	}
	PreferSetUp(stt, keys)
	return keys, nil
}

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

func (r *Controller) Pick(pageID, id string) {
	r.pickMu.Lock()
	defer r.pickMu.Unlock()
	if r.picked == nil {
		r.picked = map[string]string{}
	}
	r.picked[pageID] = id
}

func Choices(stt store.SttSettings, k Keys) []SpeechProvider {
	set, _ := SpeechProviders(stt, k)
	return set
}

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
