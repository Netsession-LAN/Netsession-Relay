package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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

	"github.com/Netsession2024/Netsession-Relay/internal/auth"
	"github.com/Netsession2024/Netsession-Relay/internal/control"
	"github.com/Netsession2024/Netsession-Relay/internal/demo"
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
	"github.com/Netsession2024/Netsession-Relay/internal/netif"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
	"github.com/Netsession2024/Netsession-Relay/internal/web"
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
	// The game database is embedded and checked by the tests; if it is broken all the same, the web part still
	// starts with the games the database already has.
	if seed, err := gamedb.Seed(); err != nil {
		log.Error("embedded game database is invalid, games are not updated", "err", err)
	} else if res, err := st.SyncSeed(ctx, seed); err != nil {
		log.Error("game database not synced into the store", "err", err)
	} else {
		log.Info("seed sync", "added", res.Added, "updated", res.Updated, "pending", res.Pending)
	}
	eng := engineapi.NewClient(af.socket)
	ui, err := web.New(st, eng, log, version)
	if err != nil {
		log.Error("web templates", "err", err)
		return 1
	}
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
	ui.SetOIDC(web.OIDCConfig{Issuer: os.Getenv("NSRELAY_OIDC_ISSUER"), ClientID: os.Getenv("NSRELAY_OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("NSRELAY_OIDC_CLIENT_SECRET"), RedirectURL: os.Getenv("NSRELAY_OIDC_REDIRECT_URL"),
		// optional: the name on the login button and the groups that give the roles (defaults in web.OIDCConfig)
		Name: os.Getenv("NSRELAY_OIDC_NAME"), GroupAdmin: os.Getenv("NSRELAY_OIDC_GROUP_ADMIN"),
		GroupOperator: os.Getenv("NSRELAY_OIDC_GROUP_OPERATOR"), GroupViewer: os.Getenv("NSRELAY_OIDC_GROUP_VIEWER")})
	if n, err := st.LocalAccounts(ctx); err != nil {
		log.Error("accounts not readable", "err", err)
	} else if n == 0 && os.Getenv("NSRELAY_OIDC_ISSUER") == "" {
		log.Warn("no account yet: THE SETUP PAGE IS OPEN. Whoever opens the web UI first creates the admin account "+
			"(or use `nsrelay admin set-password <name>`)", "page", "/einrichten")
	}
	ui.Outputs.SetSNMPAddr(os.Getenv("NSRELAY_SNMP_ADDR"))
	ui.Outputs.SetBindAddr(ctx, *addr)
	defer ui.Outputs.Close()
	go ui.WatchAlarms(ctx, 10*time.Second) // alarm open/clear events for the monitoring targets
	go refreshAfterUpdate(ctx, st, eng, log)
	// Nightly backup without a timer on the host: on unless NSRELAY_BACKUP_AUTO=0 or no directory is set.
	if dir := os.Getenv("NSRELAY_BACKUP_DIR"); dir != "" && os.Getenv("NSRELAY_BACKUP_AUTO") != "0" {
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
	return serveWeb(ctx, *addr, ui, log, af.db)
}

// refreshAfterUpdate re-applies the unchanged configuration once the engine answers, so a new version's compiler
// takes effect right after a deploy (control.Refresh). Tries for two minutes; the engine may start later.
func refreshAfterUpdate(ctx context.Context, st *store.Store, eng *engineapi.Client, log *slog.Logger) {
	for range 24 {
		tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		rev, err := control.Refresh(tctx, st, eng, "Neu übersetzt nach Update auf "+version)
		cancel()
		if err == nil {
			if rev > 0 {
				log.Info("configuration re-applied after update", "revision", rev, "version", version)
			}
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

// runWebDemo serves the UI with demo.Engine and a fresh database in a temporary directory. It never opens the
// real database or the engine socket, so it cannot change a running relay.
func runWebDemo(ctx context.Context, addr string, log *slog.Logger, fresh bool, logo string) int {
	dir, err := os.MkdirTemp("", "nsrelay-demo-")
	if err != nil {
		log.Error("demo", "err", err)
		return 1
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(filepath.Join(dir, "demo.db"))
	if err != nil {
		log.Error("database", "err", err)
		return 1
	}
	defer st.Close()
	if fresh {
		return runWebDemoSetup(ctx, addr, st, log, logo)
	}
	if _, err := control.Init(ctx, st, "demo", "netsession", "ens19", false); err != nil {
		log.Error("demo init", "err", err)
		return 1
	}
	for name, role := range map[string]string{"demo": store.RoleAdmin, "op": store.RoleOperator, "view": store.RoleViewer} {
		h, err := auth.Hash("demo")
		if err == nil {
			err = st.SetLocalUser(ctx, "demo", name, h, role)
		}
		if err != nil {
			log.Error("demo account", "err", err)
			return 1
		}
	}
	eng := demo.NewEngine()
	if _, err := control.Apply(ctx, st, eng, "demo", "Demo-Start", false); err != nil {
		log.Error("demo apply", "err", err)
		return 1
	}
	ui, err := web.New(st, eng, log, version)
	if err != nil {
		log.Error("web templates", "err", err)
		return 1
	}
	ui.Demo = true
	defer ui.Outputs.Close()
	if !demoLogo(ctx, ui, logo, log) {
		return 1
	}
	log.Warn("DEMO MODE: invented data, nothing is relayed; accounts demo (admin), op (operator), view (viewer), password demo")
	return serveWeb(ctx, addr, ui, log, "demo")
}

// runWebDemoSetup serves the first-run setup: games from the seed, no account, no network, invented host
// interfaces and the fake engine.
func runWebDemoSetup(ctx context.Context, addr string, st *store.Store, log *slog.Logger, logo string) int {
	seed, err := gamedb.Seed()
	if err == nil {
		_, err = st.SyncSeed(ctx, seed)
	}
	if err != nil {
		log.Error("demo seed", "err", err)
		return 1
	}
	ui, err := web.New(st, demo.NewEngine(), log, version)
	if err != nil {
		log.Error("web templates", "err", err)
		return 1
	}
	defer ui.Outputs.Close()
	ui.HostIfaces = func() ([]netif.HostIface, error) {
		return []netif.HostIface{
			{Name: "eth0", Kind: "device", Up: true, Carrier: true, DefaultRoute: true, Addrs: []string{"192.0.2.10/24"}},
			{Name: "eth1", Kind: "device", Up: true, Carrier: true},
			{Name: "eth2", Kind: "device"},
		}, nil
	}
	if !demoLogo(ctx, ui, logo, log) {
		return 1
	}
	log.Warn("DEMO MODE with an empty database: the first-run setup is open, nothing is relayed")
	return serveWeb(ctx, addr, ui, log, "demo")
}

// demoLogo stores the picture in file as the event logo of a demo (-demo-logo); no file, no logo.
func demoLogo(ctx context.Context, ui *web.Server, file string, log *slog.Logger) bool {
	if file == "" {
		return true
	}
	data, err := os.ReadFile(file)
	if err == nil {
		err = ui.SetEventLogo(ctx, "demo", data)
	}
	if err != nil {
		log.Error("demo logo", "file", file, "err", err)
		return false
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
	go func() {
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

// healthAddr is where the health check finds a server listening on listen: a server on every address (":8080",
// "0.0.0.0:8080") is asked on the loopback.
func healthAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// runHealth exits 0 if the URL answers 200 (used as container health check; the image has no shell/curl).
func runHealth(args []string, _, stderr io.Writer) int {
	fs := newFlags("health", stderr)
	url := fs.String("url", "http://"+healthAddr(envOr("NSRELAY_HTTP_ADDR", "127.0.0.1:8080"))+"/healthz", "health URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(*url)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, "status", resp.Status)
		return 1
	}
	return 0
}
