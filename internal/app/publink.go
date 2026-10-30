package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"log"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	codeeditor "github.com/egoist/mygo/ui/editor"
	"rsc.io/qr"

	"leaflet/internal/doc"
	"leaflet/internal/editor"
	"leaflet/internal/recording"
	"leaflet/internal/store"
	"leaflet/internal/theme"
	"leaflet/locales"
)

// A public link shares one page with anyone who has it, for as long as
// Leaflet keeps it: a server on this device's loopback has the page as a
// web page, and cloudflared opens it to the web through a Cloudflare Quick
// Tunnel, at a random trycloudflare.com address. The server keeps the page
// as it was last rendered, in memory, and never reads the database; the
// page renders again as it is saved. Its address has a secret path, so
// the bare hostname shows nothing.

// pubLink is the page shared through a public link; the app has one at a
// time. Its fields are the main thread's, but those under mu, which the
// server reads too.
type pubLink struct {
	page, token string
	// emails, when set, are who may open the link, by cloudflared's
	// --allowed-mail: each signs in with a code Cloudflare emails them.
	// They are set as the link starts and never change, so the server
	// reads them too.
	emails []string
	// state is "starting", "live" or "failed"; url is the link once live,
	// and err why it failed.
	state, url, err string
	// qr is url as a QR code, to open the link on a phone.
	qr  *ui.Bitmap
	cmd *exec.Cmd
	srv *http.Server
	// timer waits for saves to settle before the page renders again; seq
	// numbers the renders asked for.
	timer *time.Timer
	seq   int

	mu sync.Mutex
	// snap is the page as visitors see it, from the render numbered shown.
	snap  pubSnapshot
	shown int
	// host is the tunnel's hostname, once live, which the page posts from.
	host string
	// comments says whether visitors may comment; threads are the
	// threads visitors started through this link, the only ones they see.
	comments bool
	threads  map[string]bool
	// posts are when each visitor, by address, last commented, and total
	// how many comments came through this link.
	posts map[string][]time.Time
	total int
}

type pubSnapshot struct {
	html string
	// files are the pictures of this device the page shows, by name, and
	// embeds the embeds' HTML, by block.
	files  map[string]pubFile
	embeds map[string]string
	// blocks are the page's blocks, which a comment may be on, with their
	// text, which a comment's quote is from.
	blocks map[string]string
}

// pubFile is a file the shared page serves: a picture's data, or the
// path of a file or a recording's audio, which is read as it is asked
// for, a part at a time for a player. One with a name downloads as it.
type pubFile struct {
	typ  string
	data []byte
	path string
	name string
}

// pubJob is what a render needs from the main thread.
type pubJob struct {
	st                 *store.Store
	seq                int
	page, title, token string
	titles             map[string]string
	files              []store.File
	comments           bool
	threads            map[string]bool
	head               string
}

// cloudflaredInfo is what the app found of cloudflared.
type cloudflaredInfo struct {
	checked, busy bool
	path, version string
	// at is when the app last looked.
	at time.Time
}

// cloudflaredCommand installs cloudflared from a terminal, or is "" on a
// system with no one way to.
func cloudflaredCommand() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install cloudflared"
	case "windows":
		return "winget install --id Cloudflare.cloudflared"
	}
	return ""
}

// cloudflaredPrompt asks an AI agent that can run commands to install
// cloudflared on this system.
func cloudflaredPrompt() string {
	system := map[string]string{"darwin": "macOS", "windows": "Windows"}[runtime.GOOS]
	if system == "" {
		system = "Linux"
	}
	if cmd := cloudflaredCommand(); cmd != "" {
		return locales.PubInstallPrompt(system, cmd, cloudflaredDownloads)
	}
	return locales.PubInstallPromptGuide(system, cloudflaredDownloads)
}

// pubFavicon is the app's icon, its tile cropped from docs/icon/icon.svg,
// as the public page's favicon.
//
//go:embed favicon.png
var pubFavicon []byte

// cloudflaredMailVersion is the first cloudflared with --allowed-mail.
const cloudflaredMailVersion = "2026.9.3"

const (
	// Visitors comment at most this often, and this many times a link.
	pubPostsPerMinute = 5
	pubPostsPerLink   = 200
	pubMaxComment     = 4000
	pubMaxName        = 80
	// accessEmailHeader is the email Cloudflare Access verified, when the
	// link has emails.
	accessEmailHeader = "Cf-Access-Authenticated-User-Email"
)

// pubCSP lets the page run only its own scripts, and frame only the
// video players the app plays videos in, and its embeds, each of which is
// served at its own path, sandboxed off the network.
const pubCSP = "default-src 'none'; img-src 'self' https: data:; style-src 'unsafe-inline'; script-src 'self'; " +
	"frame-src 'self' https://www.youtube-nocookie.com https://player.vimeo.com; media-src 'self' https: http:; " +
	"connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// findCloudflared looks for cloudflared on the login shell's PATH, which
// an app opened from the Finder lacks, and where its installers put it,
// and asks it its version.
func findCloudflared() (path, version string) {
	name := "cloudflared"
	dirs := filepath.SplitList(loginPath())
	if runtime.GOOS == "windows" {
		name += ".exe"
		dirs = append(dirs, `C:\Program Files (x86)\cloudflared`, `C:\Program Files\cloudflared`)
	} else {
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	}
	for _, d := range dirs {
		if p := filepath.Join(d, name); d != "" && isExecutable(p) {
			path = p
			break
		}
	}
	if path == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, "--version").Output() // #nosec G204 -- cloudflared, found where it installs.
	return path, cloudflaredVersion(string(out))
}

var versionRe = regexp.MustCompile(`\b(\d{4}\.\d+\.\d+)\b`)

// cloudflaredVersion reads the version cloudflared --version says.
func cloudflaredVersion(out string) string {
	if m := versionRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// canRestrict says whether this cloudflared can keep a link to emails.
func (c cloudflaredInfo) canRestrict() bool {
	return c.version != "" && compareVersions(c.version, cloudflaredMailVersion) >= 0
}

var tunnelRe = regexp.MustCompile(`https://([a-z0-9-]+)\.trycloudflare\.com`)

// parseTunnelURL finds the Quick Tunnel's address in a line cloudflared
// logs, or returns "". Its API's address, in errors, is none.
func parseTunnelURL(line string) string {
	for _, m := range tunnelRe.FindAllStringSubmatch(line, -1) {
		if m[1] != "api" {
			return m[0]
		}
	}
	return ""
}

// qrBitmap is text as a QR code, one pixel a module, in black on white
// with its quiet zone of 4 modules all round; it is nil for text too long
// for one. The code's own Image leaves the zone out at the top and left.
func qrBitmap(text string) *ui.Bitmap {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil
	}
	const quiet = 4
	img := image.NewGray(image.Rect(0, 0, code.Size+2*quiet, code.Size+2*quiet))
	for y := range img.Rect.Dy() {
		for x := range img.Rect.Dx() {
			if !code.Black(x-quiet, y-quiet) {
				img.SetGray(x, y, color.Gray{Y: 0xff})
			}
		}
	}
	return ui.NewBitmap(img).Pixelated()
}

// splitEmails reads the emails field: addresses or *@domain, by commas,
// spaces or lines.
func splitEmails(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t' }) {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// validEmails reports whether each is an address or *@domain.
func validEmails(emails []string) bool {
	for _, e := range emails {
		local, domain, ok := strings.Cut(e, "@")
		if !ok || local == "" || !strings.Contains(domain, ".") || strings.ContainsAny(e, "\"'<>") {
			return false
		}
	}
	return true
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// checkCloudflared looks for cloudflared again, off the main thread.
func (a *App) checkCloudflared() {
	if a.cloudflared.busy {
		return
	}
	a.cloudflared.busy = true
	go func() {
		path, version := findCloudflared()
		a.update(func() { a.cloudflared = cloudflaredInfo{checked: true, path: path, version: version, at: time.Now()} })
	}()
}

// startPub shares page through a new public link, in place of any other.
// A new link for the page already shared, as for other emails or after a
// failure, keeps the threads visitors started through the old one.
func (a *App) startPub(page string, emails []string, comments bool) {
	threads := map[string]bool{}
	if old := a.pub; old != nil && old.page == page {
		old.mu.Lock()
		threads = maps.Clone(old.threads)
		old.mu.Unlock()
	}
	a.stopPub()
	p := &pubLink{page: page, token: randomToken(), emails: emails, state: "starting",
		comments: comments, threads: threads, posts: map[string][]time.Time{}}
	a.setPub(p)
	fail := func(err error) {
		p.state, p.err = "failed", err.Error()
		p.close()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err)
		return
	}
	p.srv = &http.Server{Handler: a.pubHandler(p), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = p.srv.Serve(ln) }()
	job := a.pubJob(p)
	go p.render(job)

	args := []string{"tunnel", "--no-autoupdate", "--url", "http://" + ln.Addr().String()}
	if len(emails) > 0 {
		args = append(args, "--allowed-mail", strings.Join(emails, ","))
	}
	cmd := exec.Command(a.cloudflared.path, args...) // #nosec G204 -- cloudflared, found where it installs, with arguments Leaflet writes.
	logs, err := cmd.StderrPipe()
	if err != nil {
		fail(err)
		return
	}
	if err := cmd.Start(); err != nil {
		fail(err)
		return
	}
	p.cmd = cmd
	go func() {
		// The last lines say why it stopped, if it does.
		var tail []string
		found := false
		sc := bufio.NewScanner(logs)
		for sc.Scan() {
			line := sc.Text()
			tail = append(tail, line)
			if len(tail) > 3 {
				tail = tail[1:]
			}
			if u := parseTunnelURL(line); u != "" && !found {
				found = true
				a.update(func() {
					if a.pub == p && p.state == "starting" {
						p.state, p.url = "live", u+"/"+p.token+"/"
						p.qr = qrBitmap(p.url)
						p.mu.Lock()
						p.host = strings.TrimPrefix(u, "https://")
						p.mu.Unlock()
					}
				})
			}
		}
		err := cmd.Wait()
		a.update(func() {
			if a.pub != p {
				return
			}
			msg := strings.TrimSpace(strings.Join(tail, "\n"))
			if msg == "" && err != nil {
				msg = err.Error()
			}
			p.state, p.err = "failed", msg
			p.close()
		})
	}()
	// A tunnel that has no address by now will not get one.
	time.AfterFunc(45*time.Second, func() {
		a.update(func() {
			if a.pub == p && p.state == "starting" && p.cmd != nil {
				_ = p.cmd.Process.Kill()
			}
		})
	})
}

// stopPub stops the public link: its address stops working at once.
func (a *App) stopPub() {
	if p := a.pub; p != nil {
		a.setPub(nil)
		p.close()
	}
}

func (p *pubLink) close() {
	if p.timer != nil {
		p.timer.Stop()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	if p.srv != nil {
		_ = p.srv.Close()
	}
}

// setPubComments lets visitors comment, or stops them, without a new link.
func (a *App) setPubComments(on bool) {
	p := a.pub
	if p == nil {
		return
	}
	p.mu.Lock()
	p.comments = on
	p.mu.Unlock()
	go p.render(a.pubJob(p))
}

// pubChanged renders the shared page again once a change to it settles,
// and stops sharing a page that went.
func (a *App) pubChanged(ch store.Change) {
	p := a.pub
	if p == nil {
		return
	}
	switch ch.Table {
	case "*", "pages", "files":
	case "page_contents", "comments", "recordings":
		if ch.PageID != "" && ch.PageID != p.page {
			return
		}
	default:
		return
	}
	if pg := a.pageByID(p.page); pg == nil || pg.Template {
		a.stopPub()
		return
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(300*time.Millisecond, func() {
		a.update(func() {
			if a.pub == p {
				go p.render(a.pubJob(p))
			}
		})
	})
}

// pubJob is what the next render of p needs, read on the main thread.
func (a *App) pubJob(p *pubLink) pubJob {
	p.seq++
	titles := map[string]string{}
	for _, pg := range a.pages {
		titles[pg.ID] = pg.DisplayTitle()
	}
	p.mu.Lock()
	threads, comments := maps.Clone(p.threads), p.comments
	p.mu.Unlock()
	return pubJob{st: a.st, seq: p.seq, page: p.page, title: a.pageTitleOf(p.page), token: p.token, titles: titles,
		files: slices.Clone(a.files), comments: comments, threads: threads, head: pubHead("/"+p.token+"/", comments)}
}

// render renders the page for visitors, off the main thread, unless a
// later render is already shown.
func (p *pubLink) render(job pubJob) {
	c := job.st.Content(job.page)
	if c == nil {
		c = doc.Empty()
	}
	localPath, read := ownFiles(job.st.Dir, job.files)
	files := map[string]pubFile{}
	names := map[string]string{}
	blocks := map[string]string{}
	c.Walk(func(n, _ *doc.Node) { blocks[n.ID] = squash(n.Delta().Plain()) })
	embeds := map[string]string{}
	threads := map[string][]store.Thread{}
	if len(job.threads) > 0 {
		all, _ := job.st.Threads(job.page)
		for _, t := range all {
			// A thread the owner resolved is done with.
			if job.threads[t.Root.ID] && !t.Root.Resolved {
				threads[t.Root.BlockID] = append(threads[t.Root.BlockID], t)
			}
		}
	}
	prefix := "/" + job.token + "/"
	page := doc.PageHTML(job.title, c, doc.HTMLOptions{
		Head: job.head,
		Image: func(n *doc.Node) string {
			local, ok := strings.CutPrefix(n.Str("url"), "file://")
			if !ok {
				return ""
			}
			// An uploaded file's url is a path on the device that uploaded
			// it: this device keeps its copy elsewhere.
			if lp := localPath(n.Str("file_id")); lp != "" {
				local = lp
			}
			ext := strings.ToLower(filepath.Ext(local))
			if !doc.ImageExts[ext] {
				return ""
			}
			if name, ok := names[local]; ok {
				return prefix + "f/" + name
			}
			data, err := read(local)
			if err != nil {
				return ""
			}
			name := strconv.Itoa(len(files)+1) + ext
			typ := mime.TypeByExtension(ext)
			if typ == "" {
				typ = http.DetectContentType(data)
			}
			files[name], names[local] = pubFile{typ: typ, data: data}, name
			return prefix + "f/" + name
		},
		Script: func(name string) string {
			// Visitors see the link's threads even once commenting stops.
			if name == "comments" && !job.comments && len(threads) == 0 {
				return ""
			}
			return prefix + name + ".js"
		},
		Tail: commentsPanel(c, threads, job.comments),
		Embed: func(n *doc.Node) string {
			src := n.Str("html")
			if strings.TrimSpace(src) == "" {
				return ""
			}
			embeds[n.ID] = src
			return prefix + "e/" + url.PathEscape(n.ID)
		},
		Code:        codeHTML,
		CalloutIcon: func(tone string) string { return calloutIcon(tone) },
		Icon:        theme.IconSVG,
		Chart:       chartHTML,
		Due:         dueText,
		File: func(n *doc.Node) (href, name, detail string) {
			name = n.Str("name")
			for _, f := range job.files {
				if f.ID != n.Str("file_id") {
					continue
				}
				name = f.Name
				path := localPath(f.ID)
				if _, err := os.Stat(path); path == "" || err != nil {
					break
				}
				served := strconv.Itoa(len(files)+1) + strings.ToLower(filepath.Ext(f.Name))
				files[served] = pubFile{typ: "application/octet-stream", path: path, name: f.Name}
				detail = humanSize(f.SizeBytes)
				if kind := strings.ToUpper(strings.TrimPrefix(filepath.Ext(f.Name), ".")); kind != "" {
					detail = locales.CardFileDetail(kind, detail)
				}
				return prefix + "f/" + served, name, detail
			}
			if name == "" {
				name = locales.CardFile()
			}
			return "", name, locales.CardNotOnDevice()
		},
		Recording: func(n *doc.Node) doc.RecordingView {
			rec, err := job.st.Recording(n.Str("recording_id"))
			v := doc.RecordingView{Title: recordingName(n, rec, err == nil, job.title)}
			if err != nil {
				return v
			}
			v.Length = fmtDuration(time.Duration(rec.DurationMs) * time.Millisecond)
			// Audio still being recorded, or not on this device, has no player.
			if path := rec.LocalPath; rec.Status != "recording" && inFolder(job.st.Dir, path) {
				if _, err := os.Stat(path); err == nil {
					served := strconv.Itoa(len(files)+1) + ".wav"
					files[served] = pubFile{typ: "audio/wav", path: path}
					v.Audio = prefix + "f/" + served
				}
			}
			for _, s := range recording.Tidy(rec.Transcript) {
				l := doc.TranscriptLine{Start: float64(s.StartMs) / 1000, Time: fmtDuration(time.Duration(s.StartMs) * time.Millisecond), Text: s.Text}
				if s.Speaker != "" {
					l.Speaker = locales.RecSpeaker(s.Speaker)
				}
				v.Lines = append(v.Lines, l)
			}
			return v
		},
		PageTitle: func(id string) string {
			if t, ok := job.titles[id]; ok {
				return t
			}
			return locales.CommonUntitled()
		},
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	if job.seq < p.shown {
		return
	}
	p.shown = job.seq
	p.snap = pubSnapshot{html: page, files: files, embeds: embeds, blocks: blocks}
}

// pubHead is the visitor page's look, with the app's icon under prefix,
// and its words for the comments script, which lets visitors comment
// while comments says they may.
func pubHead(prefix string, comments bool) string {
	var b strings.Builder
	b.WriteString(`<link rel="icon" type="image/png" href="` + html.EscapeString(prefix) + `favicon.png">`)
	if comments {
		b.WriteString(`<meta name="leaflet-open" content="1"><meta name="leaflet-icon" content="` + html.EscapeString(theme.IconSVG("message-square")) + `">`)
	}
	for _, s := range [][2]string{
		{"comment", locales.PubComment()}, {"reply", locales.PubReply()}, {"name", locales.PubName()},
		{"text", locales.PubText()}, {"send", locales.PubSend()}, {"cancel", locales.CommonCancel()},
		{"failed", locales.PubFailed()}, {"busy", locales.PubBusy()},
		{"full", locales.RenderedFullScreen()}, {"full-icon", theme.IconSVG("maximize-2")},
		{"exit", locales.RenderedExitFullScreen()}, {"exit-icon", theme.IconSVG("minimize-2")},
		{"fit", locales.RenderedZoomFit()}, {"fit-icon", theme.IconSVG("rotate-ccw")},
		{"out", locales.RenderedZoomOut()}, {"out-icon", theme.IconSVG("zoom-out")},
		{"in", locales.RenderedZoomIn()}, {"in-icon", theme.IconSVG("zoom-in")},
	} {
		b.WriteString(`<meta name="leaflet-` + s[0] + `" content="` + html.EscapeString(s[1]) + `">`)
	}
	b.WriteString(`<style>` + pubLook() + diagramCSS + commentsCSS + `</style>`)
	return b.String()
}

// pubLook is the visitor page in Leaflet's colors, light and dark: its
// palettes, MyGo's colors of code tokens, and the callouts' tints. It is
// written the first time a page is shared.
var pubLook = sync.OnceValue(func() string {
	vars := func(pal *theme.Palette) string {
		ct := editor.CodeTheme(pal)
		var b strings.Builder
		b.WriteString(":root{")
		for _, v := range [][2]any{
			{"fg", pal.Text}, {"muted", pal.TextMuted}, {"bg", pal.Surface}, {"line", pal.Border}, {"code", pal.Code},
			{"link", pal.Accent}, {"th", pal.Ground},
		} {
			b.WriteString("--" + v[0].(string) + ":" + cssColor(v[1].(ui.Color)) + ";")
		}
		for _, tone := range doc.CalloutTones {
			bg, border, fg, _ := editor.CalloutLook(tone, pal)
			b.WriteString("--" + tone + ":" + cssColor(bg) + ";--" + tone + "-line:" + cssColor(border) + ";--" + tone + "-icon:" + cssColor(fg) + ";")
		}
		b.WriteString("}")
		for k := codeeditor.TokenKind(1); k <= codeeditor.TokenInvalid; k++ {
			st := ct.Tokens[k]
			b.WriteString(".k" + strconv.Itoa(int(k)) + "{color:" + cssColor(ct.TokenColor(k)))
			if st.Italic {
				b.WriteString(";font-style:italic")
			}
			if st.Bold {
				b.WriteString(";font-weight:700")
			}
			b.WriteString("}")
		}
		return b.String()
	}
	return vars(theme.For(false)) + "\n@media (prefers-color-scheme:dark){" + vars(theme.For(true)) + "}\n"
})

// cssColor writes a color as CSS.
func cssColor(c ui.Color) string {
	if c.A == 255 {
		return hexOf(c)
	}
	return fmt.Sprintf("rgba(%d,%d,%d,%.3g)", c.R, c.G, c.B, float64(c.A)/255)
}

// codeHTML colors code with MyGo's highlighting of its language, as the
// app's code blocks are, each token in a class of its kind.
func codeHTML(code, language string) string {
	lang := codeeditor.LanguageNamed(language)
	if lang == nil || lang.Tokenizer == nil {
		return html.EscapeString(code)
	}
	var b strings.Builder
	var state uint64
	var tokens []codeeditor.Token
	for i, line := range strings.Split(code, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		tokens, state = lang.Tokenizer.Tokenize(line, state, tokens[:0])
		at := 0
		for _, t := range tokens {
			end := min(max(t.End, at), len(line))
			if text := line[at:end]; text != "" {
				if t.Kind == codeeditor.TokenText {
					b.WriteString(html.EscapeString(text))
				} else {
					b.WriteString(`<span class="k` + strconv.Itoa(int(t.Kind)) + `">` + html.EscapeString(text) + `</span>`)
				}
			}
			at = end
		}
		b.WriteString(html.EscapeString(line[at:]))
	}
	return b.String()
}

// calloutIcon is a callout tone's icon, as the app shows it.
func calloutIcon(tone string) string {
	_, _, _, icon := editor.CalloutLook(tone, theme.For(false))
	return theme.IconSVG(icon)
}

// pubCommentBody is a comment as the panel shows it: its Markdown's
// formatting, escaped, or that it was deleted.
func pubCommentBody(c store.Comment) string {
	if !c.DeletedAt.IsZero() {
		return html.EscapeString(commentBody(c))
	}
	return doc.FromMarkdown(c.Body).HTML()
}

// commentsPanel writes the comments visitors see: a badge for each block
// with threads, which the script sets in the margin by its block, and the
// Comments panel it opens, with the threads in the page's order.
func commentsPanel(c *doc.Content, threads map[string][]store.Thread, comments bool) string {
	if len(threads) == 0 {
		return ""
	}
	var badges, groups strings.Builder
	c.Walk(func(n, _ *doc.Node) {
		ts := threads[n.ID]
		if len(ts) == 0 {
			return
		}
		id := html.EscapeString(n.ID)
		badges.WriteString(`<button type="button" class="cbadge" data-for="` + id + `" aria-label="` +
			html.EscapeString(locales.EditorCommentCount(len(ts))) + `">` + theme.IconSVG("message-square") + strconv.Itoa(len(ts)) + `</button>`)
		groups.WriteString(`<section class="cgroup" data-for="` + id + `">`)
		if q := blockQuote(n); q != "" {
			groups.WriteString(`<p class="cquote">` + html.EscapeString(q) + `</p>`)
		}
		for _, t := range ts {
			groups.WriteString(`<div class="thread" id="t-` + html.EscapeString(t.Root.ID) + `">`)
			if t.Root.Quote != "" {
				groups.WriteString(`<p class="tquote">` + html.EscapeString(t.Root.Quote) + `</p>`)
			}
			for _, c := range t.All() {
				groups.WriteString(`<div class="c"><div class="who"><strong>` + html.EscapeString(c.AuthorName) + `</strong> <span class="muted">` +
					c.CreatedAt.Format("2006-01-02") + `</span></div><div>` + pubCommentBody(c) + `</div></div>`)
			}
			if comments {
				groups.WriteString(`<button type="button" class="creply" data-parent="` + html.EscapeString(t.Root.ID) + `">` +
					html.EscapeString(locales.PubReply()) + `</button>`)
			}
			groups.WriteString(`</div>`)
		}
		groups.WriteString(`</section>`)
	})
	return `<div id="cbadges" hidden>` + badges.String() + `</div>` +
		`<aside id="cpanel" class="cpanel" hidden aria-label="` + html.EscapeString(locales.PanelTabComments()) + `"><header><strong>` +
		html.EscapeString(locales.PanelTabComments()) + `</strong><button type="button" class="cclose" aria-label="` +
		html.EscapeString(locales.CommonClose()) + `">` + theme.IconSVG("x") + `</button></header><div class="clist">` +
		groups.String() + `</div></aside>`
}

// squash is text with its runs of spaces and lines as one space, as a
// browser's selection and a block's text both read.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// blockQuote is a line of a block's text, or its title, over its threads
// in the panel.
func blockQuote(n *doc.Node) string {
	q := n.Delta().Plain()
	for _, k := range []string{"title", "alt", "caption"} {
		if q == "" {
			q = n.Str(k)
		}
	}
	q = squash(q)
	if r := []rune(q); len(r) > 90 {
		q = string(r[:90]) + "…"
	}
	return q
}

// pubHandler serves the shared page under its secret path, and nothing
// else.
func (a *App) pubHandler(p *pubLink) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", pubCSP)
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		first, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if subtle.ConstantTimeCompare([]byte(first), []byte(p.token)) != 1 {
			http.NotFound(w, r)
			return
		}
		if rest == "comments" {
			a.pubComment(p, w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p.mu.Lock()
		snap := p.snap
		p.mu.Unlock()
		serve := func(typ, body string) {
			h.Set("Content-Type", typ)
			_, _ = w.Write([]byte(body))
		}
		switch {
		case rest == "":
			if snap.html == "" {
				http.Error(w, locales.PubStarting(), http.StatusServiceUnavailable)
				return
			}
			page := snap.html
			// A visitor Cloudflare signed in comments as their email.
			if email := p.visitorEmail(r); email != "" {
				page = strings.Replace(page, "</head>", `<meta name="leaflet-visitor" content="`+html.EscapeString(email)+`"></head>`, 1)
			}
			serve("text/html; charset=utf-8", page)
		case rest == "math.js":
			serve("text/javascript; charset=utf-8", katexJS+"\n"+mathScript)
		case rest == "diagram.js":
			serve("text/javascript; charset=utf-8", peleModule+"\n"+zoomScript+"\n"+diagramScript)
		case rest == "comments.js":
			serve("text/javascript; charset=utf-8", commentsScript)
		case rest == "embed.js":
			serve("text/javascript; charset=utf-8", embedScript)
		case rest == "recording.js":
			serve("text/javascript; charset=utf-8", recordingScript)
		case rest == "favicon.png":
			h.Set("Content-Type", "image/png")
			_, _ = w.Write(pubFavicon)
		case strings.HasPrefix(rest, "e/"):
			src, ok := snap.embeds[strings.TrimPrefix(rest, "e/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			// The embed runs in an opaque origin even when opened alone,
			// and reaches nothing but itself.
			h.Set("Content-Security-Policy", embedCSP+"; sandbox allow-scripts; frame-ancestors 'self'")
			serve("text/html; charset=utf-8", pubEmbedPage(src))
		case strings.HasPrefix(rest, "f/"):
			f, ok := snap.files[strings.TrimPrefix(rest, "f/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			h.Set("Content-Type", f.typ)
			if f.path == "" {
				_, _ = w.Write(f.data)
				return
			}
			file, err := os.Open(f.path) // #nosec G304 -- a file of the app's own.
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer func() { _ = file.Close() }()
			info, err := file.Stat()
			if err != nil {
				http.NotFound(w, r)
				return
			}
			// A file downloads, never opens as a page of the link's.
			if f.name != "" {
				h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.name}))
			}
			http.ServeContent(w, r, "", info.ModTime(), file)
		default:
			http.NotFound(w, r)
		}
	})
}

var (
	errPubStopped  = errors.New("the link stopped")
	errPubNoThread = errors.New("the thread was resolved or deleted")
)

// visitorEmail is the email Cloudflare Access verified for a request, or
// "": only a link kept to emails is behind Access, and on any other a
// visitor could send the header themselves.
func (p *pubLink) visitorEmail(r *http.Request) string {
	if len(p.emails) == 0 {
		return ""
	}
	return r.Header.Get(accessEmailHeader)
}

// pubComment takes a visitor's comment, or reply, and stores it on the
// main thread; the page renders again with it before the answer.
func (a *App) pubComment(p *pubLink, w http.ResponseWriter, r *http.Request) {
	// A refusal says why in the app's log, for its owner, quoted: much of
	// it is the visitor's.
	refuse := func(status int, why string) {
		log.Printf("public link: comment refused (%d): %q", status, why)
		http.Error(w, http.StatusText(status), status)
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		refuse(http.StatusMethodNotAllowed, r.Method)
		return
	}
	// Only the page itself posts: from the tunnel's address, or the one
	// the request came to, sending JSON, which another site's form cannot.
	p.mu.Lock()
	host := p.host
	p.mu.Unlock()
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host == "" || origin.Host != r.Host && origin.Host != host {
		refuse(http.StatusForbidden, "origin "+r.Header.Get("Origin")+" is not "+r.Host)
		return
	}
	if typ, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); typ != "application/json" {
		refuse(http.StatusUnsupportedMediaType, typ)
		return
	}
	var in struct{ Block, Parent, Name, Body, Quote string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil {
		refuse(http.StatusBadRequest, err.Error())
		return
	}
	in.Name, in.Body, in.Quote = strings.TrimSpace(in.Name), strings.TrimSpace(in.Body), squash(in.Quote)
	if email := p.visitorEmail(r); email != "" {
		in.Name = email
	}
	if in.Body == "" || in.Name == "" || utf8.RuneCountInString(in.Body) > pubMaxComment || utf8.RuneCountInString(in.Name) > pubMaxName {
		refuse(http.StatusBadRequest, "no name or text, or too long")
		return
	}
	visitor := r.Header.Get("Cf-Connecting-Ip")
	if visitor == "" {
		visitor, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	now := time.Now()
	p.mu.Lock()
	status, why := 0, ""
	switch {
	case !p.comments:
		status, why = http.StatusForbidden, "comments are off"
	case in.Parent != "":
		// A reply quotes nothing: its thread does.
		in.Quote = ""
		if !p.threads[in.Parent] {
			status, why = http.StatusBadRequest, "no thread "+in.Parent
		}
	default:
		text, ok := p.snap.blocks[in.Block]
		if !ok {
			status, why = http.StatusBadRequest, "no block "+in.Block
		}
		// A quote is text selected in the block: anything else is not.
		if in.Quote != "" && !strings.Contains(text, in.Quote) {
			in.Quote = ""
		}
	}
	if status == 0 {
		recent := slices.DeleteFunc(p.posts[visitor], func(t time.Time) bool { return now.Sub(t) > time.Minute })
		if len(recent) >= pubPostsPerMinute || p.total >= pubPostsPerLink {
			status, why = http.StatusTooManyRequests, "too many from "+visitor
		} else {
			p.posts[visitor] = append(recent, now)
			p.total++
		}
	}
	p.mu.Unlock()
	if status != 0 {
		refuse(status, why)
		return
	}

	type result struct {
		job pubJob
		err error
	}
	done := make(chan result, 1)
	a.update(func() {
		if a.pub != p {
			done <- result{err: errPubStopped}
			return
		}
		block := in.Block
		if in.Parent != "" {
			root, err := a.st.Comment(in.Parent)
			if err != nil {
				done <- result{err: err}
				return
			}
			// Visitors no longer see a thread the owner resolved or
			// deleted, and it takes no more of their replies.
			if root.Resolved || !root.DeletedAt.IsZero() {
				done <- result{err: errPubNoThread}
				return
			}
			block = root.BlockID
		}
		c, err := a.st.AddComment(p.page, block, in.Body, in.Parent, in.Quote, "guest", in.Name)
		if err == nil && in.Parent == "" {
			p.mu.Lock()
			p.threads[c.ID] = true
			p.mu.Unlock()
		}
		done <- result{job: a.pubJob(p), err: err}
	})
	select {
	case res := <-done:
		if res.err != nil {
			// A comment not stored counts against no one's limit.
			p.mu.Lock()
			if i := slices.Index(p.posts[visitor], now); i >= 0 {
				p.posts[visitor] = slices.Delete(p.posts[visitor], i, i+1)
			}
			p.total--
			p.mu.Unlock()
			status := http.StatusInternalServerError
			if errors.Is(res.err, errPubNoThread) {
				status = http.StatusBadRequest
			}
			refuse(status, res.err.Error())
			return
		}
		p.render(res.job)
		w.WriteHeader(http.StatusNoContent)
	case <-time.After(15 * time.Second):
		refuse(http.StatusServiceUnavailable, "the app did not answer")
	}
}

// pubEmbedPage is an embed's HTML for a visitor's sandboxed frame: in the
// visitor's light or dark, as embedPage gives it the app's, and telling
// the page how tall it is, so its frame grows to fit.
func pubEmbedPage(src string) string {
	meta := `<meta charset="utf-8"><meta name="color-scheme" content="light dark">` +
		`<script>document.documentElement.dataset.theme = matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";</script>` + embedFit
	if i := strings.Index(strings.ToLower(src), "<head>"); i >= 0 {
		return src[:i+6] + meta + src[i+6:]
	}
	return meta + src
}

// embedScript grows each embed's frame to the height its content says,
// as the app's embed blocks grow, from the height the block keeps up to
// 1200 pixels.
// recordingScript plays a recording from a line of its transcript, as a
// click on one does in the app, unless the click ends selecting text.
const recordingScript = `document.addEventListener("click", function (e) {
  var line = e.target.closest && e.target.closest(".recording [data-start]");
  var sel = getSelection();
  if (!line || sel && !sel.isCollapsed) return;
  var audio = line.closest(".recording").querySelector("audio");
  if (!audio) return;
  audio.currentTime = +line.dataset.start;
  audio.play();
});
`

const embedScript = `addEventListener("message", function (e) {
  var z = e.data && e.data.leafletSize;
  if (!z || typeof z[0] !== "number") return;
  document.querySelectorAll("figure.embed iframe").forEach(function (f) {
    if (f.contentWindow === e.source) f.style.height = Math.max(+f.dataset.height, Math.min(z[0], 1200)) + "px";
  });
});`

// mathScript draws the page's formulas with KaTeX, which comes before it.
const mathScript = `document.querySelectorAll(".math[data-tex]").forEach(function (el) {
  try { katex.render(el.dataset.tex, el, {displayMode: true, output: "mathml", throwOnError: true}); } catch (e) {}
});`

// diagramScript draws the page's diagrams with Pele and zooms them with
// zoomScript, which come before it. Each has a button that shows it alone,
// filling the window, to zoom and move about it with the app's bar.
const diagramScript = `{
  const word = (n) => document.querySelector('meta[name="leaflet-' + n + '"]')?.content || "";
  const button = (cls, label, icon) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = cls;
    b.title = label;
    b.setAttribute("aria-label", label);
    b.innerHTML = icon;
    return b;
  };
  let open = null, from = null;
  const leave = () => {
    if (!open) return;
    open.remove();
    open = null;
    document.documentElement.classList.remove("dfull-open");
    from.focus();
  };
  addEventListener("keydown", (e) => { if (e.key === "Escape") leave(); });
  document.querySelectorAll(".diagram[data-source]").forEach((el) => {
    try {
      el.innerHTML = peleRender(el.dataset.source, {fontFamily: "-apple-system, system-ui, sans-serif"}).svg;
      el.classList.add("pele");
    } catch (e) {
      return;
    }
    const full = button("dbtn", word("full"), word("full-icon"));
    full.onclick = () => {
      from = full;
      open = document.createElement("div");
      open.className = "dfull";
      open.setAttribute("role", "dialog");
      open.setAttribute("aria-modal", "true");
      open.setAttribute("aria-label", word("full"));
      const bar = document.createElement("div"), view = document.createElement("div"), box = document.createElement("div");
      bar.className = "dbar";
      view.className = "dview";
      box.className = "dbox";
      const fit = button("dbtn", word("fit"), word("fit-icon")), out = button("dbtn", word("out"), word("out-icon")),
        zin = button("dbtn", word("in"), word("in-icon")), exit = button("dbtn", word("exit"), word("exit-icon"));
      fit.hidden = true;
      bar.append(fit, out, zin, exit);
      // Pele sizes its drawing to the page's width; this one fills the view.
      const svg = el.querySelector("svg").cloneNode(true);
      svg.style.removeProperty("height");
      box.append(svg);
      view.append(box);
      open.append(bar, view);
      document.body.appendChild(open);
      document.documentElement.classList.add("dfull-open");
      const zoom = leafletZoom(view, box, view, true, (z) => { fit.hidden = !z; });
      fit.onclick = () => zoom("fit");
      out.onclick = () => zoom("out");
      zin.onclick = () => zoom("in");
      exit.onclick = leave;
      exit.focus();
    };
    el.appendChild(full);
  });
}`

// diagramCSS is the look of a diagram's full-window button, which shows
// over the diagram while it is pointed at, or under it on a touch screen,
// and of the diagram filling the window under its bar.
const diagramCSS = `.diagram{position:relative}
.dbtn{display:flex;padding:6px;border:1px solid var(--line);border-radius:6px;background:var(--bg);color:var(--muted);cursor:pointer}
.dbtn:hover{color:var(--fg)}
.dbtn svg{width:16px;height:16px}
.diagram>.dbtn{position:absolute;right:6px;bottom:6px;opacity:0;transition:opacity .15s}
.diagram:hover>.dbtn,.diagram>.dbtn:focus-visible{opacity:1}
@media (hover:none){.diagram>.dbtn{position:static;margin:4px 0 0 auto;opacity:1}}
.dfull{position:fixed;inset:0;z-index:40;display:flex;flex-direction:column;background:var(--bg);
padding:env(safe-area-inset-top) env(safe-area-inset-right) env(safe-area-inset-bottom) env(safe-area-inset-left)}
.dbar{display:flex;justify-content:flex-end;gap:6px;padding:12px 12px 0}
.dbtn[hidden]{display:none}
.dview{flex:1;min-height:0;overflow:hidden;touch-action:none}
.dview.zoomed{cursor:grab;-webkit-user-select:none;user-select:none}
.dview.dragging{cursor:grabbing}
.dbox{height:100%;box-sizing:border-box;padding:24px;display:flex;align-items:center;justify-content:center;transform-origin:0 0}
.dbox svg{width:100%;height:100%}
html.dfull-open{overflow:hidden}
`

const commentsCSS = `main{position:relative}
.cbtn,.cbadge{position:absolute;font:13px -apple-system,system-ui,sans-serif;white-space:nowrap;cursor:pointer}
.cbtn{display:none;padding:3px 9px;border-radius:6px;border:1px solid var(--line);background:var(--bg);color:var(--muted)}
.cbtn:hover{color:var(--fg)}
.cbtn.touch{position:fixed;z-index:25;left:50%;right:auto!important;top:auto!important;margin:0;
bottom:calc(16px + env(safe-area-inset-bottom));transform:translateX(-50%);padding:10px 20px;border-radius:999px;font-size:15px;
color:#fff;background:var(--link);border-color:var(--link);box-shadow:0 6px 20px rgba(0,0,0,.18)}
.cpicked{background:var(--code);border-radius:6px;box-shadow:0 0 0 4px var(--code)}
.cbadge{display:flex;align-items:center;gap:4px;padding:2px 7px;border-radius:999px;font-size:12px;font-weight:700;
border:1px solid var(--warning-line);background:var(--warning);color:var(--warning-icon)}
.cbadge:hover{background:var(--warning-line)}
.cbadge svg{width:12px;height:12px}
@media (min-width:900px){.cbtn,.cbadge{right:auto!important;left:100%;margin-left:8px}}
@media (max-width:899px){.cbadge{right:0}}
.cpop{position:absolute;z-index:20;width:300px;max-height:320px;overflow:auto;padding:4px 12px;font-size:.9em;
background:var(--bg);border:1px solid var(--line);border-radius:10px;box-shadow:0 8px 24px rgba(0,0,0,.14)}
.cpanel{position:fixed;top:0;right:0;bottom:0;z-index:30;width:min(360px,100%);display:flex;flex-direction:column;
background:var(--bg);border-left:1px solid var(--line);box-shadow:-8px 0 24px rgba(0,0,0,.08)}
.cpanel[hidden]{display:none}
.clist{overscroll-behavior:contain}
@media (max-width:600px){
.cpanel{width:100%;border-left:0;box-shadow:none}
html.cpanel-open{overflow:hidden}
.cbadged:not(figure):not(pre):not(.table){padding-right:48px}
}
.cpanel header{display:flex;align-items:center;justify-content:space-between;padding:12px 16px;border-bottom:1px solid var(--line)}
.cclose{display:flex;padding:4px;border:0;background:none;color:var(--muted);cursor:pointer}
.cclose:hover{color:var(--fg)}
.cclose svg{width:16px;height:16px}
.clist{overflow:auto;padding:4px 16px 24px}
.cgroup{padding:10px 0;border-bottom:1px solid var(--line)}
.cquote{margin:0 0 6px;padding-left:8px;border-left:3px solid var(--warning-line);color:var(--muted);font-size:.85em}
.tquote{margin:2px 0;padding:1px 6px;border-radius:4px;background:var(--warning);display:inline-block;font-size:.92em}
.cform .tquote{align-self:flex-start}
.csel{position:absolute;z-index:15;display:flex;align-items:center;gap:6px;padding:5px 10px;border-radius:8px;
border:1px solid var(--line);background:var(--bg);color:var(--fg);box-shadow:0 4px 14px rgba(0,0,0,.12);
font:13px -apple-system,system-ui,sans-serif;cursor:pointer}
.csel[hidden]{display:none}
.csel svg{width:14px;height:14px}
.thread{padding:4px 8px;margin:4px -8px;border-radius:6px;font-size:.92em;transition:background .6s}
.thread.flash{background:var(--warning)}
.thread .c{margin:.4em 0}
.who{font-size:.92em}
.creply,.cform button{font:13px -apple-system,system-ui,sans-serif;padding:4px 10px;border-radius:6px;border:1px solid var(--line);
background:var(--bg);color:var(--fg);cursor:pointer}
.cform{display:flex;flex-direction:column;gap:8px;margin:.6em 0 1em;padding:12px;border:1px solid var(--line);border-radius:8px}
.cform input,.cform textarea{font:15px -apple-system,system-ui,sans-serif;padding:6px 8px;border:1px solid var(--line);border-radius:6px;
background:var(--bg);color:var(--fg)}
.cform textarea{min-height:80px;resize:vertical}
.cform .row{display:flex;gap:8px;align-items:center}
.cform button[type=submit]{background:var(--link);border-color:var(--link);color:#fff}
@media (max-width:600px){.cform input,.cform textarea{font-size:16px}}`

// commentsScript sets a badge by each block with threads, which previews
// them while hovered and opens them in the Comments panel when clicked.
// Where visitors may comment, a button by the block under the pointer
// comments on it, and each thread has Reply.
const commentsScript = `(function () {
  function word(n) { var m = document.querySelector('meta[name="leaflet-' + n + '"]'); return m ? m.content : ""; }
  var main = document.querySelector("main");
  var token = location.pathname.split("/")[1];
  var visitor = word("visitor");
  var panel = document.getElementById("cpanel");
  var open = null;

  // A phone or tablet has no hover: a tap does what pointing does.
  var touch = matchMedia("(hover: none)").matches;

  function group(block) { return panel && panel.querySelector('.cgroup[data-for="' + CSS.escape(block) + '"]'); }

  // The panel opens at a block's threads, or at one thread.
  function showPanel(block, thread) {
    if (!panel) return;
    panel.hidden = false;
    document.documentElement.classList.add("cpanel-open");
    var at = thread ? document.getElementById("t-" + thread) : group(block);
    if (!at) return;
    at.scrollIntoView({block: "nearest"});
    var flash = at.classList.contains("thread") ? [at] : at.querySelectorAll(".thread");
    flash.forEach(function (t) { t.classList.add("flash"); setTimeout(function () { t.classList.remove("flash"); }, 1200); });
  }
  if (panel) panel.querySelector(".cclose").onclick = function () {
    panel.hidden = true;
    document.documentElement.classList.remove("cpanel-open");
  };

  var pop = document.createElement("div");
  pop.className = "cpop";
  pop.hidden = true;
  document.body.appendChild(pop);
  var hide = null;
  var badges = [];
  var holder = document.getElementById("cbadges");
  if (holder) {
    Array.prototype.slice.call(holder.children).forEach(function (b) {
      var el = document.getElementById("b-" + b.dataset.for);
      if (!el) return;
      main.appendChild(b);
      b.block = el;
      // A narrow page keeps room for it beside the block's text.
      el.classList.add("cbadged");
      badges.push(b);
      b.onclick = function () { pop.hidden = true; showPanel(b.dataset.for); };
      b.onmouseenter = function () {
        if (touch) return;
        clearTimeout(hide);
        pop.innerHTML = "";
        group(b.dataset.for).querySelectorAll(".thread").forEach(function (t) {
          var c = t.cloneNode(true);
          c.removeAttribute("id");
          c.querySelectorAll(".creply").forEach(function (r) { r.remove(); });
          pop.appendChild(c);
        });
        pop.hidden = false;
        var r = b.getBoundingClientRect(), w = document.documentElement.clientWidth;
        pop.style.top = (r.bottom + scrollY + 6) + "px";
        pop.style.left = (Math.max(8, Math.min(r.right - pop.offsetWidth, w - pop.offsetWidth - 8)) + scrollX) + "px";
      };
      b.onmouseleave = function () { hide = setTimeout(function () { pop.hidden = true; }, 200); };
    });
    holder.remove();
  }
  pop.onmouseenter = function () { clearTimeout(hide); };
  pop.onmouseleave = function () { pop.hidden = true; };
  // A block in a closed toggle or folded section shows where it is closed.
  function shown(el) {
    for (var d = el.parentElement && el.parentElement.closest("details:not([open])"); d; d = d.parentElement && d.parentElement.closest("details:not([open])")) el = d;
    return el;
  }
  function place() {
    var m = main.getBoundingClientRect();
    badges.forEach(function (b) { b.style.top = (shown(b.block).getBoundingClientRect().top - m.top) + "px"; });
  }
  place();
  addEventListener("load", place);
  if (window.ResizeObserver) new ResizeObserver(place).observe(main);

  function form(after, inside, block, parent, quote) {
    if (open) open.remove();
    var f = document.createElement("form");
    f.className = "cform";
    if (quote) {
      var q = document.createElement("p");
      q.className = "tquote";
      q.textContent = quote;
      f.appendChild(q);
    }
    var name = null;
    if (!visitor) {
      name = document.createElement("input");
      name.placeholder = word("name");
      name.maxLength = 80;
      name.required = true;
      try { name.value = localStorage.getItem("leaflet-name") || ""; } catch (e) {}
      f.appendChild(name);
    }
    var text = document.createElement("textarea");
    text.placeholder = word("text");
    text.maxLength = 4000;
    text.required = true;
    f.appendChild(text);
    var row = document.createElement("div");
    row.className = "row";
    var send = document.createElement("button");
    send.type = "submit";
    send.textContent = word("send");
    var cancel = document.createElement("button");
    cancel.type = "button";
    cancel.textContent = word("cancel");
    cancel.onclick = function () { f.remove(); open = null; };
    var note = document.createElement("span");
    note.className = "muted";
    row.append(send, cancel, note);
    f.appendChild(row);
    f.onsubmit = function (e) {
      e.preventDefault();
      send.disabled = true;
      note.textContent = "";
      var who = name ? name.value.trim() : visitor;
      fetch("/" + token + "/comments", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify({block: block, parent: parent, name: who, body: text.value, quote: quote || ""})
      }).then(function (r) {
        if (!r.ok) throw r.status;
        try { if (name) localStorage.setItem("leaflet-name", who); } catch (e) {}
        // The page comes back with the panel open where the comment is.
        location.hash = parent ? "t-" + parent : "c-" + block;
        location.reload();
      }).catch(function (status) {
        send.disabled = false;
        note.textContent = status === 429 ? word("busy") : word("failed");
      });
    };
    if (inside) after.appendChild(f); else after.after(f);
    open = f;
    (name && !name.value ? name : text).focus();
  }

  if (word("open") === "1") {
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "cbtn";
    btn.textContent = word("comment");
    main.appendChild(btn);
    var over = null;
    if (touch) btn.classList.add("touch");
    function offer(el) {
      if (over) over.classList.remove("cpicked");
      over = el;
      if (touch) {
        // The block tapped shows tinted, with Comment at the foot of the
        // screen, clear of the text.
        el.classList.add("cpicked");
        btn.style.display = "block";
        return;
      }
      var r = el.getBoundingClientRect(), m = main.getBoundingClientRect();
      // Below the block's badge, if it has one.
      var badged = badges.some(function (b) { return b.block === el; });
      btn.style.top = (r.top - m.top + (badged ? 30 : 0)) + "px";
      btn.style.right = "0px";
      btn.style.display = "block";
    }
    function blockOf(t) {
      if (t === btn || btn.contains(t) || t.closest(".cbadge")) return null;
      var el = t.closest("[data-block]");
      return el && !el.closest(".cform") ? el : null;
    }
    if (touch) {
      // A tap on a block, not on a link or control in it, offers Comment.
      main.addEventListener("click", function (e) {
        if (e.target === btn) return;
        var el = !e.target.closest("a, button, input, textarea, summary, video, iframe") && blockOf(e.target);
        if (el && el !== over) offer(el);
        else {
          btn.style.display = "none";
          if (over) over.classList.remove("cpicked");
          over = null;
        }
      });
    } else {
      main.addEventListener("mouseover", function (e) {
        var el = blockOf(e.target);
        if (el) offer(el);
      });
    }
    btn.onclick = function () {
      if (!over) return;
      over.classList.remove("cpicked");
      form(over, over.tagName === "LI", over.dataset.block, "");
      btn.style.display = "none";
      over = null;
    };
    document.querySelectorAll(".creply").forEach(function (b) {
      b.onclick = function () { form(b.parentElement, true, "", b.dataset.parent); };
    });

    // Text selected in one block can be commented on, as in the app.
    var sel = document.createElement("button");
    sel.type = "button";
    sel.className = "csel";
    sel.hidden = true;
    sel.innerHTML = word("icon");
    sel.appendChild(document.createTextNode(word("comment")));
    document.body.appendChild(sel);
    var picked = null;
    function selected() {
      var s = getSelection();
      if (!s.rangeCount || s.isCollapsed) return null;
      var r = s.getRangeAt(0);
      var at = function (n) { return (n.nodeType === 1 ? n : n.parentElement).closest("[data-block]"); };
      var el = at(r.startContainer);
      if (!el || el !== at(r.endContainer) || !main.contains(el) || el.closest(".cform")) return null;
      var text = s.toString().replace(/\s+/g, " ").trim();
      return text ? {el: el, text: text.slice(0, 500), rect: r.getBoundingClientRect()} : null;
    }
    document.addEventListener("selectionchange", function () {
      picked = selected();
      if (!picked) { sel.hidden = true; return; }
      sel.hidden = false;
      // Above the selection, or below it where the system's own menu
      // shows above, on a phone or tablet.
      var y = touch ? picked.rect.bottom + 12 : picked.rect.top - sel.offsetHeight - 8;
      sel.style.top = (y + scrollY) + "px";
      sel.style.left = (Math.max(8, picked.rect.left + picked.rect.width / 2 - sel.offsetWidth / 2) + scrollX) + "px";
    });
    sel.addEventListener("mousedown", function (e) { e.preventDefault(); });
    sel.onclick = function () {
      if (!picked) return;
      var p = picked;
      sel.hidden = true;
      getSelection().removeAllRanges();
      form(p.el, p.el.tagName === "LI", p.el.dataset.block, "", p.text);
    };
  }

  var h = "";
  try { h = decodeURIComponent(location.hash); } catch (e) {}
  if (h.indexOf("#t-") === 0) showPanel("", h.slice(3));
  else if (h.indexOf("#c-") === 0) showPanel(h.slice(3));
})();`
