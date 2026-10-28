package sysproxy

import (
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// What CFNetwork reads agrees with scutil's view of the same settings.
func TestReadMatchesScutil(t *testing.T) {
	out, err := exec.Command("scutil", "--proxy").Output()
	if err != nil {
		t.Skip(err)
	}
	s := read()
	t.Logf("%+v", s)
	field := func(key string) string {
		m := regexp.MustCompile(`(?m)^\s*` + key + ` : (\S+)$`).FindStringSubmatch(string(out))
		if m == nil {
			return ""
		}
		return m[1]
	}
	for kind, got := range map[string]string{"HTTP": s.HTTP, "HTTPS": s.HTTPS, "SOCKS": s.SOCKS} {
		want := ""
		if field(kind+"Enable") == "1" {
			want = field(kind+"Proxy") + ":" + field(kind+"Port")
		}
		if got != want {
			t.Errorf("%s proxy %q, scutil says %q", kind, got, want)
		}
	}
	var exceptions []string
	if _, list, ok := strings.Cut(string(out), "ExceptionsList : <array> {"); ok {
		list, _, _ = strings.Cut(list, "}")
		for _, m := range regexp.MustCompile(`(?m)^\s*\d+ : (\S+)$`).FindAllStringSubmatch(list, -1) {
			exceptions = append(exceptions, bypass(m[1]))
		}
	}
	if !slices.Equal(s.Bypass, exceptions) {
		t.Errorf("bypass %q, scutil says %q", s.Bypass, exceptions)
	}
}
