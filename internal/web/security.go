package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/auth"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
	"github.com/Netsession2024/Netsession-Relay/internal/uimsg"
)

func (s *Server) isAdmin(r *http.Request) bool {
	return store.RoleRank(sessionFrom(r).User.Role) >= store.RoleRank(store.RoleAdmin)
}

// ---- local redirect targets ----

// localPath reports whether p is a path on this site that is safe as a redirect target: it starts with a single
// "/", has no scheme or host, no backslash (browsers read "/\host" as "//host") and no control characters, raw
// or percent-encoded in the path.
func localPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || hasUnsafeChar(p) {
		return false
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return false
	}
	return strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") && !hasUnsafeChar(u.Path)
}

func hasUnsafeChar(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return true
		}
	}
	return false
}

// ---- client address behind reverse proxies ----

// ParseProxies parses NSRELAY_TRUSTED_PROXIES: addresses or networks (CIDR), separated by commas or spaces.
func ParseProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, errors.New("trusted proxy " + f + ": not an address or network")
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func (s *Server) trustedProxy(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// fromTrustedProxy reports whether the request was handed over by a configured reverse proxy.
func (s *Server) fromTrustedProxy(r *http.Request) bool {
	a, err := netip.ParseAddr(remoteHost(r))
	return err == nil && s.trustedProxy(a)
}

// clientIP is the address of the browser. Forwarding headers count only when the TCP peer is a trusted proxy
// (NSRELAY_TRUSTED_PROXIES); then the right-most address of X-Forwarded-For that is not a trusted proxy itself
// is the client (everything left of it can be invented by the client). Without such an entry CF-Connecting-IP
// is used. Without trusted proxies it is always the TCP peer.
func (s *Server) clientIP(r *http.Request) string {
	remote := remoteHost(r)
	if !s.fromTrustedProxy(r) {
		return remote
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		if h == "" {
			continue
		}
		a, err := netip.ParseAddr(h)
		if err != nil {
			break // garbage: do not look further left
		}
		if !s.trustedProxy(a) {
			return a.Unmap().String()
		}
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
		return a.Unmap().String()
	}
	return remote
}

// ---- HTTPS detection (Secure cookies, HSTS) ----

// secure reports whether the browser talks HTTPS for this request: forced with Secure (NSRELAY_COOKIE_SECURE=1),
// never with Insecure (=0); otherwise when the request arrived with TLS, a trusted proxy says so
// (X-Forwarded-Proto) or the Host header is the public HTTPS name (NSRELAY_PUBLIC_URL, else the OIDC redirect
// URL). So the HTTPS name gets Secure cookies while the plain MGMT address keeps working.
func (s *Server) secure(r *http.Request) bool {
	switch {
	case s.Insecure:
		return false
	case s.Secure, r.TLS != nil:
		return true
	case s.fromTrustedProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"):
		return true
	}
	return s.PublicHost != "" && strings.EqualFold(r.Host, s.PublicHost)
}

// SetPublicURL records the HTTPS address users open (https://relay.example.org); other schemes are ignored.
func (s *Server) SetPublicURL(raw string) {
	if u, err := url.Parse(raw); err == nil && u.Scheme == "https" && u.Host != "" {
		s.PublicHost = u.Host
	}
}

// Session cookie: with HTTPS it carries the __Host- prefix (only accepted with Secure, Path=/ and no Domain, so
// no other host of the domain and no plain-HTTP page can set it).
const (
	cookieName       = "nsrelay_session"
	cookieNameSecure = "__Host-nsrelay_session"
	loginCookie      = "nsrelay_login"
)

func (s *Server) sessionCookieName(r *http.Request) string {
	if s.secure(r) {
		return cookieNameSecure
	}
	return cookieName
}

// sessionToken is the session cookie value of a request ("" without one).
func (s *Server) sessionToken(r *http.Request) string {
	if c, err := r.Cookie(s.sessionCookieName(r)); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: s.sessionCookieName(r), Value: token, Path: "/", HttpOnly: true, Secure: s.secure(r),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func sameToken(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// loginCSRF returns the token of the login form (double submit: cookie + hidden field) and sets the cookie when
// the browser has none yet. It stops other sites from logging a visitor into an account of their choice.
func (s *Server) loginCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(loginCookie); err == nil && len(c.Value) >= 16 && len(c.Value) <= 64 {
		return c.Value
	}
	tok := auth.Token(24)
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: tok, Path: "/", HttpOnly: true, Secure: s.secure(r),
		SameSite: http.SameSiteLaxMode})
	return tok
}

// ---- messages on pages without a session ----

// Texts the login page shows from ?ok= / ?fehler=. Without a session nothing else is shown, so a link cannot put
// invented text on the login page.
var (
	errLogin    = uimsg.Error("Name oder Passwort falsch.")
	errThrottle = uimsg.Error("Zu viele Anmeldeversuche. Warte kurz und versuch es noch einmal.")
	errBusy     = uimsg.Error("Gerade melden sich viele an. Bitte gleich nochmal.")
	errStale    = uimsg.Error("Die Anmeldeseite war veraltet. Bitte noch einmal.")
)

const (
	msgOIDCOff     = "Die Anmeldung mit Single Sign-on ist nicht eingerichtet."
	msgOIDCDown    = "Der Anmeldedienst ist gerade nicht erreichbar. Nimm die Notfall-Anmeldung."
	msgOIDCExpired = "Anmeldung abgelaufen. Bitte noch einmal."
	msgOIDCDenied  = "Der Anmeldedienst hat die Anmeldung abgelehnt."
	msgOIDCToken   = "Anmeldung fehlgeschlagen (Token). Bitte noch einmal."
	msgOIDCNoToken = "Anmeldung fehlgeschlagen (kein ID-Token)."
	msgOIDCBad     = "Anmeldung fehlgeschlagen (Token ungültig)."
	msgOIDCNonce   = "Anmeldung fehlgeschlagen (Nonce)."
	msgOIDCGroup   = "Dein Konto ist in keiner Relay-Gruppe. Ein Admin kann dich beim Anmeldedienst in eine eintragen."
	msgOIDCFailed  = "Anmeldung fehlgeschlagen."
)

var publicMessages = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range []string{errLogin.Error(), errThrottle.Error(), errBusy.Error(), errStale.Error(), msgOIDCOff, msgOIDCDown,
		msgOIDCExpired, msgOIDCDenied, msgOIDCToken, msgOIDCNoToken, msgOIDCBad, msgOIDCNonce, msgOIDCGroup, msgOIDCFailed} {
		m[t] = true
	}
	return m
}()

func publicMessage(s string) string {
	if publicMessages[s] {
		return s
	}
	return ""
}

// ---- rate limits for the login ----

// ipLimiter is a token bucket per key (client address) with a bounded number of keys.
type ipLimiter struct {
	mu    sync.Mutex
	m     map[string]*ipBucket
	burst float64
	every time.Duration // one token per every
	max   int
}

type ipBucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(burst int, every time.Duration) *ipLimiter {
	return &ipLimiter{m: map[string]*ipBucket{}, burst: float64(burst), every: every, max: 4096}
}

func (l *ipLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[key]
	if b == nil {
		if len(l.m) >= l.max {
			l.evict(now)
		}
		b = &ipBucket{tokens: l.burst, last: now}
		l.m[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.every))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evict drops full (idle) buckets; when all are in use it drops arbitrary ones, so memory stays bounded. A
// dropped bucket starts full again, which only helps a client that is one of thousands at that moment.
func (l *ipLimiter) evict(now time.Time) {
	for k, b := range l.m {
		if b.tokens+float64(now.Sub(b.last))/float64(l.every) >= l.burst {
			delete(l.m, k)
		}
	}
	for k := range l.m {
		if len(l.m) < l.max*3/4 {
			break
		}
		delete(l.m, k)
	}
}

// loginThrottle slows down password guessing per (client address, account name) with a growing, bounded pause.
// The account itself is never locked: somebody else guessing does not keep the owner out, and the answer is the
// same for names that exist and names that do not.
type loginThrottle struct {
	mu sync.Mutex
	m  map[string]*throttleEntry
}

type throttleEntry struct {
	fails int
	until time.Time // no attempt before this
	last  time.Time
}

const (
	throttleFree   = 3                // failures without a pause
	throttleMax    = 5 * time.Minute  // longest pause
	throttleForget = 30 * time.Minute // a quiet key starts over
	throttleKeys   = 8192
)

func throttleKey(ip, name string) string {
	name = strings.ToLower(name)
	if len(name) > 64 {
		name = name[:64]
	}
	return limitKey(ip) + "|" + name
}

// limitKey is a client address the way the login limits count it: an IPv4 address as it is, an IPv6 address as
// its /64 network. A host with IPv6 usually has a whole /64 to itself and could otherwise take a new address for
// every attempt. Anything that is not an address stays as it is.
func limitKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

// begin asks for one attempt. A key that is pausing gets the rest of its pause back and no attempt. Otherwise
// the attempt is counted as failed right away, under the same lock, and 0 is returned: the caller checks the
// password and calls reset when it was right (or cancel when no password was checked). Counting first and
// forgiving later is what keeps attempts that arrive together from all being checked before the first failure
// is recorded.
func (t *loginThrottle) begin(key string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]*throttleEntry{}
	}
	e := t.m[key]
	if e != nil && now.Sub(e.last) > throttleForget {
		delete(t.m, key)
		e = nil
	}
	if e == nil {
		if len(t.m) >= throttleKeys {
			t.evict(now)
		}
		e = &throttleEntry{}
		t.m[key] = e
	} else if d := e.until.Sub(now); d > 0 {
		return d
	}
	e.fails++
	e.last = now
	e.until = now.Add(throttlePause(e.fails))
	return 0
}

// throttlePause is the pause after the given number of failures in a row: none for the first throttleFree, then
// 2 s, 4 s, 8 s … up to throttleMax.
func throttlePause(fails int) time.Duration {
	n := fails - throttleFree
	switch {
	case n <= 0:
		return 0
	case n >= 10:
		return throttleMax
	}
	return min(throttleMax, time.Second<<n)
}

// evict keeps the memory bounded: expired keys go first, then arbitrary ones. The caller holds the lock.
func (t *loginThrottle) evict(now time.Time) {
	for k, x := range t.m {
		if now.Sub(x.last) > throttleForget {
			delete(t.m, k)
		}
	}
	for k := range t.m {
		if len(t.m) < throttleKeys*3/4 {
			break
		}
		delete(t.m, k)
	}
}

// cancel takes back an attempt that begin counted although no password was checked in the end (all password
// checks were busy).
func (t *loginThrottle) cancel(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[key]
	if e == nil {
		return
	}
	if e.fails--; e.fails <= 0 {
		delete(t.m, key)
		return
	}
	e.until = e.last.Add(throttlePause(e.fails))
}

func (t *loginThrottle) reset(key string) {
	t.mu.Lock()
	delete(t.m, key)
	t.mu.Unlock()
}

// verifyPassword runs the Argon2id check (64 MiB each) with at most cap(s.verifySem) checks at a time; the
// others wait for a free slot until ctx ends. ok=false means no slot came free.
func (s *Server) verifyPassword(ctx context.Context, password, hash string) (ok bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, verifyWait)
	defer cancel()
	select {
	case s.verifySem <- struct{}{}:
	case <-ctx.Done():
		return false, nil
	}
	defer func() { <-s.verifySem }()
	return true, auth.Verify(password, hash)
}

const (
	verifySlots = 3 // Argon2id checks at the same time (3 x 64 MiB)
	verifyWait  = 5 * time.Second
	// MaxBody is the largest request body the web part reads. Forms only; the one upload is the event logo with
	// MaxLogoBody (logo.go).
	MaxBody = 64 << 10
	// writeTimeout ends a response nobody reads (downloads included); the live stream sets its own per event.
	writeTimeout = 2 * time.Minute
	bodyTimeout  = 20 * time.Second
)

// ---- live streams per session ----

const (
	streamsPerSession = 8
	streamsPerUser    = 32
)

type streamCount struct {
	mu      sync.Mutex
	session map[string]int
	user    map[int64]int
}

// take reserves a live stream; false when the session or the account has too many open.
func (c *streamCount) take(session string, user int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		c.session, c.user = map[string]int{}, map[int64]int{}
	}
	if c.session[session] >= streamsPerSession || c.user[user] >= streamsPerUser {
		return false
	}
	c.session[session]++
	c.user[user]++
	return true
}

func (c *streamCount) release(session string, user int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session[session]--; c.session[session] <= 0 {
		delete(c.session, session)
	}
	if c.user[user]--; c.user[user] <= 0 {
		delete(c.user, user)
	}
}
