// Package sysproxy has Go's HTTP go through the proxy set in the system's
// network settings, as browsers do. Go itself reads only HTTP_PROXY and
// its kin, which an app opened from the Finder or the Start menu does not
// have, so behind a proxy the browser could reach a site Leaflet could not.
package sysproxy

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// settings are the system's proxies, each "host:port" or "".
type settings struct {
	HTTP, HTTPS, SOCKS string
	// Bypass lists the hosts that go direct, as NO_PROXY takes them.
	Bypass []string
	// Simple sends hosts without a dot direct.
	Simple bool
}

// Install has http.DefaultTransport, which the app's clients share, use
// the system's proxy. A proxy set in the environment wins, as before.
func Install() {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	if env := httpproxy.FromEnvironment(); env.HTTPProxy != "" || env.HTTPSProxy != "" {
		return
	}
	// Read on each request, so turning a proxy on or off needs no restart.
	t.Proxy = func(r *http.Request) (*url.URL, error) { return read().proxy(r.URL) }
}

func (s settings) proxy(u *url.URL) (*url.URL, error) {
	if s.Simple && !strings.Contains(u.Hostname(), ".") && net.ParseIP(u.Hostname()) == nil {
		return nil, nil
	}
	c := &httpproxy.Config{NoProxy: strings.Join(s.Bypass, ",")}
	if s.SOCKS != "" {
		c.HTTPProxy, c.HTTPSProxy = "socks5://"+s.SOCKS, "socks5://"+s.SOCKS
	}
	if s.HTTP != "" {
		c.HTTPProxy = "http://" + s.HTTP
	}
	// A secure web proxy is still spoken to in HTTP, for CONNECT.
	if s.HTTPS != "" {
		c.HTTPSProxy = "http://" + s.HTTPS
	}
	return c.ProxyFunc()(u)
}

// bypass turns a system's exception into NO_PROXY's: "*.local" into
// ".local", and an IPv4 prefix ("10.*", "192.168.*.*", "169.254/16") into
// a CIDR, which is all NO_PROXY matches addresses by.
func bypass(host string) string {
	host = strings.TrimSpace(host)
	if cidr, ok := prefix(host); ok {
		return cidr
	}
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		return "." + rest
	}
	return host
}

// prefix reads an IPv4 prefix written with trailing "*" octets, or with
// octets left out before a "/bits", as a CIDR.
func prefix(host string) (string, bool) {
	addr, bits, slash := strings.Cut(host, "/")
	octets := strings.Split(addr, ".")
	if len(octets) > 4 {
		return "", false
	}
	given := len(octets)
	for given > 0 && octets[given-1] == "*" {
		given--
	}
	stars := given < len(octets)
	if given == 0 || stars == slash {
		return "", false
	}
	for _, o := range octets[:given] {
		if n, err := strconv.Atoi(o); err != nil || n < 0 || n > 255 {
			return "", false
		}
	}
	if stars {
		bits = strconv.Itoa(8 * given)
	}
	full := append(octets[:given:given], "0", "0", "0", "0")[:4]
	p, err := netip.ParsePrefix(strings.Join(full, ".") + "/" + bits)
	if err != nil {
		return "", false
	}
	return p.Masked().String(), true
}
