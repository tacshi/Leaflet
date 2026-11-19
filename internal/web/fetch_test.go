package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"testing"
	"time"
)

func TestFetchOnlyThePublicWeb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<title>secret</title>"))
	}))
	defer srv.Close()
	if _, err := Fetch(srv.URL); !errors.Is(err, ErrNotPublic) {
		t.Errorf("fetched a service on this machine: %v", err)
	}
	if _, err := Fetch("file:///etc/hosts"); err == nil {
		t.Error("fetched a file")
	}
	for addr, public := range map[string]bool{
		"93.184.216.34": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false, "172.16.0.1": false, "169.254.169.254": false,
		"100.100.1.1": false, "0.0.0.0": false, "::1": false, "fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false,
		"64:ff9b::7f00:1": false, "64:ff9b::a9fe:a9fe": false, "64:ff9b::5db8:d822": true, "64:ff9b:1::a00:1": false,
		"2002:7f00:1::1": false, "2002:c0a8:101::1": false, "2002:5db8:d822::1": true,
		"::127.0.0.1": false, "::10.0.0.1": false, "fec0::1": false,
		"::ffff:0:127.0.0.1": false, "::ffff:0:a9fe:a9fe": false, "::ffff:0:5db8:d822": true,
		"192.0.0.1": false, "192.0.0.170": false, "240.0.0.1": false, "250.1.2.3": false, "255.255.255.255": false,
		"::ffff:240.0.0.1": false, "64:ff9b::c000:aa": false, "2002:f000:1::1": false, "192.0.1.1": true,
		"198.18.0.1": true, "198.19.255.254": true, "239.255.255.255": false,
		"192.0.2.1": false, "198.51.100.7": false, "203.0.113.9": false, "::ffff:203.0.113.9": false,
		"2001:0:4136:e378:8000:63bf:3fff:fdd2": false, "2001:2::1": false, "2001:10::1": false, "2001:20::1": false,
		"2001:db8::1": false, "3fff::1": false, "100::1": false, "100::1:0:0:0:1": true,
		"64:ff9b::c000:201": false, "2002:cb00:7109::1": false, "2001:4860:4860::8888": true,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != public {
			t.Errorf("publicAddr(%s) = %v", addr, got)
		}
	}
}

func TestFetchOnlyWebPorts(t *testing.T) {
	for _, link := range []string{"http://mail.test:25/", "https://mail.test:587/", "http://shell.test:22/",
		"http://irc.test:6667/", "http://zero.test:0/", "http://big.test:70000/"} {
		if _, err := Fetch(link); !errors.Is(err, ErrBadPort) {
			t.Errorf("Fetch(%s) = %v", link, err)
		}
	}
	for _, link := range []string{"http://a.test/", "https://a.test/", "http://a.test:8080/", "https://a.test:8443/"} {
		u, _ := url.Parse(link)
		if !webPort(u) {
			t.Errorf("refused the port of %s", link)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://mail.test:25/", http.StatusFound)
	}))
	defer srv.Close()
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "mail.test:25" {
			return publicTransport{}.RoundTrip(r)
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	if _, err := client.Get(srv.URL); !errors.Is(err, ErrBadPort) {
		t.Errorf("followed a redirect to a mail port: %v", err)
	}
}

func TestFetchThroughAProxyAsksForTheCheckedAddress(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello from " + r.Host))
	}))
	defer site.Close()
	asked := make(chan string, 4)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.Method + " " + r.Host
		if r.Method != http.MethodConnect || r.Host != "93.184.216.34:80" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		to, err := net.Dial("tcp", site.Listener.Addr().String())
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = to.Close()
			return
		}
		go func() { _, _ = io.Copy(to, buf); _ = to.Close() }()
		_, _ = io.Copy(conn, to)
		_ = conn.Close()
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	was, wasLookup := systemProxy, lookupIP
	t.Cleanup(func() { systemProxy, lookupIP = was, wasLookup })
	systemProxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	lookupIP = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host == "inside.test" {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	if _, err := Fetch("https://rebind.test/a"); err == nil {
		t.Error("fetched through a proxy that refused")
	}
	if got := <-asked; got != "CONNECT 93.184.216.34:443" {
		t.Errorf("the proxy was asked %q", got)
	}
	body, err := Fetch("http://rebind.test/a")
	if got := <-asked; got != "CONNECT 93.184.216.34:80" {
		t.Errorf("the proxy was asked %q", got)
	}
	if err != nil || string(body) != "hello from rebind.test" {
		t.Errorf("fetched %q, %v", body, err)
	}
	if _, err := Fetch("https://inside.test/"); !errors.Is(err, ErrNotPublic) {
		t.Errorf("fetched a name that resolves inside: %v", err)
	}
	select {
	case got := <-asked:
		t.Errorf("the proxy was asked %q for a name that resolves inside", got)
	default:
	}
}

func TestReadThroughAProxyKeepsThePagesLink(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<p><a href="/next">next</a></p>`))
	}))
	defer site.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		to, err := net.Dial("tcp", site.Listener.Addr().String())
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = to.Close()
			return
		}
		go func() { _, _ = io.Copy(to, buf); _ = to.Close() }()
		_, _ = io.Copy(conn, to)
		_ = conn.Close()
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	was, wasLookup := systemProxy, lookupIP
	t.Cleanup(func() { systemProxy, lookupIP = was, wasLookup })
	systemProxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	lookupIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	p, err := Read(context.Background(), "http://news.test/a/b")
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "http://news.test/a/b" || !slices.Equal(p.Links, []string{"http://news.test/next"}) {
		t.Errorf("read at %q, links %q", p.URL, p.Links)
	}
}

func TestFetchEndsWithItsContext(t *testing.T) {
	useServer(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, err := FetchContext(ctx, srv.URL); !errors.Is(err, context.Canceled) {
		t.Errorf("fetched from a site that never answers: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the fetch went on %v after it was given up on", took)
	}
}

// A client of Transport, as netimage's pictures are downloaded by, reaches
// no service on this machine, not even by a redirect.
func TestTransportOnlyThePublicWeb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()
	client := &http.Client{Transport: Transport()}
	if _, err := client.Get(srv.URL); !errors.Is(err, ErrNotPublic) {
		t.Errorf("reached a service on this machine: %v", err)
	}
	if _, err := client.Get("file:///etc/hosts"); err == nil {
		t.Error("read a file")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL, http.StatusFound)
	}))
	defer redirect.Close()
	// The first hop is let through, as a public one would be.
	client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == redirect.Listener.Addr().String() {
			return http.DefaultTransport.RoundTrip(r)
		}
		return Transport().RoundTrip(r)
	})}
	if _, err := client.Get(redirect.URL); !errors.Is(err, ErrNotPublic) {
		t.Errorf("followed a redirect to this machine: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
