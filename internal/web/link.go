package web

import (
	"context"
	"html"
	"io"
	"mime"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"leaflet/internal/doc"
)

type LinkInfo struct{ Title, Description, Image, Site string }

var (
	metaTag   = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	titleTag  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	headEnd   = regexp.MustCompile(`(?i)</head>`)
	attribute = regexp.MustCompile(`(?is)([a-z:_-]+)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
)

func ReadLinkInfo(page, src string) LinkInfo {
	if loc := headEnd.FindStringIndex(src); loc != nil {
		src = src[:loc[0]]
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

const maxHeadBytes = 1 << 20

func LookUp(link string) (LinkInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchWait)
	defer cancel()
	res, err := get(ctx, link, "")
	if err != nil {
		return LinkInfo{}, err
	}
	defer func() { _ = res.Body.Close() }()
	ctype := res.Header.Get("Content-Type")
	if mt, _, _ := mime.ParseMediaType(ctype); mt != "" && mt != "text/html" && mt != "application/xhtml+xml" {
		return LinkInfo{}, nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxHeadBytes))
	if err != nil {
		return LinkInfo{}, err
	}
	return ReadLinkInfo(link, pageText(data, ctype)), nil
}

func Lacking(n *doc.Node, link string) bool {
	return n.Type == "link_preview" && n.Str("url") == link && n.Str("title") == ""
}

func Unfilled(content *doc.Content) []string {
	var out []string
	content.Walk(func(n, _ *doc.Node) {
		if link := n.Str("url"); Lacking(n, link) && link != "" && !slices.Contains(out, link) {
			out = append(out, link)
		}
	})
	return out
}

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

func (info LinkInfo) Data() map[string]any {
	return map[string]any{"title": info.Title, "description": info.Description, "image": info.Image, "site": info.Site}
}
