package web

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/monitor"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

var verbRe = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)

// Every entry translates to something and keeps the fmt verbs in the same order.
func TestCatalogEN(t *testing.T) {
	if len(catalogEN) == 0 {
		t.Fatal("empty English catalog")
	}
	for de, en := range catalogEN {
		if strings.TrimSpace(en) == "" {
			t.Errorf("%q has an empty translation", de)
		}
		if a, b := verbRe.FindAllString(de, -1), verbRe.FindAllString(en, -1); strings.Join(a, " ") != strings.Join(b, " ") {
			t.Errorf("%q -> %q: verbs %v vs %v", de, en, a, b)
		}
	}
}

// watchMisses records the German texts that are asked for in English and have no entry, from now until the test
// ends. The returned function gives what was recorded so far, sorted.
func watchMisses(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	report := func(s string) {
		mu.Lock()
		seen[s] = true
		mu.Unlock()
	}
	onMissEN.Store(&report)
	t.Cleanup(func() { onMissEN.Store(nil) })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, 0, len(seen))
		for s := range seen {
			out = append(out, s)
		}
		sort.Strings(out)
		return out
	}
}

func setLangCookie(t *testing.T, c *http.Client, base, lang string) {
	t.Helper()
	u, _ := url.Parse(base)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: langCookie, Value: lang, Path: "/"}})
}

// englishPages are rendered in English by TestEnglishComplete; every text asked for must be in the catalog.
var englishPages = []string{"/", "/spiele", "/spiele?zeige=alle", "/spiele/flatout2", "/spiele/flatout4", "/netz", "/system",
	"/system/monitoring", "/system/monitoring/ziel/neu", "/verlauf", "/notaus", "/spieler", "/spieler?teil=tabelle", "/finder",
	"/finder?teil=ergebnis", "/finder/neu?kind=multicast&group=239.1.2.3&port=5000", "/netz/neu", "/netz/1101/bearbeiten",
	"/spiele/neu", "/spiele/flatout2/bearbeiten", "/spiele/flatout2?teil=diagnose"}

func TestEnglishComplete(t *testing.T) {
	e := setup(t)
	misses := watchMisses(t)
	anon, _ := e.client("")
	setLangCookie(t, anon, e.srv.URL, langEN)
	if code, body := get(t, anon, e.srv.URL+"/login"); code != 200 || !strings.Contains(body, `<html lang="en">`) {
		t.Errorf("/login in English = %d", code)
	}
	c, _ := e.client("admin")
	setLangCookie(t, c, e.srv.URL, langEN)
	for _, down := range []bool{false, true} {
		e.eng.down = down
		for _, p := range englishPages {
			if code, body := get(t, c, e.srv.URL+p); code != 200 {
				t.Errorf("GET %s (engine down=%v) = %d: %.200s", p, down, code, body)
			}
		}
	}
	e.eng.down = false
	// the port finder with a running session and results
	e.eng.finder = &engineapi.FinderStart{VIDs: []int{1101}, Seconds: 60}
	for _, p := range []string{"/finder", "/finder?teil=ergebnis", "/finder?laerm=1"} {
		if code, body := get(t, c, e.srv.URL+p); code != 200 || !strings.Contains(body, "First bytes") {
			t.Errorf("GET %s with finder results = %d", p, code)
		}
	}
	e.eng.finder = nil
	// the joke theme "98" has its own markup (Start menu, taskbar)
	u, _ := url.Parse(e.srv.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: themeCookie, Value: theme98, Path: "/"}})
	if code, body := get(t, c, e.srv.URL+"/"); code != 200 || !strings.Contains(body, `class="w98-taskbar"`) {
		t.Errorf("GET / in the 98 theme = %d", code)
	}
	if code, body := getDest(t, c, e.srv.URL+"/", "document"); code != 200 || !strings.Contains(body, `id="w98-win-tpl"`) {
		t.Errorf("GET / as the 98 desktop = %d", code)
	}
	for _, s := range misses() {
		t.Errorf("no English text for %q", s)
	}
}

func TestLangSwitch(t *testing.T) {
	e := setup(t)
	c, _ := e.client("view")
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for back, want := range map[string]string{"/spiele?zeige=aus": "/spiele?zeige=aus", "//evil.example": "/", "https://evil.example": "/"} {
		resp, err := c.Get(e.srv.URL + "/sprache/en?zurueck=" + url.QueryEscape(back))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
			t.Errorf("switch back to %q: %d -> %q", back, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	c.CheckRedirect = nil
	if _, body := get(t, c, e.srv.URL+"/spiele"); !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, `href="/sprache/de?`) {
		t.Error("cookie does not switch the page to English")
	} else if !strings.Contains(body, `<script type="application/json" id="i18n">{"`) {
		t.Error("script catalog is not embedded as a JSON object")
	}
	if code, _ := get(t, c, e.srv.URL+"/sprache/fr"); code != 404 {
		t.Errorf("unknown language = %d", code)
	}
	de, _ := e.client("view")
	if _, body := get(t, de, e.srv.URL+"/spiele"); !strings.Contains(body, `<html lang="de">`) {
		t.Error("default is not German")
	}
}

// app.js looks texts up with t("…"): every one is listed in jsTexts (so the page embeds it) and translated.
func TestScriptTexts(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, k := range jsTexts {
		listed[k] = true
		if _, ok := catalogEN[k]; !ok {
			t.Errorf("script text %q has no English entry", k)
		}
	}
	for _, m := range regexp.MustCompile(`\bt\("([^"]*)"`).FindAllStringSubmatch(string(b), -1) {
		if !listed[m[1]] {
			t.Errorf("app.js uses %q, missing in jsTexts", m[1])
		}
	}
}

// Page titles are looked up quietly (they may contain names), so the fixed ones are checked here in the source.
func TestTitlesTranslated(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`\.render(?:Live)?\(w, r, "[^"]+", "([^"]+)"`)
	n := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			n++
			if _, ok := catalogEN[m[1]]; !ok {
				t.Errorf("%s: page title %q has no English entry", f, m[1])
			}
		}
	}
	if n == 0 {
		t.Error("no page titles found")
	}
}

// Messages (flash texts, errors, JSON answers) are written in German in the Go code like every other text. The
// ones that are fixed sentences are looked up when the page is rendered, so a forgotten catalog entry would
// silently stay German on an English page. This test reads the handlers and checks every such sentence; a
// sentence built with "+" cannot have an entry and has to be written with T("… %s …") instead.
func TestMessagesTranslated(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	const str = `"((?:[^"\\]|\\.)*)"`
	patterns := map[string]*regexp.Regexp{
		"uimsg.Error":       regexp.MustCompile(`uimsg\.Error\(` + str + `\)`),
		"back(ok)":          regexp.MustCompile(`\bback\(w, r, [^,]+, ` + str + `\s*[,+]`),
		"fail":              regexp.MustCompile(`\.fail\(w, r, [A-Za-z.]+, ` + str + `\s*[)+]`),
		"message constant":  regexp.MustCompile(`(?m)^\s*msg[A-Z]\w*\s*=\s*` + str + `\s*$`),
		"answer text":       regexp.MustCompile(`\b(?:Error|Message):\s*` + str),
		"answer assignment": regexp.MustCompile(`\.(?:Error|Message) = ` + str),
		"page title":        regexp.MustCompile(`\btitle := ` + str),
		"JSON error":        regexp.MustCompile(`"error":\s*` + str),
	}
	// answers of the machine interfaces (monitoring token, live stream) are English on purpose: no page shows them
	machine := map[string]bool{"monitoring token not configured (System page)": true, "missing or wrong token": true, "too many live streams": true}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for kind, re := range patterns {
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				text, err := strconv.Unquote(`"` + m[1] + `"`)
				if err != nil {
					t.Fatalf("%s: %s: %v", f, m[0], err)
				}
				if text == "" || machine[text] {
					continue
				}
				n++
				switch kind {
				case "answer text", "answer assignment", "JSON error":
					// these leave as JSON without passing the page renderer: they must be translated where they are made
					t.Errorf("%s: %s %q is not translated (use T)", f, kind, text)
				default:
					if _, ok := catalogEN[text]; !ok {
						t.Errorf("%s: %s %q has no English entry", f, kind, text)
					}
				}
			}
		}
	}
	if n < 20 {
		t.Errorf("only %d message texts found: the patterns of this test no longer match the code", n)
	}
}

// What the user reads when something goes wrong is English on an English page: refusals, messages after a
// redirect, answers to the script and the pages for errors.
func TestEnglishErrorPaths(t *testing.T) {
	e := setup(t)
	misses := watchMisses(t)
	german := regexp.MustCompile(`[äöüÄÖÜß]|\b(nicht|gibt|bitte|Bitte|fehlt|und|oder|ist)\b`)
	// message returns the flash or error text of a page, or the whole body of an error page
	message := func(body string) string {
		if m := regexp.MustCompile(`(?s)<[^>]*class="(?:flash|note)[^"]*"[^>]*>(.*?)</(?:p|div)>`).FindStringSubmatch(body); m != nil {
			return regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[1], "")
		}
		return body
	}
	check := func(what, got, want string) {
		t.Helper()
		if !strings.Contains(got, want) {
			t.Errorf("%s: %.300q, want it to contain %q", what, message(got), want)
		}
		if msg := message(got); len(msg) < 400 && german.MatchString(msg) {
			t.Errorf("%s: German text on an English page: %q", what, msg)
		}
	}

	anon, _ := e.client("")
	setLangCookie(t, anon, e.srv.URL, langEN)
	// every message the login page may show
	for de := range publicMessages {
		_, body := get(t, anon, e.srv.URL+"/login?fehler="+url.QueryEscape(de))
		check("login page with "+de, body, template.HTMLEscapeString(catalogEN[de]))
	}
	tok := loginToken(t, anon, e.srv.URL)
	r, err := anon.PostForm(e.srv.URL+"/login/local", url.Values{"name": {"admin"}, "password": {"wrong"}, "csrf": {tok}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	check("wrong password", string(b), "Wrong name or password.")
	// the script asks without a session
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/status", nil)
	req.Header.Set("Accept", "application/json")
	if r, err = anon.Do(req); err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	check("JSON without a session", string(b), "Not signed in.")

	view, _ := e.client("view")
	setLangCookie(t, view, e.srv.URL, langEN)
	_, body := get(t, view, e.srv.URL+"/system")
	check("page above the role", body, "You need the role Admin for this.")

	admin, csrf := e.client("admin")
	setLangCookie(t, admin, e.srv.URL, langEN)
	post := func(path string, v url.Values, json bool) string {
		t.Helper()
		if v.Get("csrf") == "" {
			v.Set("csrf", csrf)
		}
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if json {
			req.Header.Set("Accept", "application/json")
		}
		r, err := admin.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return string(b)
	}
	check("stale form", post("/spiele/flatout2", url.Values{"on": {"0"}, "csrf": {"wrong"}}, false), "Session expired or form outdated. Reload the page.")
	check("stale form (script)", post("/spiele/flatout2", url.Values{"on": {"0"}, "csrf": {"wrong"}}, true), "Session expired or form outdated. Reload the page.")
	check("unknown game", post("/spiele/nope", url.Values{"on": {"1"}}, true), "This game does not exist.")
	check("unknown network", post("/netz/4000", url.Values{"on": {"1"}}, true), "This network does not exist.")
	check("unknown mode", post("/system/mode", url.Values{"mode": {"turbo"}}, false), "This mode does not exist.")
	check("unknown revision", post("/system/rollback/999", url.Values{}, false), "This revision does not exist.")
	check("reserved profile name", post("/profil/neu", url.Values{"name": {"neu"}}, false), "is reserved")
	check("bad profile name", post("/profil/neu", url.Values{"name": {"Not OK!"}}, false), "Profile name:")
	check("profile created", post("/profil/neu", url.Values{"name": {"zwei"}}, false), "Profile zwei created.")
	check("profile exists", post("/profil/neu", url.Values{"name": {"zwei"}}, false), "A profile with this name already exists.")
	check("default profile", post("/profil/zwei/standard", url.Values{}, true), "Profile zwei is now the default.")
	check("delete the default profile", post("/profil/zwei/loeschen", url.Values{}, false), "The default profile cannot be deleted.")
	check("profile of a game", post("/spiele/flatout2/profil", url.Values{"profile": {"zwei"}}, true), "Profile changed and applied.")
	check("bad matrix cell", post("/profil/zwei/kante", url.Values{"src": {""}, "dst": {"tische"}}, true), "Cell invalid.")
	check("unknown test result", post("/spiele/flatout2/verifikation", url.Values{"result": {"maybe"}}, false), "Please choose a result.")
	check("bad service host", post("/system/dienste", url.Values{"ip": {"nope"}, "name": {"x"}}, false), "Please give an IPv4 address and a name")
	check("SNMP port", post("/system/monitoring/snmp", url.Values{"port": {"0"}}, false), "Please give a port between 1 and 65535")
	check("SNMP agent off", post("/system/monitoring/snmp", url.Values{"port": {"1161"}}, false), "SNMP agent is off.")
	check("seed game cannot be deleted", post("/spiele/flatout2/loeschen", url.Values{}, false), "only switched off.")
	check("unknown game page", post("/spiele/nope/bearbeiten", url.Values{}, false), "This game does not exist.")
	if _, body := get(t, admin, e.srv.URL+"/spiele/nope"); !strings.Contains(body, "This game does not exist.") {
		t.Errorf("page of an unknown game is not in English: %.300s", message(body))
	}

	// the engine rejects, or does not answer
	e.eng.rejectApply = `trunk interface "trunk0" missing`
	check("rejected by the engine", post("/spiele/flatout2", url.Values{"on": {"0"}}, true), `The engine rejected the revision and keeps running the old one: trunk interface \"trunk0\" missing`)
	e.eng.rejectApply = ""
	e.eng.down = true
	check("engine away", post("/spiele/flatout2", url.Values{"on": {"1"}}, true), "The engine does not answer. The change is saved but not applied.")
	check("acknowledge without the engine", post("/alarme/loop/gesehen", url.Values{}, true), "The engine does not answer. The alarm is not marked as acknowledged.")
	e.eng.down = false

	for _, s := range misses() {
		t.Errorf("no English text for %q", s)
	}
}

// An error nobody mapped is an internal error: the user reads one general sentence, the text of the database
// driver or of the operating system goes to the log only. Errors with an identity and worded messages pass.
func TestInternalErrorsStayInTheLog(t *testing.T) {
	e := setup(t)
	var logged strings.Builder
	e.ui.Log = slog.New(slog.NewTextHandler(&logged, nil))
	for lang, want := range map[string]string{langDE: msgInternal, langEN: catalogEN[msgInternal]} {
		req := httptest.NewRequest(http.MethodPost, "/system/mode", nil)
		req.AddCookie(&http.Cookie{Name: langCookie, Value: lang})
		raw := errors.New(`sql: database is closed: SQLITE_BUSY (5) at /var/lib/nsrelay-web/relay.db`)
		if got := e.ui.userError(req, "mode: set", raw); string(got) != want || want == "" {
			t.Errorf("%s: internal error shown as %q, want %q", lang, got, want)
		}
		if got := e.ui.applyError(req, control.ApplyResult{}, raw); string(got) != want {
			t.Errorf("%s: internal error of an apply shown as %q, want %q", lang, got, want)
		}
	}
	if !strings.Contains(logged.String(), "database is closed") || !strings.Contains(logged.String(), "mode: set") {
		t.Errorf("the cause is not in the log: %s", logged.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: langCookie, Value: langEN})
	for err, want := range map[error]string{
		fmt.Errorf("%w: game %q", store.ErrNotFound, "x"): "That does not exist (any more). Reload the page.",
		fmt.Errorf("%w: profile", store.ErrExists):        "That exists already.",
		fmt.Errorf("%w: name", store.ErrInvalid):          "The input is invalid.",
		msgProfileReserved:                                catalogEN[string(msgProfileReserved)],
	} {
		if got := e.ui.userError(req, "test", err); string(got) != want || want == "" {
			t.Errorf("userError(%v) = %q, want %q", err, got, want)
		}
	}
	res := control.ApplyResult{Invalid: errors.New("VLAN 1 and 2: networks overlap")}
	if got := e.ui.applyError(req, res, fmt.Errorf("%w:\n%w", control.ErrConfigInvalid, res.Invalid)); string(got) != "The configuration does not fit together: VLAN 1 and 2: networks overlap" {
		t.Errorf("invalid configuration shown as %q", got)
	}
}

// Every reason the engine can report for a frame has a label, and every label has its English text. The pages
// only show the reasons that occurred, so a missing one would go unnoticed until it does.
func TestDropReasonsHaveLabels(t *testing.T) {
	for _, r := range append([]string{"relayed"}, monitor.DropReasons...) {
		label, ok := reasonLabels[r]
		if !ok {
			t.Errorf("reason %q has no label", r)
			continue
		}
		if _, ok := catalogEN[label]; !ok {
			t.Errorf("label %q of reason %q has no English entry", label, r)
		}
	}
	if len(reasonLabels) != len(monitor.DropReasons)+1 {
		t.Errorf("%d labels for %d reasons: a label without a reason?", len(reasonLabels), len(monitor.DropReasons)+1)
	}
}
