package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
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

func setLangCookie(t *testing.T, c *http.Client, base, lang string) {
	t.Helper()
	u, _ := url.Parse(base)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: langCookie, Value: lang, Path: "/"}})
}

// englishPages are rendered in English by TestEnglishComplete; every text asked for must be in the catalog.
var englishPages = []string{"/", "/spiele", "/spiele?ansicht=alle", "/spiele/flatout2", "/spiele/flatout4", "/netz", "/system",
	"/system/monitoring", "/system/monitoring/ziel/neu", "/verlauf", "/notaus", "/spieler", "/spieler?teil=tabelle", "/finder",
	"/finder?teil=ergebnis", "/finder/neu?kind=multicast&group=239.1.2.3&port=5000", "/netz/neu", "/netz/1101/bearbeiten",
	"/spiele/neu", "/spiele/flatout2/bearbeiten", "/spiele/flatout2?teil=diagnose"}

func TestEnglishComplete(t *testing.T) {
	e := setup(t)
	missMu.Lock()
	missesEN = map[string]bool{}
	missMu.Unlock()
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
	// the joke theme "98" has its own markup (Start menu, taskbar)
	u, _ := url.Parse(e.srv.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: themeCookie, Value: theme98, Path: "/"}})
	if code, body := get(t, c, e.srv.URL+"/"); code != 200 || !strings.Contains(body, `class="w98-taskbar"`) {
		t.Errorf("GET / in the 98 theme = %d", code)
	}
	if code, body := getDest(t, c, e.srv.URL+"/", "document"); code != 200 || !strings.Contains(body, `id="w98-win-tpl"`) {
		t.Errorf("GET / as the 98 desktop = %d", code)
	}
	missMu.Lock()
	var miss []string
	for s := range missesEN {
		miss = append(miss, s)
	}
	missMu.Unlock()
	sort.Strings(miss)
	for _, s := range miss {
		t.Errorf("no English text for %q", s)
	}
}

func TestLangSwitch(t *testing.T) {
	e := setup(t)
	c, _ := e.client("view")
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for back, want := range map[string]string{"/spiele?ansicht=an": "/spiele?ansicht=an", "//evil.example": "/", "https://evil.example": "/"} {
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
