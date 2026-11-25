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
