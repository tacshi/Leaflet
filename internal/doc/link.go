package doc

import (
	"net/url"
	"strings"
)

// LinkHref is where a link goes for the address typed for it: a page link
// or a web address as it is, a bare host such as example.com on https, and
// an email address as mailto. Nothing typed takes the link away, as "", and
// ok is false for what makes no link that opens.
func LinkHref(typed string) (href string, ok bool) {
	s := strings.TrimSpace(typed)
	lower := strings.ToLower(s)
	if _, page := PageLinkID(s); s == "" || page || WebURL(s) {
		return s, true
	}
	if strings.ContainsAny(s, " \t\n") {
		return "", false
	}
	if strings.HasPrefix(lower, "mailto:") {
		return s, len(s) > len("mailto:")
	}
	if user, host, at := strings.Cut(s, "@"); at && user != "" && strings.Contains(host, ".") && !strings.ContainsAny(s, "/:") {
		return "mailto:" + s, true
	}
	if strings.Contains(s, "://") {
		return "", false
	}
	u, err := url.Parse("https://" + s)
	if err != nil || !WebURL(u.String()) || !strings.Contains(u.Hostname(), ".") && u.Hostname() != "localhost" {
		return "", false
	}
	return "https://" + s, true
}
