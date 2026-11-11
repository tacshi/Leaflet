package mathtex

import (
	"image/color"
	"os"
	"strings"
	"testing"
)

func TestLayout(t *testing.T) {
	f, ok := Layout(`x^2+\frac{1}{2}`, 16)
	if !ok || f.Width < 20 || f.Ascent <= 0 || f.Descent <= 0 || len(f.Shapes()) == 0 {
		t.Fatalf("a formula laid out as %+v, %v", f, ok)
	}
	if again, _ := Layout(`x^2+\frac{1}{2}`, 16); again != f {
		t.Error("a formula laid out twice")
	}
	png, err := f.PNG(2, color.NRGBA{A: 0xff})
	if err != nil || len(png) < 100 {
		t.Fatalf("PNG: %d bytes, %v", len(png), err)
	}
	if out := os.Getenv("MATHTEX_OUT"); out != "" {
		for name, tex := range map[string]string{"frac": `x^2+\frac{1}{2}`, "sum": `\sum_{i=1}^{n} i^2 = \alpha\beta`, "sqrt": `e^{i\pi}+1=0, \sqrt{a^2+b^2}`} {
			g, ok := Layout(tex, 32)
			if !ok {
				t.Fatalf("%s did not lay out", name)
			}
			png, _ := g.PNG(2, color.NRGBA{A: 0xff})
			_ = os.WriteFile(out+"/"+name+".png", png, 0o600)
			t.Logf("%s: %.1f wide, %.1f up, %.1f down", name, g.Width, g.Ascent, g.Descent)
		}
	}
	for _, bad := range []string{"", `\frac{1`, `\nosuchcommand{x}`} {
		if _, ok := Layout(bad, 16); ok {
			t.Errorf("%q laid out", bad)
		}
	}
}

func TestDisplayAndSVG(t *testing.T) {
	in, _ := Layout(`\sum_{i=1}^{n} i^2`, 16)
	out, ok := Display(`\sum_{i=1}^{n} i^2`, 16, 400)
	if !ok || out.Ascent+out.Descent <= in.Ascent+in.Descent {
		t.Fatalf("a formula on its own lines: %+v beside %+v", out, in)
	}
	svg := out.SVG(16 * DisplayScale)
	if !strings.HasPrefix(svg, "<svg ") || !strings.Contains(svg, "<path d=\"M") || !strings.HasSuffix(svg, "</svg>") || strings.Contains(svg, "NaN") {
		t.Errorf("SVG: %.200s", svg)
	}
	if out := os.Getenv("MATHTEX_OUT"); out != "" {
		_ = os.WriteFile(out+"/sum.svg", []byte(svg), 0o600)
	}
}
