package webpage

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"slices"

	_ "image/gif"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const printPixels = 1400

const maxDecodedBytes = 256 << 20

func forPrint(data []byte) (small []byte, typ string, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", false
	}
	turn := orientation(data)
	seenWidth := cfg.Width
	if turn >= 5 {
		seenWidth = cfg.Height
	}
	if seenWidth <= printPixels || decodedBytes(cfg) > maxDecodedBytes {
		return nil, "", false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false
	}
	scaled := shrunk(img, min(seenWidth/printPixels, cfg.Width, cfg.Height))
	scale := float64(printPixels) / float64(seenWidth)
	w, h := max(1, int(float64(cfg.Width)*scale+0.5)), max(1, int(float64(cfg.Height)*scale+0.5))
	if scaled.Rect.Dx() != w || scaled.Rect.Dy() != h {
		to := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(to, to.Rect, scaled, scaled.Rect, draw.Src, nil)
		scaled = to
	}
	seen := turned(scaled, turn)
	var buf bytes.Buffer
	if seen.Opaque() {
		typ, err = "image/jpeg", jpeg.Encode(&buf, seen, &jpeg.Options{Quality: 85})
	} else {
		typ, err = "image/png", png.Encode(&buf, seen)
	}
	if err != nil || buf.Len() >= len(data) {
		return nil, "", false
	}
	return buf.Bytes(), typ, true
}

func shrunk(img image.Image, k int) *image.RGBA {
	b := img.Bounds()
	k = max(k, 1)
	w, h := b.Dx()/k, b.Dy()/k
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	row := image.NewRGBA(image.Rect(0, 0, b.Dx(), 1))
	sums := make([]uint32, 4*w)
	n := uint32(k * k)
	for y := range h {
		clear(sums)
		for dy := range k {
			draw.Draw(row, row.Rect, img, image.Pt(b.Min.X, b.Min.Y+y*k+dy), draw.Src)
			for x := range w {
				s := sums[4*x : 4*x+4]
				for p := range slices.Chunk(row.Pix[4*k*x:4*k*(x+1)], 4) {
					s[0] += uint32(p[0])
					s[1] += uint32(p[1])
					s[2] += uint32(p[2])
					s[3] += uint32(p[3])
				}
			}
		}
		line := out.Pix[y*out.Stride:]
		for i, s := range sums {
			line[i] = uint8((s + n/2) / n)
		}
	}
	return out
}

func decodedBytes(cfg image.Config) int {
	per := 4
	switch cfg.ColorModel {
	case color.GrayModel:
		per = 1
	case color.Gray16Model:
		per = 2
	case color.YCbCrModel:
		per = 3
	case color.RGBA64Model, color.NRGBA64Model:
		per = 8
	}
	if _, ok := cfg.ColorModel.(color.Palette); ok {
		per = 1
	}
	return cfg.Width * cfg.Height * per
}

func orientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			return 1
		}
		marker := data[i+1]
		switch marker {
		case 0xff:
			i++
			continue
		case 0xda, 0xd9:
			return 1
		}
		size := int(binary.BigEndian.Uint16(data[i+2:]))
		if size < 2 || i+2+size > len(data) {
			return 1
		}
		if seg := data[i+4 : i+2+size]; marker == 0xe1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return exifOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	dir := int(order.Uint32(tiff[4:]))
	if dir < 8 || dir+2 > len(tiff) {
		return 1
	}
	for i := range int(order.Uint16(tiff[dir:])) {
		e := dir + 2 + 12*i
		if e+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[e:]) == 0x0112 {
			if o := int(order.Uint16(tiff[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

func turned(img *image.RGBA, turn int) *image.RGBA {
	if turn <= 1 || turn > 8 {
		return img
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	if turn >= 5 {
		out = image.NewRGBA(image.Rect(0, 0, h, w))
	}
	for y := range h {
		for x := range w {
			var dx, dy int
			switch turn {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			copy(out.Pix[out.PixOffset(dx, dy):][:4], img.Pix[img.PixOffset(x, y):][:4])
		}
	}
	return out
}
