package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// ---- sessions, roles, CSRF ----

type ctxKey struct{}

func sessionFrom(r *http.Request) store.Session {
	se, _ := r.Context().Value(ctxKey{}).(store.Session)
	return se
}

// need wraps a handler: valid session, minimum role, CSRF token on writes.
func (s *Server) need(role string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store") // nothing behind the login belongs into a cache
		T := s.T(r)
		se, err := s.Store.SessionByToken(r.Context(), s.sessionToken(r))
		if err != nil {
			s.noSession(w, r, T)
			return
		}
		// the handler that renders the refusal needs the session too (name and menu on the error page)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, se))
		if store.RoleRank(se.User.Role) < store.RoleRank(role) {
			s.fail(w, r, http.StatusForbidden, T("Dafür fehlt dir die Rolle %s.", T(roleName(role))))
			return
		}
		if r.Method == http.MethodPost && !sameToken(csrfToken(r), se.CSRF) {
			s.fail(w, r, http.StatusForbidden, msgCSRF)
			return
		}
		h(w, r)
	})
}

// noSession answers a request that needs a session but has none: scripts and the live stream get a JSON 401,
// a browser the setup on a fresh installation, else the login page.
func (s *Server) noSession(w http.ResponseWriter, r *http.Request, T translator) {
	switch {
	case wantsJSON(r) || strings.Contains(r.Header.Get("Accept"), "text/event-stream"):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": T("Nicht angemeldet.")})
	case s.needsAccount(r.Context()): // a fresh installation: every page leads to the setup
		http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// msgCSRF is shown when a form comes without the token of the session (an old tab, or another site).
const msgCSRF = "Sitzung abgelaufen oder Formular veraltet. Lade die Seite neu."

// csrfToken is the token a write request carries: the header set by app.js, else the hidden form field.
func csrfToken(r *http.Request) string {
	if tok := r.Header.Get("X-CSRF-Token"); tok != "" {
		return tok
	}
	return r.PostFormValue("csrf")
}

func (s *Server) actor(r *http.Request) string { return sessionFrom(r).User.Name }

// ---- login ----

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.needsAccount(r.Context()) {
		http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
		return
	}
	d := map[string]any{"OIDC": s.oidc != nil, "LoginCSRF": s.loginCSRF(w, r)}
	if s.oidc != nil {
		c := s.oidc.cfg
		d["OIDCName"], d["GroupAdmin"], d["GroupOperator"], d["GroupViewer"] = c.Name, c.GroupAdmin, c.GroupOperator, c.GroupViewer
	}
	s.render(w, r, "login", "Anmelden", "login", d)
}

// loginLocal is the emergency login with a local account. Order: budget per client address, form token,
// pause per (address, name) after failures, then the password check (a few at a time). Every refusal reads the
// same whether the name exists or not.
//
// The attempt is counted as failed before the password is checked and only taken back when the password was
// right. So attempts that arrive at the same moment cannot all be checked before the first failure is known:
// after the free ones the next has to wait, however many connections the client opens.
func (s *Server) loginLocal(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	ip := s.clientIP(r)
	key := throttleKey(ip, name)
	if err := s.admitLocalLogin(r, name, ip, key, time.Now()); err != nil {
		back(w, r, "/login", "", err)
		return
	}
	u, err := s.checkLocalLogin(r.Context(), name, r.PostFormValue("password"), ip, key)
	if err != nil {
		back(w, r, "/login", "", err)
		return
	}
	s.throttle.reset(key)
	token := auth.Token(32)
	if err := s.Store.CreateSession(r.Context(), u, token, auth.Token(24), ip); err != nil {
		s.Log.Error("login: session", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Anmeldung gerade nicht möglich.")
		return
	}
	s.setSessionCookie(w, r, token, int(store.SessionTTL.Seconds()))
	s.clearCookie(w, r, loginCookie, "/", false)
	s.Log.Info("login", "name", name, "ip", ip)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// admitLocalLogin decides whether the password of an attempt may be checked at all: the budget of the client
// address, the form token, the pause of (address, name) after failures. It returns nil or the refusal; on nil
// the attempt is already counted as failed (loginThrottle.begin) under key.
func (s *Server) admitLocalLogin(r *http.Request, name, ip, key string, now time.Time) error {
	if !s.loginLimit.allow(limitKey(ip), now) {
		s.Log.Warn("login refused: too many attempts from this address", "ip", ip)
		return errThrottle
	}
	if c, err := r.Cookie(loginCookie); err != nil || !sameToken(r.PostFormValue("csrf"), c.Value) {
		return errStale
	}
	if d := s.throttle.begin(key, now); d > 0 {
		s.Log.Warn("login refused: pause after failed attempts", "name", name, "ip", ip, "wait", d.Round(time.Second))
		return errThrottle
	}
	return nil
}

// checkLocalLogin checks the password of the local account name. It returns the account, or errBusy when no
// check could run (the attempt is taken back) or errLogin, the same for an unknown name as for a wrong password.
func (s *Server) checkLocalLogin(ctx context.Context, name, pw, ip, key string) (store.User, error) {
	u, err := s.Store.UserByName(ctx, name)
	known := err == nil && u.Kind == "local" && u.PasswordHash != ""
	hash := u.PasswordHash
	if !known {
		hash = dummyHash() // same work for an unknown name as for a wrong password
	}
	ran, verr := s.verifyPassword(ctx, pw, hash)
	if !ran {
		s.throttle.cancel(key) // no password was checked: the attempt does not count
		s.Log.Warn("login refused: all password checks busy", "ip", ip)
		return u, errBusy
	}
	if known && verr == nil {
		return u, nil
	}
	if known {
		s.localLoginFailed(ctx, u, name, ip, verr)
	}
	s.Log.Warn("login failed", "name", name, "ip", ip)
	return u, errLogin
}

// localLoginFailed records a failed login of an existing local account; verr is the result of the check.
func (s *Server) localLoginFailed(ctx context.Context, u store.User, name, ip string, verr error) {
	if err := s.Store.LoginFailed(ctx, u, ip); err != nil {
		s.Log.Error("audit entry not written", "action", "login.failed", "user", u.Name, "object", ip, "err", err)
	}
	if !errors.Is(verr, auth.ErrMismatch) { // the stored hash itself is broken, not the password
		s.Log.Error("login: stored password hash unusable", "name", name, "err", verr)
	}
}

// dummyHash is a valid hash no password of a user matches. It is computed at the first login attempt, not at
// program start: one Argon2id run takes 64 MiB, and most subcommands never check a password.
var dummyHash = sync.OnceValue(func() string {
	h, err := auth.Hash("timing-equaliser")
	if err != nil {
		// only when the system has no random numbers; without them no login can work either
		panic("web: cannot compute the placeholder password hash: " + err.Error())
	}
	return h
})

// logout ends the session of the browser. With a valid session the form token is required like for every other
// write, so another site cannot log a user out by posting to this address. Without a session there is nothing
// to protect: the cookies are cleared and the login page follows.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if tok := s.sessionToken(r); tok != "" {
		se, err := s.Store.SessionByToken(r.Context(), tok)
		if err == nil && !sameToken(csrfToken(r), se.CSRF) {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, se))
			s.fail(w, r, http.StatusForbidden, msgCSRF)
			return
		}
		if err := s.Store.DeleteSession(r.Context(), tok); err != nil {
			// the cookie goes, but the session stays valid on the server until it expires
			s.Log.Error("logout: session not deleted", "err", err)
		}
	}
	// both names: the plain one may be left from before HTTPS was detected
	s.clearCookie(w, r, cookieName, "/", false)
	s.clearCookie(w, r, cookieNameSecure, "/", true) // a __Host- cookie can only be set, and so deleted, with Secure
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
