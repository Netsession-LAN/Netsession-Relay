package web

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
)

// HTTPS detection (Secure cookies, HSTS), the session cookie and the CSRF check of the login form.

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
