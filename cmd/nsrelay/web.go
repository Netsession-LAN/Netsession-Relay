package main

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/web"
)

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

// runWeb starts the web part: database + seed sync, admin UI (internal/web), /healthz, /api/v1/status.
func runWeb(args []string, _, stderr io.Writer) int {
	fs := newFlags("web", stderr)
	var af adminFlags
	af.register(fs)
	addr := fs.String("addr", envOr("NSRELAY_HTTP_ADDR", "127.0.0.1:8080"), "listen address; \":8080\" = every address of the host except the relay addresses")
	demoMode := fs.Bool("demo", false, "development only: fake engine with invented traffic, throwaway database, accounts demo/op/view (password demo)")
	demoSetup := fs.Bool("demo-setup", false, "development only: like -demo, but with an empty database, so the first-run setup shows")
	demoLogo := fs.String("demo-logo", "", "development only, with -demo or -demo-setup: PNG or JPEG file shown as the event logo")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	// one context for the whole web part: SIGINT/SIGTERM end the HTTP server and every background task
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *demoMode || *demoSetup {
		return runWebDemo(ctx, *addr, log, *demoSetup, *demoLogo)
	}
	st, err := store.Open(af.db)
	if err != nil {
		log.Error("database", "err", err)
		return 1
	}
	defer st.Close()
	syncSeed(ctx, st, log)
	eng := engineapi.NewClient(af.socket)
	ui, err := web.New(st, eng, log, version)
	if err != nil {
		log.Error("web templates", "err", err)
		return 1
	}
	configureFromEnv(ui, log)
	warnOpenSetup(ctx, st, log)
	ui.Outputs.SetSNMPAddr(os.Getenv("NSRELAY_SNMP_ADDR"))
	ui.Outputs.SetBindAddr(ctx, *addr)
	defer ui.Outputs.Close()
	go ui.WatchAlarms(ctx, 10*time.Second) // alarm open/clear events for the monitoring targets
	go refreshAfterUpdate(ctx, st, eng, log)
	setupBackups(ctx, ui, st, af.db, log)
	return serveWeb(ctx, *addr, ui, log, af.db)
}

// syncSeed brings the embedded game database into the store. The game database is embedded and checked by the
// tests; if it is broken all the same, the web part still starts with the games the database already has.
func syncSeed(ctx context.Context, st *store.Store, log *slog.Logger) {
	seed, err := gamedb.Seed()
	if err != nil {
		log.Error("embedded game database is invalid, games are not updated", "err", err)
		return
	}
	res, err := st.SyncSeed(ctx, seed)
	if err != nil {
		log.Error("game database not synced into the store", "err", err)
		return
	}
	log.Info("seed sync", "added", res.Added, "updated", res.Updated, "pending", res.Pending)
}

// configureFromEnv sets what the environment decides about the UI: cookies, public URL, trusted proxies,
// language, instance name and single sign-on.
func configureFromEnv(ui *web.Server, log *slog.Logger) {
	// Secure cookies and HSTS follow the request (TLS, X-Forwarded-Proto from a trusted proxy, the public HTTPS
	// name); NSRELAY_COOKIE_SECURE=1 forces them, =0 switches them off.
	ui.Secure, ui.Insecure = os.Getenv("NSRELAY_COOKIE_SECURE") == "1", os.Getenv("NSRELAY_COOKIE_SECURE") == "0"
	ui.SetPublicURL(cmp.Or(os.Getenv("NSRELAY_PUBLIC_URL"), os.Getenv("NSRELAY_OIDC_REDIRECT_URL")))
	if px, err := web.ParseProxies(os.Getenv("NSRELAY_TRUSTED_PROXIES")); err != nil {
		log.Error("NSRELAY_TRUSTED_PROXIES ignored", "err", err)
	} else if ui.TrustedProxies = px; len(px) > 0 {
		log.Info("forwarding headers are trusted from", "proxies", px)
	}
	ui.DefaultLang = os.Getenv("NSRELAY_LANG")  // "en" for English without a choice by the user; default German
	ui.Instance = os.Getenv("NSRELAY_INSTANCE") // name of a second instance, e.g. "mDNS"
	ui.SetOIDC(oidcFromEnv())
}

// oidcFromEnv is the single sign-on configuration from the NSRELAY_OIDC_* variables (no issuer: off).
func oidcFromEnv() web.OIDCConfig {
	return web.OIDCConfig{Issuer: os.Getenv("NSRELAY_OIDC_ISSUER"), ClientID: os.Getenv("NSRELAY_OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("NSRELAY_OIDC_CLIENT_SECRET"), RedirectURL: os.Getenv("NSRELAY_OIDC_REDIRECT_URL"),
		// optional: the name on the login button and the groups that give the roles (defaults in web.OIDCConfig)
		Name: os.Getenv("NSRELAY_OIDC_NAME"), GroupAdmin: os.Getenv("NSRELAY_OIDC_GROUP_ADMIN"),
		GroupOperator: os.Getenv("NSRELAY_OIDC_GROUP_OPERATOR"), GroupViewer: os.Getenv("NSRELAY_OIDC_GROUP_VIEWER")}
}

// warnOpenSetup warns loudly when there is no local account and no single sign-on: then the first-run setup
// page is open to whoever comes first.
func warnOpenSetup(ctx context.Context, st *store.Store, log *slog.Logger) {
	n, err := st.LocalAccounts(ctx)
	if err != nil {
		log.Error("accounts not readable", "err", err)
		return
	}
	if n == 0 && os.Getenv("NSRELAY_OIDC_ISSUER") == "" {
		log.Warn("no account yet: THE SETUP PAGE IS OPEN. Whoever opens the web UI first creates the admin account "+
			"(or use `nsrelay admin set-password <name>`)", "page", "/einrichten")
	}
}

// setupBackups gives backup downloads their scratch directories and starts the nightly backup without a timer on
// the host: on unless NSRELAY_BACKUP_AUTO=0 or no directory (NSRELAY_BACKUP_DIR) is set.
func setupBackups(ctx context.Context, ui *web.Server, st *store.Store, db string, log *slog.Logger) {
	dir := os.Getenv("NSRELAY_BACKUP_DIR")
	// a backup download needs a place for its copy: the root file system of the container is read-only
	if dir != "" {
		ui.ScratchDirs = append(ui.ScratchDirs, dir)
	}
	ui.ScratchDirs = append(ui.ScratchDirs, filepath.Dir(db))
	if dir == "" || os.Getenv("NSRELAY_BACKUP_AUTO") == "0" {
		return
	}
	ui.BackupDir, ui.BackupKeep = dir, envInt("NSRELAY_BACKUP_KEEP", 14)
	go control.AutoBackup(ctx, st, dir, ui.BackupKeep, func(path string, err error) {
		if err != nil {
			log.Error("automatic backup failed", "dir", dir, "err", err)
			return
		}
		log.Info("automatic backup written", "file", path, "keep", ui.BackupKeep)
	})
	log.Info("automatic backup: daily", "dir", dir, "keep", ui.BackupKeep)
}

// refreshAfterUpdate re-applies the unchanged configuration once the engine answers, so a new version's compiler
// takes effect right after a deploy (control.Refresh). Tries for two minutes; the engine may start later.
func refreshAfterUpdate(ctx context.Context, st *store.Store, eng *engineapi.Client, log *slog.Logger) {
	for range 24 {
		if refreshOnce(ctx, st, eng, log) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	log.Warn("could not check whether the configuration needs a re-apply after the update (engine not reachable)")
}

// refreshOnce tries control.Refresh once (20 s at most) and reports whether the engine answered.
func refreshOnce(ctx context.Context, st *store.Store, eng *engineapi.Client, log *slog.Logger) bool {
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rev, err := control.Refresh(tctx, st, eng, "Neu übersetzt nach Update auf "+version)
	if err != nil {
		return false
	}
	if rev > 0 {
		log.Info("configuration re-applied after update", "revision", rev, "version", version)
	}
	return true
}

// serveWeb serves the UI until ctx ends, then gives open requests five seconds to finish. The live streams of
// open pages never finish by themselves: they are ended first (ui.Close), so they do not hold the shutdown for
// the whole five seconds. The requests do not get ctx as their base context on purpose: a change that is being
// stored and applied when the signal arrives is finished, not cut in the middle.
func serveWeb(ctx context.Context, addr string, ui *web.Server, log *slog.Logger, db string) int {
	// Request bodies and responses get their deadlines per request in web.Server.ServeHTTP (the live stream must
	// not have a write timeout); the server bounds the header phase, idle connections and the header size.
	srv := &http.Server{Addr: addr, Handler: ui, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 64 << 10}
	go func() { //nolint:gosec // G118: shuts down after ctx is done, so it needs a fresh context
		<-ctx.Done()
		ui.Close()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			log.Warn("web: open requests cut off at shutdown", "err", err)
		}
	}()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("web cannot listen", "addr", addr, "err", err)
		return 1
	}
	log.Info("nsrelay web listening", "addr", addr, "version", version, "db", db)
	// the guard closes connections that arrive on a relay address (participant networks) before reading them
	if err := srv.Serve(ui.Listener(ln)); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("web stopped", "err", err)
		return 1
	}
	return 0
}
