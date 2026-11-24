package web

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// setupFresh is a relay as it comes out of the box: games from the seed, no account, no network.
func setupFresh(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seed, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SyncSeed(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{}
	ui, err := New(st, eng, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	ui.LiveInterval = 20 * time.Millisecond
	ui.HostIfaces = func() ([]netif.HostIface, error) {
		return []netif.HostIface{
			{Name: "lo", Kind: "device", Up: true, Loopback: true, Addrs: []string{"127.0.0.1/8"}},
			{Name: "eth0", Kind: "device", Up: true, Carrier: true, DefaultRoute: true, Addrs: []string{"192.0.2.10/24"}},
			{Name: "eth1", Kind: "device", Up: true, Carrier: true},
			{Name: "eth1.11", Kind: "vlan", Up: true, Relay: true, Addrs: []string{"10.10.1.250/24"}},
			{Name: "docker0", Kind: "bridge", Addrs: []string{"172.17.0.1/16"}},
			{Name: "veth1a2b3c", Kind: "veth", Up: true, Carrier: true},
		}, nil
	}
	srv := httptest.NewServer(ui)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, st: st, eng: eng, ui: ui}
}

func postForm(t *testing.T, c *http.Client, u string, v url.Values) (int, string, string) {
	t.Helper()
	resp, err := c.PostForm(u, v)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Request.URL.RequestURI(), string(b)
}

var formToken = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func TestSetupWizard(t *testing.T) {
	e := setupFresh(t)
	base := e.srv.URL
	ctx := context.Background()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	var (
		page, path, body, csrf string
		code                   int
		tok                    []string
		other                  *http.Client
	)
	account := func(name, pw, pw2, csrf string) (int, string, string) {
		return postForm(t, c, base+"/einrichten/konto", url.Values{"name": {name}, "password": {pw}, "password2": {pw2}, "csrf": {csrf}})
	}
	network := func(v url.Values) (int, string, string) {
		v.Set("csrf", csrf)
		return postForm(t, c, base+"/einrichten/netz", v)
	}
	two := func() url.Values {
		return url.Values{"trunk": {"eth1"}, "host": {"250"}, "event": {"Testlan"}, "mode": {"observe"},
			"vid": {"11", "12", ""}, "name": {"Tisch 1", "Tisch 2", ""}, "cidr": {"10.10.1.0/24", "10.10.2.0/24", ""}, "group": {"tische", "tische", "tische"}}
	}
	// The test is one story: every step builds on the one before it. A step that fails ends the story, the
	// later ones would only fail because of it.
	step := func(name string, fn func(t *testing.T)) {
		if !t.Failed() {
			t.Run(name, fn)
		}
	}
	// every page of a fresh relay leads to step 1
	step("every page leads to the account step", func(t *testing.T) {
		for _, p := range []string{"/", "/spiele", "/login", "/system"} {
			resp, err := c.Get(base + p)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.Request.URL.Path != "/einrichten" || !strings.Contains(string(b), `action="/einrichten/konto"`) {
				t.Fatalf("GET %s on a fresh relay ends at %s", p, resp.Request.URL)
			}
		}
		_, page = get(t, c, base+"/einrichten")
		tok = formToken.FindStringSubmatch(page)
		if tok == nil {
			t.Fatal("step 1 without form token")
		}
	})
	step("account step refuses bad input", func(t *testing.T) {
		for want, try := range map[string][4]string{
			"Seite war veraltet":          {"chef", "ein langes passwort", "ein langes passwort", "falsch"},
			"mindestens 10 Zeichen":       {"chef", "kurz", "kurz", tok[1]},
			"nicht gleich":                {"chef", "ein langes passwort", "ein anderes passwort", tok[1]},
			"Der Name braucht 2 bis 32":   {"x y", "ein langes passwort", "ein langes passwort", tok[1]},
			"Der Name braucht 2 bis 32 Z": {"", "ein langes passwort", "ein langes passwort", tok[1]},
		} {
			if code, _, body := account(try[0], try[1], try[2], try[3]); code != 422 || !strings.Contains(body, want) {
				t.Errorf("step 1 with %v = %d, want the message %q", try, code, want)
			}
		}
		if n, _ := e.st.LocalAccounts(ctx); n != 0 {
			t.Fatalf("rejected forms created %d accounts", n)
		}
	})
	step("account step creates the admin", func(t *testing.T) {
		code, path, body = account("chef", "ein langes passwort", "ein langes passwort", tok[1])
		if code != 200 || path != "/einrichten" || !strings.Contains(body, `action="/einrichten/netz"`) {
			t.Fatalf("step 1 = %d at %s, want step 2", code, path)
		}
		if u, err := e.st.UserByName(ctx, "chef"); err != nil || u.Role != store.RoleAdmin || u.Kind != "local" {
			t.Fatalf("admin account: %+v, %v", u, err)
		}
		csrf = formToken.FindStringSubmatch(body)[1]
	})
	// the account step is closed for everybody else now
	step("account step is closed afterwards", func(t *testing.T) {
		other, _ = e.client("")
		_, page = get(t, other, base+"/einrichten")
		if !strings.Contains(page, `action="/login/local"`) {
			t.Error("second visitor does not land on the login page")
		}
		if _, p, _ := postForm(t, other, base+"/einrichten/konto", url.Values{"name": {"zweiter"}, "password": {"ein langes passwort"},
			"password2": {"ein langes passwort"}, "csrf": {loginToken(t, other, base)}}); p != "/login" {
			t.Errorf("second account request ends at %s", p)
		}
		if n, _ := e.st.LocalAccounts(ctx); n != 1 {
			t.Errorf("%d accounts after a second request", n)
		}
	})
	// step 2: the management interface is listed but cannot be chosen, plumbing is not listed
	step("network step lists the interfaces", func(t *testing.T) {
		for _, want := range []string{`value="eth0" disabled`, `value="eth1" checked`, "192.0.2.10/24", `value="observe" checked`} {
			if !strings.Contains(body, want) {
				t.Errorf("step 2 misses %q", want)
			}
		}
		for _, not := range []string{`value="lo"`, `value="docker0"`, `value="eth1.11"`, `value="veth1a2b3c"`, `class="top"`} {
			if strings.Contains(body, not) {
				t.Errorf("step 2 shows %q", not)
			}
		}
	})
	// buttons that only change the form
	step("buttons only change the form", func(t *testing.T) {
		_, _, ex := network(url.Values{"aktion": {"beispiel"}, "trunk": {"eth1"}, "mode": {"active"}})
		if !strings.Contains(ex, `value="10.10.99.0/24"`) || !strings.Contains(ex, `value="Tisch 4"`) || !strings.Contains(ex, `value="active" checked`) {
			t.Error("example layout not loaded (or the form lost its values)")
		}
		_, _, more := network(url.Values{"aktion": {"mehr"}, "vid": {"11"}, "name": {"A"}, "cidr": {"10.10.1.0/24"}, "group": {"tische"}})
		if got := strings.Count(more, `name="cidr"`); got != setupRowsMin {
			t.Errorf("more rows: %d rows, want %d (1 typed + 4 new, filled up to the minimum)", got, setupRowsMin)
		}
		if n, _ := e.st.Revisions(ctx, 5); len(n) != 0 || len(e.eng.applied) != 0 {
			t.Error("form buttons applied something")
		}

	})
	step("network step refuses bad input", func(t *testing.T) {
		bad := map[string]func(url.Values){
			"Verwaltungsschnittstelle":      func(v url.Values) { v.Set("trunk", "eth0") },
			"gibt es auf diesem Host nicht": func(v url.Values) { v.Set("trunk_other", "ens99") },
			"Bitte die Schnittstelle":       func(v url.Values) { v.Del("trunk") },
			"mindestens zwei Netze":         func(v url.Values) { v["vid"][1], v["name"][1], v["cidr"][1] = "", "", "" },
			"Netz in Zeile 2":               func(v url.Values) { v["cidr"][1] = "kein netz" },
			"VLAN 11 gibt es schon":         func(v url.Values) { v["vid"][1] = "11" },
			"überschneiden sich":            func(v url.Values) { v["cidr"][1] = "10.10.0.0/23" },
			"passt nicht in 10.10.2.0/28":   func(v url.Values) { v["cidr"][1] = "10.10.2.0/28" },
			"Bitte eine Betriebsart":        func(v url.Values) { v.Set("mode", "paused") },
			"Stelle der Relay-Adresse":      func(v url.Values) { v.Set("host", "abc") },
		}
		for want, change := range bad {
			v := two()
			change(v)
			if code, _, b := network(v); code != 422 || !strings.Contains(b, want) {
				t.Errorf("step 2, case %q = %d without that message", want, code)
			} else if !strings.Contains(b, `value="Tisch 1"`) {
				t.Errorf("step 2, case %q: the form lost what was typed", want)
			}
		}
		e.eng.reject = "VLAN 11: interface not allowed"
		if code, _, b := network(two()); code != 422 || !strings.Contains(b, "Die Engine lehnt diese Netze ab") {
			t.Errorf("engine rejection = %d", code)
		}
		e.eng.reject = ""
		if _, err := e.st.ExportConfig(ctx); err == nil || len(e.eng.applied) != 0 {
			t.Fatal("a rejected setup stored or applied something")
		}
	})
	step("finish stores and applies", func(t *testing.T) {
		code, path, body = network(two())
		if code != 200 || !strings.HasPrefix(path, "/?ok=") || !strings.Contains(body, "Fertig eingerichtet: 2 Netze") {
			t.Fatalf("finish = %d at %s", code, path)
		}
		cfg, err := e.st.ExportConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Event != "Testlan" || cfg.Mode != compile.ModeObserve || len(cfg.VLANs) != 2 || cfg.VLANs[0].RelayIP != "10.10.1.250" ||
			cfg.VLANs[1].Parent != "eth1" || !cfg.VLANs[1].Enabled || cfg.VLANs[0].Attach != compile.AttachTrunk {
			t.Errorf("stored configuration: %+v", cfg)
		}
		if len(cfg.Games) == 0 || len(cfg.Profiles) != 1 || !cfg.Profiles[0].Default || len(cfg.Profiles[0].Edges) != 1 {
			t.Errorf("games %d, profiles %+v (want the default games and one edge tische->tische)", len(cfg.Games), cfg.Profiles)
		}
		if len(e.eng.applied) != 1 || e.eng.applied[0].Mode != compile.ModeObserve || len(e.eng.applied[0].VLANs) != 2 {
			t.Errorf("engine got %d snapshots", len(e.eng.applied))
		}
	})
	// done: the setup is gone, for the admin and for visitors
	step("the setup is gone afterwards", func(t *testing.T) {
		for _, cl := range []*http.Client{c, other} {
			resp, err := cl.Get(base + "/einrichten")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if p := resp.Request.URL.Path; p != "/" && p != "/login" {
				t.Errorf("/einrichten after the setup ends at %s", p)
			}
		}
		if _, p, _ := network(two()); p != "/" {
			t.Errorf("second finish ends at %s", p)
		}
		if len(e.eng.applied) != 1 {
			t.Error("second finish applied again")
		}
	})
}

// An installation with accounts and networks never sees the setup; one with single sign-on but no local
// account does not get the account step either.
func TestSetupNotForExisting(t *testing.T) {
	e := setup(t)
	anon, _ := e.client("")
	for _, p := range []string{"/", "/einrichten"} {
		resp, err := anon.Get(e.srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Request.URL.Path != "/login" {
			t.Errorf("GET %s without session ends at %s", p, resp.Request.URL.Path)
		}
	}
	if _, p, _ := postForm(t, anon, e.srv.URL+"/einrichten/konto", url.Values{"name": {"neu"}, "password": {"ein langes passwort"},
		"password2": {"ein langes passwort"}, "csrf": {loginToken(t, anon, e.srv.URL)}}); p != "/login" {
		t.Errorf("account request ends at %s", p)
	}
	if _, err := e.st.UserByName(context.Background(), "neu"); err == nil {
		t.Error("the setup created an account on an existing installation")
	}
	admin, csrf := e.client("admin")
	if resp, err := admin.Get(e.srv.URL + "/einrichten"); err != nil || resp.Request.URL.Path != "/" {
		t.Errorf("admin of a configured relay is not sent to the overview: %v", err)
	}
	if _, p, _ := postForm(t, admin, e.srv.URL+"/einrichten/netz", url.Values{"csrf": {csrf}, "trunk": {"eth1"}}); p != "/" {
		t.Errorf("network step on a configured relay ends at %s", p)
	}
	if len(e.eng.applied) != 0 {
		t.Error("network step applied something on a configured relay")
	}

	f := setupFresh(t)
	f.ui.oidc = &oidcClient{} // single sign-on configured: admins come from there
	c, _ := f.client("")
	resp, err := c.Get(f.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/login" {
		t.Errorf("with single sign-on a fresh relay shows %s", resp.Request.URL.Path)
	}
	if _, p, _ := postForm(t, c, f.srv.URL+"/einrichten/konto", url.Values{"name": {"neu"}, "password": {"ein langes passwort"},
		"password2": {"ein langes passwort"}, "csrf": {"x"}}); p != "/login" {
		t.Errorf("account step with single sign-on ends at %s", p)
	}
	if n, _ := f.st.LocalAccounts(context.Background()); n != 0 {
		t.Error("account step created an account although single sign-on is configured")
	}
}

// The setup in English: every text is in the catalog.
func TestSetupEnglish(t *testing.T) {
	e := setupFresh(t)
	misses := watchMisses(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	setLangCookie(t, c, e.srv.URL, langEN)
	_, page := get(t, c, e.srv.URL+"/einrichten")
	if !strings.Contains(page, `<html lang="en">`) || !strings.Contains(page, "Create account") {
		t.Error("step 1 is not English")
	}
	tok := formToken.FindStringSubmatch(page)[1]
	for _, try := range [][4]string{{"chef", "a long passphrase", "a long passphrase", "wrong"}, {"x y", "a long passphrase", "a long passphrase", tok},
		{"chef", "short", "short", tok}, {"chef", "a long passphrase", "another passphrase", tok}, {"chef", strings.Repeat("long ", 50), "", tok},
		{"chef", "a long passphrase", "a long passphrase", tok}} {
		postForm(t, c, e.srv.URL+"/einrichten/konto", url.Values{"name": {try[0]}, "password": {try[1]}, "password2": {try[2]}, "csrf": {try[3]}})
	}
	_, page = get(t, c, e.srv.URL+"/einrichten")
	if !strings.Contains(page, "Step 2 of 2") {
		t.Fatal("step 2 is not English")
	}
	csrf := formToken.FindStringSubmatch(page)[1]
	post := func(v url.Values) string {
		v.Set("csrf", csrf)
		_, _, b := postForm(t, c, e.srv.URL+"/einrichten/netz", v)
		return b
	}
	if b := post(url.Values{"aktion": {"beispiel"}}); !strings.Contains(b, `value="Table 4"`) {
		t.Error("example is not English")
	}
	good := url.Values{"trunk": {"eth1"}, "host": {"250"}, "event": {"Testlan"}, "mode": {"active"},
		"vid": {"11", "12"}, "name": {"A", "B"}, "cidr": {"10.10.1.0/24", "10.10.2.0/24"}, "group": {"tische", "orga"}}
	for _, change := range []func(url.Values){
		func(v url.Values) { v.Set("trunk", "eth0") }, func(v url.Values) { v.Set("trunk_other", "ens99") }, func(v url.Values) { v.Del("trunk") },
		func(v url.Values) { v["vid"] = []string{"11"} }, func(v url.Values) { v["cidr"][1] = "x" }, func(v url.Values) { v["vid"][1] = "11" },
		func(v url.Values) { v["cidr"][1] = "10.10.0.0/23" }, func(v url.Values) { v["cidr"][1] = "10.10.2.0/28" },
		func(v url.Values) { v.Set("mode", "x") }, func(v url.Values) { v.Set("host", "x") }, func(v url.Values) { v.Set("event", "") },
		func(v url.Values) { v["group"][1] = "x" },
	} {
		v := url.Values{}
		for k, vals := range good {
			v[k] = append([]string(nil), vals...)
		}
		change(v)
		post(v)
	}
	e.eng.reject = "no"
	post(good)
	e.eng.reject = ""
	e.eng.down = true
	post(good)
	e.eng.down = false
	if b := post(good); !strings.Contains(b, "Setup finished") {
		t.Errorf("finish message is not English: %.200s", b)
	}
	get(t, c, e.srv.URL+"/system")
	get(t, c, e.srv.URL+"/verlauf")
	for _, s := range misses() {
		t.Errorf("no English text for %q", s)
	}
}

// The web part does not serve the UI on relay addresses: such connections are closed unread. Addresses on the
// interface with the default route stay open, even when the relay uses that interface.
func TestRelayAddressGuard(t *testing.T) {
	e := setup(t)
	var own net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			own = n.IP.To4()
			break
		}
	}
	relay := netif.HostIface{Name: "trunk0.1101", Kind: "vlan", Up: true, Relay: true, Addrs: []string{"10.10.1.55/24"}}
	if own != nil {
		relay.Addrs = append(relay.Addrs, own.String()+"/24")
	}
	mgmt := netif.HostIface{Name: "eth0", Kind: "device", Up: true, DefaultRoute: true, Addrs: []string{"192.0.2.10/24"}}
	// the server goroutine below asks for the interfaces while the test changes them
	var ifMu sync.Mutex
	e.ui.HostIfaces = func() ([]netif.HostIface, error) {
		ifMu.Lock()
		defer ifMu.Unlock()
		return []netif.HostIface{mgmt, relay}, nil
	}

	blocked := e.ui.relayAddrs(context.Background())
	for a, want := range map[string]bool{"10.10.1.55": true, "10.10.0.55": true, "192.0.2.10": false, "127.0.0.1": false} {
		if got := blocked[netip.MustParseAddr(a)]; got != want {
			t.Errorf("relay address %s: blocked=%v, want %v", a, got, want)
		}
	}
	// the relay on the management interface (a second instance for mDNS): its address there stays open
	ifMu.Lock()
	mgmt.Addrs = append(mgmt.Addrs, "10.10.0.55/24")
	ifMu.Unlock()
	if e.ui.relayAddrs(context.Background())[netip.MustParseAddr("10.10.0.55")] {
		t.Error("an address on the default-route interface is blocked")
	}
	if a := e.ui.mgmtAddr(); a.String() != "192.0.2.10" {
		t.Errorf("management address = %s", a)
	}

	if own == nil {
		t.Skip("no non-loopback IPv4 address: the listener part needs one")
	}
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e.ui}
	go srv.Serve(e.ui.Listener(ln))
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	hc := &http.Client{Timeout: 3 * time.Second}
	at := func(ip string) string { return "http://" + net.JoinHostPort(ip, strconv.Itoa(port)) + "/healthz" }
	if resp, err := hc.Get(at("127.0.0.1")); err != nil || resp.StatusCode != 200 {
		t.Errorf("loopback: %v", err)
	}
	if resp, err := hc.Get(at(own.String())); err == nil {
		resp.Body.Close()
		t.Errorf("the UI answered on a relay address (%s): %d", own, resp.StatusCode)
	}
	// the same address on the management interface is served
	ifMu.Lock()
	relay.Addrs = relay.Addrs[:1]
	mgmt.Addrs = append(mgmt.Addrs, own.String()+"/24")
	ifMu.Unlock()
	e.ui.guard.mu.Lock()
	e.ui.guard.blocked = nil
	e.ui.guard.mu.Unlock()
	if resp, err := hc.Get(at(own.String())); err != nil || resp.StatusCode != 200 {
		t.Errorf("management address: %v", err)
	}
}
