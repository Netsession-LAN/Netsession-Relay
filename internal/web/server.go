// Package web serves the admin UI (server-rendered Go templates in the Netsession brand),
// the JSON API, login and roles.
package web

import (
	"context"
	"embed"
	"errors"
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

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
	"github.com/Netsession-LAN/Netsession-Relay/internal/outputs"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
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
	// ScratchDirs are writable directories for short-lived files (the copy of a backup download), tried in this
	// order before the temporary directory of the system: the backup directory and the one of the database.
	ScratchDirs []string

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
	if err := s.parseTemplates(templateFuncs(version)); err != nil {
		return nil, err
	}
	s.routes()
	s.loadLogo(context.Background())
	s.Outputs = outputs.New(outputs.Options{Store: st, Log: log, Version: version, Report: s.monitorReport, MgmtAddr: s.mgmtAddr})
	st.SetOnAudit(s.auditToOutputs)
	return s, nil
}

// templateFuncs are the template functions that do not depend on the language (localFuncs adds those that do).
func templateFuncs(version string) template.FuncMap {
	return template.FuncMap{
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
}

// parseTemplates parses every page with the layout, once per language.
func (s *Server) parseTemplates(base template.FuncMap) error {
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
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
				return err
			}
			s.tpl[lang][name] = t
		}
	}
	return nil
}

// Close ends what would keep a shutdown waiting: the open live streams and the poll that feeds them. The HTTP
// server calls it before it shuts down (cmd/nsrelay); requests that are not streams finish normally. It does not
// close the monitoring outputs (Outputs.Close).
func (s *Server) Close() {
	s.closeOnce.Do(func() { close(s.done) })
}

// ServeHTTP implements http.Handler with security headers.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.setSecurityHeaders(w, r)
	rc := http.NewResponseController(w)
	if r.URL.Path != "/api/v1/events" { // the live stream sets a deadline per event
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
	}
	if r.Body != nil && r.Body != http.NoBody && !s.readForm(w, r, rc) {
		return
	}
	s.mux.ServeHTTP(w, r)
}

// setSecurityHeaders sets the headers every answer carries; HSTS only over HTTPS.
func (s *Server) setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'self'; form-action 'self'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "SAMEORIGIN") // the theme "98" shows pages in windows (iframes of the same origin)
	if s.secure(r) {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

// readForm reads the request body into the form values. Bodies are small forms: bounded in size and in the
// time a client may take to send them. The one upload (the event logo) gets more of both, and only for an
// admin: nobody else makes the server read that much. It reports false when it has answered the request itself
// (a body too large or malformed).
func (s *Server) readForm(w http.ResponseWriter, r *http.Request, rc *http.ResponseController) bool {
	limit, wait := int64(MaxBody), bodyTimeout
	upload := r.Method == http.MethodPost && r.URL.Path == logoUploadPath && s.adminSession(r)
	if upload {
		limit, wait = MaxLogoBody, uploadTimeout
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	_ = rc.SetReadDeadline(time.Now().Add(wait))
	if err := parseBody(r, limit); err != nil {
		refuseBody(w, r, err, upload)
		return false
	}
	_ = rc.SetReadDeadline(time.Time{})
	return true
}

// parseBody reads the whole body now (forms from pages are urlencoded, the ones from app.js multipart); files
// stay in memory, nothing is written to a temporary directory.
func parseBody(r *http.Request, limit int64) error {
	err := r.ParseForm()
	if err == nil && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		err = r.ParseMultipartForm(limit)
	}
	return err
}

// refuseBody answers a request whose body could not be read: 413 when it was too large (for the logo upload a
// page the admin can read instead of a bare status), else 400.
func refuseBody(w http.ResponseWriter, r *http.Request, err error, upload bool) {
	var tooBig *http.MaxBytesError
	switch {
	case !errors.As(err, &tooBig):
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	case upload:
		back(w, r, logoPanel, "", msgLogoTooBig)
	default:
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
	}
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

// ---- logging ----

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

// ---- health ----

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(r.Context()); err != nil {
		s.Log.Error("healthz: database", "err", err)
		http.Error(w, "database not available", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}
