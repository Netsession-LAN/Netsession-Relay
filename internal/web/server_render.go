package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// What a request gets back: rendered pages, JSON, redirects with a message, and errors worded for the user.

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
	f := template.FuncMap{}
	for k, v := range base {
		f[k] = v
	}
	f["t"] = func(s string, args ...any) string { return T(s, args...) }
	// th: the text contains markup (trusted, from the templates and the catalog); arguments are escaped
	f["th"] = func(s string, args ...any) template.HTML { return template.HTML(T(s, escapeArgs(args)...)) } //nolint:gosec // G203: every variable part is escaped (HTMLEscapeString / escapeArgs)
	f["tq"] = func(s string) string { return lookupQuiet(lang, s) }                                         // may be a name or a message from a library
	f["lang"] = func() string { return lang }
	// raw JSON inside <script type="application/json">; json.Marshal escapes <, > and &
	f["jsCatalog"] = func() template.JS { return template.JS(jsCatalog(lang)) } //nolint:gosec // G203: every variable part is escaped (HTMLEscapeString / escapeArgs)
	for _, k := range []string{"modeName", "roleName", "roleLabel", "group", "resultName", "kindName", "dirName", "confName", "resultBadge", "hostIssue"} {
		fn := base[k].(func(string) string)
		f[k] = func(s string) string { return T(fn(s)) }
	}
	return f
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
	http.Redirect(w, r, path+q+frag, http.StatusSeeOther)
}

// ---- errors for the user ----

// msgInternal is what the user reads when something failed that cannot be fixed from the page (database, file
// system, a bug). The cause is in the log, not on the page: texts of the database driver or of the operating
// system help nobody at the browser and do not belong into a URL.
const msgInternal = "Das hat wegen eines internen Fehlers nicht geklappt. Die Ursache steht im Log des Relays."

// internal logs err with what was being done and returns the sentence shown instead, in the language of r.
func (s *Server) internal(r *http.Request, what string, err error) uimsg.Error {
	if !errors.Is(err, context.Canceled) {
		s.Log.Error("request failed", "what", what, "method", r.Method, "path", r.URL.Path, "user", s.actor(r), "err", err)
	}
	return uimsg.Error(s.T(r)(msgInternal))
}

// failInternal answers a request with the page for an internal error (HTTP 500).
func (s *Server) failInternal(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.fail(w, r, http.StatusInternalServerError, string(s.internal(r, what, err)))
}

// userError turns an error from below the web part into what the user reads, in the language of r. A message
// that is already worded (uimsg.Error) is translated; the errors with an identity that can reach any handler
// get a general sentence (handlers that know better map them before they call this); everything else is an
// internal error: logged, and shown without its text.
func (s *Server) userError(r *http.Request, what string, err error) uimsg.Error {
	T := s.T(r)
	var worded uimsg.Error
	switch {
	case errors.As(err, &worded):
		return uimsg.Error(lookupQuiet(s.langOf(r), string(worded)))
	case errors.Is(err, store.ErrNotFound):
		return uimsg.Error(T("Das gibt es nicht (mehr). Lade die Seite neu."))
	case errors.Is(err, store.ErrExists):
		return uimsg.Error(T("Das gibt es schon."))
	case errors.Is(err, store.ErrInvalid):
		return uimsg.Error(T("Die Eingabe ist ungültig."))
	}
	return s.internal(r, what, err)
}

// applyError words a failed apply or rollback (control.Apply, control.Rollback). The mistakes of a configuration
// and the reason of the engine are shown as they are: they name networks, games and interfaces, and the admin
// needs them to fix the configuration.
func (s *Server) applyError(r *http.Request, res control.ApplyResult, err error) uimsg.Error {
	T := s.T(r)
	switch {
	case errors.Is(err, control.ErrConfigInvalid):
		return uimsg.Error(T("Die Konfiguration passt nicht zusammen: %s", res.Invalid))
	case errors.Is(err, control.ErrEngineRejected):
		return uimsg.Error(T("Die Engine hat den Stand abgelehnt und läuft mit dem alten weiter: %s", res.Engine.Error))
	case errors.Is(err, control.ErrRevisionNotFound):
		return uimsg.Error(T("Diese Revision gibt es nicht."))
	case errors.Is(err, control.ErrRevisionRestore):
		s.Log.Error("rollback: revision cannot be restored", "user", s.actor(r), "err", err)
		return uimsg.Error(T("Dieser Stand lässt sich nicht wiederherstellen, meist weil er ein Spiel enthält, das es nicht mehr gibt. Details stehen im Log."))
	case errors.Is(err, control.ErrEngineUnreachable):
		s.Log.Error("apply: engine not reachable", "user", s.actor(r), "err", err)
		return uimsg.Error(T("Die Engine antwortet nicht. Die Änderung ist gespeichert, aber nicht angewendet."))
	}
	return s.userError(r, "apply", err)
}
