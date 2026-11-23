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
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"github.com/Netsession2024/Netsession-Relay/internal/eventlogo"
	"github.com/Netsession2024/Netsession-Relay/internal/netif"
	"github.com/Netsession2024/Netsession-Relay/internal/notify"
	"github.com/Netsession2024/Netsession-Relay/internal/snmp"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
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

	// BindAddr is the source address of outgoing monitoring connections (set with SetBindAddr).
	BindAddr netip.Addr
	// SNMPAddr overrides the address the SNMP agent binds (NSRELAY_SNMP_ADDR); default BindAddr.
	SNMPAddr string

	// HostIfaces lists the interfaces of the host (default: netif.Interfaces); tests and the demo set their own.
	HostIfaces func() ([]netif.HostIface, error)
	// BackupDir is where the automatic backup writes (NSRELAY_BACKUP_DIR), empty = no automatic backup;
	// BackupKeep is how many it keeps. Only shown on the system page, cmd/nsrelay runs it.
	BackupDir  string
	BackupKeep int

	guard      relayGuard                      // addresses the UI is not served on (guard.go)
	logo       atomic.Pointer[store.EventLogo] // the event logo, nil without one (logo.go)
	hasAccount atomic.Bool                     // somebody can log in: the first-run setup for accounts is closed (setup.go)

	agentMu  sync.Mutex
	agent    *snmp.Agent
	agentErr string

	oidc    *oidcClient
	notify  *notify.Dispatcher
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
}

// New builds the server and parses all templates.
func New(st *store.Store, eng Engine, log *slog.Logger, version string) (*Server, error) {
	s := &Server{Store: st, Engine: eng, Log: log, Version: version, tpl: map[string]map[string]*template.Template{}, started: time.Now(),
		hub:        &liveHub{subs: map[chan []byte]struct{}{}},
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
		"tableKey":    tableKey,
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
		"viewShows":   func(view string, on bool) bool { return view == "alle" || (view == "an") == on },
		"kindName":    kindName,
		"dirName":     dirName,
		"confName":    confName,
		"alarmNets":   alarmNetsText,
		"shortName":   shortName,
		"sub":         func(a, b int) int { return a - b },
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
	s.initOutputs()
	st.SetOnAudit(s.auditToOutputs)
	return s, nil
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
					back(w, r, logoPanel, "", eventlogo.ErrTooBig)
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
	m.Handle("POST /system/syslog", s.need(store.RoleAdmin, s.syslogSet))
	m.Handle("POST /system/dienste", s.need(store.RoleAdmin, s.serviceSet))
	m.Handle("POST /system/dienste/{ip}/loeschen", s.need(store.RoleAdmin, s.serviceDelete))
	m.Handle("POST /system/syslog/test", s.need(store.RoleAdmin, s.syslogTest))
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
	m.Handle("GET /dev/komponenten", s.need(store.RoleViewer, s.components))
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
		se, err := s.Store.SessionByToken(r.Context(), s.sessionToken(r))
		if err != nil {
			if wantsJSON(r) || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Nicht angemeldet."})
				return
			}
			if s.needsAccount(r.Context()) { // a fresh installation: every page leads to the setup
				http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if store.RoleRank(se.User.Role) < store.RoleRank(role) {
			s.fail(w, r, http.StatusForbidden, "Dafür fehlt dir die Rolle "+roleName(role)+".")
			return
		}
		if r.Method == http.MethodPost {
			tok := r.Header.Get("X-CSRF-Token")
			if tok == "" {
				tok = r.PostFormValue("csrf")
			}
			if !sameToken(tok, se.CSRF) {
				s.fail(w, r, http.StatusForbidden, "Sitzung abgelaufen oder Formular veraltet. Lade die Seite neu.")
				return
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, se)))
	})
}

func (s *Server) actor(r *http.Request) string { return sessionFrom(r).User.Name }

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
	for _, k := range []string{"modeName", "roleName", "group", "resultName", "kindName", "dirName", "confName", "resultBadge", "hostIssue"} {
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
	if wantsJSON(r) {
		writeJSON(w, code, map[string]string{"error": msg})
		return
	}
	w.WriteHeader(code)
	s.render(w, r, "error", "Fehler", "", msg)
}

// back redirects to a page with a flash message (ok) or error (fehler).
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
	if err := s.Store.DB.PingContext(r.Context()); err != nil {
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
	s.render(w, r, "login", "Anmelden", "login", map[string]any{"OIDC": s.oidc != nil, "LoginCSRF": s.loginCSRF(w, r)})
}

// loginLocal is the emergency login with a local account. Order: budget per client address, form token,
// pause per (address, name) after failures, then the password check (a few at a time). Every refusal reads the
// same whether the name exists or not.
func (s *Server) loginLocal(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	pw := r.PostFormValue("password")
	ip := s.clientIP(r)
	now := time.Now()
	if !s.loginLimit.allow(ip, now) {
		s.Log.Warn("login refused: too many attempts from this address", "ip", ip)
		back(w, r, "/login", "", errThrottle)
		return
	}
	if c, err := r.Cookie(loginCookie); err != nil || !sameToken(r.PostFormValue("csrf"), c.Value) {
		back(w, r, "/login", "", errStale)
		return
	}
	key := throttleKey(ip, name)
	if d := s.throttle.wait(key, now); d > 0 {
		s.Log.Warn("login refused: pause after failed attempts", "name", name, "ip", ip, "wait", d.Round(time.Second))
		back(w, r, "/login", "", errThrottle)
		return
	}
	u, err := s.Store.UserByName(r.Context(), name)
	known := err == nil && u.Kind == "local" && u.PasswordHash != ""
	hash := dummyHash // same work for an unknown name as for a wrong password
	if known {
		hash = u.PasswordHash
	}
	ran, verr := s.verifyPassword(r.Context(), pw, hash)
	if !ran {
		s.Log.Warn("login refused: all password checks busy", "ip", ip)
		back(w, r, "/login", "", errBusy)
		return
	}
	if !known || verr != nil {
		s.throttle.fail(key, now)
		if known {
			_ = s.Store.LoginFailed(r.Context(), u, ip)
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

var dummyHash, _ = auth.Hash("timing-equaliser")

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if tok := s.sessionToken(r); tok != "" {
		_ = s.Store.DeleteSession(r.Context(), tok)
	}
	// both names: the plain one may be left from before HTTPS was detected
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.SetCookie(w, &http.Cookie{Name: cookieNameSecure, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
