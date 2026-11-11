package editor

import (
	"sync"

	"github.com/caelyreth/untie/geometry"
	"github.com/egoist/mygo/ui"

	"leaflet/internal/mathtex"
)

type mathPiece struct {
	path  *ui.Path
	color *ui.Color
}

var mathPieces sync.Map // *mathtex.Formula -> []mathPiece

// piecesOf are a formula's shapes as paths to fill, its left edge on its
// baseline at the origin.
func piecesOf(f *mathtex.Formula) []mathPiece {
	if kept, ok := mathPieces.Load(f); ok {
		return kept.([]mathPiece)
	}
	var out []mathPiece
	for _, s := range f.Shapes() {
		path := &ui.Path{}
		if s.Path != nil {
			for _, seg := range s.Path {
				a, b, c := seg.Points[0], seg.Points[1], seg.Points[2]
				switch seg.Op {
				case geometry.SegMoveTo:
					path.MoveTo(a.X, a.Y)
				case geometry.SegLineTo:
					path.LineTo(a.X, a.Y)
				case geometry.SegQuadTo:
					path.QuadTo(a.X, a.Y, b.X, b.Y)
				case geometry.SegCubeTo:
					path.CubeTo(a.X, a.Y, b.X, b.Y, c.X, c.Y)
				case geometry.SegClose:
					path.Close()
				}
			}
		} else {
			r := s.Rule
			path.MoveTo(r.X, r.Y).LineTo(r.X+r.Width, r.Y).LineTo(r.X+r.Width, r.Y+r.Height).LineTo(r.X, r.Y+r.Height).Close()
		}
		piece := mathPiece{path: path}
		if s.Color != nil {
			c := ui.RGBA(s.Color.R, s.Color.G, s.Color.B, float32(s.Color.A)/255)
			piece.color = &c
		}
		out = append(out, piece)
	}
	mathPieces.Store(f, out)
	return out
}

// PaintFormula draws a formula in ink with its left edge at x on the
// baseline at y.
func PaintFormula(p *ui.Painter, f *mathtex.Formula, ink ui.Color, x, y float32) {
	paintMath(p, typeset{formula: f, ink: ink}, x, y)
}

// paintMath draws a typeset formula with its left edge at x on the
// baseline at y.
func paintMath(p *ui.Painter, m typeset, x, y float32) {
	p.Transform(ui.Translation(x, y), 0, 0, func() {
		for _, piece := range piecesOf(m.formula) {
			ink := m.ink
			if piece.color != nil {
				ink = *piece.color
			}
			p.FillPath(piece.path, ink)
		}
	})
}
