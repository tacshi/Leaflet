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
	"testing"
)

// Links from shared pages reach only the web: not files on this device,
// nor services on it or its network.
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
		// IPv6 addresses standing for IPv4 ones are as those are.
		"64:ff9b::7f00:1": false, "64:ff9b::a9fe:a9fe": false, "64:ff9b::5db8:d822": true, "64:ff9b:1::a00:1": false,
		"2002:7f00:1::1": false, "2002:c0a8:101::1": false, "2002:5db8:d822::1": true,
		"::127.0.0.1": false, "::10.0.0.1": false, "fec0::1": false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != public {
			t.Errorf("publicAddr(%s) = %v", addr, got)
		}
	}
}

// Pictures load from files in the data folder only, links followed.

// Through a proxy, which looks names up itself, a fetch asks the proxy for
// the address it checked, not the name, which may resolve to this machine
// or its network the second time; a name that does already goes nowhere.
func TestFetchThroughAProxyAsksForTheCheckedAddress(t *testing.T) {
	// The site, where the proxy tunnels to the address it is asked for on
	// port 80, and the host it is asked for.
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
