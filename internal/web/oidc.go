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
		if r := c.groupRole(strings.TrimSpace(g)); store.RoleRank(r) > store.RoleRank(best) {
			best = r
		}
	}
	return best
}

// groupRole is the role one group gives, "" for any other group.
func (c OIDCConfig) groupRole(g string) string {
	switch {
	case strings.EqualFold(g, c.GroupAdmin):
		return store.RoleAdmin
	case strings.EqualFold(g, c.GroupOperator):
		return store.RoleOperator
	case strings.EqualFold(g, c.GroupViewer):
		return store.RoleViewer
	}
	return ""
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
	s.setCookie(w, r, &http.Cookie{Name: oidcCookie, Value: state + "." + nonce + "." + verifier, Path: "/oidc/", MaxAge: 600}) //nolint:gosec // G124: setCookie sets HttpOnly, SameSite and, per request, Secure
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
	if msg := s.oidcCallbackRefusal(r); msg != "" {
		s.oidcFail(w, r, msg, nil)
		return
	}
	st, msg, err := oidcCallbackState(r)
	if msg != "" {
		s.oidcFail(w, r, msg, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cl, msg, err := s.oidcIdentity(ctx, r.URL.Query().Get("code"), st)
	if msg != "" {
		s.oidcFail(w, r, msg, err)
		return
	}
	u, role, msg, err := s.oidcAccount(r, cl)
	if msg != "" {
		s.oidcFail(w, r, msg, err)
		return
	}
	s.startOIDCSession(w, r, u, role)
}

// startOIDCSession logs the browser in as u and leaves the login flow.
func (s *Server) startOIDCSession(w http.ResponseWriter, r *http.Request, u store.User, role string) {
	token := auth.Token(32)
	if err := s.Store.CreateSession(r.Context(), u, token, auth.Token(24), s.clientIP(r)); err != nil {
		s.Log.Error("oidc login: session", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Anmeldung gerade nicht möglich.")
		return
	}
	s.clearCookie(w, r, oidcCookie, "/oidc/", false)
	s.setSessionCookie(w, r, token, int(store.SessionTTL.Seconds()))
	s.Log.Info("login", "name", u.Name, "kind", "oidc", "role", role)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// oidcFail ends a failed OIDC login: err (may be nil) goes to the log, the browser back to the login page with msg.
func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	if err != nil {
		s.Log.Warn("oidc login", "err", err)
	}
	s.clearCookie(w, r, oidcCookie, "/oidc/", false)
	back(w, r, "/login", "", errors.New(msg))
}

// oidcCallbackRefusal says why a callback is not handled at all (too many logins from the address, OIDC switched
// off), or "".
func (s *Server) oidcCallbackRefusal(r *http.Request) string {
	if !s.loginLimit.allow("oidc|"+limitKey(s.clientIP(r)), time.Now()) {
		return errThrottle.Error()
	}
	if s.oidc == nil || s.oidc.oauth == nil {
		return msgOIDCOff
	}
	return ""
}

// oidcCallbackState reads the login cookie of the callback and whether the identity provider sent an error
// instead of a code. On failure msg is the text for the user and err (may be nil) the detail for the log.
func oidcCallbackState(r *http.Request) (st oidcState, msg string, err error) {
	st, ok := readOIDCState(r)
	if !ok {
		return st, msgOIDCExpired, nil
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return st, msgOIDCDenied, errors.New(e + ": " + q.Get("error_description"))
	}
	return st, "", nil
}

// oidcAccount gives the local account of a verified identity, with the role its groups give. An identity without
// a relay group is refused (and audited). On failure msg is the text for the user and err the detail for the log.
func (s *Server) oidcAccount(r *http.Request, cl oidcClaims) (u store.User, role, msg string, err error) {
	role = s.oidc.cfg.role(cl.Groups)
	if role == "" {
		s.audit(r.Context(), cmp.Or(cl.Username, cl.Subject), "login.denied", s.clientIP(r), "", "no relay group")
		return u, "", msgOIDCGroup, nil
	}
	u, err = s.Store.UpsertOIDCUser(r.Context(), cl.Subject, cmp.Or(cl.Username, cl.Name), cl.Email, role)
	if err != nil {
		return u, role, msgOIDCFailed, err
	}
	return u, role, "", nil
}

// oidcState is what oidcLogin stored in the login cookie: the state, the nonce expected in the ID token and the
// PKCE verifier.
type oidcState struct{ state, nonce, verifier string }

// readOIDCState reads the login cookie; ok is false when it is missing, malformed or belongs to another state
// than the one the identity provider sent back (expired or foreign login).
func readOIDCState(r *http.Request) (oidcState, bool) {
	c, err := r.Cookie(oidcCookie)
	if err != nil {
		return oidcState{}, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 || r.URL.Query().Get("state") != parts[0] {
		return oidcState{}, false
	}
	return oidcState{state: parts[0], nonce: parts[1], verifier: parts[2]}, true
}

// oidcIdentity redeems the authorization code and returns the claims of the verified ID token. On failure msg is
// the text for the user and err (may be nil) the detail for the log.
func (s *Server) oidcIdentity(ctx context.Context, code string, st oidcState) (cl oidcClaims, msg string, err error) {
	tok, err := s.oidc.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.verifier))
	if err != nil {
		return cl, msgOIDCToken, err
	}
	if cl, msg, err = s.oidcIDTokenClaims(ctx, tok, st.nonce); msg != "" {
		return cl, msg, err
	}
	if len(cl.Groups) == 0 {
		cl.Groups = s.oidcUserInfoGroups(ctx, tok)
	}
	return cl, "", nil
}

// oidcIDTokenClaims verifies the ID token that came with tok and returns its claims; the token must carry nonce.
// On failure msg is the text for the user and err (may be nil) the detail for the log.
func (s *Server) oidcIDTokenClaims(ctx context.Context, tok *oauth2.Token, nonce string) (cl oidcClaims, msg string, err error) {
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return cl, msgOIDCNoToken, nil
	}
	idt, err := s.oidc.verifier.Verify(ctx, raw)
	if err != nil {
		return cl, msgOIDCBad, err
	}
	if err = idt.Claims(&cl); err != nil || cl.Nonce != nonce {
		return cl, msgOIDCNonce, err
	}
	return cl, "", nil
}

// oidcUserInfoGroups reads the groups from the userinfo endpoint, for setups that put them only there (nil when
// that fails).
func (s *Server) oidcUserInfoGroups(ctx context.Context, tok *oauth2.Token) []string {
	ui, err := s.oidc.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil {
		return nil
	}
	var more oidcClaims
	if ui.Claims(&more) != nil {
		return nil
	}
	return more.Groups
}
