package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Netsession2024/Netsession-Relay/internal/auth"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

// Login with the Netsession account (Authentik, OIDC authorization code flow with PKCE). Roles come from the
// groups relay-admin, relay-operator, relay-viewer and are refreshed at every login; without one of them there
// is no access. The local emergency login stays.

// OIDCConfig comes from the environment (NSRELAY_OIDC_*); empty Issuer = OIDC off.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Enabled reports whether the configuration is complete.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

type oidcClient struct {
	mu       sync.Mutex
	cfg      OIDCConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

const oidcCookie = "nsrelay_oidc"

// SetOIDC enables the Authentik login. Discovery happens lazily at the first login, so the web part starts
// even when Authentik is unreachable.
func (s *Server) SetOIDC(c OIDCConfig) {
	if !c.Enabled() {
		s.oidc = nil
		return
	}
	s.oidc = &oidcClient{cfg: c}
}

func (o *oidcClient) setup(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, o.cfg.Issuer)
	if err != nil {
		return err
	}
	o.provider = p
	o.verifier = p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID})
	o.oauth = &oauth2.Config{ClientID: o.cfg.ClientID, ClientSecret: o.cfg.ClientSecret, RedirectURL: o.cfg.RedirectURL,
		Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email", "profile"}}
	return nil
}

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimit.allow("oidc|"+s.clientIP(r), time.Now()) {
		back(w, r, "/login", "", errThrottle)
		return
	}
	if s.oidc == nil {
		back(w, r, "/login", "", errors.New(msgOIDCOff))
		return
	}
	if err := s.oidc.setup(r.Context()); err != nil {
		s.Log.Warn("oidc discovery", "err", err)
		back(w, r, "/login", "", errors.New(msgOIDCDown))
		return
	}
	state, nonce, verifier := auth.Token(24), auth.Token(24), oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: state + "." + nonce + "." + verifier, Path: "/oidc/",
		HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	url := s.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// oidcClaims are the claims the relay reads (Authentik puts groups into the profile scope).
type oidcClaims struct {
	Subject  string   `json:"sub"`
	Username string   `json:"preferred_username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
	Nonce    string   `json:"nonce"`
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string, err error) {
		if err != nil {
			s.Log.Warn("oidc login", "err", err)
		}
		http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/oidc/", MaxAge: -1})
		back(w, r, "/login", "", errors.New(msg))
	}
	if !s.loginLimit.allow("oidc|"+s.clientIP(r), time.Now()) {
		fail(errThrottle.Error(), nil)
		return
	}
	if s.oidc == nil || s.oidc.oauth == nil {
		fail(msgOIDCOff, nil)
		return
	}
	c, err := r.Cookie(oidcCookie)
	parts := []string{}
	if err == nil {
		parts = strings.Split(c.Value, ".")
	}
	if len(parts) != 3 || r.URL.Query().Get("state") != parts[0] {
		fail(msgOIDCExpired, nil)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		fail(msgOIDCDenied, errors.New(e+": "+r.URL.Query().Get("error_description")))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	tok, err := s.oidc.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(parts[2]))
	if err != nil {
		fail(msgOIDCToken, err)
		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		fail(msgOIDCNoToken, nil)
		return
	}
	idt, err := s.oidc.verifier.Verify(ctx, raw)
	if err != nil {
		fail(msgOIDCBad, err)
		return
	}
	var cl oidcClaims
	if err := idt.Claims(&cl); err != nil || cl.Nonce != parts[1] {
		fail(msgOIDCNonce, err)
		return
	}
	if len(cl.Groups) == 0 {
		// some setups put groups only into userinfo
		if ui, err := s.oidc.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
			var more oidcClaims
			if ui.Claims(&more) == nil {
				cl.Groups = more.Groups
			}
		}
	}
	role := store.RoleFromGroups(cl.Groups)
	if role == "" {
		_ = s.Store.Audit(r.Context(), cmpOr(cl.Username, cl.Subject), "login.denied", s.clientIP(r), "", "no relay group")
		fail(msgOIDCGroup, nil)
		return
	}
	u, err := s.Store.UpsertOIDCUser(r.Context(), cl.Subject, cmpOr(cl.Username, cl.Name), cl.Email, role)
	if err != nil {
		fail(msgOIDCFailed, err)
		return
	}
	token := auth.Token(32)
	if err := s.Store.CreateSession(r.Context(), u, token, auth.Token(24), s.clientIP(r)); err != nil {
		s.Log.Error("oidc login: session", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Anmeldung gerade nicht möglich.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/oidc/", MaxAge: -1})
	s.setSessionCookie(w, r, token, int(store.SessionTTL.Seconds()))
	s.Log.Info("login", "name", u.Name, "kind", "oidc", "role", role)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
