// Package endpoint reads the address of an OpenAI-compatible server, as
// people paste it into Settings, for the agents and for transcription.
package endpoint

import (
	"net/netip"
	"net/url"
	"strings"
)

// NormalizeBaseURL trims slashes and a pasted /chat/completions or /models.
func NormalizeBaseURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	for _, s := range []string{"/chat/completions", "/models"} {
		u = strings.TrimSuffix(u, s)
	}
	return u
}

// SameOrigin reports whether two endpoints are at one origin, the same
// scheme, host and port, as a key saved for the one may go to the other: a
// base URL that names another server, or the same one without TLS, is no
// longer where the key was saved for.
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

// IsLocal reports whether an endpoint is on this machine or its network:
// localhost, a .local name, or a loopback, private or link-local address.
func IsLocal(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	// An address, not a name that starts like one, as 10.example.com.
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
