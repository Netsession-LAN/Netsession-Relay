package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// Single sign-on with OpenID Connect (authorization code flow with PKCE). The role of an account comes from
// its groups (the "groups" claim) and is refreshed at every login; an account in none of the three relay groups
// gets no access. The local emergency login stays.

// OIDCConfig comes from the environment (NSRELAY_OIDC_*); empty Issuer = OIDC off.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// Name is what the login button calls the identity provider ("Sign in with <Name>"); default "SSO".
	Name string
	// GroupAdmin, GroupOperator and GroupViewer are the groups of the identity provider that give the three
	// roles; defaults relay-admin, relay-operator, relay-viewer.
	GroupAdmin, GroupOperator, GroupViewer string
}

// Defaults of the optional OIDC settings.
const (
	DefaultOIDCName          = "SSO"
	DefaultOIDCGroupAdmin    = "relay-admin"
	DefaultOIDCGroupOperator = "relay-operator"
	DefaultOIDCGroupViewer   = "relay-viewer"
)

// Enabled reports whether the configuration is complete.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

// withDefaults fills the optional settings.
func (c OIDCConfig) withDefaults() OIDCConfig {
	c.Name = cmp.Or(strings.TrimSpace(c.Name), DefaultOIDCName)
	c.GroupAdmin = cmp.Or(strings.TrimSpace(c.GroupAdmin), DefaultOIDCGroupAdmin)
	c.GroupOperator = cmp.Or(strings.TrimSpace(c.GroupOperator), DefaultOIDCGroupOperator)
	c.GroupViewer = cmp.Or(strings.TrimSpace(c.GroupViewer), DefaultOIDCGroupViewer)
	return c
}

// role maps the groups of an account to a role: the highest one a group gives, "" when no group gives one.
// Group names are compared without regard to case.
func (c OIDCConfig) role(groups []string) string {
	best := ""
	for _, g := range groups {
		g = strings.TrimSpace(g)
		var r string
		switch {
		case strings.EqualFold(g, c.GroupAdmin):
			r = store.RoleAdmin
		case strings.EqualFold(g, c.GroupOperator):
			r = store.RoleOperator
		case strings.EqualFold(g, c.GroupViewer):
			r = store.RoleViewer
		}
		if store.RoleRank(r) > store.RoleRank(best) {
			best = r
		}
	}
	return best
}

type oidcClient struct {
	mu       sync.Mutex
	cfg      OIDCConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
}

const oidcCookie = "nsrelay_oidc"

// SetOIDC enables single sign-on. Discovery happens lazily at the first login, so the web part starts even when
// the identity provider is unreachable.
func (s *Server) SetOIDC(c OIDCConfig) {
	if !c.Enabled() {
		s.oidc = nil
		return
	}
	s.oidc = &oidcClient{cfg: c.withDefaults()}
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
	if !s.loginLimit.allow("oidc|"+limitKey(s.clientIP(r)), time.Now()) {
		back(w, r, "/login", "", errThrottle)
		return
	}
	if s.oidc == nil {
		back(w, r, "/login", "", uimsg.Error(msgOIDCOff))
		return
	}
	if err := s.oidc.setup(r.Context()); err != nil {
		s.Log.Warn("oidc discovery", "err", err)
		back(w, r, "/login", "", uimsg.Error(msgOIDCDown))
		return
	}
	state, nonce, verifier := auth.Token(24), auth.Token(24), oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: state + "." + nonce + "." + verifier, Path: "/oidc/",
		HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	url := s.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// oidcClaims are the claims the relay reads (identity providers usually put the groups into the profile scope).
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
	if !s.loginLimit.allow("oidc|"+limitKey(s.clientIP(r)), time.Now()) {
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
	role := s.oidc.cfg.role(cl.Groups)
	if role == "" {
		s.audit(r.Context(), cmp.Or(cl.Username, cl.Subject), "login.denied", s.clientIP(r), "", "no relay group")
		fail(msgOIDCGroup, nil)
		return
	}
	u, err := s.Store.UpsertOIDCUser(r.Context(), cl.Subject, cmp.Or(cl.Username, cl.Name), cl.Email, role)
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
