package eventlogo

import (
	"image"
)

// Scaling the logo down by area averaging: a horizontal pass, then a vertical one.

// shrink scales src down to w x h by area averaging: every target pixel is the mean of the source area it
// covers, partly covered source pixels count by their share. Horizontal pass, then vertical.
func shrink(src *image.RGBA, w, h int) *image.RGBA {
	return shrinkColumns(shrinkRows(src, w), w, src.Bounds().Dy(), h)
}

// shrinkRows is the horizontal pass: every row of src scaled to w pixels, 4 float channels per pixel.
func shrinkRows(src *image.RGBA, w int) []float32 {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	mid := make([]float32, w*sh*4)
	for y := 0; y < sh; y++ {
		row := src.Pix[y*src.Stride:]
		for x, sp := range spans(sw, w) {
			acc := sp.meanOfRow(row)
			copy(mid[(y*w+x)*4:], acc[:])
		}
	}
	return mid
}

// shrinkColumns is the vertical pass: the w columns of mid, sh pixels high, scaled to h pixels and rounded
// into an RGBA picture.
func shrinkColumns(mid []float32, w, sh, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y, sp := range spans(sh, h) {
		for x := 0; x < w; x++ {
			setPremultiplied(dst, x, y, sp.meanOfColumn(mid, w, x))
		}
	}
	return dst
}

// setPremultiplied rounds the channels of a pixel into dst; a colour is never larger than its alpha.
func setPremultiplied(dst *image.RGBA, x, y int, acc [4]float32) {
	a := min(255, acc[3]+0.5)
	for c := 0; c < 3; c++ {
		dst.Pix[y*dst.Stride+x*4+c] = uint8(min(a, acc[c]+0.5))
	}
	dst.Pix[y*dst.Stride+x*4+3] = uint8(a)
}

// span is the source range [lo, hi) one target pixel covers, in source pixels; from and to are the whole
// pixels it touches.
type span struct {
	lo, hi   float32
	from, to int
}

// weight is the share of source pixel i in the mean.
func (s span) weight(i int) float32 {
	return (min(s.hi, float32(i+1)) - max(s.lo, float32(i))) / (s.hi - s.lo)
}

// meanOfRow is the weighted mean of the 8-bit RGBA pixels of row that the span covers.
func (s span) meanOfRow(row []uint8) [4]float32 {
	var acc [4]float32
	for i := s.from; i < s.to; i++ {
		wt := s.weight(i)
		for c := 0; c < 4; c++ {
			acc[c] += float32(row[i*4+c]) * wt
		}
	}
	return acc
}

// meanOfColumn is the weighted mean of the pixels of column x in mid (w pixels per row, 4 channels each) that
// the span covers.
func (s span) meanOfColumn(mid []float32, w, x int) [4]float32 {
	var acc [4]float32
	for i := s.from; i < s.to; i++ {
		wt := s.weight(i)
		for c := 0; c < 4; c++ {
			acc[c] += mid[(i*w+x)*4+c] * wt
		}
	}
	return acc
}

func spans(from, to int) []span {
	out := make([]span, to)
	for i := range out {
		lo, hi := float32(i*from)/float32(to), float32((i+1)*from)/float32(to)
		a, b := i*from/to, ((i+1)*from+to-1)/to // floor and ceil without rounding errors
		out[i] = span{lo: lo, hi: hi, from: a, to: min(b, from)}
	}
	return out
}
