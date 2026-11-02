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
