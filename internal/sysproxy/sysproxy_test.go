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
		{settings{HTTPS: "127.0.0.1:7891", Bypass: []string{bypass("10.*"), bypass("192.168.1.*")}}, "https://10.2.3.4/", ""},
		{settings{HTTPS: "127.0.0.1:7891", Bypass: []string{bypass("10.*"), bypass("192.168.1.*")}}, "https://192.168.1.20/", ""},
		{settings{HTTPS: "127.0.0.1:7891", Bypass: []string{bypass("10.*"), bypass("192.168.1.*")}}, "https://192.168.2.20/", "http://127.0.0.1:7891"},
		{settings{HTTPS: "127.0.0.1:7891", Bypass: []string{bypass("169.254/16"), bypass("192.168.0.0/16")}}, "https://169.254.1.1/", ""},
		{settings{HTTPS: "127.0.0.1:7891", Bypass: []string{bypass("169.254/16"), bypass("192.168.0.0/16")}}, "https://192.168.7.7/", ""},
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

func TestBypass(t *testing.T) {
	cases := map[string]string{
		"*.local":        ".local",
		" example.com ":  "example.com",
		"10.*":           "10.0.0.0/8",
		"172.16.*":       "172.16.0.0/16",
		"192.168.1.*":    "192.168.1.0/24",
		"192.168.*.*":    "192.168.0.0/16",
		"169.254/16":     "169.254.0.0/16",
		"192.168.0.0/16": "192.168.0.0/16",
		"127.0.0.1":      "127.0.0.1",
		"*":              "*",
		"<local>":        "<local>",
		"10.*/8":         "10.*/8",
		"foo.*":          "foo.*",
		"1.2.3.4.*":      "1.2.3.4.*",
		"300.*":          "300.*",
	}
	for in, want := range cases {
		if got := bypass(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
