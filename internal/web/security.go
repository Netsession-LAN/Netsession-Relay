package web

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

func (s *Server) isAdmin(r *http.Request) bool {
	return store.RoleRank(sessionFrom(r).User.Role) >= store.RoleRank(store.RoleAdmin)
}

// ---- local redirect targets ----

// localPath reports whether p is a path on this site that is safe as a redirect target: it starts with a single
// "/", has no scheme or host, no backslash (browsers read "/\host" as "//host") and no control characters, raw
// or percent-encoded in the path.
func localPath(p string) bool {
	if !isSitePath(p) {
		return false
	}
	u, err := url.Parse(p)
	if err != nil || !pathOnly(u) {
		return false
	}
	return isSitePath(u.Path)
}

// isSitePath reports whether p starts with a single "/" and has no unsafe character; localPath checks it on the
// raw target and on the decoded path.
func isSitePath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !hasUnsafeChar(p)
}

// pathOnly reports whether u has nothing that leads to another site: no scheme, host, user or opaque part.
func pathOnly(u *url.URL) bool {
	return u.Scheme == "" && u.Host == "" && u.User == nil && u.Opaque == ""
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
		p, ok := proxyPrefix(f)
		if !ok {
			return nil, errors.New("trusted proxy " + f + ": not an address or network")
		}
		out = append(out, p)
	}
	return out, nil
}

// proxyPrefix reads one trusted proxy: a network, or an address as a network of its own.
func proxyPrefix(f string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(f); err == nil {
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(f)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
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
	if a, ok := s.forwardedFor(r); ok {
		return a.Unmap().String()
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
		return a.Unmap().String()
	}
	return remote
}

// forwardedFor is the right-most address of X-Forwarded-For that is not a trusted proxy; ok is false when there is
// none, or when garbage comes first from the right.
func (s *Server) forwardedFor(r *http.Request) (netip.Addr, bool) {
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
			return a, true
		}
	}
	return netip.Addr{}, false
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
	s.setCookie(w, r, &http.Cookie{Name: s.sessionCookieName(r), Value: token, Path: "/", MaxAge: maxAge}) //nolint:gosec // G124: setCookie sets HttpOnly, SameSite and, per request, Secure
}

// setCookie sets a cookie of the web UI. Every cookie goes through here, so none gets weaker attributes by
// accident: always HttpOnly and SameSite=Lax, Secure when the request came over HTTPS (secure) or when the cookie
// asks for it (the __Host- session cookie).
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, c *http.Cookie) { //nolint:gosec // G124: setCookie sets HttpOnly, SameSite and, per request, Secure
	c.HttpOnly, c.SameSite = true, http.SameSiteLaxMode
	c.Secure = c.Secure || s.secure(r)
	http.SetCookie(w, c) //nolint:gosec // G124: Secure is decided per request in secure(); HttpOnly and SameSite are always set
}

// clearCookie deletes a cookie set by setCookie.
func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request, name, path string, secure bool) {
	s.setCookie(w, r, &http.Cookie{Name: name, Path: path, MaxAge: -1, Secure: secure}) //nolint:gosec // G124: setCookie sets HttpOnly, SameSite and, per request, Secure
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
	s.setCookie(w, r, &http.Cookie{Name: loginCookie, Value: tok, Path: "/"}) //nolint:gosec // G124: setCookie sets HttpOnly, SameSite and, per request, Secure
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
	msgOIDCToken   = "Anmeldung fehlgeschlagen (Token). Bitte noch einmal." //nolint:gosec // G101: a setting or message name, not a credential
	msgOIDCNoToken = "Anmeldung fehlgeschlagen (kein ID-Token)."            //nolint:gosec // G101: a setting or message name, not a credential
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
