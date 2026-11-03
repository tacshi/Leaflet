package web

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"leaflet/internal/remote"
)

// Reading a page for an agent: its text, with Markdown's headings, list
// items and links, the links made whole. The agent function reads pages
// the same way (supabase/functions/_shared/page.ts); both read the pages
// of its testdata/pages.json alike.

// Page is a web page as an agent reads it.
type Page struct {
	// URL is where the page was read, after the redirects on the way.
	URL   string
	Title string
	Text  string
	// Links are the web links in Text, in the order they come.
	Links []string
	// Truncated is a page whose text went on past MaxPageText.
	Truncated bool
}

// MaxPageText is the most of a page's text an agent reads, in characters:
// a long article whole, and no more than a model takes in at once.
const MaxPageText = 30_000

// maxPageBytes is the most of a page read from the web.
const maxPageBytes = 5 << 20

// ErrNotText is Read's for what is not a page of text, as a picture.
var ErrNotText = errors.New("not a page of text")

// get asks for a page for Read; tests answer from servers of their own,
// which Fetch refuses.
var get = func(ctx context.Context, link string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fetchAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")
	return publicClient.Do(req)
}

// Read reads a page for an agent from the public web only, as Fetch does:
// its text, when it is HTML or other text.
func Read(ctx context.Context, link string) (Page, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Page{}, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Page{}, errors.New("not a web link")
	}
	res, err := get(ctx, link)
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 400 {
		return Page{}, &remote.Error{Status: res.StatusCode, Message: res.Status}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPageBytes))
	if err != nil {
		return Page{}, err
	}
	final := link
	if res.Request != nil && res.Request.URL != nil {
		final = res.Request.URL.String()
	}
	ctype := res.Header.Get("Content-Type")
	if ctype == "" {
		ctype = http.DetectContentType(body)
	}
	mt, _, _ := mime.ParseMediaType(ctype)
	text := strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		mt == "application/xml" || strings.HasSuffix(mt, "+xml")
	if !text && mt != "application/xhtml+xml" {
		return Page{}, ErrNotText
	}
	// Text in another encoding than UTF-8 is read in it, as a browser reads
	// it: by its byte order mark, its Content-Type, its own <meta>, or else
	// as UTF-8 if it is that.
	enc, _, _ := charset.DetermineEncoding(body, ctype)
	if decoded, err := enc.NewDecoder().Bytes(body); err == nil {
		body = decoded
	}
	src := strings.ToValidUTF8(string(body), "�")
	if mt == "text/html" || mt == "application/xhtml+xml" {
		return ReadHTML(final, src), nil
	}
	t, cut := truncate(strings.TrimSpace(src))
	return Page{URL: final, Text: t, Truncated: cut, Links: urlsOf(t)}, nil
}

// skipped are the elements whose content is no text to read: code, styles,
// media, controls, and what a browser shows in place of those.
var skipped = map[string]bool{
	"head": true, "script": true, "style": true, "noscript": true, "template": true, "iframe": true,
	"object": true, "embed": true, "canvas": true, "video": true, "audio": true, "picture": true, "map": true,
	"select": true, "button": true, "textarea": true, "input": true, "dialog": true,
}

// blocks are the elements that start a line of their own.
var blocks = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "caption": true, "center": true,
	"dd": true, "details": true, "div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "header": true, "hgroup": true, "li": true, "main": true, "nav": true, "ol": true, "p": true,
	"pre": true, "section": true, "summary": true, "table": true, "tbody": true, "tfoot": true, "thead": true,
	"tr": true, "ul": true,
}

// spaces are what a page's text runs together into one space: HTML's
// spaces, and the no-break space pages put between words.
var spaces = regexp.MustCompile("[\t\n\f\r  ]+")

// pageReader writes a page's text: its finished lines in out, the line
// being written in line, to go after prefix (a heading's #s, a list
// item's -) once it has text.
type pageReader struct {
	base   *url.URL
	self   string
	out    strings.Builder
	line   string
	prefix string
	pre    int
}

// ReadHTML reads a page's HTML, page being where it was read, which its
// links are made whole against.
func ReadHTML(page, src string) Page {
	p := Page{URL: page}
	base, err := url.Parse(page)
	if err != nil {
		return p
	}
	root, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return p
	}
	r := &pageReader{base: base, self: linkString(base)}
	if head := find(root, "head"); head != nil {
		if t := find(head, "title"); t != nil {
			var b strings.Builder
			for c := t.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					b.WriteString(c.Data)
				}
			}
			p.Title = strings.TrimSpace(spaces.ReplaceAllString(b.String(), " "))
		}
		if b := find(head, "base"); b != nil {
			if href, ok := attr(b, "href"); ok {
				if u, err := base.Parse(strings.TrimSpace(href)); err == nil {
					r.base = u
				}
			}
		}
	}
	r.walk(root)
	r.flush()
	p.Text, p.Truncated = truncate(strings.TrimRight(r.out.String(), "\n"))
	p.Links = urlsOf(p.Text)
	return p
}

// find is the first element named name in n, n itself or one inside it.
func find(n *html.Node, name string) *html.Node {
	if n.Type == html.ElementNode && n.Namespace == "" && n.Data == name {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, name); f != nil {
			return f
		}
	}
	return nil
}

func (r *pageReader) walk(n *html.Node) {
	switch n.Type {
	case html.DocumentNode:
		r.children(n)
		return
	case html.TextNode:
		r.write(n.Data)
		return
	case html.ElementNode:
	default:
		return
	}
	// An SVG or MathML element is a picture or a formula.
	name := n.Data
	if n.Namespace != "" || skipped[name] {
		return
	}
	switch name {
	case "br":
		r.flush()
		return
	case "hr":
		r.flush()
		r.out.WriteString("---\n")
		return
	case "img":
		return
	}
	// An element with no text, as a link around a picture or an empty
	// box, breaks no line and says nothing.
	if !hasText(n) {
		return
	}
	switch name {
	case "td", "th":
		if strings.TrimSpace(r.line) != "" {
			r.write(" | ")
		}
	}
	block := blocks[name]
	if block {
		r.flush()
		switch name {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			r.prefix = strings.Repeat("#", int(name[1]-'0')) + " "
		case "li":
			r.prefix = "- "
		case "pre":
			r.pre++
		}
	}
	if link := r.link(n); link != "" {
		r.anchor(n, link)
	} else {
		r.children(n)
	}
	if block {
		r.flush()
		r.prefix = ""
		if name == "pre" {
			r.pre--
		}
	}
}

// hasText reports whether n has text to read: a text node of more than
// spaces, or an element with one in it that is read.
func hasText(n *html.Node) bool {
	switch n.Type {
	case html.TextNode:
		return strings.TrimSpace(spaces.ReplaceAllString(n.Data, " ")) != ""
	case html.ElementNode:
		if n.Namespace != "" || skipped[n.Data] {
			return false
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasText(c) {
			return true
		}
	}
	return false
}

func (r *pageReader) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.walk(c)
	}
}

// anchor writes a link's text as Markdown's [text](link). One whose text
// runs over lines, as a card's, is followed by a line of its link alone.
func (r *pageReader) anchor(n *html.Node, link string) {
	lines, at := r.out.Len(), len(r.line)
	r.children(n)
	if r.out.Len() != lines {
		r.flush()
		r.out.WriteString("<" + link + ">\n")
		return
	}
	text := r.line[at:]
	inner := strings.TrimSpace(text)
	lead, trail := "", ""
	if strings.HasPrefix(text, " ") {
		lead = " "
	}
	if strings.HasSuffix(text, " ") {
		trail = " "
	}
	r.line = r.line[:at] + lead + "[" + inner + "](" + link + ")" + trail
}

// link is where an <a> goes, whole and without its fragment, or "" when
// it goes nowhere to show: not to the web, or to this page itself.
func (r *pageReader) link(n *html.Node) string {
	if n.Data != "a" {
		return ""
	}
	href, ok := attr(n, "href")
	if !ok {
		return ""
	}
	u, err := r.base.Parse(strings.TrimSpace(href))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	if s := linkString(u); s != r.self {
		return s
	}
	return ""
}

// linkString is u as a page's text gives it, as a browser writes it out
// (its host in lower case, a path of / at least) but for its fragment,
// left out, and its parentheses, escaped, which would end a Markdown link.
func linkString(u *url.URL) string {
	c := *u
	c.Fragment, c.RawFragment = "", ""
	c.Host = strings.ToLower(c.Host)
	if c.Path == "" && c.Opaque == "" {
		c.Path, c.RawPath = "/", ""
	}
	return strings.NewReplacer("(", "%28", ")", "%29").Replace(c.String())
}

// write adds text to the line: its spaces run together as a browser shows
// them, but in a <pre>, which keeps them and its line breaks.
func (r *pageReader) write(s string) {
	if r.pre > 0 {
		for i, l := range strings.Split(s, "\n") {
			if i > 0 {
				r.out.WriteString(r.prefix + strings.TrimRight(r.line, " \t") + "\n")
				r.line, r.prefix = "", ""
			}
			r.line += l
		}
		return
	}
	s = spaces.ReplaceAllString(s, " ")
	if r.line == "" || strings.HasSuffix(r.line, " ") {
		s = strings.TrimLeft(s, " ")
	}
	r.line += s
}

// flush ends the line, when it has text, after its prefix.
func (r *pageReader) flush() {
	line := strings.TrimSpace(r.line)
	if r.pre > 0 {
		line = strings.TrimRight(r.line, " \t")
	}
	r.line = ""
	if strings.TrimSpace(line) != "" {
		r.out.WriteString(r.prefix + line + "\n")
		r.prefix = ""
	}
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// truncate cuts text to MaxPageText characters, at the end of a line when
// one ends in the last quarter, and reports whether it cut it.
func truncate(text string) (string, bool) {
	if utf8.RuneCountInString(text) <= MaxPageText {
		return text, false
	}
	n, cut := 0, text
	for i := range text {
		if n == MaxPageText {
			cut = text[:i]
			break
		}
		n++
	}
	if at := strings.LastIndexByte(cut, '\n'); at >= 0 && utf8.RuneCountInString(cut[:at]) >= MaxPageText*3/4 {
		cut = cut[:at]
	}
	return strings.TrimRight(cut, " \t\n"), true
}

var (
	// markdownURL is a Markdown link's or a <link>'s address.
	markdownURL = regexp.MustCompile(`\]\((https?://[^()\t\n\f\r ]+)\)|<(https?://[^<>\t\n\f\r ]+)>`)
	// bareURL is an address written out, to the first space or bracket.
	bareURL = regexp.MustCompile(`https?://[^\t\n\f\r <>"'()\[\]]+`)
)

// urlsOf are the web links in text, once each: those it links, in order,
// then those written out.
func urlsOf(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, m := range markdownURL.FindAllStringSubmatch(text, -1) {
		add(m[1] + m[2])
	}
	for _, u := range bareURL.FindAllString(text, -1) {
		add(strings.TrimRight(u, ".,;:!?"))
	}
	return out
}
