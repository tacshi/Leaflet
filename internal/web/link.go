package web

import (
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"leaflet/internal/doc"
)

// LinkInfo is what a web page says about itself in its head.
type LinkInfo struct{ Title, Description, Image, Site string }

var (
	metaTag   = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	titleTag  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	attribute = regexp.MustCompile(`(?is)([a-z:_-]+)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
)

// ReadLinkInfo reads a page's title, description, picture and site name
// from its Open Graph tags, or its title and description tags. A picture
// given relative to the page is made whole.
func ReadLinkInfo(page, src string) LinkInfo {
	if i := strings.Index(strings.ToLower(src), "</head>"); i >= 0 {
		src = src[:i]
	}
	meta := map[string]string{}
	for _, tag := range metaTag.FindAllString(src, -1) {
		attrs := map[string]string{}
		for _, m := range attribute.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(m[1])] = html.UnescapeString(strings.Trim(m[2], `"'`))
		}
		key := strings.ToLower(attrs["property"])
		if key == "" {
			key = strings.ToLower(attrs["name"])
		}
		if key != "" && meta[key] == "" {
			meta[key] = strings.TrimSpace(attrs["content"])
		}
	}
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := meta[k]; v != "" {
				return strings.Join(strings.Fields(v), " ")
			}
		}
		return ""
	}
	info := LinkInfo{
		Title:       first("og:title", "twitter:title"),
		Description: first("og:description", "twitter:description", "description"),
		Image:       first("og:image", "og:image:url", "twitter:image"),
		Site:        first("og:site_name"),
	}
	if m := titleTag.FindStringSubmatch(src); info.Title == "" && m != nil {
		info.Title = strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
	}
	if info.Image != "" {
		base, err1 := url.Parse(page)
		ref, err2 := url.Parse(info.Image)
		if err1 != nil || err2 != nil {
			info.Image = ""
		} else if abs := base.ResolveReference(ref); abs.Scheme == "http" || abs.Scheme == "https" {
			info.Image = abs.String()
		} else {
			info.Image = ""
		}
	}
	return info
}

// LookUp asks link's page for its info.
func LookUp(link string) (LinkInfo, error) {
	data, err := Fetch(link)
	if err != nil {
		return LinkInfo{}, err
	}
	return ReadLinkInfo(link, string(data[:min(len(data), 1<<20)])), nil
}

// Lacking reports whether n is a link card of link without its info yet.
func Lacking(n *doc.Node, link string) bool {
	return n.Type == "link_preview" && n.Str("url") == link && n.Str("title") == ""
}

// Unfilled are the links of content's link cards without their info.
func Unfilled(content *doc.Content) []string {
	var out []string
	content.Walk(func(n, _ *doc.Node) {
		if link := n.Str("url"); Lacking(n, link) && link != "" && !slices.Contains(out, link) {
			out = append(out, link)
		}
	})
	return out
}

// Fill puts info in content's cards of link that lack it, and reports
// whether there were any.
func (info LinkInfo) Fill(content *doc.Content, link string) bool {
	found := false
	content.Walk(func(n, _ *doc.Node) {
		if Lacking(n, link) {
			for k, v := range info.Data() {
				n.Set(k, v)
			}
			found = true
		}
	})
	return found
}

// Data is info as a link card keeps it.
func (info LinkInfo) Data() map[string]any {
	return map[string]any{"title": info.Title, "description": info.Description, "image": info.Image, "site": info.Site}
}
