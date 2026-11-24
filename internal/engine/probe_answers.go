package engine

import (
	"bytes"
	"encoding/binary"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// Reply handles a unicast frame to ProbePort on VLAN v. It records the sender as a server and returns a follow-up
// frame (A2S request with the server's challenge) to send on the same VLAN, or nil.
func (p *Prober) Reply(v *VLANRuntime, frame []byte, now time.Time) []byte {
	f, err := pkt.Parse(frame)
	if err != nil || !isProbeAnswer(&f, v) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.roundAt.IsZero() || now.Sub(p.roundAt) > answerWindow {
		return nil // no round running: unsolicited
	}
	rule := p.byPort[f.SrcPort]
	if rule == "" {
		return nil // not a port we probed
	}
	s := p.server(rule, &f, v.VID, now)
	if s == nil {
		return nil
	}
	s.Last, s.MAC = now, f.SrcMAC
	s.Count++
	pl := f.Payload
	switch {
	case len(pl) >= 9 && bytes.Equal(pl[:5], []byte{0xff, 0xff, 0xff, 0xff, 'A'}):
		return p.askInfo(s, v, &f)
	case len(pl) >= 6 && bytes.Equal(pl[:5], []byte{0xff, 0xff, 0xff, 0xff, 'I'}):
		if a, ok := parseA2SInfo(pl[5:]); ok {
			s.Name, s.Map, s.Game, s.Num, s.Max = a.name, a.mp, a.game, a.num, a.mx
		}
	case bytes.HasPrefix(pl, []byte("\xff\xff\xff\xffinfoResponse")):
		if a, ok := parseQ3Info(pl[len("\xff\xff\xff\xffinfoResponse"):]); ok {
			s.Name, s.Map, s.Num, s.Max = a.name, a.mp, a.num, a.mx
		}
	}
	return nil
}

// isProbeAnswer reports whether f is addressed to the probe of VLAN v and comes from inside the VLAN's network.
func isProbeAnswer(f *pkt.Frame, v *VLANRuntime) bool {
	toProbe := f.DstPort == ProbePort && f.Dst == v.RelayIP && f.DstMAC == v.MAC
	return toProbe && v.CIDR.Contains(f.Src)
}

// server returns the known server that sent f, or records a new one (nil when the table is full). p.mu is held.
func (p *Prober) server(rule string, f *pkt.Frame, vid int, now time.Time) *Server {
	k := serverKey{f.Src, f.SrcPort, vid}
	if p.servers == nil {
		p.servers = map[serverKey]*Server{}
	}
	if s := p.servers[k]; s != nil {
		return s
	}
	if len(p.servers) >= maxServers {
		return nil
	}
	s := &Server{Rule: rule, Src: f.Src, Port: f.SrcPort, VID: vid, First: now}
	p.servers[k] = s
	return s
}

// askInfo answers an A2S challenge (f) with the A2S_INFO request that carries it, once per round and server.
// p.mu is held.
func (p *Prober) askInfo(s *Server, v *VLANRuntime, f *pkt.Frame) []byte {
	if s.asked == p.round {
		return nil // already asked in this round
	}
	s.asked = p.round
	req := append(append([]byte(nil), a2sInfo...), f.Payload[5:9]...)
	out := pkt.Frame{DstMAC: f.SrcMAC, SrcMAC: v.MAC, TTL: 64, Src: v.RelayIP, Dst: f.Src, SrcPort: ProbePort,
		DstPort: f.SrcPort, UDPChecksum: 1, Payload: req}
	b, err := pkt.Build(nil, &out)
	if err != nil {
		return nil
	}
	return b
}

// GC removes servers that did not answer for probeTTL.
func (p *Prober) GC(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, s := range p.servers {
		if now.Sub(s.Last) > probeTTL {
			delete(p.servers, k)
		}
	}
}

// Servers returns the servers that answered recently, most recent first.
func (p *Prober) Servers() []Server {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Server, 0, len(p.servers))
	for _, s := range p.servers {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// a2sInfo is the decoded part of an A2S_INFO answer.
type a2sAnswer struct {
	name, mp, game string
	num, mx        int
}

// parseA2SInfo reads name, map, game and player counts of an A2S_INFO answer (after the 'I' header byte).
func parseA2SInfo(b []byte) (a a2sAnswer, ok bool) {
	var name, mp, game string
	if len(b) < 1 {
		return a, false
	}
	b = b[1:] // protocol
	str := func() (string, bool) {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			return "", false
		}
		s := string(b[:i])
		b = b[i+1:]
		return s, true
	}
	if name, ok = str(); !ok {
		return a, false
	}
	if mp, ok = str(); !ok {
		return a, false
	}
	if _, ok = str(); !ok { // folder
		return a, false
	}
	if game, ok = str(); !ok {
		return a, false
	}
	if len(b) < 4 {
		return a, false
	}
	_ = binary.LittleEndian.Uint16(b[:2]) // app id
	return a2sAnswer{name: clean(name), mp: clean(mp), game: clean(game), num: int(b[2]), mx: int(b[3])}, true
}

// parseQ3Info reads an id Tech 3 info string ("\hostname\x\mapname\y\clients\3\sv_maxclients\16").
func parseQ3Info(b []byte) (a a2sAnswer, ok bool) {
	parts := strings.Split(strings.TrimSpace(string(b)), "\\")
	if len(parts) < 3 {
		return a, false
	}
	kv := map[string]string{}
	for i := 1; i+1 < len(parts); i += 2 {
		kv[strings.ToLower(parts[i])] = parts[i+1]
	}
	num, _ := strconv.Atoi(kv["clients"])
	mx, _ := strconv.Atoi(kv["sv_maxclients"])
	return a2sAnswer{name: clean(stripQ3Colors(kv["hostname"])), mp: clean(kv["mapname"]), num: num, mx: mx}, true
}

// stripQ3Colors removes the ^N colour codes of id Tech 3 names.
func stripQ3Colors(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '^' && i+1 < len(s) {
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// clean keeps printable text and limits the length (names come from the network).
func clean(s string) string {
	r := []rune(s)
	out := make([]rune, 0, len(r))
	for _, c := range r {
		if c >= 0x20 && c != 0x7f && c != 0xfffd {
			out = append(out, c)
		}
		if len(out) >= 64 {
			break
		}
	}
	return string(out)
}
