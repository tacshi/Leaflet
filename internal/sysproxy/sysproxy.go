// Package sysproxy has Go's HTTP go through the proxy set in the system's
// network settings, as browsers do. Go itself reads only HTTP_PROXY and
// its kin, which an app opened from the Finder or the Start menu does not
// have, so behind a proxy the browser could reach a site Leaflet could not.
package sysproxy

import (
	"net"
	"net/http"
	"net/url"
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

// bypass turns a system's exception ("*.local") into NO_PROXY's (".local").
func bypass(host string) string {
	host = strings.TrimSpace(host)
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		return "." + rest
	}
	return host
}
