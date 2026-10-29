// Package theme is Leaflet's look: the color tokens of the v1 design with a
// matching dark set and a sepia one, in a choice of accents, Figtree for the interface, Source Serif 4 for pages
// unless the user chose other fonts, and Lucide's icons.
package theme

import (
	"embed"
	"log"
	"strings"
	"sync"
	"unicode"

	"github.com/egoist/mygo/ui"
)

//go:embed fonts/*.ttf
var fonts embed.FS

//go:embed icons/*.svg
var icons embed.FS

// UIFont and PageFont are the families of the interface and of pages.
const (
	UIFont   = "Figtree"
	PageFont = "Source Serif 4"
)

// Palette is the color tokens. Separator is a line on the sidebar, whose
// translucent material shows a solid light line as a white stripe.
type Palette struct {
	Ground, Sidebar, Surface, Panel                   ui.Color
	Border, Divider, Separator                        ui.Color
	Text, TextSecondary, TextMuted, Placeholder, Icon ui.Color
	Accent, OnAccent, AccentSoft, AccentSoftBorder    ui.Color
	AccentText, NavSelected, AgentSection, Selection  ui.Color
	WarnBg, WarnBorder, WarnText                      ui.Color
	OkBg, OkBorder, OkText                            ui.Color
	DangerBg, DangerBorder, Danger, DangerText        ui.Color
	ChartMuted, Inverse, OnInverse, Hover             ui.Color
	Shadow                                            ui.Color
	// Code is the card of code blocks; MyGo's code theme colors the code.
	Code ui.Color
	// Dark is the dark appearance's; Opaque paints the sidebar over the
	// window's material.
	Dark, Opaque bool
}

var light = Palette{
	Ground: ui.Hex("#F5F6F8"), Sidebar: ui.Hex("#EDEFF3"), Surface: ui.Hex("#FFFFFF"), Panel: ui.Hex("#FAFAFB"),
	Border: ui.Hex("#DDE0E6"), Divider: ui.Hex("#E6E8EC"),
	Text: ui.Hex("#16181D"), TextSecondary: ui.Hex("#3D424C"), TextMuted: ui.Hex("#565C67"), Placeholder: ui.Hex("#6B717C"), Icon: ui.Hex("#4A505B"),
	Accent: ui.Hex("#3446C9"), OnAccent: ui.Hex("#FFFFFF"), AccentSoft: ui.Hex("#ECEEFC"), AccentSoftBorder: ui.Hex("#C9D0F3"),
	AccentText: ui.Hex("#2A3699"), NavSelected: ui.Hex("#DFE3F6"), AgentSection: ui.Hex("#F1F3FE"), Selection: ui.Hex("#D9DEF8"),
	WarnBg: ui.Hex("#FDF3E7"), WarnBorder: ui.Hex("#F3DDC2"), WarnText: ui.Hex("#7A3A08"),
	OkBg: ui.Hex("#F1F8F3"), OkBorder: ui.Hex("#D5E5DA"), OkText: ui.Hex("#1E5A35"),
	DangerBg: ui.Hex("#FFF7F7"), DangerBorder: ui.Hex("#F0C9C9"), Danger: ui.Hex("#C62828"), DangerText: ui.Hex("#A61B1B"),
	ChartMuted: ui.Hex("#9AA1AD"), Inverse: ui.Hex("#16181D"), OnInverse: ui.Hex("#FFFFFF"),
	Hover: ui.RGBA(22, 24, 29, 0.05), Shadow: ui.RGBA(16, 18, 22, 0.10), Code: ui.Hex("#F1F2F5"),
	Separator: ui.RGBA(0, 0, 0, 0.09),
}

var dark = Palette{
	Ground: ui.Hex("#101216"), Sidebar: ui.Hex("#16191E"), Surface: ui.Hex("#1B1E24"), Panel: ui.Hex("#181B20"),
	Border: ui.Hex("#2E333C"), Divider: ui.Hex("#272B33"),
	Text: ui.Hex("#E9EBEF"), TextSecondary: ui.Hex("#C0C5CE"), TextMuted: ui.Hex("#9EA4AF"), Placeholder: ui.Hex("#8A909B"), Icon: ui.Hex("#A9AFBA"),
	Accent: ui.Hex("#4F5FD9"), OnAccent: ui.Hex("#FFFFFF"), AccentSoft: ui.Hex("#242A4D"), AccentSoftBorder: ui.Hex("#3A4377"),
	AccentText: ui.Hex("#BAC3FF"), NavSelected: ui.Hex("#283058"), AgentSection: ui.Hex("#1E2342"), Selection: ui.Hex("#34407A"),
	WarnBg: ui.Hex("#34261A"), WarnBorder: ui.Hex("#5A4127"), WarnText: ui.Hex("#F2C493"),
	OkBg: ui.Hex("#16291E"), OkBorder: ui.Hex("#29483A"), OkText: ui.Hex("#93D6AA"),
	DangerBg: ui.Hex("#341B1B"), DangerBorder: ui.Hex("#5C2D2D"), Danger: ui.Hex("#E25B5B"), DangerText: ui.Hex("#FFA3A3"),
	ChartMuted: ui.Hex("#6B7280"), Inverse: ui.Hex("#E9EBEF"), OnInverse: ui.Hex("#16181D"),
	Hover: ui.RGBA(233, 235, 239, 0.06), Shadow: ui.RGBA(0, 0, 0, 0.35), Code: ui.Hex("#23272E"),
	Separator: ui.RGBA(255, 255, 255, 0.08), Dark: true,
}

// sepia is the light appearance on warm paper, for long reading. Its
// sidebar is painted over the window's material, which would show gray.
var sepia = Palette{
	Ground: ui.Hex("#F1E8D6"), Sidebar: ui.Hex("#ECE2CD"), Surface: ui.Hex("#F8F1E3"), Panel: ui.Hex("#F4ECDC"),
	Border: ui.Hex("#DCCFB6"), Divider: ui.Hex("#E5D9C3"),
	Text: ui.Hex("#3B2A1A"), TextSecondary: ui.Hex("#5A4632"), TextMuted: ui.Hex("#6B5743"), Placeholder: ui.Hex("#806B55"), Icon: ui.Hex("#67533F"),
	WarnBg: ui.Hex("#F8E8D2"), WarnBorder: ui.Hex("#EBD2B0"), WarnText: ui.Hex("#7A3A08"),
	OkBg: ui.Hex("#EAEEDB"), OkBorder: ui.Hex("#D2DBBC"), OkText: ui.Hex("#2D5A28"),
	DangerBg: ui.Hex("#F8E6DE"), DangerBorder: ui.Hex("#EBC6B8"), Danger: ui.Hex("#B8321F"), DangerText: ui.Hex("#9A2414"),
	ChartMuted: ui.Hex("#A8987F"), Inverse: ui.Hex("#3B2A1A"), OnInverse: ui.Hex("#F8F1E3"),
	Hover: ui.RGBA(59, 42, 26, 0.06), Shadow: ui.RGBA(60, 40, 20, 0.12), Code: ui.Hex("#EFE5D2"),
	Separator: ui.RGBA(80, 50, 20, 0.12), Opaque: true,
}

func init() { sepia = withAccent(sepia, light.Accent) }

// Accents are the accent colors to choose from, by name, each light and
// dark; Indigo is Leaflet's own.
var Accents = []struct {
	Name        string
	Light, Dark ui.Color
}{
	{"Indigo", light.Accent, dark.Accent},
	{"Blue", ui.Hex("#1F6FD1"), ui.Hex("#3B82F6")},
	{"Purple", ui.Hex("#7A3FC2"), ui.Hex("#8B5CF6")},
	{"Pink", ui.Hex("#B8256A"), ui.Hex("#D9468A")},
	{"Orange", ui.Hex("#B8500F"), ui.Hex("#D2661F")},
	{"Green", ui.Hex("#1E7A4C"), ui.Hex("#2E8F5E")},
	{"Graphite", ui.Hex("#525866"), ui.Hex("#6E7583")},
}

// look is what Look chose, which every palette follows.
var look struct {
	sepia  bool
	accent string
}

// Look chooses sepia paper for the light appearance and the accent: a
// name of Accents, "system" for the desktop's, or "" for Indigo.
func Look(isSepia bool, accent string) {
	look.sepia, look.accent = isSepia, accent
}

// Accent returns the accent named, light or dark, with system the
// desktop's; ok is false where there is none, as for Indigo.
func Accent(name string, isDark bool, system ui.Color) (ui.Color, bool) {
	if name == "system" {
		if system.A == 0 {
			return ui.Color{}, false
		}
		system.A = 255
		if isDark {
			return system.Mix(ui.Hex("#FFFFFF"), 0.12), true
		}
		return system, true
	}
	for _, a := range Accents[1:] {
		if a.Name == name {
			if isDark {
				return a.Dark, true
			}
			return a.Light, true
		}
	}
	return ui.Color{}, false
}

type paletteKey struct {
	dark, sepia bool
	accent      ui.Color
}

// palettes are the palettes made, kept so that each look has one.
var palettes = map[paletteKey]*Palette{}

// Of returns the palette for the window's appearance and the look.
func Of(c *ui.Context) *Palette {
	isDark := c.Theme().Dark
	accent, _ := Accent(look.accent, isDark, c.Preferences().Accent)
	k := paletteKey{isDark, look.sepia && !isDark, accent}
	if p, ok := palettes[k]; ok {
		return p
	}
	p := *For(isDark)
	if k.sepia {
		p = sepia
	}
	if accent.A > 0 {
		p = withAccent(p, accent)
	}
	palettes[k] = &p
	return &p
}

// For returns Leaflet's own palette of an appearance.
func For(isDark bool) *Palette {
	if isDark {
		return &dark
	}
	return &light
}

// withAccent returns p in accent, its tints mixed from its surface as
// Indigo's are, and the text on it dark where white would not stand out.
func withAccent(p Palette, accent ui.Color) Palette {
	tint := func(light, dark float32) ui.Color {
		if p.Dark {
			return p.Surface.Mix(accent, dark)
		}
		return p.Surface.Mix(accent, light)
	}
	p.Accent = accent
	p.AccentSoft, p.AccentSoftBorder = tint(0.09, 0.2), tint(0.27, 0.5)
	p.NavSelected, p.AgentSection, p.Selection = tint(0.16, 0.28), tint(0.06, 0.13), tint(0.19, 0.45)
	if p.Dark {
		p.AccentText = accent.Mix(ui.Hex("#FFFFFF"), 0.6)
	} else {
		p.AccentText = accent.Mix(p.Text, 0.25)
	}
	p.OnAccent = ui.Hex("#FFFFFF")
	if 0.299*float32(accent.R)+0.587*float32(accent.G)+0.114*float32(accent.B) > 165 {
		p.OnAccent = ui.Hex("#18181B")
	}
	return p
}

// Apply sets the window theme from the palette, keeping what follows the
// system (its appearance and the desktop's preferences).
func Apply(c *ui.Context) {
	p := Of(c)
	t := *c.Theme()
	t.Background = p.Surface
	t.Surface = p.Ground
	t.SurfaceHover = p.Hover
	t.SurfacePressed = p.Divider
	t.Border = p.Border
	t.Text = p.Text
	t.TextMuted = p.TextMuted
	t.Accent = p.Accent
	t.AccentHover = p.Accent.Mix(p.Text, 0.12)
	t.AccentPressed = p.Accent.Mix(p.Text, 0.22)
	t.AccentText = p.OnAccent
	t.Danger = p.Danger
	t.Selection = p.Selection
	t.Inverse, t.InverseText = p.Inverse, p.OnInverse
	t.Radius = 8
	t.FontSize = 14
	t.Font = UIFont
	c.SetTheme(&t)
}

// pageFonts is what SetFonts chose, which pages are set in.
var pageFonts struct{ latin, chinese, japanese, code string }

// SetFonts chooses the families of pages: latin for their text, chinese
// and japanese for the characters of those languages, and code for code,
// each "" for Leaflet's own.
func SetFonts(latin, chinese, japanese, code string) {
	pageFonts.latin, pageFonts.chinese, pageFonts.japanese, pageFonts.code = latin, chinese, japanese, code
}

// PageFamily returns the family list of page text: the Latin font, then
// the font of the language text's Han characters are in, Japanese where it
// has kana and Chinese otherwise, when one is chosen; the system's fonts
// draw what they lack.
func PageFamily(text string) string {
	return family(pageFonts.latin, PageFont, text)
}

// CodeFamily returns the family list of code, as PageFamily, with the
// code font first.
func CodeFamily(text string) string {
	return family(pageFonts.code, "monospace", text)
}

func family(first, fallback, text string) string {
	if first == "" {
		first = fallback
	}
	cjk := pageFonts.chinese
	if hasKana(text) {
		cjk = pageFonts.japanese
	}
	if cjk == "" {
		return first
	}
	return first + ", " + cjk
}

// hasKana reports whether text has Hiragana or Katakana, which only
// Japanese writes.
func hasKana(text string) bool {
	for _, r := range text {
		if r >= 0x3040 && unicode.In(r, unicode.Hiragana, unicode.Katakana) {
			return true
		}
	}
	return false
}

var registerOnce sync.Once

// RegisterFonts registers the bundled fonts; call it once at startup.
func RegisterFonts() {
	registerOnce.Do(func() {
		for _, f := range []struct{ file, family string }{
			{"fonts/Figtree.ttf", UIFont},
			{"fonts/Figtree-Italic.ttf", UIFont},
			{"fonts/SourceSerif4.ttf", PageFont},
			{"fonts/SourceSerif4-Italic.ttf", PageFont},
		} {
			data, err := fonts.ReadFile(f.file)
			if err != nil {
				log.Fatal(err)
			}
			if err := ui.RegisterFont(data, f.family); err != nil {
				log.Printf("font %s: %v", f.file, err)
			}
		}
	})
}

var (
	iconMu    sync.Mutex
	iconCache = map[string]*ui.SVG{}
)

// Icon returns a Lucide icon by name, as "file-text".
func Icon(name string) *ui.SVG {
	iconMu.Lock()
	defer iconMu.Unlock()
	if s, ok := iconCache[name]; ok {
		return s
	}
	data, err := icons.ReadFile("icons/" + name + ".svg")
	if err != nil {
		log.Printf("icon %s: %v", name, err)
		data, _ = icons.ReadFile("icons/circle-alert.svg")
	}
	s := ui.MustParseSVG(data)
	iconCache[name] = s
	return s
}

// IconNames lists the bundled icons.
func IconNames() []string {
	entries, _ := icons.ReadDir("icons")
	var out []string
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".svg"))
	}
	return out
}
