// Package web serves the admin UI (server-rendered Go templates in the Netsession brand),
// the JSON API, login and roles.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/auth"
	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/control"
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"github.com/Netsession2024/Netsession-Relay/internal/netif"
	"github.com/Netsession2024/Netsession-Relay/internal/outputs"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
	"github.com/Netsession2024/Netsession-Relay/internal/uimsg"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Engine is what the web part needs from the engine (engineapi.Client, or a fake in tests).
type Engine interface {
	Status(context.Context) (engineapi.Status, error)
	Validate(context.Context, *compile.Snapshot) (engineapi.Result, error)
	Apply(context.Context, *compile.Snapshot) (engineapi.Result, error)
	Players(context.Context) ([]engineapi.Player, error)
	Ack(ctx context.Context, kind, by string) error
	FinderStart(context.Context, engineapi.FinderStart) error
	FinderStop(context.Context) error
	Finder(context.Context) (engineapi.FinderStatus, error)
	FinderPcap(context.Context, io.Writer) error
}

// Server is the web part.
type Server struct {
	Store   *store.Store
	Engine  Engine
	Log     *slog.Logger
	Version string
	// Secure forces the Secure flag on cookies and HSTS (NSRELAY_COOKIE_SECURE=1), Insecure switches both off
	// (=0). With neither, HTTPS is detected per request (security.go, secure).
	Secure   bool
	Insecure bool
	// PublicHost is the host of the public HTTPS address (SetPublicURL); requests for it get Secure cookies.
	PublicHost string
	// TrustedProxies are the reverse proxies whose forwarding headers count (NSRELAY_TRUSTED_PROXIES).
	TrustedProxies []netip.Prefix
	// Demo marks the UI when the engine is the built-in fake (nsrelay web -demo).
	Demo bool
	// DefaultLang is the UI language without a choice by the user ("de" or "en", NSRELAY_LANG; default German).
	DefaultLang string
	// Instance names this relay when several run side by side (NSRELAY_INSTANCE, e.g. "mDNS"); empty for the main one.
	Instance string
	// LiveInterval is the pause between two live samples (default 2 s).
	LiveInterval time.Duration

	// Outputs are the monitoring outputs: push targets and the SNMP agent. cmd/nsrelay tells it the listen
	// address (SetBindAddr) and closes it at shutdown.
	Outputs *outputs.Service

	// HostIfaces lists the interfaces of the host (default: netif.Interfaces); tests and the demo set their own.
	HostIfaces func() ([]netif.HostIface, error)
	// BackupDir is where the automatic backup writes (NSRELAY_BACKUP_DIR), empty = no automatic backup;
	// BackupKeep is how many it keeps. Only shown on the system page, cmd/nsrelay runs it.
	BackupDir  string
	BackupKeep int

	guard      relayGuard                      // addresses the UI is not served on (guard.go)
	logo       atomic.Pointer[store.EventLogo] // the event logo, nil without one (logo.go)
	hasAccount atomic.Bool                     // somebody can log in: the first-run setup for accounts is closed (setup.go)

	oidc    *oidcClient
	started time.Time
	tpl     map[string]map[string]*template.Template // language -> page -> template
	mux     *http.ServeMux
	hub     *liveHub
	ruleMu  sync.Mutex
	ruleIdx *ruleIndex

	loginLimit *ipLimiter // POST /login/local and /oidc/* per client address
	throttle   loginThrottle
	verifySem  chan struct{}
	streams    streamCount

	done      chan struct{} // closed by Close: live streams and the hub end
	closeOnce sync.Once
}

// New builds the server and parses all templates.
func New(st *store.Store, eng Engine, log *slog.Logger, version string) (*Server, error) {
	s := &Server{Store: st, Engine: eng, Log: log, Version: version, tpl: map[string]map[string]*template.Template{}, started: time.Now(),
		hub: &liveHub{subs: map[chan []byte]struct{}{}}, done: make(chan struct{}),
		loginLimit: newIPLimiter(20, 3*time.Second), verifySem: make(chan struct{}, verifySlots)}
	base := template.FuncMap{
		"static":      func(p string) string { return "/static/" + p + "?v=" + version },
		"icon":        icon,
		"icon98":      icon98,
		"join":        strings.Join,
		"has":         slices.Contains[[]string],
		"list":        func(v ...string) []string { return v },
		"upper":       strings.ToUpper,
		"short":       func(h string) string { return shortHash(h) },
		"num":         formatNum,
		"hasRole":     func(u store.User, role string) bool { return store.RoleRank(u.Role) >= store.RoleRank(role) },
		"modeName":    modeName,
		"add":         func(a, b int) int { return a + b },
		"ts":          timeTag,
		"dict":        dict,
		"roleName":    roleName,
		"group":       groupName,
		"resultName":  resultName,
		"serviceURL":  serviceFormURL,
		"resultTone":  resultTone,
		"resultIcon":  resultIcon,
		"resultBadge": resultBadge,
		"viewShows":   func(view string, on bool) bool { return view == viewAll || (view == viewOn) == on },
		"roleLabel":   roleLabel,
		"kindName":    kindName,
		"dirName":     dirName,
		"confName":    confName,
		"hostIssue":   hostIssueText,
	}
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	for _, lang := range langs {
		funcs := localFuncs(base, lang)
		s.tpl[lang] = map[string]*template.Template{}
		for _, p := range pages {
			name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
			if name == "layout" {
				continue
			}
			t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", p)
			if err != nil {
				return nil, err
			}
			s.tpl[lang][name] = t
		}
	}
	s.routes()
	s.loadLogo(context.Background())
	s.Outputs = outputs.New(outputs.Options{Store: st, Log: log, Version: version, Report: s.monitorReport, MgmtAddr: s.mgmtAddr})
	st.SetOnAudit(s.auditToOutputs)
	return s, nil
}

// Close ends what would keep a shutdown waiting: the open live streams and the poll that feeds them. The HTTP
// server calls it before it shuts down (cmd/nsrelay); requests that are not streams finish normally. It does not
// close the monitoring outputs (Outputs.Close).
func (s *Server) Close() {
	s.closeOnce.Do(func() { close(s.done) })
}

// ServeHTTP implements http.Handler with security headers.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'self'; form-action 'self'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "SAMEORIGIN") // the theme "98" shows pages in windows (iframes of the same origin)
	if s.secure(r) {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	rc := http.NewResponseController(w)
	if r.URL.Path != "/api/v1/events" { // the live stream sets a deadline per event
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	}
	if r.Body != nil && r.Body != http.NoBody {
		// bodies are small forms: bounded in size and in the time a client may take to send them. The one upload
		// (the event logo) gets more of both, and only for an admin: nobody else makes the server read that much.
		limit, wait := int64(MaxBody), bodyTimeout
		upload := r.Method == http.MethodPost && r.URL.Path == logoUploadPath && s.adminSession(r)
		if upload {
			limit, wait = MaxLogoBody, uploadTimeout
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		_ = rc.SetReadDeadline(time.Now().Add(wait))
		// reads the whole body now (forms from pages are urlencoded, the ones from app.js multipart); files stay
		// in memory, nothing is written to a temporary directory
		err := r.ParseForm()
		if err == nil && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			err = r.ParseMultipartForm(limit)
		}
		if err != nil {
			code := http.StatusBadRequest
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				code = http.StatusRequestEntityTooLarge
				if upload { // a page the admin can read instead of a bare status
					back(w, r, logoPanel, "", msgLogoTooBig)
					return
				}
			}
			http.Error(w, http.StatusText(code), code)
			return
		}
		_ = rc.SetReadDeadline(time.Time{})
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	m := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	fileServer := http.StripPrefix("/static/", http.FileServerFS(static))
	m.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fileServer.ServeHTTP(w, r)
	}))
	m.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/ns/logo/favicon.ico", http.StatusMovedPermanently)
	})
	m.HandleFunc("GET /healthz", s.healthz)
	m.HandleFunc("GET "+logoPath, s.eventLogo) // public: the login and setup pages show it
	m.HandleFunc("GET /sprache/{lang}", s.setLang)
	m.HandleFunc("GET /aussehen/{theme}", s.setTheme)
	m.HandleFunc("GET /einrichten", s.setupPage)
	m.HandleFunc("POST /einrichten/konto", s.setupAccount)
	m.Handle("POST /einrichten/netz", s.need(store.RoleAdmin, s.setupNetwork))
	m.HandleFunc("GET /login", s.loginPage)
	m.HandleFunc("GET /login/local", s.loginPage)
	m.HandleFunc("POST /login/local", s.loginLocal)
	m.HandleFunc("POST /logout", s.logout)
	m.HandleFunc("GET /oidc/login", s.oidcLogin)
	m.HandleFunc("GET /oidc/callback", s.oidcCallback)

	m.Handle("GET /{$}", s.need(store.RoleViewer, s.overview))
	m.Handle("GET /spiele", s.need(store.RoleViewer, s.games))
	m.Handle("GET /spiele/{id}", s.need(store.RoleViewer, s.game))
	m.Handle("POST /spiele/{id}", s.need(store.RoleOperator, s.toggleGame))
	m.Handle("GET /spiele/neu", s.need(store.RoleOperator, s.gameNewForm))
	m.Handle("POST /spiele/neu", s.need(store.RoleOperator, s.gameCreate))
	m.Handle("GET /spiele/{id}/bearbeiten", s.need(store.RoleOperator, s.gameEditForm))
	m.Handle("POST /spiele/{id}/bearbeiten", s.need(store.RoleOperator, s.gameUpdate))
	m.Handle("POST /spiele/{id}/loeschen", s.need(store.RoleOperator, s.gameDelete))
	m.Handle("GET /netz", s.need(store.RoleAdmin, s.network))
	m.Handle("POST /netz/{vid}", s.need(store.RoleAdmin, s.toggleVLAN))
	m.Handle("GET /netz/neu", s.need(store.RoleAdmin, s.vlanNewForm))
	m.Handle("POST /netz/neu", s.need(store.RoleAdmin, s.vlanCreate))
	m.Handle("GET /netz/{vid}/bearbeiten", s.need(store.RoleAdmin, s.vlanEditForm))
	m.Handle("POST /netz/{vid}/bearbeiten", s.need(store.RoleAdmin, s.vlanUpdate))
	m.Handle("POST /netz/{vid}/loeschen", s.need(store.RoleAdmin, s.vlanDelete))
	m.Handle("POST /profil/neu", s.need(store.RoleAdmin, s.profileCreate))
	m.Handle("POST /profil/{name}", s.need(store.RoleAdmin, s.profileSave))
	m.Handle("POST /profil/{name}/loeschen", s.need(store.RoleAdmin, s.profileDelete))
	m.Handle("POST /profil/{name}/standard", s.need(store.RoleAdmin, s.profileDefault))
	m.Handle("POST /spiele/{id}/profil", s.need(store.RoleAdmin, s.gameProfile))
	m.Handle("POST /spiele/{id}/absender", s.need(store.RoleAdmin, s.gameSources))
	m.Handle("POST /profil/{name}/kante", s.need(store.RoleAdmin, s.profileEdge))
	m.Handle("POST /profil/{name}/umbenennen", s.need(store.RoleAdmin, s.profileRename))
	m.Handle("POST /spiele/{id}/verifikation", s.need(store.RoleOperator, s.verifyAdd))
	m.Handle("POST /spiele/{id}/verifikation/{vid}/loeschen", s.need(store.RoleOperator, s.verifyDelete))
	m.Handle("GET /spiele/{id}/export.yaml", s.need(store.RoleViewer, s.gameYAML))
	m.Handle("GET /spiele/export.yaml", s.need(store.RoleViewer, s.changedGamesYAML))
	m.Handle("GET /system/konfiguration.yaml", s.need(store.RoleAdmin, s.configYAML))
	m.Handle("GET /system/monitoring", s.need(store.RoleAdmin, s.monitoringPage))
	m.Handle("POST /system/monitoring/name", s.need(store.RoleAdmin, s.monitoringHost))
	m.Handle("POST /system/monitoring/snmp", s.need(store.RoleAdmin, s.agentSave))
	m.Handle("GET /system/monitoring/ziel/neu", s.need(store.RoleAdmin, s.targetNew))
	m.Handle("GET /system/monitoring/mib", s.need(store.RoleViewer, s.mibFile))
	m.Handle("GET /system/monitoring/ziel/{id}", s.need(store.RoleAdmin, s.targetEdit))
	m.Handle("POST /system/monitoring/ziel", s.need(store.RoleAdmin, s.targetSave))
	m.Handle("POST /system/monitoring/ziel/{id}/test", s.need(store.RoleAdmin, s.targetTest))
	m.Handle("POST /system/monitoring/ziel/{id}/loeschen", s.need(store.RoleAdmin, s.targetDelete))
	m.Handle("POST /system/dienste", s.need(store.RoleAdmin, s.serviceSet))
	m.Handle("POST /system/dienste/{ip}/loeschen", s.need(store.RoleAdmin, s.serviceDelete))
	m.Handle("GET /system", s.need(store.RoleAdmin, s.system))
	m.Handle("POST /system/mode", s.need(store.RoleAdmin, s.setMode))
	m.Handle("POST /system/server-abfrage", s.need(store.RoleAdmin, s.setProbe))
	m.Handle("POST "+logoUploadPath, s.need(store.RoleAdmin, s.logoSet))
	m.Handle("POST /system/logo/entfernen", s.need(store.RoleAdmin, s.logoRemove))
	m.Handle("POST /system/notaus", s.need(store.RoleOperator, s.emergencyStop))
	m.Handle("POST /system/wieder-an", s.need(store.RoleOperator, s.emergencyRelease))
	m.Handle("POST /system/rollback/{id}", s.need(store.RoleAdmin, s.rollback))
	m.Handle("GET /system/backup", s.need(store.RoleAdmin, s.downloadBackup))
	m.Handle("GET /notaus", s.need(store.RoleOperator, s.emergencyConfirm))
	m.Handle("GET /verlauf", s.need(store.RoleAdmin, s.history))
	m.Handle("GET /protokoll", http.RedirectHandler("/verlauf", http.StatusSeeOther))
	m.Handle("GET /api/v1/events", s.need(store.RoleViewer, s.apiEvents))
	m.Handle("GET /spieler", s.need(store.RoleViewer, s.players))
	m.Handle("POST /alarme/{kind}/gesehen", s.need(store.RoleOperator, s.ackAlarm))
	m.Handle("GET /finder", s.need(store.RoleOperator, s.finder))
	m.Handle("POST /finder/start", s.need(store.RoleOperator, s.finderStart))
	m.Handle("POST /finder/stop", s.need(store.RoleOperator, s.finderStop))
	m.Handle("GET /finder/mitschnitt.pcap", s.need(store.RoleOperator, s.finderPcap))
	m.Handle("GET /finder/neu", s.need(store.RoleOperator, s.finderNewForm))
	m.Handle("POST /finder/neu", s.need(store.RoleOperator, s.gameCreate))
	m.Handle("GET /dev/komponenten", s.need(store.RoleViewer, s.components)) // design reference, demo mode only
	m.Handle("GET /api/v1/status", s.need(store.RoleViewer, s.apiStatus))
	m.Handle("GET /api/v1/monitoring", s.monitorAuth(s.apiMonitoring))
	m.Handle("GET /metrics", s.monitorAuth(s.metrics))
	m.Handle("POST /system/monitoring-token", s.need(store.RoleAdmin, s.newMonitorToken))
	s.mux = m
}

// ---- sessions, roles, CSRF ----

type ctxKey struct{}

func sessionFrom(r *http.Request) store.Session {
	se, _ := r.Context().Value(ctxKey{}).(store.Session)
	return se
}

// need wraps a handler: valid session, minimum role, CSRF token on writes.
func (s *Server) need(role string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store") // nothing behind the login belongs into a cache
		T := s.T(r)
		se, err := s.Store.SessionByToken(r.Context(), s.sessionToken(r))
		if err != nil {
			if wantsJSON(r) || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": T("Nicht angemeldet.")})
				return
			}
			if s.needsAccount(r.Context()) { // a fresh installation: every page leads to the setup
				http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		// the handler that renders the refusal needs the session too (name and menu on the error page)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, se))
		if store.RoleRank(se.User.Role) < store.RoleRank(role) {
			s.fail(w, r, http.StatusForbidden, T("Dafür fehlt dir die Rolle %s.", T(roleName(role))))
			return
		}
		if r.Method == http.MethodPost && !sameToken(csrfToken(r), se.CSRF) {
			s.fail(w, r, http.StatusForbidden, msgCSRF)
			return
		}
		h(w, r)
	})
}

// msgCSRF is shown when a form comes without the token of the session (an old tab, or another site).
const msgCSRF = "Sitzung abgelaufen oder Formular veraltet. Lade die Seite neu."

// csrfToken is the token a write request carries: the header set by app.js, else the hidden form field.
func csrfToken(r *http.Request) string {
	if tok := r.Header.Get("X-CSRF-Token"); tok != "" {
		return tok
	}
	return r.PostFormValue("csrf")
}

func (s *Server) actor(r *http.Request) string { return sessionFrom(r).User.Name }

// audit writes an audit entry for something that already happened. The action is not undone when the entry
// cannot be written; the failure is logged instead, with what the entry would have said.
func (s *Server) audit(ctx context.Context, actor, action, object, before, after string) {
	if err := s.Store.Audit(ctx, actor, action, object, before, after); err != nil {
		s.Log.Error("audit entry not written", "action", action, "user", actor, "object", object, "err", err)
	}
}

// degraded logs a failed read for a page that is rendered anyway, without the part that could not be read.
// A request the client gave up is not worth a line.
func (s *Server) degraded(what string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		s.Log.Error("page shown without data that could not be read", "what", what, "err", err)
	}
}

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
	f["th"] = func(s string, args ...any) template.HTML {
		for i, a := range args {
			args[i] = template.HTMLEscapeString(fmt.Sprint(a))
		}
		return template.HTML(T(s, args...))
	}
	f["tq"] = func(s string) string { return lookupQuiet(lang, s) } // may be a name or a message from a library
	f["lang"] = func() string { return lang }
	// raw JSON inside <script type="application/json">; json.Marshal escapes <, > and &
	f["jsCatalog"] = func() template.JS { return template.JS(jsCatalog(lang)) }
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

// ---- login ----

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(r.Context()); err != nil {
		s.Log.Error("healthz: database", "err", err)
		http.Error(w, "database not available", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.needsAccount(r.Context()) {
		http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
		return
	}
	d := map[string]any{"OIDC": s.oidc != nil, "LoginCSRF": s.loginCSRF(w, r)}
	if s.oidc != nil {
		c := s.oidc.cfg
		d["OIDCName"], d["GroupAdmin"], d["GroupOperator"], d["GroupViewer"] = c.Name, c.GroupAdmin, c.GroupOperator, c.GroupViewer
	}
	s.render(w, r, "login", "Anmelden", "login", d)
}

// loginLocal is the emergency login with a local account. Order: budget per client address, form token,
// pause per (address, name) after failures, then the password check (a few at a time). Every refusal reads the
// same whether the name exists or not.
//
// The attempt is counted as failed before the password is checked and only taken back when the password was
// right. So attempts that arrive at the same moment cannot all be checked before the first failure is known:
// after the free ones the next has to wait, however many connections the client opens.
func (s *Server) loginLocal(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	pw := r.PostFormValue("password")
	ip := s.clientIP(r)
	now := time.Now()
	if !s.loginLimit.allow(limitKey(ip), now) {
		s.Log.Warn("login refused: too many attempts from this address", "ip", ip)
		back(w, r, "/login", "", errThrottle)
		return
	}
	if c, err := r.Cookie(loginCookie); err != nil || !sameToken(r.PostFormValue("csrf"), c.Value) {
		back(w, r, "/login", "", errStale)
		return
	}
	key := throttleKey(ip, name)
	if d := s.throttle.begin(key, now); d > 0 {
		s.Log.Warn("login refused: pause after failed attempts", "name", name, "ip", ip, "wait", d.Round(time.Second))
		back(w, r, "/login", "", errThrottle)
		return
	}
	u, err := s.Store.UserByName(r.Context(), name)
	known := err == nil && u.Kind == "local" && u.PasswordHash != ""
	hash := u.PasswordHash
	if !known {
		hash = dummyHash() // same work for an unknown name as for a wrong password
	}
	ran, verr := s.verifyPassword(r.Context(), pw, hash)
	if !ran {
		s.throttle.cancel(key) // no password was checked: the attempt does not count
		s.Log.Warn("login refused: all password checks busy", "ip", ip)
		back(w, r, "/login", "", errBusy)
		return
	}
	if !known || verr != nil {
		if known {
			if err := s.Store.LoginFailed(r.Context(), u, ip); err != nil {
				s.Log.Error("audit entry not written", "action", "login.failed", "user", u.Name, "object", ip, "err", err)
			}
		}
		if known && !errors.Is(verr, auth.ErrMismatch) { // the stored hash itself is broken, not the password
			s.Log.Error("login: stored password hash unusable", "name", name, "err", verr)
		}
		s.Log.Warn("login failed", "name", name, "ip", ip)
		back(w, r, "/login", "", errLogin)
		return
	}
	s.throttle.reset(key)
	token := auth.Token(32)
	if err := s.Store.CreateSession(r.Context(), u, token, auth.Token(24), ip); err != nil {
		s.Log.Error("login: session", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "Anmeldung gerade nicht möglich.")
		return
	}
	s.setSessionCookie(w, r, token, int(store.SessionTTL.Seconds()))
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	s.Log.Info("login", "name", name, "ip", ip)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// dummyHash is a valid hash no password of a user matches. It is computed at the first login attempt, not at
// program start: one Argon2id run takes 64 MiB, and most subcommands never check a password.
var dummyHash = sync.OnceValue(func() string {
	h, err := auth.Hash("timing-equaliser")
	if err != nil {
		// only when the system has no random numbers; without them no login can work either
		panic("web: cannot compute the placeholder password hash: " + err.Error())
	}
	return h
})

// logout ends the session of the browser. With a valid session the form token is required like for every other
// write, so another site cannot log a user out by posting to this address. Without a session there is nothing
// to protect: the cookies are cleared and the login page follows.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if tok := s.sessionToken(r); tok != "" {
		se, err := s.Store.SessionByToken(r.Context(), tok)
		if err == nil && !sameToken(csrfToken(r), se.CSRF) {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, se))
			s.fail(w, r, http.StatusForbidden, msgCSRF)
			return
		}
		if err := s.Store.DeleteSession(r.Context(), tok); err != nil {
			// the cookie goes, but the session stays valid on the server until it expires
			s.Log.Error("logout: session not deleted", "err", err)
		}
	}
	// both names: the plain one may be left from before HTTPS was detected
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.SetCookie(w, &http.Cookie{Name: cookieNameSecure, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
