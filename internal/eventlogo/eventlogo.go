// Package eventlogo turns an uploaded picture into the event logo the web UI shows next to the product mark:
// PNG or JPEG in, a freshly encoded PNG of bounded size out. Nothing of the upload but its pixels survives.
package eventlogo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/draw"
	_ "image/jpeg" // accepted input format
	"image/png"
)

const (
	// MaxUpload is the largest file accepted.
	MaxUpload = 1 << 20
	// MaxSide is the largest width or height accepted, in pixels.
	MaxSide = 2048
	// Side is the longer side of the stored logo at most, in pixels; larger pictures are scaled down.
	Side = 512
)

// Errors of Process. The web part puts them into words for the admin.
var (
	ErrEmpty   = errors.New("eventlogo: no file")
	ErrTooBig  = errors.New("eventlogo: file larger than 1 MiB")
	ErrFormat  = errors.New("eventlogo: neither PNG nor JPEG")
	ErrTooWide = errors.New("eventlogo: picture larger than 2048 x 2048 pixels")
)

// Logo is a processed event logo.
type Logo struct {
	PNG           []byte
	Hash          string // SHA-256 of PNG, hex
	Width, Height int
}

// Process checks an upload and returns the logo to store. The format is decided by decoding, never by a file
// name or a declared type. The picture is decoded completely, scaled down to at most Side pixels on its longer
// side and encoded again as PNG, so metadata and anything hidden behind the picture data are gone.
func Process(data []byte) (Logo, error) {
	switch {
	case len(data) == 0:
		return Logo{}, ErrEmpty
	case len(data) > MaxUpload:
		return Logo{}, ErrTooBig
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width < 1 || cfg.Height < 1 {
		return Logo{}, ErrFormat
	}
	if cfg.Width > MaxSide || cfg.Height > MaxSide { // before Decode: bounds the memory a small file can ask for
		return Logo{}, ErrTooWide
	}
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return Logo{}, ErrFormat
	}
	b := src.Bounds()
	if b.Dx() != cfg.Width || b.Dy() != cfg.Height {
		return Logo{}, ErrFormat
	}
	img := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy())) // 8 bit, alpha-premultiplied: right for averaging
	draw.Draw(img, img.Bounds(), src, b.Min, draw.Src)
	if long := max(b.Dx(), b.Dy()); long > Side {
		w := max(1, (b.Dx()*Side+long/2)/long)
		h := max(1, (b.Dy()*Side+long/2)/long)
		img = shrink(img, w, h)
	}
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&out, img); err != nil {
		return Logo{}, err
	}
	sum := sha256.Sum256(out.Bytes())
	return Logo{PNG: out.Bytes(), Hash: hex.EncodeToString(sum[:]), Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}, nil
}

// shrink scales src down to w x h by area averaging: every target pixel is the mean of the source area it
// covers, partly covered source pixels count by their share. Horizontal pass, then vertical.
func shrink(src *image.RGBA, w, h int) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	mid := make([]float32, w*sh*4)
	for y := 0; y < sh; y++ {
		row := src.Pix[y*src.Stride:]
		for x, sp := range spans(sw, w) {
			var acc [4]float32
			for i := sp.from; i < sp.to; i++ {
				wt := sp.weight(i)
				for c := 0; c < 4; c++ {
					acc[c] += float32(row[i*4+c]) * wt
				}
			}
			copy(mid[(y*w+x)*4:], acc[:])
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y, sp := range spans(sh, h) {
		for x := 0; x < w; x++ {
			var acc [4]float32
			for i := sp.from; i < sp.to; i++ {
				wt := sp.weight(i)
				for c := 0; c < 4; c++ {
					acc[c] += mid[(i*w+x)*4+c] * wt
				}
			}
			a := min(255, acc[3]+0.5)
			for c := 0; c < 3; c++ { // premultiplied: a colour is never larger than its alpha
				dst.Pix[y*dst.Stride+x*4+c] = uint8(min(a, acc[c]+0.5))
			}
			dst.Pix[y*dst.Stride+x*4+3] = uint8(a)
		}
	}
	return dst
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

func spans(from, to int) []span {
	out := make([]span, to)
	for i := range out {
		lo, hi := float32(i*from)/float32(to), float32((i+1)*from)/float32(to)
		a, b := i*from/to, ((i+1)*from+to-1)/to // floor and ceil without rounding errors
		out[i] = span{lo: lo, hi: hi, from: a, to: min(b, from)}
	}
	return out
}
