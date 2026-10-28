package sysproxy

import (
	"net/url"
	"testing"
)

func TestProxy(t *testing.T) {
	cases := []struct {
		s    settings
		u    string
		want string
	}{
		{settings{HTTP: "127.0.0.1:7890", HTTPS: "127.0.0.1:7891"}, "https://auth.openai.com/x", "http://127.0.0.1:7891"},
		{settings{HTTP: "127.0.0.1:7890", HTTPS: "127.0.0.1:7891"}, "http://example.com/", "http://127.0.0.1:7890"},
		{settings{HTTP: "127.0.0.1:7890"}, "https://auth.openai.com/x", ""},
		{settings{SOCKS: "127.0.0.1:7892"}, "https://auth.openai.com/x", "socks5://127.0.0.1:7892"},
		{settings{SOCKS: "127.0.0.1:7892", HTTPS: "127.0.0.1:7891"}, "https://auth.openai.com/x", "http://127.0.0.1:7891"},
		{settings{HTTPS: "127.0.0.1:7891"}, "https://localhost:1455/auth/callback", ""},
		{settings{HTTPS: "127.0.0.1:7891"}, "https://127.0.0.1:1455/auth/callback", ""},
		{settings{HTTPS: "127.0.0.1:7891", Bypass: []string{bypass("*.local")}}, "https://printer.local/", ""},
		{settings{HTTPS: "127.0.0.1:7891", Bypass: []string{"example.com"}}, "https://api.example.com/", ""},
		{settings{HTTPS: "127.0.0.1:7891", Simple: true}, "https://intranet/", ""},
		{settings{HTTPS: "127.0.0.1:7891", Simple: true}, "https://auth.openai.com/", "http://127.0.0.1:7891"},
		{settings{}, "https://auth.openai.com/x", ""},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.u)
		got, err := c.s.proxy(u)
		if err != nil {
			t.Fatal(err)
		}
		s := ""
		if got != nil {
			s = got.String()
		}
		if s != c.want {
			t.Errorf("%+v %s: got %v, want %q", c.s, c.u, got, c.want)
		}
	}
}
