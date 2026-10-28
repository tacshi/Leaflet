package sysproxy

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// The proxy Windows Settings › Network › Proxy sets, for the current user.
func read() settings {
	var s settings
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return s
	}
	defer func() { _ = k.Close() }()
	if on, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || on == 0 {
		return s
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	s = parseServer(server)
	override, _, _ := k.GetStringValue("ProxyOverride")
	parseOverride(&s, override)
	return s
}

// parseOverride reads ProxyOverride, the exceptions split by ";", where
// "<local>" stands for hosts without a dot.
func parseOverride(s *settings, override string) {
	for _, h := range strings.Split(override, ";") {
		switch h = bypass(h); {
		case h == "":
		case strings.EqualFold(h, "<local>"):
			s.Simple = true
		default:
			s.Bypass = append(s.Bypass, h)
		}
	}
}

// parseServer reads ProxyServer: one "host:port" for every protocol, or
// "http=host:port;https=host:port;socks=host:port".
func parseServer(server string) settings {
	var s settings
	for _, part := range strings.Split(server, ";") {
		kind, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			kind, addr = "", kind
		}
		if _, rest, ok := strings.Cut(addr, "://"); ok {
			addr = rest
		}
		if addr == "" {
			continue
		}
		switch strings.ToLower(kind) {
		case "":
			s.HTTP, s.HTTPS = addr, addr
		case "http":
			s.HTTP = addr
		case "https":
			s.HTTPS = addr
		case "socks":
			s.SOCKS = addr
		}
	}
	return s
}
