package engine

import (
	"bytes"
	"sort"
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
	s := p.answering(&f, v.VID, now)
	if s == nil {
		return nil
	}
	s.Last, s.MAC = now, f.SrcMAC
	s.Count++
	return p.readAnswer(s, v, &f)
}

// Answer headers of the probed protocols.
var (
	a2sChallengeHeader = []byte{0xff, 0xff, 0xff, 0xff, 'A'}
	a2sInfoHeader      = []byte{0xff, 0xff, 0xff, 0xff, 'I'}
	q3InfoHeader       = []byte("\xff\xff\xff\xffinfoResponse")
)

// answering returns the server that sent f in answer to the current probe round (recorded on its first answer),
// or nil: no round running (unsolicited), a port that was not probed, or the server table is full. p.mu is held.
func (p *Prober) answering(f *pkt.Frame, vid int, now time.Time) *Server {
	if p.roundAt.IsZero() || now.Sub(p.roundAt) > answerWindow {
		return nil // no round running: unsolicited
	}
	rule := p.byPort[f.SrcPort]
	if rule == "" {
		return nil // not a port we probed
	}
	return p.server(rule, f, vid, now)
}

// readAnswer takes name, map and players from an info answer of server s, or returns the A2S_INFO request for an
// A2S challenge. p.mu is held.
func (p *Prober) readAnswer(s *Server, v *VLANRuntime, f *pkt.Frame) []byte {
	pl := f.Payload
	switch {
	case len(pl) >= 9 && bytes.HasPrefix(pl, a2sChallengeHeader):
		return p.askInfo(s, v, f)
	case bytes.HasPrefix(pl, a2sInfoHeader):
		s.readA2SInfo(pl[len(a2sInfoHeader):])
	case bytes.HasPrefix(pl, q3InfoHeader):
		s.readQ3Info(pl[len(q3InfoHeader):])
	}
	return nil
}

// readA2SInfo takes name, map, game and players from an A2S_INFO answer (after the header).
func (s *Server) readA2SInfo(b []byte) {
	if a, ok := parseA2SInfo(b); ok {
		s.Name, s.Map, s.Game, s.Num, s.Max = a.name, a.mp, a.game, a.num, a.mx
	}
}

// readQ3Info takes name, map and players from an id Tech 3 infoResponse (after the header).
func (s *Server) readQ3Info(b []byte) {
	if a, ok := parseQ3Info(b); ok {
		s.Name, s.Map, s.Num, s.Max = a.name, a.mp, a.num, a.mx
	}
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
