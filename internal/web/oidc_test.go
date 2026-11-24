package web

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

// fakeIdP is a minimal OIDC provider: discovery, JWKS and a token endpoint that returns an ID token with the
// nonce of the last authorize request.
type fakeIdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	groups []string
	user   string
	nonce  string
}

func newIdP(t *testing.T) *fakeIdP {
	t.Helper()
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: k, user: "dana", groups: []string{"relay-admin"}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks", "userinfo_endpoint": f.srv.URL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &k.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("code_verifier") == "" || r.PostForm.Get("code") != "good-code" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		sig, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: k}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
		claims := map[string]any{"iss": f.srv.URL, "sub": "subject-" + f.user, "aud": "relay", "exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(), "nonce": f.nonce, "preferred_username": f.user, "email": f.user + "@example.org", "groups": f.groups}
		raw, _ := jwt.Signed(sig).Claims(claims).Serialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": raw, "expires_in": 3600})
	})
	return f
}

// login runs the browser side of the flow and returns the final URL (path + query).
func (f *fakeIdP) login(t *testing.T, base string) (*http.Client, *url.URL) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if strings.HasPrefix(req.URL.String(), f.srv.URL+"/authorize") {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	r, err := c.Get(base + "/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	auth, _ := url.Parse(r.Header.Get("Location"))
	q := auth.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "relay" {
		t.Fatalf("authorize request %s", auth)
	}
	f.nonce = q.Get("nonce")
	r, err = c.Get(base + "/oidc/callback?code=good-code&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(r.Body)
	r.Body.Close()
	return c, r.Request.URL
}

func TestOIDCLogin(t *testing.T) {
	e := setup(t)
	idp := newIdP(t)
	e.ui.SetOIDC(OIDCConfig{Issuer: idp.srv.URL, ClientID: "relay", ClientSecret: "s3cret", RedirectURL: e.srv.URL + "/oidc/callback"})
	if _, page := get(t, http.DefaultClient, e.srv.URL+"/login"); !strings.Contains(page, "Anmelden mit SSO") || !strings.Contains(page, "relay-operator") {
		t.Fatal("login page without the single sign-on button or the names of the groups")
	}
	// admin group: logged in as admin; the local account "dana" does not exist in the test setup
	c, final := idp.login(t, e.srv.URL)
	if final.Path != "/" {
		t.Fatalf("after login at %s", final)
	}
	if code, _ := get(t, c, e.srv.URL+"/system"); code != 200 {
		t.Errorf("oidc admin on /system = %d", code)
	}
	u, err := e.st.UserByName(context.Background(), "dana")
	if err != nil || u.Kind != "oidc" || u.Role != "admin" {
		t.Fatalf("user %+v %v", u, err)
	}
	// role follows the groups at the next login
	idp.groups = []string{"relay-viewer", "andere"}
	c, _ = idp.login(t, e.srv.URL)
	if code, _ := get(t, c, e.srv.URL+"/system"); code != http.StatusForbidden {
		t.Errorf("viewer on /system = %d", code)
	}
	// a name that a local account already has gets @sso
	idp.user, idp.groups = "admin", []string{"relay-operator"}
	_, final = idp.login(t, e.srv.URL)
	if final.Path != "/" {
		t.Fatalf("second user at %s", final)
	}
	if u, err := e.st.UserByName(context.Background(), "admin@sso"); err != nil || u.Role != "operator" {
		t.Errorf("name clash: %+v %v", u, err)
	}
	// no relay group: refused, no session
	idp.user, idp.groups = "gast", []string{"lan-orga"}
	c, final = idp.login(t, e.srv.URL)
	if final.Path != "/login" || !strings.Contains(final.RawQuery, "fehler") {
		t.Errorf("no group ended at %s", final)
	}
	if r, err := c.Get(e.srv.URL + "/system"); err != nil || r.Request.URL.Path != "/login" {
		t.Error("user without group got in")
	} else {
		r.Body.Close()
	}
	// wrong state: refused
	jar, _ := cookiejar.New(nil)
	r, _ := (&http.Client{Jar: jar}).Get(e.srv.URL + "/oidc/callback?code=good-code&state=forged")
	r.Body.Close()
	if !strings.Contains(r.Request.URL.RawQuery, "fehler") {
		t.Error("forged state accepted")
	}
}

// The name on the button and the groups that give the roles come from the configuration.
func TestOIDCNameAndGroups(t *testing.T) {
	e := setup(t)
	idp := newIdP(t)
	e.ui.SetOIDC(OIDCConfig{Issuer: idp.srv.URL, ClientID: "relay", ClientSecret: "s3cret", RedirectURL: e.srv.URL + "/oidc/callback",
		Name: "Example ID", GroupAdmin: "net-admins", GroupOperator: "helpdesk", GroupViewer: "crew"})
	_, page := get(t, http.DefaultClient, e.srv.URL+"/login")
	for _, want := range []string{"Anmelden mit Example ID", "net-admins", "helpdesk", "crew"} {
		if !strings.Contains(page, want) {
			t.Errorf("login page without %q", want)
		}
	}
	// the default group names give nothing any more
	idp.user, idp.groups = "erin", []string{"relay-admin"}
	if _, final := idp.login(t, e.srv.URL); final.Path != "/login" {
		t.Errorf("default group accepted although others are configured: ended at %s", final)
	}
	idp.groups = []string{"Crew", "HelpDesk"} // the highest role wins, case does not matter
	if _, final := idp.login(t, e.srv.URL); final.Path != "/" {
		t.Fatalf("configured group refused: ended at %s", final)
	}
	if u, err := e.st.UserByName(context.Background(), "erin"); err != nil || u.Role != store.RoleOperator {
		t.Errorf("user %+v %v, want an operator", u, err)
	}
}

func TestOIDCRole(t *testing.T) {
	c := OIDCConfig{}.withDefaults()
	for _, tt := range []struct {
		groups []string
		want   string
	}{
		{nil, ""},
		{[]string{"lan-orga"}, ""},
		{[]string{"relay-viewer"}, store.RoleViewer},
		{[]string{"relay-viewer", " Relay-Admin ", "relay-operator"}, store.RoleAdmin},
		{[]string{"relay-operator", "relay-viewer"}, store.RoleOperator},
	} {
		if got := c.role(tt.groups); got != tt.want {
			t.Errorf("role(%q) = %q, want %q", tt.groups, got, tt.want)
		}
	}
}
