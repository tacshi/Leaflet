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
	"slices"
	"strconv"
	"syscall"
	"time"

	"leaflet/internal/remote"
)

var (
	ErrNotPublic = errors.New("not a public address")
	ErrBadPort   = errors.New("not a port for the web")
	ErrNotWeb    = errors.New("not a web link")
)

func Fetch(link string) ([]byte, error) {
	return FetchContext(context.Background(), link)
}

func FetchContext(ctx context.Context, link string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchWait)
	defer cancel()
	res, err := get(ctx, link, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	return io.ReadAll(io.LimitReader(res.Body, maxFetchBytes))
}

const maxFetchBytes = 20 << 20

var fetchWait = 30 * time.Second

func get(ctx context.Context, link, accept string) (*http.Response, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	if !webScheme(u) {
		return nil, ErrNotWeb
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

var do = publicClient.Do

const fetchAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Leaflet"

var publicClient = &http.Client{Transport: publicTransport{}}

// Transport reaches only public addresses on the web's ports, as Fetch does,
// at every redirect of a client that uses it, and names Leaflet as Fetch does.
func Transport() http.RoundTripper { return agentTransport{} }

// PublicTransport goes to public addresses alone, through the system's
// proxy, as Fetch does, leaving a request's User-Agent as its caller set
// it: services that ask apps to name themselves, as map routers do.
func PublicTransport() http.RoundTripper { return publicTransport{} }

type agentTransport struct{}

func (agentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", fetchAgent)
	return publicTransport{}.RoundTrip(r)
}

var direct = &http.Transport{
	DialContext:           publicDialer.DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: answerWait,
	ExpectContinueTimeout: time.Second,
}

const answerWait = 20 * time.Second

var systemProxy = func(r *http.Request) (*url.URL, error) {
	if t, ok := http.DefaultTransport.(*http.Transport); ok && t.Proxy != nil {
		return t.Proxy(r)
	}
	return nil, nil
}

var lookupIP = net.DefaultResolver.LookupNetIP

type publicTransport struct{}

func (publicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// A link of another scheme goes nowhere, not even to the resolver or
	// the proxy.
	if !webScheme(r.URL) {
		return nil, ErrNotWeb
	}
	if !webPort(r.URL) {
		return nil, ErrBadPort
	}
	proxy, err := systemProxy(r)
	if err != nil {
		return nil, err
	}
	if proxy == nil {
		return direct.RoundTrip(r)
	}
	return viaProxy(r, proxy)
}

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
	asked := r
	r = r.Clone(r.Context())
	if r.Host == "" {
		r.Host = r.URL.Host
	}
	r.URL.Host = addr
	res, err := t.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	res.Request = asked
	return res, nil
}

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

func webScheme(u *url.URL) bool { return u.Scheme == "http" || u.Scheme == "https" }

func webPort(u *url.URL) bool {
	_, p, err := net.SplitHostPort(hostPort(u))
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(p)
	return err == nil && n > 0 && n <= 65535 && !badPorts[n]
}

var badPorts = map[int]bool{
	1: true, 7: true, 9: true, 11: true, 13: true, 15: true, 17: true, 19: true, 20: true, 21: true, 22: true, 23: true,
	25: true, 37: true, 42: true, 43: true, 53: true, 69: true, 77: true, 79: true, 87: true, 95: true, 101: true,
	102: true, 103: true, 104: true, 109: true, 110: true, 111: true, 113: true, 115: true, 117: true, 119: true,
	123: true, 135: true, 137: true, 139: true, 143: true, 161: true, 179: true, 389: true, 427: true, 465: true,
	512: true, 513: true, 514: true, 515: true, 526: true, 530: true, 531: true, 532: true, 540: true, 548: true,
	554: true, 556: true, 563: true, 587: true, 601: true, 636: true, 989: true, 990: true, 993: true, 995: true,
	1719: true, 1720: true, 1723: true, 2049: true, 3659: true, 4045: true, 4190: true, 5060: true, 5061: true,
	6000: true, 6566: true, 6665: true, 6666: true, 6667: true, 6668: true, 6669: true, 6679: true, 6697: true,
	10080: true,
}

var (
	nat64        = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour    = netip.MustParsePrefix("2002::/16")
	v4Compat     = netip.MustParsePrefix("::/96")
	v4Translated = netip.MustParsePrefix("::ffff:0:0:0/96")
	notPublic    = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2001:2::/48"),
		netip.MustParsePrefix("2001:10::/28"),
		netip.MustParsePrefix("2001:20::/28"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("3fff::/20"),
		netip.MustParsePrefix("fec0::/10"),
	}
)

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	b := ip.As16()
	switch {
	case !ip.Is6():
	case nat64.Contains(ip), v4Compat.Contains(ip), v4Translated.Contains(ip):
		ip = netip.AddrFrom4([4]byte(b[12:16]))
	case sixToFour.Contains(ip):
		ip = netip.AddrFrom4([4]byte(b[2:6]))
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() &&
		!slices.ContainsFunc(notPublic, func(p netip.Prefix) bool { return p.Contains(ip) })
}
