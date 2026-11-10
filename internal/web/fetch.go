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

var ErrNotPublic = errors.New("not a public address")

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

var do = publicClient.Do

const fetchAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Leaflet"

var publicClient = &http.Client{Transport: publicTransport{}}

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

var (
	sharedSpace   = netip.MustParsePrefix("100.64.0.0/10")
	thisNetwork   = netip.MustParsePrefix("0.0.0.0/8")
	protocolSpace = netip.MustParsePrefix("192.0.0.0/24")
	futureSpace   = netip.MustParsePrefix("240.0.0.0/4")
	nat64         = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local    = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour     = netip.MustParsePrefix("2002::/16")
	v4Compat      = netip.MustParsePrefix("::/96")
	v4Translated  = netip.MustParsePrefix("::ffff:0:0:0/96")
	siteLocal     = netip.MustParsePrefix("fec0::/10")
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
	case nat64Local.Contains(ip), siteLocal.Contains(ip):
		return false
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !sharedSpace.Contains(ip) && !thisNetwork.Contains(ip) &&
		!protocolSpace.Contains(ip) && !futureSpace.Contains(ip)
}
