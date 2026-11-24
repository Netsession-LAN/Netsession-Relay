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
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/engine"
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"github.com/Netsession2024/Netsession-Relay/internal/lab"
	"github.com/Netsession2024/Netsession-Relay/internal/pkt"
	"github.com/Netsession2024/Netsession-Relay/internal/snmp"
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
	{"snmp-mib", "print NETSESSION-RELAY-MIB; -zabbix: the Zabbix SNMP template (deploy/zabbix/template_nsrelay_snmp.yaml)", runSNMPMIB},
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
	for _, c := range commands {
		if c.name == args[0] {
			return c.run(args[1:], stdout, stderr)
		}
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

const labNames = "snapshot | send | expect | a2s-server | beacon | tcp-listen | tcp-dial"

var labCommands = map[string]func(args []string, stdout, stderr io.Writer) int{
	"snapshot":   labSnapshot,
	"send":       labSend,
	"expect":     labExpect,
	"a2s-server": labA2S,
	"beacon":     labBeacon,
	"tcp-listen": labTCPListen,
	"tcp-dial":   labTCPDial,
}

func runLab(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: nsrelay lab "+labNames+" [options]")
		return 2
	}
	run, ok := labCommands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "nsrelay lab: unknown subcommand %q\n", args[0])
		return 2
	}
	return run(args[1:], stdout, stderr)
}

// labTCPListen accepts TCP connections for a while (a stand-in for SSH or any other service of a host) and
// prints how many arrived.
func labTCPListen(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("lab tcp-listen", stderr)
	at := fs.String("addr", ":2222", "listen address")
	timeout := fs.Int("timeout", 5000, "milliseconds to listen")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ln, err := net.Listen("tcp4", *at)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	time.AfterFunc(time.Duration(*timeout)*time.Millisecond, func() { ln.Close() })
	n := 0
	for {
		c, err := ln.Accept()
		if err != nil {
			break
		}
		n++
		c.Close()
	}
	fmt.Fprintf(stdout, "%d connections\n", n)
	return 0
}

// labTCPDial exits 0 when a TCP connection to the address can be opened within the timeout.
func labTCPDial(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("lab tcp-dial", stderr)
	to := fs.String("addr", "", "address to connect to (ip:port)")
	timeout := fs.Int("timeout", 1500, "milliseconds to wait")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := net.DialTimeout("tcp4", *to, time.Duration(*timeout)*time.Millisecond)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	c.Close()
	fmt.Fprintln(stdout, "connected to", *to)
	return 0
}

func labSnapshot(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("lab snapshot", stderr)
	out := fs.String("out", "", "write snapshot to this file (default: stdout summary only)")
	parent := fs.String("parent", "trunk0", "trunk interface")
	mode := fs.String("mode", compile.ModeActive, "engine mode: active|observe|paused")
	relayHost := fs.Int("relay-host", 55, "host part of the relay IP in every lab VLAN")
	games := fs.String("games", "", "comma-separated game ids to switch on (default: the defaults of the game database)")
	sources := fs.String("sources", "", "source filter for every switched-on game: IPv4 addresses/networks, comma-separated (default: none)")
	stormPPS := fs.Int("storm-pps", 0, "storm brake limit in frames/s sent (default: the engine default)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	filter, err := compile.ParseSources(*sources)
	if err != nil {
		fmt.Fprintln(stderr, "-sources:", err)
		return 2
	}
	var ids []string
	if *games != "" {
		ids = strings.Split(*games, ",")
	}
	var limits compile.Limits
	if *stormPPS > 0 {
		limits = compile.DefaultLimits
		limits.StormPPS = *stormPPS
	}
	s, warns, err := lab.SnapshotLimited(*parent, *mode, *relayHost, filter, limits, ids...)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *out != "" {
		if err := compile.WriteSnapshotFile(*out, s); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "lab snapshot %s: %d VLANs, %d rules, %d warnings\n", s.Hash[:12], len(s.VLANs), len(s.Rules), len(warns))
	return 0
}

// labBeacon sends relay beacons the way an attacker in a game VLAN would: unsigned, active, started long ago,
// all ports - the frame that used to put every relay into standby.
func labBeacon(args []string, _, stderr io.Writer) int {
	fs := newFlags("lab beacon", stderr)
	iface := fs.String("iface", "eth0", "interface")
	count := fs.Int("count", 5, "number of frames")
	interval := fs.Int("interval", 500, "milliseconds between frames")
	key := fs.String("key", "", "sign the beacons with this key (default: unsigned)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	b := engine.Beacon{Started: 1, Active: true, AllPorts: true}
	copy(b.Instance[:], "forged-by-the-lab")
	err := lab.SendRaw(*iface, *count, *interval, func(mac pkt.MAC) []byte {
		f := engine.BuildBeacon(mac, b)
		if *key != "" {
			f = engine.SignBeacon(f, []byte(*key), time.Now())
		}
		return f
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func parseOptAddr(s string) (netip.Addr, error) {
	if s == "" {
		return netip.Addr{}, nil
	}
	return netip.ParseAddr(s)
}

func labSend(args []string, _, stderr io.Writer) int {
	fs := newFlags("lab send", stderr)
	iface := fs.String("iface", "eth0", "interface")
	src := fs.String("src", "", "source IP (may be spoofed)")
	dst := fs.String("dst", "", "destination IP (255.255.255.255, subnet broadcast or multicast group)")
	sport := fs.Uint("sport", 50000, "source port")
	dport := fs.Uint("dport", 0, "destination port")
	payload := fs.String("payload", "", "payload text")
	payloadHex := fs.String("payload-hex", "", "payload as hex (overrides -payload)")
	size := fs.Int("size", 0, "payload of N zero bytes (overrides -payload)")
	count := fs.Int("count", 1, "number of frames")
	interval := fs.Int("interval", 0, "milliseconds between frames")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	o := lab.SendOptions{Iface: *iface, SrcPort: uint16(*sport), DstPort: uint16(*dport), Payload: []byte(*payload),
		Count: *count, Interval: *interval}
	var err error
	if o.Src, err = netip.ParseAddr(*src); err != nil {
		fmt.Fprintln(stderr, "-src:", err)
		return 2
	}
	if o.Dst, err = netip.ParseAddr(*dst); err != nil {
		fmt.Fprintln(stderr, "-dst:", err)
		return 2
	}
	if *payloadHex != "" {
		if o.Payload, err = hex.DecodeString(*payloadHex); err != nil {
			fmt.Fprintln(stderr, "-payload-hex:", err)
			return 2
		}
	}
	if *size > 0 {
		o.Payload = make([]byte, *size)
	}
	if err := lab.Send(o); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// labA2S runs a fake Steam/Source server for the server probe case; fails if no info answer was sent.
func labA2S(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("lab a2s-server", stderr)
	iface := fs.String("iface", "eth0", "interface")
	ip := fs.String("ip", "", "the server's IP address")
	port := fs.Uint("port", 27015, "server port")
	name := fs.String("name", "Lab CS2", "server name")
	timeout := fs.Int("timeout", 3000, "milliseconds to keep answering")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	a, err := netip.ParseAddr(*ip)
	if err != nil {
		fmt.Fprintln(stderr, "-ip:", err)
		return 2
	}
	n, err := lab.A2SServer(lab.A2SOptions{Iface: *iface, IP: a, Port: uint16(*port), Name: *name, Timeout: *timeout})
	fmt.Fprintf(stdout, "%d info answer(s)\n", n)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if n == 0 {
		return 1
	}
	return 0
}

func labExpect(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("lab expect", stderr)
	iface := fs.String("iface", "eth0", "interface")
	dport := fs.Uint("dport", 0, "destination port")
	src := fs.String("src", "", "only frames from this source IP")
	dst := fs.String("dst", "", "only frames to this destination IP")
	count := fs.Int("count", 1, "frames needed")
	maxN := fs.Int("max", 0, "collect until timeout; succeed if count..max frames arrived")
	none := fs.Bool("none", false, "succeed only if nothing arrives")
	timeout := fs.Int("timeout", 1500, "timeout in milliseconds")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	o := lab.ExpectOptions{Iface: *iface, DstPort: uint16(*dport), Count: *count, Max: *maxN, None: *none, Timeout: *timeout}
	var err error
	if o.Src, err = parseOptAddr(*src); err != nil {
		fmt.Fprintln(stderr, "-src:", err)
		return 2
	}
	if o.Dst, err = parseOptAddr(*dst); err != nil {
		fmt.Fprintln(stderr, "-dst:", err)
		return 2
	}
	got, err := lab.Expect(o)
	for _, f := range got {
		fmt.Fprintf(stdout, "%s:%d -> %s:%d len=%d ttl=%d srcmac=%s\n", f.Src, f.SrcPort, f.Dst, f.DstPort, len(f.Payload), f.TTL, f.SrcMAC)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
