package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"syscall"
	"time"

	"leaflet/internal/remote"
)

// errNotPublic is fetch's for an address on this machine or its network.
var errNotPublic = errors.New("not a public address")

// fetch reads a link's page, a picture or a video's info from the web.
// The links come from pages other people share and from the web, so only
// http(s) to a public address goes: never a file on this device, nor a
// service on it or its network, as a router's page. The address is
// checked as it is dialed, after the name resolves, so a name that
// resolves elsewhere the second time cannot get round it.
func fetch(link string) ([]byte, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("not a web link")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return nil, err
	}
	res, err := publicClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 400 {
		return nil, &remote.Error{Status: res.StatusCode, Message: res.Status}
	}
	return io.ReadAll(io.LimitReader(res.Body, 20<<20))
}

// publicClient is fetch's: through the system's proxy, as the app's other
// clients go, and to public addresses only.
var publicClient = &http.Client{Transport: &http.Transport{
	Proxy:                 publicProxy,
	DialContext:           publicDial,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: time.Second,
}}

// proxies are the proxies' addresses publicProxy has chosen: dialing them
// is fine, whatever their address, as the system's settings name them.
var proxies sync.Map

// publicProxy is the proxy the app's other clients would use for r. A
// proxy dials the site itself, so the site's address is checked here,
// before; going direct, it is checked as it is dialed.
func publicProxy(r *http.Request) (*url.URL, error) {
	var p *url.URL
	if t, ok := http.DefaultTransport.(*http.Transport); ok && t.Proxy != nil {
		var err error
		if p, err = t.Proxy(r); err != nil {
			return nil, err
		}
	}
	host := r.URL.Hostname()
	if p == nil {
		if _, ok := proxies.Load(hostPort(r.URL)); ok {
			return nil, errNotPublic
		}
		return nil, nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(r.Context(), "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !publicAddr(ip) {
			return nil, errNotPublic
		}
	}
	proxies.Store(hostPort(p), true)
	return p, nil
}

// hostPort is where a URL connects, its port given by its scheme when it
// has none, as the transport dials it.
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[u.Scheme]
	}
	return net.JoinHostPort(u.Hostname(), port)
}

var (
	plainDialer  = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	publicDialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: publicControl}
)

func publicDial(ctx context.Context, network, addr string) (net.Conn, error) {
	if _, ok := proxies.Load(addr); ok {
		return plainDialer.DialContext(ctx, network, addr)
	}
	return publicDialer.DialContext(ctx, network, addr)
}

// publicControl refuses an address, as resolved, that is not public.
func publicControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip) {
		return errNotPublic
	}
	return nil
}

// sharedSpace is 100.64.0.0/10, carriers' and Tailscale's, and
// thisNetwork 0.0.0.0/8.
var (
	sharedSpace = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
)

// publicAddr reports whether ip is on the internet: not this machine, nor
// its network, nor one of the ranges kept for neither.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedSpace.Contains(ip) && !thisNetwork.Contains(ip)
}
