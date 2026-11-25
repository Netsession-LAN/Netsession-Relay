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
	src, err := decodeUpload(data)
	if err != nil {
		return Logo{}, err
	}
	img := fitted(src)
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&out, img); err != nil {
		return Logo{}, err
	}
	sum := sha256.Sum256(out.Bytes())
	return Logo{PNG: out.Bytes(), Hash: hex.EncodeToString(sum[:]), Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}, nil
}

// fitted copies src into an RGBA picture, scaled down to at most Side pixels on its longer side.
func fitted(src image.Image) *image.RGBA {
	b := src.Bounds()
	img := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy())) // 8 bit, alpha-premultiplied: right for averaging
	draw.Draw(img, img.Bounds(), src, b.Min, draw.Src)
	long := max(b.Dx(), b.Dy())
	if long <= Side {
		return img
	}
	w := max(1, (b.Dx()*Side+long/2)/long)
	h := max(1, (b.Dy()*Side+long/2)/long)
	return shrink(img, w, h)
}

// accepted reports whether a decoded format is one of the accepted input formats.
func accepted(format string) bool { return format == "png" || format == "jpeg" }

// decodeUpload decodes a PNG or JPEG upload. The header is checked first: the size bounds the memory a small file
// can ask for before the pixels are decoded, and the decoded picture must have the size its header announced.
func decodeUpload(data []byte) (image.Image, error) {
	cfg, err := decodeHeader(data)
	if err != nil {
		return nil, err
	}
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || !accepted(format) || src.Bounds().Size() != image.Pt(cfg.Width, cfg.Height) {
		return nil, ErrFormat
	}
	return src, nil
}

// decodeHeader reads the header of a PNG or JPEG upload and checks the size it announces.
func decodeHeader(data []byte) (image.Config, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || !accepted(format) {
		return cfg, ErrFormat
	}
	return cfg, checkSize(cfg)
}

// checkSize accepts a picture of at least one pixel and at most MaxSide pixels on either side.
func checkSize(cfg image.Config) error {
	if cfg.Width < 1 || cfg.Height < 1 {
		return ErrFormat
	}
	if cfg.Width > MaxSide || cfg.Height > MaxSide {
		return ErrTooWide
	}
	return nil
}
