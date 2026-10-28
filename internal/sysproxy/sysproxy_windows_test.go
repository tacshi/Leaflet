package sysproxy

import "testing"

func TestParseServer(t *testing.T) {
	cases := map[string]settings{
		"127.0.0.1:7890": {HTTP: "127.0.0.1:7890", HTTPS: "127.0.0.1:7890"},
		"http=127.0.0.1:7890;https=127.0.0.1:7891;socks=127.0.0.1:7892": {HTTP: "127.0.0.1:7890", HTTPS: "127.0.0.1:7891", SOCKS: "127.0.0.1:7892"},
		"http://127.0.0.1:7890": {HTTP: "127.0.0.1:7890", HTTPS: "127.0.0.1:7890"},
		"socks=127.0.0.1:7892":  {SOCKS: "127.0.0.1:7892"},
		"":                      {},
	}
	for in, want := range cases {
		if got := parseServer(in); got.HTTP != want.HTTP || got.HTTPS != want.HTTPS || got.SOCKS != want.SOCKS {
			t.Errorf("%q: %+v, want %+v", in, got, want)
		}
	}
}
