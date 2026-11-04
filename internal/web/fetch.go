// Package web reads from the web for both apps: a link's page, a picture
// or a video's info, from public addresses only, and what a page says
// about itself, for its link cards.
package web

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"leaflet/internal/remote"
)

// ErrNotPublic is Fetch's for an address on this machine or its network.
var ErrNotPublic = errors.New("not a public address")

// Fetch reads a link's page, a picture or a video's info from the web.
// The links come from pages other people share and from the web, so only
// http(s) to a public address goes: never a file on this device, nor a
// service on it or its network, as a router's page. The address is
// checked as it is dialed, after the name resolves, so a name that
// resolves elsewhere the second time cannot get round it.
func Fetch(link string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchWait)
	defer cancel()
	res, err := get(ctx, link, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	return io.ReadAll(io.LimitReader(res.Body, maxFetchBytes))
}

// maxFetchBytes is the most Fetch reads: a large picture whole.
const maxFetchBytes = 20 << 20

// fetchWait is how long Fetch, and a page's reading, wait for what they
// read, whole. Tests shorten it.
var fetchWait = 30 * time.Second

// get asks for link as Fetch does, with accept, if any, as its Accept
// header. An answer that is an error is its error; another's body is the
// caller's to read and close.
func get(ctx context.Context, link, accept string) (*http.Response, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("not a web link")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fetchAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	res, err := do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		_ = res.Body.Close()
		return nil, &remote.Error{Status: res.StatusCode, Message: res.Status}
	}
	return res, nil
}

// do sends get's requests, to public addresses only. Tests answer from
// servers of their own, which it refuses.
var do = publicClient.Do

// fetchAgent is the User-Agent Fetch names, as a browser's: some sites
// refuse Go's own, as segmentfault.com's picture links answer it 410 Gone
// where a browser is sent on to the picture.
const fetchAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Leaflet"

// publicClient is Fetch's: through the system's proxy, as the app's other
// clients go, and to public addresses only.
var publicClient = &http.Client{Transport: publicTransport{}}

// direct goes to a site itself, its address checked as it is dialed.
var direct = &http.Transport{
	DialContext:           publicDialer.DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: answerWait,
	ExpectContinueTimeout: time.Second,
}

// answerWait is how long a site that took a request has to start its
// answer: one that never does holds nothing up for longer.
const answerWait = 20 * time.Second

// systemProxy is the proxy the app's other clients would use for r, or
// nil to go direct.
var systemProxy = func(r *http.Request) (*url.URL, error) {
	if t, ok := http.DefaultTransport.(*http.Transport); ok && t.Proxy != nil {
		return t.Proxy(r)
	}
	return nil, nil
}

// lookupIP finds the addresses of a host's name.
var lookupIP = net.DefaultResolver.LookupNetIP

// publicTransport takes each request, redirects too, direct or through
// the system's proxy.
type publicTransport struct{}

func (publicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	proxy, err := systemProxy(r)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		return direct.RoundTrip(r)
	}
	return viaProxy(r, proxy)
}

// viaProxy takes a request through a proxy. A proxy would look the site's
// name up itself, and a name can resolve elsewhere the second time, to
// this machine, whose proxy it may be, or its network: the address is
// found and checked here, and the proxy asked for it, the name going only
// in the Host header and to TLS. Each request has a connection of its
// own, which goes with its response.
func viaProxy(r *http.Request, proxy *url.URL) (*http.Response, error) {
	host := r.URL.Hostname()
	ips, err := lookupIP(r.Context(), "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !publicAddr(ip) {
			return nil, ErrNotPublic
		}
	}
	if len(ips) == 0 {
		return nil, ErrNotPublic
	}
	_, port, _ := net.SplitHostPort(hostPort(r.URL))
	addr := net.JoinHostPort(ips[0].Unmap().String(), port)
	t := &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: answerWait,
		ExpectContinueTimeout: time.Second}
	switch proxy.Scheme {
	case "http", "https":
		// A tunnel to the address, as CONNECT asks for, the site's TLS
		// in it by the site's name.
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) { return tunnel(ctx, proxy, addr) }
		t.DialTLSContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := tunnel(ctx, proxy, addr)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
			if err := tc.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return tc, nil
		}
	case "socks5", "socks5h":
		t.Proxy = http.ProxyURL(proxy)
		t.DialContext = plainDialer.DialContext
		t.TLSClientConfig = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	default:
		return nil, fmt.Errorf("web: proxy scheme %q", proxy.Scheme)
	}
	r = r.Clone(r.Context())
	if r.Host == "" {
		r.Host = r.URL.Host
	}
	r.URL.Host = addr
	return t.RoundTrip(r)
}

// tunnel opens a connection to addr through an HTTP proxy, which CONNECT
// asks to go there.
func tunnel(ctx context.Context, proxy *url.URL, addr string) (net.Conn, error) {
	conn, err := plainDialer.DialContext(ctx, "tcp", hostPort(proxy))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (net.Conn, error) {
		_ = conn.Close()
		return nil, err
	}
	if proxy.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{ServerName: proxy.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(err)
		}
		conn = tc
	}
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if u := proxy.User; u != nil {
		pass, _ := u.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pass)))
	}
	if err := req.Write(conn); err != nil {
		return fail(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return fail(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("web: the proxy answered %s", res.Status))
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
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

// publicControl refuses an address, as resolved, that is not public.
func publicControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip) {
		return ErrNotPublic
	}
	return nil
}

// sharedSpace is 100.64.0.0/10, carriers' and Tailscale's, thisNetwork
// 0.0.0.0/8, protocolSpace 192.0.0.0/24, kept for protocols' own
// addresses, and futureSpace 240.0.0.0/4, kept for later, as is
// 255.255.255.255, a network's broadcast. IPv6 addresses of nat64,
// sixToFour and v4Compat stand for IPv4 ones, in their last 4 bytes, or
// 6to4's 2 bytes in; nat64Local's and siteLocal's are a network's own.
var (
	sharedSpace   = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork   = netip.MustParsePrefix("0.0.0.0/8")
	protocolSpace = netip.MustParsePrefix("192.0.0.0/24")
	futureSpace   = netip.MustParsePrefix("240.0.0.0/4")
	nat64         = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local    = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour     = netip.MustParsePrefix("2002::/16")
	v4Compat      = netip.MustParsePrefix("::/96")
	siteLocal     = netip.MustParsePrefix("fec0::/10")
)

// publicAddr reports whether ip is on the internet: not this machine, nor
// its network, nor one of the ranges kept for neither, nor an IPv6
// address standing for an IPv4 one that is not.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	b := ip.As16()
	switch {
	case !ip.Is6():
	case nat64.Contains(ip), v4Compat.Contains(ip):
		ip = netip.AddrFrom4([4]byte(b[12:16]))
	case sixToFour.Contains(ip):
		ip = netip.AddrFrom4([4]byte(b[2:6]))
	case nat64Local.Contains(ip), siteLocal.Contains(ip):
		return false
	}
	// 198.18.0.0/15, kept for benchmarks, is public here: fake-IP proxies,
	// as Clash, answer every name with one of its addresses, and refusing
	// it would stop link cards and reading the web behind them.
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedSpace.Contains(ip) && !thisNetwork.Contains(ip) &&
		!protocolSpace.Contains(ip) && !futureSpace.Contains(ip)
}
