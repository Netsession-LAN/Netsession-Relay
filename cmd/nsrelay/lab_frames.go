package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"

	"github.com/Netsession-LAN/Netsession-Relay/internal/lab"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// Frames for the lab: lab send and lab expect, and the fake game server of the server probe case.

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
	o := lab.SendOptions{Iface: *iface, SrcPort: uint16(*sport), DstPort: uint16(*dport),
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
	if o.Payload, err = labPayload(*payload, *payloadHex, *size); err != nil {
		fmt.Fprintln(stderr, "-payload-hex:", err)
		return 2
	}
	if err := lab.Send(o); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// labPayload is the payload of lab send: the text, or the hex payload instead, or size zero bytes instead of
// either. Only the hex payload can be invalid.
func labPayload(text, hexText string, size int) ([]byte, error) {
	b := []byte(text)
	if hexText != "" {
		var err error
		if b, err = hex.DecodeString(hexText); err != nil {
			return nil, err
		}
	}
	if size > 0 {
		b = make([]byte, size)
	}
	return b, nil
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
	printFrames(stdout, got)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// printFrames prints one line per received frame: addresses, payload length, TTL and source MAC.
func printFrames(w io.Writer, frames []pkt.Frame) {
	for _, f := range frames {
		fmt.Fprintf(w, "%s:%d -> %s:%d len=%d ttl=%d srcmac=%s\n", f.Src, f.SrcPort, f.Dst, f.DstPort, len(f.Payload), f.TTL, f.SrcMAC)
	}
}
