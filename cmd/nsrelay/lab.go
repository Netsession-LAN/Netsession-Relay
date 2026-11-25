package main

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engine"
	"github.com/Netsession-LAN/Netsession-Relay/internal/lab"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// The lab subcommand: small tools the network lab (lab/test.sh) runs inside its namespaces to build a snapshot,
// send and expect frames and stand in for game servers, attackers and other hosts.

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
	s, warns, err := lab.SnapshotLimited(*parent, *mode, *relayHost, filter, labLimits(*stormPPS), labGames(*games)...)
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

// labGames splits the -games flag; empty means the defaults of the game database.
func labGames(games string) []string {
	if games == "" {
		return nil
	}
	return strings.Split(games, ",")
}

// labLimits are the engine limits for the -storm-pps flag: the defaults with that storm brake limit, or the zero
// value (the engine's defaults) for 0.
func labLimits(stormPPS int) compile.Limits {
	if stormPPS <= 0 {
		return compile.Limits{}
	}
	limits := compile.DefaultLimits
	limits.StormPPS = stormPPS
	return limits
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
