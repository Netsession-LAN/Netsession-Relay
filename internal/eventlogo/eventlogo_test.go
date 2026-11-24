package eventlogo

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func pngOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func flat(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

var white = color.NRGBA{R: 255, G: 255, B: 255, A: 255}

func decode(t *testing.T, l Logo) image.Image {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(l.PNG))
	if err != nil || format != "png" {
		t.Fatalf("stored logo: %v, format %q", err, format)
	}
	if b := img.Bounds(); b.Dx() != l.Width || b.Dy() != l.Height {
		t.Fatalf("stored logo is %v, reported %dx%d", b, l.Width, l.Height)
	}
	return img
}

func TestProcessKeepsSmallPNG(t *testing.T) {
	src := flat(40, 20, color.NRGBA{R: 200, G: 30, B: 40, A: 255})
	src.SetNRGBA(3, 4, color.NRGBA{}) // a transparent pixel
	l, err := Process(pngOf(t, src))
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, l)
	if l.Width != 40 || l.Height != 20 || len(l.Hash) != 64 {
		t.Fatalf("logo %dx%d hash %q", l.Width, l.Height, l.Hash)
	}
	if r, g, b, a := img.At(10, 10).RGBA(); r>>8 != 200 || g>>8 != 30 || b>>8 != 40 || a>>8 != 255 {
		t.Errorf("pixel = %d %d %d %d", r>>8, g>>8, b>>8, a>>8)
	}
	if _, _, _, a := img.At(3, 4).RGBA(); a != 0 {
		t.Errorf("transparent pixel has alpha %d", a)
	}
}

// Whatever sits behind the picture data (a script, a second file) does not survive the re-encoding.
func TestProcessStripsTrailingData(t *testing.T) {
	in := append(pngOf(t, flat(8, 8, white)), []byte("<script>alert(1)</script>")...)
	l, err := Process(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(l.PNG, []byte("script")) {
		t.Error("data behind the picture survived")
	}
}

func TestProcessScalesDown(t *testing.T) {
	// left half red, right half blue, 2000 x 1000 -> 512 x 256
	src := image.NewNRGBA(image.Rect(0, 0, 2000, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 2000; x++ {
			c := color.NRGBA{R: 255, A: 255}
			if x >= 1000 {
				c = color.NRGBA{B: 255, A: 255}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	l, err := Process(pngOf(t, src))
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, l)
	if l.Width != 512 || l.Height != 256 {
		t.Fatalf("scaled to %dx%d", l.Width, l.Height)
	}
	if r, _, b, a := img.At(100, 100).RGBA(); r>>8 != 255 || b != 0 || a>>8 != 255 {
		t.Errorf("left = %d %d %d", r>>8, b>>8, a>>8)
	}
	if r, _, b, _ := img.At(400, 100).RGBA(); r != 0 || b>>8 != 255 {
		t.Errorf("right = %d %d", r>>8, b>>8)
	}
	// portrait: the longer side decides
	l, err = Process(pngOf(t, flat(300, 1200, color.NRGBA{A: 255})))
	if err != nil || l.Width != 128 || l.Height != 512 {
		t.Errorf("portrait = %dx%d, %v", l.Width, l.Height, err)
	}
	// half-transparent pictures keep colour and alpha
	l, err = Process(pngOf(t, flat(1024, 1024, color.NRGBA{R: 250, G: 100, B: 20, A: 128})))
	if err != nil {
		t.Fatal(err)
	}
	c := color.NRGBAModel.Convert(decode(t, l).At(200, 300)).(color.NRGBA)
	if c.A != 128 || c.R < 247 || c.G < 98 || c.G > 102 || c.B < 18 || c.B > 22 {
		t.Errorf("half-transparent pixel = %+v", c)
	}
}

func TestProcessJPEG(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, flat(1024, 768, color.NRGBA{G: 180, A: 255}), nil); err != nil {
		t.Fatal(err)
	}
	l, err := Process(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	decode(t, l)
	if l.Width != 512 || l.Height != 384 {
		t.Errorf("jpeg scaled to %dx%d", l.Width, l.Height)
	}
}

func TestProcessRejects(t *testing.T) {
	var g bytes.Buffer
	if err := gif.Encode(&g, flat(8, 8, white), nil); err != nil {
		t.Fatal(err)
	}
	big := append(pngOf(t, flat(8, 8, white)), make([]byte, MaxUpload)...)
	half := pngOf(t, flat(64, 64, white))
	for name, c := range map[string]struct {
		in   []byte
		want error
	}{
		"empty":     {nil, ErrEmpty},
		"gif":       {g.Bytes(), ErrFormat},
		"svg":       {[]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"><script>alert(1)</script></svg>`), ErrFormat},
		"garbage":   {bytes.Repeat([]byte{0x42, 0x00, 0xff}, 500), ErrFormat},
		"truncated": {half[:len(half)-20], ErrFormat},
		"too big":   {big, ErrTooBig},
		"too wide":  {pngOf(t, flat(MaxSide+1, 4, white)), ErrTooWide},
		"too high":  {pngOf(t, flat(4, MaxSide+1, white)), ErrTooWide},
	} {
		if _, err := Process(c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}
