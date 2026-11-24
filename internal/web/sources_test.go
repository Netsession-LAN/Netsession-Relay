package web

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

func TestGameSources(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	admin, csrf := e.client("admin")
	post := func(path string, v url.Values) string {
		t.Helper()
		v.Set("csrf", csrf)
		r, err := admin.PostForm(e.srv.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.Request.URL.RawQuery
	}
	sourcesOf := func(id string) []string {
		c, _ := e.st.ExportConfig(ctx)
		for _, g := range c.Games {
			if g.ID == id {
				return g.Sources
			}
		}
		return []string{"(not switched on)"}
	}
	ruleOf := func(id string) compile.Rule {
		t.Helper()
		s := e.eng.applied[len(e.eng.applied)-1]
		for _, r := range s.Rules {
			if slices.Contains(r.Specs, id) {
				return r
			}
		}
		t.Fatalf("no rule for %s", id)
		return compile.Rule{}
	}

	if _, page := get(t, admin, e.srv.URL+"/spiele/flatout2"); !strings.Contains(page, `action="/spiele/flatout2/absender"`) {
		t.Fatal("game page without source filter form")
	}
	if q := post("/spiele/flatout2/absender", url.Values{"sources": {"drucker"}}); !strings.Contains(q, "fehler") || sourcesOf("flatout2") != nil {
		t.Errorf("invalid input: %s, stored %v", q, sourcesOf("flatout2"))
	}
	q := post("/spiele/flatout2/absender", url.Values{"sources": {"10.10.1.20, 10.9.9.9"}})
	if strings.Contains(q, "fehler") || !slices.Equal(sourcesOf("flatout2"), []string{"10.9.9.9", "10.10.1.20"}) {
		t.Fatalf("save: %s, stored %v", q, sourcesOf("flatout2"))
	}
	if m, _ := url.ParseQuery(q); !strings.Contains(m.Get("ok"), "Trifft kein Netz und wirkt nicht: 10.9.9.9.") {
		t.Errorf("no hint for the prefix outside every network: %s", q)
	}
	// every other game on the same ports has no filter yet: open wins
	shared := ruleOf("flatout2")
	if len(shared.Specs) > 1 && len(shared.SrcAllow) != 0 {
		t.Errorf("shared rule restricted although other games have no filter: %+v", shared)
	}
	for _, id := range shared.Specs {
		if id != "flatout2" {
			post("/spiele/"+id+"/absender", url.Values{"sources": {"10.10.1.20/32"}})
		}
	}
	if r := ruleOf("flatout2"); !slices.Equal(r.SrcAllow, []netip.Prefix{netip.MustParsePrefix("10.10.1.20/32")}) {
		t.Errorf("rule sent to the engine: %+v", r)
	}

	if _, page := get(t, admin, e.srv.URL+"/spiele/flatout2"); !strings.Contains(page, "Nur diese Absender") ||
		!strings.Contains(page, "trifft kein Netz") || !strings.Contains(page, `value="10.9.9.9 10.10.1.20"`) {
		t.Error("game page does not show the filter")
	}
	if _, page := get(t, admin, e.srv.URL+"/spiele"); !strings.Contains(page, "Absender-Filter") {
		t.Error("games list without filter marker")
	}
	// operators see the filter but cannot change it
	op, opcsrf := e.client("op")
	if _, page := get(t, op, e.srv.URL+"/spiele/flatout2"); !strings.Contains(page, "10.10.1.20") || strings.Contains(page, `/absender"`) {
		t.Error("operator view of the filter")
	}
	r, _ := op.PostForm(e.srv.URL+"/spiele/flatout2/absender", url.Values{"csrf": {opcsrf}, "sources": {""}})
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("operator changing the filter = %d", r.StatusCode)
	}

	// English: every new text is in the catalog
	misses := watchMisses(t)
	setLangCookie(t, admin, e.srv.URL, langEN)
	for _, p := range []string{"/spiele", "/spiele/flatout2", "/verlauf"} {
		if code, _ := get(t, admin, e.srv.URL+p); code != 200 {
			t.Errorf("GET %s = %d", p, code)
		}
	}
	for _, s := range misses() {
		t.Errorf("no English text for %q", s)
	}
	setLangCookie(t, admin, e.srv.URL, langDE)

	// clear; a switched-off game cannot get a filter
	if q := post("/spiele/flatout2/absender", url.Values{"sources": {" "}}); strings.Contains(q, "fehler") || sourcesOf("flatout2") != nil {
		t.Errorf("clear: %s, stored %v", q, sourcesOf("flatout2"))
	}
	post("/spiele/flatout2", url.Values{"on": {"0"}})
	if q := post("/spiele/flatout2/absender", url.Values{"sources": {"10.10.1.20"}}); !strings.Contains(q, "fehler") {
		t.Errorf("filter on a switched-off game: %s", q)
	}
}
