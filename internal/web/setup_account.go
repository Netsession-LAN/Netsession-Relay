package web

import (
	"context"
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
	if msg := s.setupAccountRequestProblem(r, &f, ip, T); msg != "" {
		reject(msg)
		return
	}
	hash, msg := s.setupPasswordHash(ctx, r.PostFormValue("password"), T)
	if msg != "" {
		reject(msg)
		return
	}
	u, err := s.Store.CreateFirstAdmin(ctx, f.Name, hash)
	if errors.Is(err, store.ErrExists) { // somebody else was faster
		s.hasAccount.Store(true)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err != nil {
		reject(s.setupAccountError(err, &f, T))
		return
	}
	s.hasAccount.Store(true)
	s.loginFirstAdmin(w, r, u, ip)
}

// setupAccountRequestProblem checks the login rate, the form token and the credentials of the account form. It
// returns "" or what is wrong, in the language of T.
func (s *Server) setupAccountRequestProblem(r *http.Request, f *setupForm, ip string, T translator) string {
	if !s.loginLimit.allow(limitKey(ip), time.Now()) {
		return T(errThrottle.Error())
	}
	if c, err := r.Cookie(loginCookie); err != nil || !sameToken(r.PostFormValue("csrf"), c.Value) {
		return T("Die Seite war veraltet. Bitte noch einmal.")
	}
	return f.setupCredentialsProblem(r.PostFormValue("password"), r.PostFormValue("password2"), T)
}

// setupPasswordHash hashes the password of the first account, or returns why not in the language of T.
// Hashing takes 64 MiB like a login: it shares the slots of the password checks.
func (s *Server) setupPasswordHash(ctx context.Context, pw string, T translator) (hash, msg string) {
	hash, ok, err := s.hashPassword(ctx, pw)
	switch {
	case !ok:
		return "", T(errBusy.Error())
	case err != nil:
		s.Log.Error("setup: password hash", "err", err)
		return "", T("Das Konto ließ sich nicht anlegen. Bitte noch einmal.")
	}
	return hash, ""
}

// setupAccountError words why the first account could not be stored; a taken name marks the name field.
func (s *Server) setupAccountError(err error, f *setupForm, T translator) string {
	if errors.Is(err, store.ErrInvalid) {
		f.ErrName = true
		return T("Diesen Namen gibt es schon. Nimm einen anderen.")
	}
	s.Log.Error("setup: account", "err", err)
	return T("Das Konto ließ sich nicht anlegen. Bitte noch einmal.")
}

// loginFirstAdmin starts a session for the account just created and continues with the setup. Without a session
// the admin logs in by hand.
func (s *Server) loginFirstAdmin(w http.ResponseWriter, r *http.Request, u store.User, ip string) {
	token := auth.Token(32)
	if err := s.Store.CreateSession(r.Context(), u, token, auth.Token(24), ip); err != nil {
		s.Log.Error("setup: session", "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w, r, token, int(store.SessionTTL.Seconds()))
	s.clearCookie(w, r, loginCookie, "/", false)
	s.Log.Info("setup: first admin account created, the setup page for accounts is closed now", "name", u.Name, "ip", ip)
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
