package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"sync"
	"sync/atomic"
)

func TestLocalPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/":                    true,
		"/verlauf":             true, // contains an "r": the old check sent it to "/"
		"/spiele?zeige=aus":    true,
		"/system#abfrage":      true,
		"/spiele/flatout2":     true,
		"":                     false,
		"verlauf":              false,
		"//evil.com":           false,
		"/\\evil.com":          false,
		"/\t/evil.com":         false,
		"/%09/evil.com":        false,
		"/%5Cevil.com":         false,
		"/\r\nSet-Cookie: a=b": false,
		"/a\x7f":               false,
		"https://evil.com":     false,
		"/%2Fevil.com":         false,
		"/%zz":                 false,
	} {
		if got := localPath(p); got != want {
			t.Errorf("localPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// The theme and language switches work without a login and must only ever redirect to a local path.
func TestSwitchRedirects(t *testing.T) {
	e := setup(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for back, want := range map[string]string{"/verlauf": "/verlauf", "/spiele?zeige=aus": "/spiele?zeige=aus", "//evil.com": "/",
		"/\\evil.com": "/", "/\t/evil.com": "/", "/%09/evil.com": "/", "https://evil.com/": "/"} {
		for _, sw := range []string{"/aussehen/98", "/sprache/en"} {
			resp, err := c.Get(e.srv.URL + sw + "?zurueck=" + url.QueryEscape(back))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != want {
				t.Errorf("%s back to %q: %d -> %q, want %q", sw, back, resp.StatusCode, loc, want)
			}
		}
	}
}

func TestClientIP(t *testing.T) {
	s := &Server{}
	req := func(remote string, h map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	// without trusted proxies the headers are ignored
	if got := s.clientIP(req("192.0.2.7:1234", map[string]string{"X-Forwarded-For": "10.0.0.1", "CF-Connecting-IP": "10.0.0.2"})); got != "192.0.2.7" {
		t.Errorf("untrusted peer: %s", got)
	}
	var err error
	if s.TrustedProxies, err = ParseProxies("192.0.2.7, 198.51.100.0/24"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		remote, xff, cf, want string
	}{
		{"192.0.2.7:1", "203.0.113.5", "", "203.0.113.5"},
		{"192.0.2.7:1", "1.2.3.4, 203.0.113.5", "", "203.0.113.5"},               // the left entry is the client's claim
		{"192.0.2.7:1", "1.2.3.4, 203.0.113.5, 198.51.100.9", "", "203.0.113.5"}, // a second trusted proxy in the chain
		{"192.0.2.7:1", "", "", "192.0.2.7"},
		{"192.0.2.7:1", "", "203.0.113.9", "203.0.113.9"},
		{"192.0.2.7:1", "evil, 198.51.100.9", "", "192.0.2.7"},
		{"203.0.113.50:1", "1.2.3.4", "1.2.3.4", "203.0.113.50"}, // not a proxy: headers do not count
	} {
		h := map[string]string{}
		if c.xff != "" {
			h["X-Forwarded-For"] = c.xff
		}
		if c.cf != "" {
			h["CF-Connecting-IP"] = c.cf
		}
		if got := s.clientIP(req(c.remote, h)); got != c.want {
			t.Errorf("remote %s xff %q cf %q: %s, want %s", c.remote, c.xff, c.cf, got, c.want)
		}
	}
	if _, err := ParseProxies("10.0.0.1, kaputt"); err == nil {
		t.Error("bad proxy list accepted")
	}
}

// HTTPS is detected per request: Secure cookies with the __Host- name and HSTS there, plain cookies on the
// plain MGMT address, so both ways in keep working without configuration.
func TestSecureByRequest(t *testing.T) {
	e := setup(t)
	e.ui.TrustedProxies, _ = ParseProxies("127.0.0.1")
	login := func(proto string) (*http.Response, []*http.Cookie) {
		c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		do := func(method, path string, form url.Values, cookies []*http.Cookie) *http.Response {
			req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(form.Encode()))
			if form != nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			if proto != "" {
				req.Header.Set("X-Forwarded-Proto", proto)
			}
			for _, ck := range cookies {
				req.AddCookie(ck)
			}
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			return resp
		}
		page := do("GET", "/login", nil, nil)
		b, _ := io.ReadAll(page.Body)
		page.Body.Close()
		_, rest, _ := strings.Cut(string(b), `name="csrf" value="`)
		tok, _, _ := strings.Cut(rest, `"`)
		resp := do("POST", "/login/local", url.Values{"name": {"admin"}, "password": {"pw-admin"}, "csrf": {tok}}, page.Cookies())
		resp.Body.Close()
		return resp, resp.Cookies()
	}
	find := func(cs []*http.Cookie, name string) *http.Cookie {
		for _, c := range cs {
			if c.Name == name && c.Value != "" {
				return c
			}
		}
		return nil
	}
	resp, cs := login("https")
	if c := find(cs, cookieNameSecure); c == nil || !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" {
		t.Errorf("HTTPS login: cookie %+v", cs)
	}
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("HTTPS answer without HSTS")
	}
	resp, cs = login("")
	if c := find(cs, cookieName); c == nil || c.Secure {
		t.Errorf("plain login: cookie %+v", cs)
	}
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("plain HTTP answer with HSTS")
	}
	// the header counts only from a trusted proxy; the public host name works without one
	e.ui.TrustedProxies = nil
	if _, cs = login("https"); find(cs, cookieName) == nil {
		t.Error("X-Forwarded-Proto from an untrusted peer switched to Secure cookies")
	}
	e.ui.SetPublicURL("https://" + strings.TrimPrefix(e.srv.URL, "http://") + "/oidc/callback")
	if _, cs = login(""); find(cs, cookieNameSecure) == nil {
		t.Error("request for the public HTTPS host did not get the Secure cookie")
	}
	e.ui.Insecure = true
	if _, cs = login(""); find(cs, cookieName) == nil {
		t.Error("NSRELAY_COOKIE_SECURE=0 does not switch Secure off")
	}
}

func TestLoginPageShowsOnlyKnownMessages(t *testing.T) {
	e := setup(t)
	_, body := get(t, http.DefaultClient, e.srv.URL+"/login?fehler="+url.QueryEscape("Ruf 0900-123 an und nenne dein Passwort")+"&ok="+url.QueryEscape("Alles gut, klick hier"))
	if strings.Contains(body, "Ruf 0900") || strings.Contains(body, "klick hier") || strings.Contains(body, `class="note"`) {
		t.Error("login page shows free text from the query")
	}
	_, body = get(t, http.DefaultClient, e.srv.URL+"/login?fehler="+url.QueryEscape(errLogin.Error()))
	if !strings.Contains(body, "Name oder Passwort falsch.") {
		t.Error("login page lost the known error message")
	}
	// with a session the flash messages of forms still work
	c, _ := e.client("view")
	if _, body = get(t, c, e.srv.URL+"/spiele?ok=Gespeichert"); !strings.Contains(body, "Gespeichert") {
		t.Error("flash message gone for logged-in pages")
	}
}

func TestBodyLimitAndCacheHeaders(t *testing.T) {
	e := setup(t)
	c, csrf := e.client("admin")
	big := url.Values{"csrf": {csrf}, "on": {"1"}, "pad": {strings.Repeat("x", MaxBody+1)}}
	r, err := c.PostForm(e.srv.URL+"/spiele/flatout2", big)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusRequestEntityTooLarge || len(e.eng.applied) != 0 {
		t.Errorf("oversized form = %d (applied %d)", r.StatusCode, len(e.eng.applied))
	}
	for _, p := range []string{"/", "/system", "/api/v1/status", "/system/konfiguration.yaml", "/system/backup", "/login"} {
		resp, err := c.Get(e.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("GET %s: Cache-Control %q", p, cc)
		}
	}
	resp, _ := c.Get(e.srv.URL + "/static/app.css")
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Cache-Control"), "public") {
		t.Error("static files lost their cache header")
	}
}

func TestLoginLimiterAndThrottle(t *testing.T) {
	now := time.Now()
	l := newIPLimiter(3, time.Second)
	for i := range 3 {
		if !l.allow("a", now) {
			t.Fatalf("attempt %d refused inside the burst", i)
		}
	}
	if l.allow("a", now) || !l.allow("b", now) || !l.allow("a", now.Add(time.Second)) {
		t.Error("bucket: burst, other key or refill wrong")
	}
	l.max = 8
	for i := range 100 {
		l.allow(string(rune('A'+i)), now)
	}
	if len(l.m) > l.max {
		t.Errorf("limiter holds %d keys, max %d", len(l.m), l.max)
	}
	var th loginThrottle
	k := throttleKey("192.0.2.1", "Admin")
	if k != throttleKey("192.0.2.1", "admin") || k == throttleKey("192.0.2.2", "admin") {
		t.Error("throttle key must ignore case and separate addresses")
	}
	// an attempt counts when it begins: the first throttleFree+1 are let through one after the other, every
	// further one has to wait for the pause the one before started, and the pause grows up to throttleMax
	at := now
	var last time.Duration
	for i := 1; i <= 20; i++ {
		if d := th.begin(k, at); d != 0 {
			t.Fatalf("attempt %d after the pause was refused for another %s", i, d)
		}
		d := th.begin(k, at) // right behind it: how long is the pause now?
		if d == 0 {
			th.cancel(k) // no pause: the question itself was counted as an attempt, take it back
		}
		if (i <= throttleFree) != (d == 0) || d < last || d > throttleMax {
			t.Fatalf("failure %d: pause %s after %s", i, d, last)
		}
		last = d
		at = at.Add(d)
	}
	if last != throttleMax {
		t.Errorf("pause must grow to %s, got %s", throttleMax, last)
	}
	if th.begin(throttleKey("192.0.2.2", "admin"), at) != 0 {
		t.Error("another address is paused too (lockout for the owner)")
	}
	th.reset(k)
	if th.begin(k, at) != 0 {
		t.Error("reset keeps the pause")
	}
	// cancel gives an attempt back: any number of attempts that never checked a password leave no pause
	var busy loginThrottle
	for range 50 {
		if busy.begin(k, now) != 0 {
			t.Fatal("cancelled attempts add up to a pause")
		}
		busy.cancel(k)
	}
	// IPv6: one /64 is one client, IPv4 addresses and other /64s stay apart
	if limitKey("2001:db8:1:2::1") != limitKey("2001:db8:1:2:ffff:ffff:ffff:ffff") || limitKey("2001:db8:1:2::1") == limitKey("2001:db8:1:3::1") {
		t.Error("IPv6 clients must be counted per /64")
	}
	if limitKey("192.0.2.1") != "192.0.2.1" || limitKey("::ffff:192.0.2.1") != "192.0.2.1" || limitKey("fe80::1%eth0") != limitKey("fe80::2") {
		t.Errorf("limit keys: %q %q %q", limitKey("192.0.2.1"), limitKey("::ffff:192.0.2.1"), limitKey("fe80::1%eth0"))
	}
	if limitKey("not an address") != "not an address" {
		t.Error("a key that is no address must stay as it is")
	}
	if throttleKey("2001:db8:1:2::1", "admin") != throttleKey("2001:db8:1:2::2", "Admin") {
		t.Error("the pause must follow the /64, not the single IPv6 address")
	}
}

// Password checks run a few at a time; one more waits and gives up when the request ends.
func TestVerifySemaphore(t *testing.T) {
	e := setup(t)
	for range cap(e.ui.verifySem) {
		e.ui.verifySem <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if ran, _ := e.ui.verifyPassword(ctx, "x", dummyHash()); ran {
		t.Error("password check ran although all slots were taken")
	}
	<-e.ui.verifySem
	if ran, err := e.ui.verifyPassword(context.Background(), "timing-equaliser", dummyHash()); !ran || err != nil {
		t.Errorf("free slot: ran %v, err %v", ran, err)
	}
}

func TestStreamCap(t *testing.T) {
	var c streamCount
	for i := range streamsPerSession {
		if !c.take("s1", 1) {
			t.Fatalf("stream %d refused", i)
		}
	}
	if c.take("s1", 1) || !c.take("s2", 1) {
		t.Error("cap per session wrong")
	}
	c.release("s1", 1)
	if !c.take("s1", 1) {
		t.Error("released stream not free again")
	}
	// over HTTP: the ninth stream of one session gets 429
	e := setup(t)
	cl, _ := e.client("view")
	var open []*http.Response
	defer func() {
		for _, r := range open {
			r.Body.Close()
		}
	}()
	for i := 0; i <= streamsPerSession; i++ {
		resp, err := cl.Get(e.srv.URL + "/api/v1/events")
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, resp)
		if want := http.StatusOK; i == streamsPerSession {
			if resp.StatusCode != http.StatusTooManyRequests {
				t.Errorf("stream %d = %d, want 429", i, resp.StatusCode)
			}
		} else if resp.StatusCode != want {
			t.Errorf("stream %d = %d", i, resp.StatusCode)
		}
	}
}

// The hidden advanced fields of a discovery row are an admin matter.
func TestAdvancedFieldsOnlyFromAdmins(t *testing.T) {
	T := trFor(langDE)
	base := gamedb.Game{ID: "x", Name: "X", Discovery: []gamedb.Spec{{Kind: "broadcast", Ports: "1000", Direction: "both", RatePPS: 50}}}
	keep := advOf(base.Discovery[0])
	evil := `{"kind":"","ports":"","rate_pps":100000,"rate_burst":100000,"payload_max":65000,"source_mode":"relay"}`
	form := func(adv string) gameForm {
		return gameForm{ID: "x", Name: "X", Specs: []specForm{{Kind: "broadcast", Ports: "1000-1001", Direction: "both", Adv: adv}}}
	}
	if g, msg := form(keep).game(base, "op", false, T); msg != "" || g.Discovery[0].RatePPS != 50 || g.Discovery[0].Ports != "1000-1001" {
		t.Errorf("operator editing ports must keep the stored advanced fields: %q %+v", msg, g.Discovery)
	}
	if _, msg := form(evil).game(base, "op", false, T); msg == "" {
		t.Error("operator could set advanced fields")
	}
	if _, msg := form(evil).game(gamedb.Game{}, "op", false, T); msg == "" {
		t.Error("operator could set advanced fields on a new game")
	}
	if g, msg := form(evil).game(base, "admin", true, T); msg != "" || g.Discovery[0].RatePPS != 100000 {
		t.Errorf("admin cannot set advanced fields: %q", msg)
	}
	if g, msg := form("").game(base, "op", false, T); msg != "" || g.Discovery[0].RatePPS != 0 {
		t.Errorf("row without advanced fields: %q %+v", msg, g.Discovery)
	}
}

// A new password ends the sessions of the account; so does `nsrelay admin logout`.
func TestSessionsEndOnPasswordChange(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c, _ := e.client("op")
	if code, _ := get(t, c, e.srv.URL+"/api/v1/status"); code != 200 {
		t.Fatalf("status = %d", code)
	}
	h, _ := auth.Hash("neu")
	if err := e.st.SetLocalUser(ctx, "test", "op", h, "operator"); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/status", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old session after a password change = %d", resp.StatusCode)
	}
	c2, _ := e.client("view")
	if n, err := e.st.Logout(ctx, "test", "view"); err != nil || n != 1 {
		t.Errorf("logout: %d, %v", n, err)
	}
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/v1/status", nil)
	req.Header.Set("Accept", "application/json")
	if resp, err = c2.Do(req); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("session after admin logout = %d", resp.StatusCode)
	}
	if _, err := e.st.Logout(ctx, "test", "gibtsnicht"); err == nil {
		t.Error("logout of an unknown account must fail")
	}
}

func TestHealthzHidesDatabaseError(t *testing.T) {
	e := setup(t)
	e.st.Close()
	code, body := get(t, http.DefaultClient, e.srv.URL+"/healthz")
	if code != http.StatusServiceUnavailable || strings.Contains(body, "sql") || strings.Contains(body, "closed") {
		t.Errorf("healthz = %d %q", code, body)
	}
}

// Many wrong passwords sent at the same moment must not all be checked: the attempt is counted before the
// password check, so after the free attempts the rest is refused, however many connections the client opens.
func TestParallelLoginsAreThrottled(t *testing.T) {
	e := setup(t)
	anon, _ := e.client("")
	tok := loginToken(t, anon, e.srv.URL)
	const attempts = 16 // below the budget per address, so only the pause per name can refuse
	var checked, paused, other atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := anon.PostForm(e.srv.URL+"/login/local", url.Values{"name": {"admin"}, "password": {"falsch"}, "csrf": {tok}})
			if err != nil {
				other.Add(1)
				return
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			switch {
			case strings.Contains(string(b), "Name oder Passwort falsch"):
				checked.Add(1)
			case strings.Contains(string(b), "Zu viele Anmeldeversuche"):
				paused.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if c := checked.Load(); c < 1 || c > throttleFree+1 {
		t.Errorf("%d of %d parallel attempts got their password checked, want at most %d", c, attempts, throttleFree+1)
	}
	if int(checked.Load()+paused.Load()) != attempts || other.Load() != 0 {
		t.Errorf("answers: %d checked, %d paused, %d other", checked.Load(), paused.Load(), other.Load())
	}
	// the right password does not get in during the pause either, and gets in after it
	if b := postLogin(t, anon, e.srv.URL, "admin", "pw-admin", tok); !strings.Contains(b, "Zu viele Anmeldeversuche") {
		t.Error("the pause does not hold for the right password")
	}
	e.ui.throttle.reset(throttleKey("127.0.0.1", "admin"))
	if b := postLogin(t, anon, e.srv.URL, "admin", "pw-admin", loginToken(t, anon, e.srv.URL)); !strings.Contains(b, "data-csrf=") {
		t.Error("right password after the pause does not log in")
	}
}

func postLogin(t *testing.T, c *http.Client, base, name, pw, csrf string) string {
	t.Helper()
	r, err := c.PostForm(base+"/login/local", url.Values{"name": {name}, "password": {pw}, "csrf": {csrf}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// Logging out is a write like any other: without the form token of the session nothing happens, so another site
// cannot end a session by posting to /logout.
func TestLogoutNeedsTheFormToken(t *testing.T) {
	e := setup(t)
	c, csrf := e.client("admin")
	for _, v := range []url.Values{{}, {"csrf": {"wrong"}}} {
		r, err := c.PostForm(e.srv.URL+"/logout", v)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("logout with token %q = %d, want 403", v.Get("csrf"), r.StatusCode)
		}
	}
	if code, _ := get(t, c, e.srv.URL+"/spiele"); code != http.StatusOK {
		t.Fatalf("the session ended without the form token (GET /spiele = %d)", code)
	}
	// the script sends the token as a header
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/logout", nil)
	req.Header.Set("X-CSRF-Token", csrf)
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.Request.URL.Path != "/login" {
		t.Errorf("logout with the token ended on %s, want the login page", r.Request.URL.Path)
	}
	if _, body := get(t, c, e.srv.URL+"/spiele"); !strings.Contains(body, `action="/login/local"`) {
		t.Error("the session is still valid after the logout")
	}
	// without a session there is nothing to protect: the login page follows
	anon, _ := e.client("")
	r, err = anon.PostForm(e.srv.URL+"/logout", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK || r.Request.URL.Path != "/login" {
		t.Errorf("logout without a session = %d on %s", r.StatusCode, r.Request.URL.Path)
	}
}
