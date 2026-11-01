package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
