package endpoint

import (
	"net/netip"
	"net/url"
	"strings"
)

func NormalizeBaseURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	for _, s := range []string{"/chat/completions", "/models"} {
		u = strings.TrimSuffix(u, s)
	}
	return u
}

func SameOrigin(a, b string) bool {
	origin := func(s string) string {
		u, err := url.Parse(NormalizeBaseURL(s))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return ""
		}
		return strings.ToLower(u.Scheme + "://" + u.Host)
	}
	return origin(a) != "" && origin(a) == origin(b)
}

func IsLocal(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
