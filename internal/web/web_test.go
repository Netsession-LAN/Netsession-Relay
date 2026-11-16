package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/auth"
	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/control"
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

type fakeEngine struct {
	down    bool
	applied []*compile.Snapshot
}

func (f *fakeEngine) Status(context.Context) (engineapi.Status, error) {
	if f.down {
		return engineapi.Status{}, errors.New("engine not reachable")
	}
	st := engineapi.Status{Version: "test", Started: time.Now(), Mode: "observe", RXRate: 1204, SentRate: 0,
		Alarms:     []engineapi.Alarm{{Kind: "foreign_relay", Key: "1101", Detail: "VLAN 1101 receives frames from 10.10.2.99", Count: 3}},
		TopUnknown: []engineapi.PortCount{{Port: 4711, Count: 9}},
		VLANStats:  []engineapi.VLANStat{{VID: 1100, Name: "Gameserver", Group: "gameserver", In: 10, InRate: 2}, {VID: 1101, Name: "Tisch 1", Group: "tische"}},
		RuleStats:  []engineapi.RuleStat{{ID: "bc:23757", RX: 5, Relayed: 4}}}
	if n := len(f.applied); n > 0 {
		st.SnapshotHash = f.applied[n-1].Hash
		st.Mode = f.applied[n-1].Mode
	}
	return st, nil
}
func (f *fakeEngine) Validate(_ context.Context, s *compile.Snapshot) (engineapi.Result, error) {
	return engineapi.Result{OK: true, Hash: s.Hash}, nil
}
func (f *fakeEngine) Apply(_ context.Context, s *compile.Snapshot) (engineapi.Result, error) {
	f.applied = append(f.applied, s)
	return engineapi.Result{OK: true, Hash: s.Hash}, nil
}

type env struct {
	t   *testing.T
	srv *httptest.Server
	st  *store.Store
	eng *fakeEngine
	ui  *Server
}

func setup(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := control.Init(ctx, st, "test", "lab", "trunk0", false); err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]string{"admin": "admin", "op": "operator", "view": "viewer"} {
		h, _ := auth.Hash("pw-" + name)
		if err := st.SetLocalUser(ctx, "test", name, h, role); err != nil {
			t.Fatal(err)
		}
	}
	eng := &fakeEngine{}
	ui, err := New(st, eng, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	ui.LiveInterval = 20 * time.Millisecond
	srv := httptest.NewServer(ui)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, st: st, eng: eng, ui: ui}
}

// client logs in and returns a client with session cookie plus the CSRF token.
func (e *env) client(name string) (*http.Client, string) {
	e.t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if name == "" {
		return c, ""
	}
	resp, err := c.PostForm(e.srv.URL+"/login/local", url.Values{"name": {name}, "password": {"pw-" + name}})
	if err != nil {
		e.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`data-csrf="([^"]+)"`).FindSubmatch(body)
	if m == nil {
		e.t.Fatalf("login as %s failed (status %d): %.300s", name, resp.StatusCode, body)
	}
	return c, string(m[1])
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPagesRender(t *testing.T) {
	e := setup(t)
	c, _ := e.client("admin")
	for _, down := range []bool{false, true} {
		e.eng.down = down
		for _, p := range []string{"/", "/spiele", "/spiele/flatout2", "/spiele/flatout4", "/netz", "/system", "/verlauf", "/notaus", "/dev/komponenten", "/api/v1/status"} {
			code, body := get(t, c, e.srv.URL+p)
			if code != 200 {
				t.Errorf("GET %s (engine down=%v) = %d: %.300s", p, down, code, body)
			}
			if strings.Contains(body, "template:") || strings.Contains(body, "<no value>") {
				t.Errorf("GET %s has template residue", p)
			}
		}
	}
	e.eng.down = false
	_, body := get(t, c, e.srv.URL+"/")
	for _, want := range []string{"Relay <em>Übersicht</em>", `class="bar"`, "Fremdes Relay aktiv", "Tisch 1", "4711", "1 204"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview misses %q", want)
		}
	}
	if code, _ := get(t, c, e.srv.URL+"/spiele/gibtsnicht"); code != 404 {
		t.Errorf("unknown game = %d", code)
	}
}

func TestLoginAndLockout(t *testing.T) {
	e := setup(t)
	anon, _ := e.client("")
	resp, err := anon.Get(e.srv.URL + "/spiele")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/login" {
		t.Errorf("anonymous request not redirected to /login: %s", resp.Request.URL)
	}
	for i := 0; i < store.MaxFailed; i++ {
		r, _ := anon.PostForm(e.srv.URL+"/login/local", url.Values{"name": {"admin"}, "password": {"falsch"}})
		r.Body.Close()
	}
	r, _ := anon.PostForm(e.srv.URL+"/login/local", url.Values{"name": {"admin"}, "password": {"pw-admin"}})
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "gesperrt") {
		t.Error("account not locked after repeated failures")
	}
	log, _ := e.st.AuditLog(context.Background(), 20)
	if !slices.ContainsFunc(log, func(a store.AuditEntry) bool { return a.Action == "login.locked" }) {
		t.Error("lockout not in audit log")
	}
}

func TestRolesAndCSRF(t *testing.T) {
	e := setup(t)
	view, vcsrf := e.client("view")
	if code, _ := get(t, view, e.srv.URL+"/system"); code != 403 {
		t.Errorf("viewer on /system = %d", code)
	}
	r, _ := view.PostForm(e.srv.URL+"/spiele/flatout2", url.Values{"csrf": {vcsrf}, "on": {"0"}})
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Errorf("viewer toggling = %d", r.StatusCode)
	}
	op, _ := e.client("op")
	r, _ = op.PostForm(e.srv.URL+"/spiele/flatout2", url.Values{"csrf": {"wrong"}, "on": {"0"}})
	r.Body.Close()
	if r.StatusCode != 403 || len(e.eng.applied) != 0 {
		t.Errorf("wrong CSRF = %d, applied %d", r.StatusCode, len(e.eng.applied))
	}
}

func TestToggleGameAppliesToEngine(t *testing.T) {
	e := setup(t)
	op, csrf := e.client("op")
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/spiele/flatout2", strings.NewReader(url.Values{"on": {"0"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := op.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var a applyAnswer
	_ = json.NewDecoder(resp.Body).Decode(&a)
	resp.Body.Close()
	if !a.OK || a.Enabled || a.Revision != 1 || len(e.eng.applied) != 1 {
		t.Fatalf("answer %+v, applied %d", a, len(e.eng.applied))
	}
	// flatout1 still shares 23757, so the rule stays but lists only flatout1
	for _, r := range e.eng.applied[0].Rules {
		if r.Ports == "23757" && slices.Contains(r.Specs, "flatout2") {
			t.Error("flatout2 still in the snapshot")
		}
	}
	log, _ := e.st.AuditLog(context.Background(), 5)
	if !slices.ContainsFunc(log, func(a store.AuditEntry) bool { return a.Action == "game.disable" && a.User == "op" }) {
		t.Error("toggle not audited with the operator name")
	}
}

func TestAdminActions(t *testing.T) {
	e := setup(t)
	admin, csrf := e.client("admin")
	post := func(path string, v url.Values) {
		v.Set("csrf", csrf)
		r, err := admin.PostForm(e.srv.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}
	post("/system/mode", url.Values{"mode": {"observe"}})
	post("/netz/1103", url.Values{"on": {"0"}})
	post("/system/notaus", url.Values{})
	if n := len(e.eng.applied); n != 3 || e.eng.applied[2].Mode != "paused" || len(e.eng.applied[1].VLANs) != 3 {
		t.Fatalf("applied %d snapshots", n)
	}
	post("/system/rollback/1", url.Values{})
	if last := e.eng.applied[len(e.eng.applied)-1]; last.Mode != "observe" || len(last.VLANs) != 4 {
		t.Errorf("rollback to revision 1 gave mode %s with %d VLANs", last.Mode, len(last.VLANs))
	}
	resp, err := admin.Get(e.srv.URL + "/system/backup")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(b), "SQLite format 3") {
		t.Errorf("backup download = %d, %d bytes", resp.StatusCode, len(b))
	}
}

// Brand rule 2 (docs/02-plan.md 6.4): own CSS uses tokens only, no colour literals.
func TestCSSUsesTokensOnly(t *testing.T) {
	css, err := fs.ReadFile(staticFS, "static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(`).FindAll(css, -1); len(m) > 0 {
		t.Errorf("app.css contains colour literals: %q", m)
	}
	for _, banned := range []string{"backdrop-filter", "font-family: system-ui", "-apple-system"} {
		if strings.Contains(string(css), banned) {
			t.Errorf("app.css contains %q (generic look, rule 15)", banned)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := setup(t)
	resp, err := http.Get(e.srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers %v", resp.Header)
	}
}
