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

// maxPageBytes is the most of a page read from the web: a long article
// whole, and no more than reading it takes a moment for, on the device
// and on the server alike (page.ts MAX_PAGE_BYTES).
const maxPageBytes = 2 << 20

// pageAccept is what Read asks for: a page, else other text.
const pageAccept = "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5"

// ErrNotText is Read's for what is not a page of text, as a picture.
var ErrNotText = errors.New("not a page of text")

// Read reads a page for an agent from the public web only, as Fetch does:
// its text, when it is HTML or other text. It takes as long as Fetch may,
// however long the agent's run has left.
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
	// What is not text is not read, as its Content-Type says, or else its
	// first bytes, as a browser tells.
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
	// Text in another encoding than UTF-8 is read in it, as a browser reads
	// it: by its byte order mark, its Content-Type, its own <meta>, or else
	// as UTF-8 if it is that.
	enc, _, _ := charset.DetermineEncoding(data, ctype)
	if decoded, err := enc.NewDecoder().Bytes(data); err == nil {
		data = decoded
	}
	src := strings.ToValidUTF8(string(data), "�")
	if mt == "text/html" || mt == "application/xhtml+xml" {
		return readHTML(ctx, final, src)
	}
	t, cut := truncate(strings.TrimSpace(src))
	return Page{URL: final, Text: t, Truncated: cut, Links: urlsOf(t)}, nil
}

// readable reports whether a media type is text an agent reads.
func readable(mt string) bool {
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json") ||
		mt == "application/xml" || strings.HasSuffix(mt, "+xml")
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
// being written in parts, to go after prefix (a heading's #s, a list
// item's -) once it has text. Each piece of the page is read once, so a
// page takes as long as it is long, and the reading stops once the text
// has gone on past MaxPageText for good, as no more of it is read, or
// once ctx ends.
type pageReader struct {
	ctx  context.Context
	base *url.URL
	self string
	out  strings.Builder
	// outRunes are the characters in out, and outText those up to its
	// last that is not a line break.
	outRunes, outText int
	// parts are the line, piece by piece: a link holds a piece of its own
	// before its text, for its [ once it ends in the line.
	parts []string
	// links are the links being read, outermost first; in the first
	// decided of them a line has ended, so each ends in a line of its own.
	links   []openLink
	decided int
	// marks counts the line's characters that are not spaces; space is a
	// line that ends in one, and empty one with nothing in it.
	marks        int
	space, empty bool
	prefix       string
	pre          int
	// read are hasText's answers, each element asked once.
	read map[*html.Node]bool
	// nodes counts the nodes walked, for how often ctx is asked; done
	// stops the walk, with err when ctx ended it.
	nodes int
	done  bool
	err   error
}

// openLink is a link being read: its piece in the line, and the line's
// marks before it.
type openLink struct{ at, marks int }

// ReadHTML reads a page's HTML, page being where it was read, which its
// links are made whole against.
func ReadHTML(page, src string) Page {
	p, _ := readHTML(context.Background(), page, src)
	return p
}

// readHTML is ReadHTML until ctx ends, its parsing too, which takes a
// page whose elements are nested deep a while for each of its tags. HTML
// the parser refuses, as a page of elements nested more than 512 deep,
// has no text, as the server reads it (page.ts parseHtml).
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

// ctxReader reads from r until ctx ends.
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
		r.writeOut("---\n")
		return
	case "img":
		return
	}
	// An element with no text, as a link around a picture or an empty
	// box, breaks no line and says nothing.
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

// hasText reports whether n has text to read: a text node of more than
// spaces, or an element with one in it that is read.
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

// anchor writes a link's text as Markdown's [text](link). One whose text
// runs over lines, as a card's, is followed by a line of its link alone.
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
	// The text goes in the brackets without the spaces it starts and ends
	// with, a space of which stays outside them.
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

// add puts s at the end of the line.
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

// flush ends the line, when it has text, after its prefix.
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

// newLine starts the next line, after one written, which decides the
// links open in it, or after one with no text, from which each link open
// in it keeps its piece, the text it has being all the line's.
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

// writeOut adds s to the finished lines, the line it ends begun anew.
func (r *pageReader) writeOut(s string) {
	r.out.WriteString(s)
	if t := strings.TrimRight(s, "\n"); t != "" {
		r.outText = r.outRunes + utf8.RuneCountInString(t)
	}
	r.outRunes += utf8.RuneCountInString(s)
	r.check()
}

// check stops the reading once the text has gone on past MaxPageText
// whatever comes next: once out has, or out and the line, after its
// prefix, with the characters other than spaces it has before any link
// still open, which nothing that comes moves or takes away.
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
