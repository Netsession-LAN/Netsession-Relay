package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Step 1 of the setup ("Konto"): the first admin account.

// setupAccount creates the first admin account (step 1) and logs the browser in.
func (s *Server) setupAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.needsAccount(ctx) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	T := s.T(r)
	f := setupForm{Step: "konto", Name: strings.TrimSpace(r.PostFormValue("name")), MinPassword: setupMinPassword}
	reject := func(msg string) {
		f.Err = msg
		f.LoginCSRF = s.loginCSRF(w, r)
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "setup", "Einrichten", "setup", f)
	}
	ip := s.clientIP(r)
	if !s.loginLimit.allow(limitKey(ip), time.Now()) {
		reject(T(errThrottle.Error()))
		return
	}
	if c, err := r.Cookie(loginCookie); err != nil || !sameToken(r.PostFormValue("csrf"), c.Value) {
		reject(T("Die Seite war veraltet. Bitte noch einmal."))
		return
	}
	pw := r.PostFormValue("password")
	if msg := f.setupCredentialsProblem(pw, r.PostFormValue("password2"), T); msg != "" {
		reject(msg)
		return
	}
	// hashing takes 64 MiB like a login: it shares the slots of the password checks
	hash, ok, err := s.hashPassword(ctx, pw)
	switch {
	case !ok:
		reject(T(errBusy.Error()))
		return
	case err != nil:
		s.Log.Error("setup: password hash", "err", err)
		reject(T("Das Konto ließ sich nicht anlegen. Bitte noch einmal."))
		return
	}
	u, err := s.Store.CreateFirstAdmin(ctx, f.Name, hash)
	switch {
	case errors.Is(err, store.ErrExists): // somebody else was faster
		s.hasAccount.Store(true)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	case errors.Is(err, store.ErrInvalid):
		f.ErrName = true
		reject(T("Diesen Namen gibt es schon. Nimm einen anderen."))
		return
	case err != nil:
		s.Log.Error("setup: account", "err", err)
		reject(T("Das Konto ließ sich nicht anlegen. Bitte noch einmal."))
		return
	}
	s.hasAccount.Store(true)
	token := auth.Token(32)
	if err := s.Store.CreateSession(ctx, u, token, auth.Token(24), ip); err != nil {
		s.Log.Error("setup: session", "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w, r, token, int(store.SessionTTL.Seconds()))
	s.clearCookie(w, r, loginCookie, "/", false)
	s.Log.Info("setup: first admin account created, the setup page for accounts is closed now", "name", f.Name, "ip", ip)
	http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
}

// setupCredentialsProblem checks the name of the form and the password typed twice. It returns "" or what is
// wrong, in the language of T, and marks the field (ErrName, ErrPassword).
func (f *setupForm) setupCredentialsProblem(pw, pw2 string, T translator) string {
	if !setupName.MatchString(f.Name) {
		f.ErrName = true
		return T("Der Name braucht 2 bis 32 Zeichen: Buchstaben, Ziffern, Punkt, Bindestrich oder Unterstrich.")
	}
	msg := ""
	switch {
	case len([]rune(pw)) < setupMinPassword:
		msg = T("Das Passwort braucht mindestens %d Zeichen.", setupMinPassword)
	case len(pw) > 200:
		msg = T("Das Passwort ist zu lang (höchstens 200 Zeichen).")
	case pw != pw2:
		msg = T("Die beiden Passwörter sind nicht gleich.")
	}
	f.ErrPassword = msg != ""
	return msg
}
