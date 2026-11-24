package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/demo"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/web"
)

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
	// a preset starts in observe; the demo shows a relay that is switched on
	if err := st.SetMode(ctx, "demo", compile.ModeActive); err != nil {
		log.Error("demo mode", "err", err)
		return 1
	}
	if err := demoAccounts(ctx, st); err != nil {
		log.Error("demo account", "err", err)
		return 1
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

// demoAccounts creates the demo accounts demo (admin), op (operator) and view (viewer), all with password demo.
func demoAccounts(ctx context.Context, st *store.Store) error {
	for name, role := range map[string]string{"demo": store.RoleAdmin, "op": store.RoleOperator, "view": store.RoleViewer} {
		h, err := auth.Hash("demo")
		if err == nil {
			err = st.SetLocalUser(ctx, "demo", name, h, role)
		}
		if err != nil {
			return err
		}
	}
	return nil
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
