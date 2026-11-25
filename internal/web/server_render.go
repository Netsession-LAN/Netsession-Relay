package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// What a request gets back: rendered pages, JSON and redirects with a message. Errors worded for the user are in
// server_errors.go.

// ---- rendering ----

// page is the data every template gets.
type page struct {
	Title    string
	Nav      string
	User     store.User
	CSRF     string
	Version  string
	Flash    string
	Error    string
	Engine   engineapi.Status
	EngErr   string
	Live     *liveView
	Demo     bool
	Data     any
	Lang     string    // page language
	Path     string    // path with query, for the language switch
	Theme    string    // "netsession" or "98" (theme.go)
	Instance string    // Server.Instance
	Logo     *logoView // the event logo next to the product mark, nil without one (logo.go)
	W98      string    // theme "98" only: "shell" = the desktop, "frame" = a page inside a window, "" = one page as one window
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title, nav string, data any) {
	s.renderLive(w, r, name, title, nav, data, nil)
}

// renderLive renders a page; live is the live view the handler already fetched (nil: fetch it here).
func (s *Server) renderLive(w http.ResponseWriter, r *http.Request, name, title, nav string, data any, live *liveView) {
	se := sessionFrom(r)
	p := page{Title: title, Nav: nav, User: se.User, CSRF: se.CSRF, Version: s.Version, Data: data, Demo: s.Demo,
		Flash: r.URL.Query().Get("ok"), Error: r.URL.Query().Get("fehler"), Lang: s.langOf(r), Path: r.URL.RequestURI(), Theme: themeOf(r), Instance: s.Instance}
	T := trFor(p.Lang)
	p.Logo = s.logoView(r.Context(), T)
	if se.ID == "" { // no session (login page): only known messages, a link must not put its own text there
		p.Flash, p.Error = publicMessage(p.Flash), publicMessage(p.Error)
		w.Header().Set("Cache-Control", "no-store")
	}
	p.Title, p.Flash, p.Error = lookupQuiet(p.Lang, title), lookupQuiet(p.Lang, p.Flash), lookupQuiet(p.Lang, p.Error)
	if se.ID != "" {
		s.addSessionParts(w, r, &p, live, T)
	}
	t := s.tpl[p.Lang][name]
	if t == nil {
		http.Error(w, "template "+name+" missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout.html", p); err != nil {
		s.Log.Error("render", "page", name, "err", err)
	}
}

// addSessionParts fills in what only pages behind the login show: the frame of the theme "98" and the live view
// of the header (live: already fetched by the handler, nil: fetched here).
func (s *Server) addSessionParts(w http.ResponseWriter, r *http.Request, p *page, live *liveView, T translator) {
	p.W98 = w98Mode(r, p.Theme)
	if p.Theme == theme98 {
		w.Header().Add("Vary", "Sec-Fetch-Dest")
	}
	if live == nil {
		live = s.liveNow(r.Context())
	}
	p.Live, p.Engine = live, live.Engine
	if !live.EngineOK {
		p.EngErr = T("Engine nicht erreichbar.")
	}
}

// escapeArgs prepares the arguments of a text with markup: each one prints as an HTML-escaped string.
func escapeArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = htmlArg{a}
	}
	return out
}

// htmlArg is one argument of a text with markup. It prints HTML-escaped whatever the verb is; a plain number
// keeps the verbs for numbers (%d, %03d), it cannot carry markup.
type htmlArg struct{ v any }

func (a htmlArg) Format(f fmt.State, verb rune) {
	switch a.v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		if verb != 's' && verb != 'v' {
			fmt.Fprintf(f, fmt.FormatString(f, verb), a.v)
			return
		}
	}
	_, _ = io.WriteString(f, template.HTMLEscapeString(fmt.Sprint(a.v)))
}

// localFuncs adds the language-bound functions to the template functions: t translates, the name helpers that
// return German texts are translated too.
func localFuncs(base template.FuncMap, lang string) template.FuncMap {
	T := trFor(lang)
	f := maps.Clone(base)
	f["t"] = func(s string, args ...any) string { return T(s, args...) }
	// th: the text contains markup (trusted, from the templates and the catalog); arguments are escaped
	f["th"] = func(s string, args ...any) template.HTML { return template.HTML(T(s, escapeArgs(args)...)) } //nolint:gosec // G203: every variable part is escaped (HTMLEscapeString / escapeArgs)
	f["tq"] = func(s string) string { return lookupQuiet(lang, s) }                                         // may be a name or a message from a library
	f["lang"] = func() string { return lang }
	// raw JSON inside <script type="application/json">; json.Marshal escapes <, > and &
	f["jsCatalog"] = func() template.JS { return template.JS(jsCatalog(lang)) } //nolint:gosec // G203: every variable part is escaped (HTMLEscapeString / escapeArgs)
	translateNameFuncs(f, T)
	return f
}

// translatedNameFuncs are the template functions that return a German name; localFuncs translates their result.
var translatedNameFuncs = []string{"modeName", "roleName", "roleLabel", "group", "resultName", "kindName", "dirName", "confName", "resultBadge", "hostIssue"}

// translateNameFuncs wraps the name functions of f (translatedNameFuncs) so their result is translated by T.
func translateNameFuncs(f template.FuncMap, T translator) {
	for _, k := range translatedNameFuncs {
		fn := f[k].(func(string) string)
		f[k] = func(s string) string { return T(fn(s)) }
	}
}

// page returns the template of a page in the language of r (for fragments rendered without the layout).
func (s *Server) page(r *http.Request, name string) *template.Template {
	return s.tpl[s.langOf(r)][name]
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	msg = lookupQuiet(s.langOf(r), msg) // callers pass the German text or a sentence that is translated already
	if wantsJSON(r) {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	w.WriteHeader(code)
	s.render(w, r, "error", "Fehler", "", msg)
}

// back redirects to a page with a flash message (ok) or error (fehler).
//
// The message travels as text in the query string and is translated when the page is rendered (the German text
// is the key of the English catalog, see i18n.go). That keeps the redirect stateless. The price is that the text
// is display-only: nothing may branch on it. Packages outside web return errors with an identity (errors.Is,
// e.g. store.ErrExists, eventlogo.ErrFormat); the handler maps them to a uimsg.Error or a translated sentence
// right before it calls back. Without a session only the messages in publicMessages are shown, so a link
// cannot put its own text on the login page.
func back(w http.ResponseWriter, r *http.Request, path, okMsg string, err error) {
	q := "?ok=" + urlQuery(okMsg)
	if err != nil {
		q = "?fehler=" + urlQuery(err.Error())
	}
	frag := ""
	if i := strings.IndexByte(path, '#'); i >= 0 { // the message goes before the fragment ("/system#abfrage")
		path, frag = path[:i], path[i:]
	}
	http.Redirect(w, r, path+q+frag, http.StatusSeeOther) //nolint:gosec // G710: callers pass fixed paths, fixed paths with an escaped ID, or backPath (checked by localPath)
}
