// Command nsrelay is the Netsession Relay: a UDP broadcast/multicast relay for
// LAN-party game discovery across VLANs, with a web UI.
//
// Subcommands:
//
//	engine    packet data plane (Linux, needs CAP_NET_RAW + CAP_NET_ADMIN)
//	web       web UI, JSON API, monitoring outputs, SQLite
//	admin     local administration: configuration, revisions, accounts, port finder
//	lab       helpers for the network lab: snapshot, send, expect, ...
//	health    check a health URL (container health check)
//	backup    backup and restore of the database
//	snmp-mib  print the SNMP MIB or the Zabbix SNMP template
//	version   print version information
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"syscall"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engine"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/hostprep"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
)

// Set at build time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

type command struct {
	name string
	help string
	run  func(args []string, stdout, stderr io.Writer) int
}

var commands = []command{
	{"engine", "run the packet data plane from a snapshot file", runEngine},
	{"web", "run the web part: UI, JSON API, monitoring outputs", runWeb},
	{"admin", "configuration: init, games, enable/disable, apply, revisions, rollback, status", runAdmin},
	{"lab", "helpers for the network lab: " + labNames, runLab},
	{"health", "check a health URL (container health checks; no curl in the image)", runHealth},
	{"backup", "backup [-dir] | backup restore <file>", runBackup},
	{"cleanup", "remove what the engine created on the host (VLAN interfaces, firewall table, routing rules); stop the engine first", runCleanup},
	{"snmp-mib", "print NETSESSION-RELAY-MIB; -zabbix: the Zabbix SNMP template (deploy/zabbix/template_nsrelay_snmp.yaml)", runSNMPMIB},
}

// runCleanup removes what the engine left on the host, for taking the relay out of service: the VLAN interfaces
// it created (alias "nsrelay"), its nftables table and the policy routing for the management side. It needs the
// same rights as the engine (CAP_NET_ADMIN in the network namespace of the host) and refuses while an engine
// answers on the socket: that one would put everything back.
func runCleanup(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("cleanup", stderr)
	socket := fs.String("socket", envOr("NSRELAY_SOCKET", engineapi.DefaultSocket), "engine API socket, to check that no engine runs")
	force := fs.Bool("force", false, "clean up although an engine answers on the socket")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: nsrelay cleanup [-socket path] [-force]")
		return 2
	}
	if !*force && engineAnswers(*socket) {
		fmt.Fprintln(stderr, "cleanup: an engine is running and would put everything back; stop it first (docker compose stop), then run cleanup again")
		return 1
	}
	return cleanupHost(stdout, stderr)
}

// cleanupHost removes the interfaces, then the firewall table and the routing, and prints what went.
func cleanupHost(stdout, stderr io.Writer) int {
	code := 0
	links, err := netif.Cleanup(nil)
	if err != nil {
		fmt.Fprintln(stderr, "cleanup: interfaces:", err)
		code = 1
	}
	printRemoved(stdout, "removed: interface", links)
	rest, err := hostprep.Remove()
	if err != nil {
		fmt.Fprintln(stderr, "cleanup: firewall and routing:", err)
		code = 1
	}
	printRemoved(stdout, "removed:", rest)
	if code == 0 && len(links)+len(rest) == 0 {
		fmt.Fprintln(stdout, "nothing to remove: no interface, firewall table or routing rule of the relay on this host")
	}
	return code
}

// printRemoved prints one line per removed item, after prefix.
func printRemoved(w io.Writer, prefix string, items []string) {
	for _, it := range items {
		fmt.Fprintln(w, prefix, it)
	}
}

// engineAnswers reports whether an engine answers on the API socket within two seconds; no socket, no engine.
func engineAnswers(socket string) bool {
	if socket == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := engineapi.NewClient(socket).Status(ctx)
	return err == nil
}

func runSNMPMIB(args []string, stdout, _ io.Writer) int {
	if len(args) > 0 && args[0] == "-zabbix" {
		fmt.Fprint(stdout, snmp.ZabbixTemplate())
		return 0
	}
	fmt.Fprint(stdout, snmp.MIB())
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-version", "-v":
		fmt.Fprintf(stdout, "nsrelay %s (commit %s, %s, %s/%s)\n",
			version, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "--help", "-help", "-h":
		usage(stdout)
		return 0
	}
	if i := slices.IndexFunc(commands, func(c command) bool { return c.name == args[0] }); i >= 0 {
		return commands[i].run(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "nsrelay: unknown command %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: nsrelay <command> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.help)
	}
	fmt.Fprintf(w, "  %-8s %s\n", "version", "print version information")
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("nsrelay "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func runEngine(args []string, _, stderr io.Writer) int {
	fs := newFlags("engine", stderr)
	path := fs.String("snapshot", envOr("NSRELAY_SNAPSHOT", "/var/lib/nsrelay/active.json"), "snapshot file (written by the web part or `nsrelay lab snapshot`)")
	socket := fs.String("socket", envOr("NSRELAY_SOCKET", engineapi.DefaultSocket), "engine API Unix socket (empty = off)")
	gid := fs.Int("socket-gid", envInt("NSRELAY_SOCKET_GID", -1), "group ID for the socket (web container user group)")
	debug := fs.Bool("debug", false, "debug logging")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	log.Info("nsrelay engine starting", "version", version, "commit", commit, "snapshot", *path)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	e := &engine.Engine{Path: *path, Socket: *socket, SocketGID: *gid, Version: version, Log: log,
		BeaconKey: os.Getenv("NSRELAY_BEACON_KEY"), AllowedIfaces: engine.ParseIfaceList(os.Getenv("NSRELAY_ALLOWED_IFACES"))}
	if err := e.Run(ctx); err != nil {
		log.Error("engine stopped", "err", err)
		return 1
	}
	return 0
}
