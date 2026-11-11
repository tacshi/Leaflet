package mobile

import (
	"encoding/base64"
	"image/color"

	"leaflet/internal/mathtex"
)

type mathPicture struct {
	PNG     string  `json:"png"`
	Width   float32 `json:"width"`
	Ascent  float32 `json:"ascent"`
	Descent float32 `json:"descent"`
	// Left and Top are where the formula's left edge on its baseline is
	// in the picture, in points.
	Left float32 `json:"left"`
	Top  float32 `json:"top"`
}

// InlineMath is the picture of a formula in a line of text of the given
// size, scale pixels to a point, in the ink r, g, b, a: as JSON, or empty
// for what is no formula yet.
func InlineMath(tex string, size, scale float64, r, g, b, a int) string {
	defer catch(nil)
	f, ok := mathtex.Layout(tex, float32(size))
	if !ok {
		return ""
	}
	return picture(f, scale, r, g, b, a)
}

// DisplayMath is the picture of a formula on lines of its own, as a formula
// block shows it in a page whose text is of the given size: broken into
// lines where it is wider than width and has places to break.
func DisplayMath(tex string, size, width, scale float64, r, g, b, a int) string {
	defer catch(nil)
	f, ok := mathtex.Display(tex, float32(size), 0)
	if ok && width > 0 && float64(f.Width) > width {
		f, ok = mathtex.Display(tex, float32(size), float32(width))
	}
	if !ok {
		return ""
	}
	return picture(f, scale, r, g, b, a)
}

func picture(f *mathtex.Formula, scale float64, r, g, b, a int) string {
	png, err := f.PNG(float32(scale), color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(a)}) // #nosec G115
	if err != nil {
		return ""
	}
	left, top := f.PictureOrigin()
	out, err := encode(mathPicture{PNG: base64.StdEncoding.EncodeToString(png), Width: f.Width, Ascent: f.Ascent,
		Descent: f.Descent, Left: left, Top: top})
	if err != nil {
		return ""
	}
	return out
}
