// Package theme is Leaflet's look: the color tokens of the v1 design with a
// matching dark set, Figtree for the interface, Source Serif 4 for pages,
// and Lucide's icons.
package theme

import (
	"embed"
	"log"
	"strings"
	"sync"

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
	ChartMuted, SwitchOff, Inverse, OnInverse, Hover  ui.Color
	Shadow                                            ui.Color
	Code                                              ui.Color
	// Syntax colors of code blocks, after GitHub's.
	CodeKeyword, CodeString, CodeNumber, CodeComment ui.Color
	CodeFunction, CodeType, CodeTag                  ui.Color
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
	ChartMuted: ui.Hex("#9AA1AD"), SwitchOff: ui.Hex("#B8BDC7"), Inverse: ui.Hex("#16181D"), OnInverse: ui.Hex("#FFFFFF"),
	Hover: ui.RGBA(22, 24, 29, 0.05), Shadow: ui.RGBA(16, 18, 22, 0.10), Code: ui.Hex("#F1F2F5"),
	Separator:   ui.RGBA(0, 0, 0, 0.09),
	CodeKeyword: ui.Hex("#CF222E"), CodeString: ui.Hex("#0A3069"), CodeNumber: ui.Hex("#0550AE"), CodeComment: ui.Hex("#59636E"),
	CodeFunction: ui.Hex("#8250DF"), CodeType: ui.Hex("#953800"), CodeTag: ui.Hex("#116329"),
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
	ChartMuted: ui.Hex("#6B7280"), SwitchOff: ui.Hex("#4A505B"), Inverse: ui.Hex("#E9EBEF"), OnInverse: ui.Hex("#16181D"),
	Hover: ui.RGBA(233, 235, 239, 0.06), Shadow: ui.RGBA(0, 0, 0, 0.35), Code: ui.Hex("#23272E"),
	Separator:   ui.RGBA(255, 255, 255, 0.08),
	CodeKeyword: ui.Hex("#FF7B72"), CodeString: ui.Hex("#A5D6FF"), CodeNumber: ui.Hex("#79C0FF"), CodeComment: ui.Hex("#8B949E"),
	CodeFunction: ui.Hex("#D2A8FF"), CodeType: ui.Hex("#FFA657"), CodeTag: ui.Hex("#7EE787"),
}

// Of returns the palette for the window's appearance.
func Of(c *ui.Context) *Palette {
	if c.Theme().Dark {
		return &dark
	}
	return &light
}

// For returns the palette of an appearance.
func For(isDark bool) *Palette {
	if isDark {
		return &dark
	}
	return &light
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
