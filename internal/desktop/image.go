package desktop

import (
	"bytes"
	"image"
	"image/png"
)

// encodePNG encodes an image to PNG bytes with best-speed compression (these
// are large screen images; encode latency matters more than a few KB).
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// downscale box-averages src down to at most maxWidth wide, preserving aspect
// ratio. Pure standard-library (no golang.org/x/image) so the package stays
// dependency-free, consistent with internal/montage. Box averaging (rather than
// nearest) keeps text and thin UI lines legible after shrinking.
func downscale(src *image.RGBA, maxWidth int) *image.RGBA {
	sw := src.Bounds().Dx()
	sh := src.Bounds().Dy()
	if maxWidth <= 0 || sw <= maxWidth {
		return src
	}
	dw := maxWidth
	dh := sh * dw / sw
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// For each destination pixel, average the source box that maps to it.
	for dy := 0; dy < dh; dy++ {
		sy0 := dy * sh / dh
		sy1 := (dy + 1) * sh / dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			sx0 := dx * sw / dw
			sx1 := (dx + 1) * sw / dw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, b, a, n uint32
			for sy := sy0; sy < sy1; sy++ {
				row := src.PixOffset(src.Bounds().Min.X, src.Bounds().Min.Y+sy)
				for sx := sx0; sx < sx1; sx++ {
					i := row + sx*4
					r += uint32(src.Pix[i])
					g += uint32(src.Pix[i+1])
					b += uint32(src.Pix[i+2])
					a += uint32(src.Pix[i+3])
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			di := dst.PixOffset(dx, dy)
			dst.Pix[di] = uint8(r / n)
			dst.Pix[di+1] = uint8(g / n)
			dst.Pix[di+2] = uint8(b / n)
			dst.Pix[di+3] = uint8(a / n)
		}
	}
	return dst
}
