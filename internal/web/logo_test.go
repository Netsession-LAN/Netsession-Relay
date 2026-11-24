package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/eventlogo"
)

func testPicture(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	return img
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, testPicture(w, h)); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// upload posts a file as the logo form does (multipart, token in a form field). csrf "" leaves the field out.
func upload(t *testing.T, c *http.Client, base, csrf, filename string, data []byte) (int, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if csrf != "" {
		_ = mw.WriteField("csrf", csrf)
	}
	if data != nil {
		fw, _ := mw.CreateFormFile("logo", filename)
		_, _ = fw.Write(data)
	}
	mw.Close()
	resp, err := c.Post(base+logoUploadPath, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func logoOf(t *testing.T, c *http.Client, base string, header ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+logoPath, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestEventLogo(t *testing.T) {
	e := setup(t)
	anon, _ := e.client("")
	admin, csrf := e.client("admin")

	var (
		resp   *http.Response
		stored []byte
		etag   string
		cfg    image.Config
		format string
		err    error
	)
	// The test is one story: every step builds on the one before it. A step that fails ends the story, the
	// later ones would only fail because of it.
	step := func(name string, fn func(t *testing.T)) {
		if !t.Failed() {
			t.Run(name, fn)
		}
	}
	// without a logo: nothing to fetch, no page mentions it
	step("no logo", func(t *testing.T) {
		resp = logoOf(t, anon, e.srv.URL)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("no logo: GET %s = %d", logoPath, resp.StatusCode)
		}
		for _, p := range []string{"/", "/login"} {
			c := admin
			if p == "/login" {
				c = anon
			}
			if _, body := get(t, c, e.srv.URL+p); strings.Contains(body, logoPath) {
				t.Errorf("%s shows a logo although none is set", p)
			}
		}
	})
	// PNG, larger than the stored size: scaled down and encoded again
	step("upload a PNG", func(t *testing.T) {
		code, body := upload(t, admin, e.srv.URL, csrf, "logo.png", testPNG(t, 1200, 600))
		if code != 200 || !strings.Contains(body, "Logo gespeichert.") {
			t.Fatalf("upload = %d: %.300s", code, body)
		}
		resp = logoOf(t, anon, e.srv.URL)
		stored, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		etag = resp.Header.Get("ETag")
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("X-Content-Type-Options") != "nosniff" || etag == "" {
			t.Fatalf("logo: %d, headers %v", resp.StatusCode, resp.Header)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=300") || !strings.Contains(cc, "must-revalidate") {
			t.Errorf("Cache-Control = %q", cc)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "img-src 'self'") {
			t.Errorf("CSP = %q", resp.Header.Get("Content-Security-Policy"))
		}
		cfg, format, err = image.DecodeConfig(bytes.NewReader(stored))
		if err != nil || format != "png" || cfg.Width != eventlogo.Side || cfg.Height != eventlogo.Side/2 {
			t.Fatalf("stored logo: %s %dx%d, %v", format, cfg.Width, cfg.Height, err)
		}
		resp = logoOf(t, anon, e.srv.URL, "If-None-Match", etag)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotModified {
			t.Errorf("If-None-Match = %d, want 304", resp.StatusCode)
		}
	})
	// everybody sees it: header and overview, login page; the system page has the form
	step("every page shows it", func(t *testing.T) {
		for p, c := range map[string]*http.Client{"/": admin, "/login": anon, "/system": admin} {
			_, body := get(t, c, e.srv.URL+p)
			if !strings.Contains(body, `src="`+logoPath+`?v=`) {
				t.Errorf("%s does not show the logo", p)
			}
		}
		viewer, _ := e.client("view")
		if _, body := get(t, viewer, e.srv.URL+"/"); !strings.Contains(body, `class="top-ev"`) || !strings.Contains(body, `class="pagehead-ev"`) {
			t.Error("viewer: overview without the logo")
		}
		u, _ := url.Parse(e.srv.URL)
		admin.Jar.SetCookies(u, []*http.Cookie{{Name: themeCookie, Value: theme98, Path: "/"}})
		if _, body := get(t, admin, e.srv.URL+"/"); !strings.Contains(body, `class="w98-wall"`) {
			t.Error("theme 98: no logo on the desktop")
		}
		if _, body := getDest(t, admin, e.srv.URL+"/", "document"); !strings.Contains(body, `class="w98-wall"`) {
			t.Error("theme 98 desktop shell: no logo on the desktop")
		}
		admin.Jar.SetCookies(u, []*http.Cookie{{Name: themeCookie, Value: "", Path: "/", MaxAge: -1}})
	})
	// JPEG replaces it; the stored picture is a PNG again and the address changes
	step("a JPEG replaces it", func(t *testing.T) {
		var jb bytes.Buffer
		if err := jpeg.Encode(&jb, testPicture(300, 200), nil); err != nil {
			t.Fatal(err)
		}
		if code, body := upload(t, admin, e.srv.URL, csrf, "logo.jpg", jb.Bytes()); code != 200 || !strings.Contains(body, "Logo gespeichert.") {
			t.Fatalf("jpeg upload = %d: %.300s", code, body)
		}
		resp = logoOf(t, anon, e.srv.URL, "If-None-Match", etag)
		stored, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		cfg, format, err = image.DecodeConfig(bytes.NewReader(stored))
		if resp.StatusCode != 200 || err != nil || format != "png" || cfg.Width != 300 || cfg.Height != 200 || resp.Header.Get("ETag") == etag {
			t.Fatalf("after the jpeg: %d %s %dx%d %v", resp.StatusCode, format, cfg.Width, cfg.Height, err)
		}
		etag = resp.Header.Get("ETag")
	})
	// refused, and the logo stays as it was
	step("refused uploads", func(t *testing.T) {
		var gb bytes.Buffer
		if err := gif.Encode(&gb, testPicture(8, 8), nil); err != nil {
			t.Fatal(err)
		}
		for name, c := range map[string]struct {
			file string
			data []byte
			want string
		}{
			"svg":        {"logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), string(msgLogoFormat)},
			"svg as png": {"logo.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), string(msgLogoFormat)},
			"gif":        {"logo.gif", gb.Bytes(), string(msgLogoFormat)},
			"gif as png": {"logo.png", gb.Bytes(), string(msgLogoFormat)},
			"garbage":    {"logo.png", bytes.Repeat([]byte{7, 0, 255, 13}, 4000), string(msgLogoFormat)},
			"too wide":   {"logo.png", testPNG(t, eventlogo.MaxSide+1, 2), string(msgLogoTooWide)},
			"no file":    {"", nil, string(msgLogoEmpty)},
			// more than the file may have, less than the request may have
			"too big": {"logo.png", append(testPNG(t, 16, 16), make([]byte, eventlogo.MaxUpload)...), string(msgLogoTooBig)},
			// more than the request may have: answered before the handler
			"much too big": {"logo.png", append(testPNG(t, 16, 16), make([]byte, MaxLogoBody)...), string(msgLogoTooBig)},
		} {
			code, body := upload(t, admin, e.srv.URL, csrf, c.file, c.data)
			if code != 200 || !strings.Contains(body, c.want) || strings.Contains(body, "Logo gespeichert.") {
				t.Errorf("%s: %d, want the message %q: %.200s", name, code, c.want, body)
			}
		}
		resp = logoOf(t, anon, e.srv.URL)
		resp.Body.Close()
		if resp.Header.Get("ETag") != etag {
			t.Error("a refused upload changed the logo")
		}
	})
	// the form token is required
	step("form token and role", func(t *testing.T) {
		if code, _ := upload(t, admin, e.srv.URL, "", "logo.png", testPNG(t, 20, 20)); code != http.StatusForbidden {
			t.Errorf("upload without token = %d, want 403", code)
		}
		if code, _ := upload(t, admin, e.srv.URL, "wrong", "logo.png", testPNG(t, 20, 20)); code != http.StatusForbidden {
			t.Errorf("upload with a wrong token = %d, want 403", code)
		}
		// admins only: an operator is refused, also with a valid token of his own; without a session it is the login
		op, opCSRF := e.client("op")
		if code, _ := upload(t, op, e.srv.URL, opCSRF, "logo.png", testPNG(t, 20, 20)); code != http.StatusForbidden {
			t.Errorf("operator upload = %d, want 403", code)
		}
		// ... and nobody but an admin gets the larger body limit
		if code, _ := upload(t, op, e.srv.URL, opCSRF, "logo.png", append(testPNG(t, 16, 16), make([]byte, 2*MaxBody)...)); code != http.StatusRequestEntityTooLarge {
			t.Errorf("operator, large upload = %d, want 413", code)
		}
		if code, _ := upload(t, anon, e.srv.URL, "x", "logo.png", append(testPNG(t, 16, 16), make([]byte, 2*MaxBody)...)); code != http.StatusRequestEntityTooLarge {
			t.Errorf("no session, large upload = %d, want 413", code)
		}
		if code, body := upload(t, anon, e.srv.URL, "x", "logo.png", testPNG(t, 20, 20)); code != 200 || !strings.Contains(body, `action="/login/local"`) {
			t.Errorf("no session = %d, want the login page", code)
		}
		resp, err = op.PostForm(e.srv.URL+"/system/logo/entfernen", url.Values{"csrf": {opCSRF}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("operator removes = %d, want 403", resp.StatusCode)
		}
		resp, err = admin.PostForm(e.srv.URL+"/system/logo/entfernen", url.Values{})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("remove without token = %d, want 403", resp.StatusCode)
		}
		resp = logoOf(t, anon, e.srv.URL)
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("ETag") != etag {
			t.Fatalf("logo after the refused requests: %d", resp.StatusCode)
		}
	})
	// the other forms keep the small limit
	step("other forms keep the small limit", func(t *testing.T) {
		resp, err = admin.PostForm(e.srv.URL+"/system/dienste", url.Values{"csrf": {csrf}, "note": {strings.Repeat("x", 2*MaxBody)}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("large form elsewhere = %d, want 413", resp.StatusCode)
		}
	})
	// a restart reads it from the database
	step("read back after a restart", func(t *testing.T) {
		e.ui.logo.Store(nil)
		e.ui.loadLogo(context.Background())
		if l := e.ui.logo.Load(); l == nil || `"`+l.Hash+`"` != etag {
			t.Error("logo not read back from the database")
		}
	})
	// removal
	step("removal", func(t *testing.T) {
		resp, err = admin.PostForm(e.srv.URL+"/system/logo/entfernen", url.Values{"csrf": {csrf}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), "Logo entfernt.") || strings.Contains(string(b), logoPath) {
			t.Errorf("remove = %d: %.200s", resp.StatusCode, b)
		}
		resp = logoOf(t, anon, e.srv.URL)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("after removal: GET %s = %d", logoPath, resp.StatusCode)
		}
	})
	// both steps are in the audit log
	step("audit log", func(t *testing.T) {
		log, _ := e.st.AuditLog(context.Background(), 50)
		seen := map[string]int{}
		for _, a := range log {
			if strings.HasPrefix(a.Action, "event.logo.") {
				seen[a.Action]++
				if a.User != "admin" {
					t.Errorf("audit %s by %q", a.Action, a.User)
				}
			}
		}
		if seen["event.logo.set"] != 2 || seen["event.logo.remove"] != 1 {
			t.Errorf("audit entries = %v", seen)
		}
	})
}

// The first-run setup shows a logo that is already there (nsrelay web -demo-logo, a database with a logo but
// without accounts); without one the page has no trace of it.
func TestEventLogoOnSetup(t *testing.T) {
	e := setupFresh(t)
	c, _ := e.client("")
	if code, body := get(t, c, e.srv.URL+"/einrichten"); code != 200 || strings.Contains(body, logoPath) {
		t.Errorf("setup without a logo = %d, mentions one: %v", code, strings.Contains(body, logoPath))
	}
	if err := e.ui.SetEventLogo(context.Background(), "test", testPNG(t, 64, 32)); err != nil {
		t.Fatal(err)
	}
	if code, body := get(t, c, e.srv.URL+"/einrichten"); code != 200 || !strings.Contains(body, `class="login-ev" src="`+logoPath) {
		t.Errorf("setup with a logo = %d, logo missing", code)
	}
}

// Every text of the logo is there in English: the panel, the history, the messages.
func TestEventLogoEnglishAndHistory(t *testing.T) {
	e := setup(t)
	if err := e.ui.SetEventLogo(context.Background(), "admin", testPNG(t, 64, 32)); err != nil {
		t.Fatal(err)
	}
	c, _ := e.client("admin")
	setLangCookie(t, c, e.srv.URL, langEN)
	misses := watchMisses(t)
	for _, p := range []string{"/system", "/verlauf", "/"} {
		if code, _ := get(t, c, e.srv.URL+p); code != 200 {
			t.Errorf("GET %s = %d", p, code)
		}
	}
	if _, body := get(t, c, e.srv.URL+"/verlauf"); !strings.Contains(body, "LAN party logo set") {
		t.Error("history without the logo entry in English")
	}
	if _, body := get(t, c, e.srv.URL+"/system?fehler="+url.QueryEscape(string(msgLogoFormat))); !strings.Contains(body, "This is not a PNG or JPEG image.") {
		t.Error("error message not in English")
	}
	for _, s := range misses() {
		t.Errorf("no English text for %q", s)
	}
}
