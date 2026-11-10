package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

type Page struct {
	URL       string
	Title     string
	Text      string
	Links     []string
	Truncated bool
}

const MaxPageText = 30_000

const maxPageBytes = 2 << 20

const pageAccept = "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5"

var ErrNotText = errors.New("not a page of text")

func Read(ctx context.Context, link string) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchWait)
	defer cancel()
	res, err := get(ctx, link, pageAccept)
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = res.Body.Close() }()
	final := link
	if res.Request != nil && res.Request.URL != nil {
		final = res.Request.URL.String()
	}
	body := io.LimitReader(res.Body, maxPageBytes)
	ctype := res.Header.Get("Content-Type")
	var head []byte
	if ctype == "" {
		head = make([]byte, 512)
		n, err := io.ReadFull(body, head)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return Page{}, err
		}
		head = head[:n]
		ctype = http.DetectContentType(head)
	}
	mt, _, _ := mime.ParseMediaType(ctype)
	if !readable(mt) {
		return Page{}, ErrNotText
	}
	data, err := io.ReadAll(io.MultiReader(bytes.NewReader(head), body))
	if err != nil {
		return Page{}, err
	}
	src := pageText(data, ctype)
	if mt == "text/html" || mt == "application/xhtml+xml" {
		return readHTML(ctx, final, src)
	}
	t, cut := truncate(strings.TrimSpace(src))
	return Page{URL: final, Text: t, Truncated: cut, Links: urlsOf(t)}, nil
}

func pageText(data []byte, ctype string) string {
	enc, _, _ := charset.DetermineEncoding(data, ctype)
	if decoded, err := enc.NewDecoder().Bytes(data); err == nil {
		data = decoded
	}
	return strings.ToValidUTF8(string(data), "�")
}

func readable(mt string) bool {
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		mt == "application/xml" || strings.HasSuffix(mt, "+xml")
}

var skipped = map[string]bool{
	"head": true, "script": true, "style": true, "noscript": true, "template": true, "iframe": true,
	"object": true, "embed": true, "canvas": true, "video": true, "audio": true, "picture": true, "map": true,
	"select": true, "button": true, "textarea": true, "input": true, "dialog": true,
}

var blocks = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "caption": true, "center": true,
	"dd": true, "details": true, "div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "header": true, "hgroup": true, "li": true, "main": true, "nav": true, "ol": true, "p": true,
	"pre": true, "section": true, "summary": true, "table": true, "tbody": true, "tfoot": true, "thead": true,
	"tr": true, "ul": true,
}

var spaces = regexp.MustCompile("[\t\n\f\r  ]+")

type pageReader struct {
	ctx               context.Context
	base              *url.URL
	self              string
	out               strings.Builder
	outRunes, outText int
	parts             []string
	links             []openLink
	decided           int
	marks             int
	space, empty      bool
	prefix            string
	pre               int
	read              map[*html.Node]bool
	nodes             int
	done              bool
	err               error
}

type openLink struct{ at, marks int }

func ReadHTML(page, src string) Page {
	p, _ := readHTML(context.Background(), page, src)
	return p
}

func readHTML(ctx context.Context, page, src string) (Page, error) {
	p := Page{URL: page}
	base, err := url.Parse(page)
	if err != nil {
		return p, nil
	}
	root, err := html.Parse(ctxReader{ctx, strings.NewReader(src)})
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if err != nil {
		return p, nil
	}
	r := &pageReader{ctx: ctx, base: base, self: linkString(base), empty: true, read: map[*html.Node]bool{}}
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
	if r.err != nil {
		return Page{}, r.err
	}
	r.flush()
	p.Text, p.Truncated = truncate(strings.TrimRight(r.out.String(), "\n"))
	p.Links = urlsOf(p.Text)
	return p, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

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
	if r.done {
		return
	}
	if r.nodes++; r.nodes%1024 == 0 {
		if err := r.ctx.Err(); err != nil {
			r.err, r.done = err, true
			return
		}
	}
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
		r.writeOut("---\n")
		return
	case "img":
		return
	}
	if !r.hasText(n) {
		return
	}
	switch name {
	case "td", "th":
		if r.marks > 0 {
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

func (r *pageReader) hasText(n *html.Node) bool {
	switch n.Type {
	case html.TextNode:
		return strings.ContainsFunc(n.Data, notSpace)
	case html.ElementNode:
		if n.Namespace != "" || skipped[n.Data] {
			return false
		}
	}
	if n.FirstChild == nil {
		return false
	}
	if has, ok := r.read[n]; ok {
		return has
	}
	has := false
	for c := n.FirstChild; c != nil && !has; c = c.NextSibling {
		has = r.hasText(c)
	}
	r.read[n] = has
	return has
}

func notSpace(c rune) bool { return !unicode.IsSpace(c) }

func (r *pageReader) children(n *html.Node) {
	for c := n.FirstChild; c != nil && !r.done; c = c.NextSibling {
		r.walk(c)
	}
}

func (r *pageReader) anchor(n *html.Node, link string) {
	lines := r.out.Len()
	r.links = append(r.links, openLink{at: len(r.parts), marks: r.marks})
	r.parts = append(r.parts, "")
	r.children(n)
	l := r.links[len(r.links)-1]
	r.links = r.links[:len(r.links)-1]
	r.decided = min(r.decided, len(r.links))
	if r.out.Len() != lines {
		r.flush()
		r.writeOut("<" + link + ">\n")
		return
	}
	lead, trail := "", ""
	for _, p := range r.parts[l.at+1:] {
		if p != "" {
			if p[0] == ' ' {
				lead = " "
			}
			break
		}
	}
	for i := len(r.parts) - 1; i > l.at; i-- {
		if p := r.parts[i]; p != "" {
			if p[len(p)-1] == ' ' {
				trail = " "
			}
			break
		}
	}
	for i := l.at + 1; i < len(r.parts); i++ {
		if r.parts[i] = strings.TrimLeftFunc(r.parts[i], unicode.IsSpace); r.parts[i] != "" {
			break
		}
	}
	for i := len(r.parts) - 1; i > l.at; i-- {
		if r.parts[i] = strings.TrimRightFunc(r.parts[i], unicode.IsSpace); r.parts[i] != "" {
			break
		}
	}
	r.parts[l.at] = lead + "["
	r.parts = append(r.parts, "]("+link+")"+trail)
	r.marks += len("[]()") + utf8.RuneCountInString(link)
	r.empty, r.space = false, trail != ""
	r.check()
}

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

func linkString(u *url.URL) string {
	c := *u
	c.Fragment, c.RawFragment = "", ""
	c.Host = strings.ToLower(c.Host)
	if c.Path == "" && c.Opaque == "" {
		c.Path, c.RawPath = "/", ""
	}
	return strings.NewReplacer("(", "%28", ")", "%29").Replace(c.String())
}

func (r *pageReader) write(s string) {
	if r.pre > 0 {
		for !r.done {
			l, rest, more := strings.Cut(s, "\n")
			r.add(l)
			if !more {
				return
			}
			line := r.prefix + strings.TrimRight(strings.Join(r.parts, ""), " \t") + "\n"
			r.prefix = ""
			r.newLine(true)
			r.writeOut(line)
			s = rest
		}
		return
	}
	s = spaces.ReplaceAllString(s, " ")
	if r.empty || r.space {
		s = strings.TrimLeft(s, " ")
	}
	r.add(s)
}

func (r *pageReader) add(s string) {
	if s == "" {
		return
	}
	r.parts = append(r.parts, s)
	r.empty, r.space = false, s[len(s)-1] == ' '
	for _, c := range s {
		if !unicode.IsSpace(c) {
			r.marks++
		}
	}
	r.check()
}

func (r *pageReader) flush() {
	if r.marks == 0 {
		r.newLine(false)
		return
	}
	line := strings.Join(r.parts, "")
	if r.pre > 0 {
		line = strings.TrimRight(line, " \t")
	} else {
		line = strings.TrimSpace(line)
	}
	line = r.prefix + line + "\n"
	r.prefix = ""
	r.newLine(true)
	r.writeOut(line)
}

func (r *pageReader) newLine(wrote bool) {
	r.parts = r.parts[:0]
	if wrote {
		r.decided = len(r.links)
	}
	for i := r.decided; i < len(r.links); i++ {
		r.links[i] = openLink{at: len(r.parts)}
		r.parts = append(r.parts, "")
	}
	r.marks, r.space, r.empty = 0, false, true
}

func (r *pageReader) writeOut(s string) {
	r.out.WriteString(s)
	if t := strings.TrimRight(s, "\n"); t != "" {
		r.outText = r.outRunes + utf8.RuneCountInString(t)
	}
	r.outRunes += utf8.RuneCountInString(s)
	r.check()
}

func (r *pageReader) check() {
	kept := r.marks
	if r.decided < len(r.links) {
		kept = r.links[r.decided].marks
	}
	if r.outText > MaxPageText || kept > 0 && r.outRunes+utf8.RuneCountInString(r.prefix)+kept > MaxPageText {
		r.done = true
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
	markdownURL = regexp.MustCompile(`\]\((https?://[^()\t\n\f\r ]+)\)|<(https?://[^<>\t\n\f\r ]+)>`)
	bareURL     = regexp.MustCompile(`https?://[^\t\n\f\r <>"'()\[\]]+`)
)

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
