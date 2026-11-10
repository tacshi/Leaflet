package endpoint

import "testing"

func TestSameOrigin(t *testing.T) {
	saved := "https://api.openai.com/v1"
	for other, same := range map[string]bool{
		"https://api.openai.com/v1/":                 true,
		"https://api.openai.com/v2":                  true,
		"https://API.openai.com/v1/chat/completions": true,
		"https://attacker.example/v1":                false,
		"https://api.openai.com.attacker.example":    false,
		"http://api.openai.com/v1":                   false,
		"https://api.openai.com:8443/v1":             false,
		"api.openai.com/v1":                          false,
		"":                                           false,
	} {
		if got := SameOrigin(saved, other); got != same {
			t.Errorf("%q: %v", other, got)
		}
	}
	if SameOrigin("", "") {
		t.Error("no address is no origin")
	}
}
