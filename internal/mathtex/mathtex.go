// Package mathtex typesets a formula written in TeX, for the editors to
// draw in a line of text.
package mathtex

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"sync"

	"github.com/caelyreth/untie"
	"github.com/caelyreth/untie/encode/raster"
	"github.com/caelyreth/untie/geometry"
	"github.com/caelyreth/untie/render"
	"github.com/caelyreth/untie/source"
)

// New Computer Modern Math, under the GUST Font License
// (font/GUST-FONT-LICENSE.txt).
//
//go:embed font/NewCMMath-Book.otf
var font []byte

var renderer = sync.OnceValue(func() *untie.Renderer {
	fonts, err := untie.LoadMathFont(font)
	if err != nil {
		return nil
	}
	r, err := untie.NewRenderer(fonts, untie.RendererOptions{DefaultDialect: untie.TeX, CacheEntries: 256})
	if err != nil {
		return nil
	}
	return r
})

// A Formula is a formula laid out at one size: Width wide, standing Ascent
// above the baseline of the line it is in and reaching Descent below it.
// Overflow is how far its picture (PNG) reaches past its nominal box on
// every side.
type Formula struct {
	Width, Ascent, Descent, Overflow float32
	box                              *untie.Box
}

type key struct {
	tex     string
	size    float32
	display bool
	width   float32
}

const kept = 512

var (
	mu    sync.Mutex
	cache = map[key]*Formula{}
)

// Scale is how large a formula is set beside text: its letters are smaller
// for their size than a text face's.
const Scale = 1.1

// Layout typesets tex as it stands in a line of text of the given size. It
// is false for what is no formula yet, as one half typed: the editors show
// its source then.
func Layout(tex string, size float32) (*Formula, bool) {
	return kept1(key{tex: tex, size: size * Scale})
}

// DisplayScale is how large a formula on lines of its own is set beside
// the text of its page.
const DisplayScale = 1.3

// Display typesets tex on lines of its own, as large as a formula block
// shows it in a page whose text is of the given size, broken into lines no
// wider than width where it has places to break, if width is positive.
func Display(tex string, size, width float32) (*Formula, bool) {
	return kept1(key{tex: strings.TrimSpace(tex), size: size * DisplayScale, display: true, width: max(width, 0)})
}

func kept1(k key) (*Formula, bool) {
	tex, size := k.tex, k.size
	if tex == "" || size <= 0 {
		return nil, false
	}
	mu.Lock()
	f, ok := cache[k]
	mu.Unlock()
	if ok {
		return f, f != nil
	}
	f = layout(k)
	mu.Lock()
	if len(cache) >= kept {
		clear(cache)
	}
	cache[k] = f
	mu.Unlock()
	return f, f != nil
}

func layout(k key) *Formula {
	r := renderer()
	if r == nil {
		return nil
	}
	req := untie.RenderRequest{Mode: untie.Inline, Style: untie.Style{Size: k.size}}
	if k.display {
		req.Mode, req.Style.MaxWidth = untie.Display, k.width
	}
	res, err := r.Render(context.Background(), k.tex, req)
	if err != nil || res == nil || res.Box == nil {
		return nil
	}
	for _, d := range res.Diagnostics {
		if d.Severity == source.SeverityError {
			return nil
		}
	}
	box := res.Box
	if box.Width <= 0 || box.Ascent+box.Descent <= 0 {
		return nil
	}
	f := &Formula{Width: box.Width, Ascent: box.Ascent, Descent: box.Descent, Overflow: max(box.Overflow, 0), box: box}
	// The box may be lower than its ink, as a fraction's is below the
	// baseline: the line makes room for all that is drawn.
	for _, s := range f.Shapes() {
		top, bottom := s.Rule.Y, s.Rule.Y+s.Rule.Height
		for i, seg := range s.Path {
			for _, p := range seg.Points[:points(seg.Op)] {
				if i == 0 && seg.Op == geometry.SegMoveTo && s.Rule == (geometry.Rect{}) {
					top, bottom = p.Y, p.Y
				}
				top, bottom = min(top, p.Y), max(bottom, p.Y)
			}
		}
		f.Ascent, f.Descent = max(f.Ascent, -top), max(f.Descent, bottom)
	}
	return f
}

func points(op geometry.SegOp) int {
	switch op {
	case geometry.SegMoveTo, geometry.SegLineTo:
		return 1
	case geometry.SegQuadTo:
		return 2
	case geometry.SegCubeTo:
		return 3
	}
	return 0
}

// A Shape is one filled piece of a formula, a path or a rule, placed with
// the formula's left edge on its baseline at the origin and y going down.
// Color is the color the formula gives it itself, if any.
type Shape struct {
	Path  geometry.Path
	Rule  geometry.Rect
	Color *color.NRGBA
}

// Shapes are the pieces to fill, in the order they are painted.
func (f *Formula) Shapes() []Shape {
	var out []Shape
	_ = render.Walk(f.box, func(c render.Command) error {
		var s Shape
		if c.Paint.HasExplicit {
			e := c.Paint.Explicit
			s.Color = &color.NRGBA{R: e.R, G: e.G, B: e.B, A: e.A}
		}
		switch c.Kind {
		case render.CommandPath:
			path, err := render.ResolvePath(c)
			if err != nil {
				return nil
			}
			s.Path = path
		case render.CommandRule:
			s.Rule = c.Rect
		default:
			return nil
		}
		out = append(out, s)
		return nil
	})
	return out
}

// SVG is the formula as an inline SVG element that takes the color of the
// text around it and scales with it: size is the text size the formula was
// laid out for. Its baseline is the text's.
func (f *Formula) SVG(size float32) string {
	o := f.Overflow
	em := func(v float32) string { return strconv.FormatFloat(float64(v/size), 'f', 3, 32) + "em" }
	num := func(v float32) string { return strconv.FormatFloat(float64(v), 'f', 2, 32) }
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" role="img" fill="currentColor" width="` + em(f.Width+2*o) +
		`" height="` + em(f.Ascent+f.Descent+2*o) + `" viewBox="` + num(-o) + " " + num(-f.Ascent-o) + " " +
		num(f.Width+2*o) + " " + num(f.Ascent+f.Descent+2*o) + `" style="vertical-align:` + em(-f.Descent-o) +
		`;margin:0 ` + em(-o) + `;overflow:visible">`)
	for _, s := range f.Shapes() {
		fill := ""
		if c := s.Color; c != nil {
			fill = fmt.Sprintf(` fill="#%02x%02x%02x"`, c.R, c.G, c.B)
			if c.A < 0xff {
				fill += ` fill-opacity="` + num(float32(c.A)/255) + `"`
			}
		}
		if s.Path == nil {
			r := s.Rule
			b.WriteString(`<rect x="` + num(r.X) + `" y="` + num(r.Y) + `" width="` + num(r.Width) + `" height="` + num(r.Height) + `"` + fill + `/>`)
			continue
		}
		b.WriteString(`<path d="`)
		for _, seg := range s.Path {
			b.WriteByte("MLQCZ"[seg.Op])
			for i, p := range seg.Points[:points(seg.Op)] {
				if i > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(num(p.X) + " " + num(p.Y))
			}
		}
		b.WriteString(`"` + fill + `/>`)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// PictureOrigin is where the formula's left edge on its baseline is in
// its picture, from the picture's top left corner.
func (f *Formula) PictureOrigin() (x, y float32) {
	return f.Overflow, f.box.Ascent + f.Overflow
}

// PNG draws the formula in ink on nothing, scale pixels to a point: an
// image of its box and Overflow more on every side.
func (f *Formula) PNG(scale float32, ink color.NRGBA) ([]byte, error) {
	opts := raster.DefaultOptions()
	opts.Scale, opts.Padding, opts.Palette.Ink = scale, 0, ink
	var b bytes.Buffer
	if err := raster.EncodePNG(&b, f.box, &opts); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
