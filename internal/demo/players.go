package demo

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// Players invents a few searching and hosting PCs on the first rules.
func (d *Engine) Players(context.Context) ([]engineapi.Player, error) {
	d.mu.Lock()
	s := d.snap
	d.mu.Unlock()
	if s == nil || len(s.Rules) == 0 || len(s.VLANs) < 3 {
		return nil, nil
	}
	now := time.Now()
	out := searchingPlayers(s, now)
	out = append(out, serverScanner(s, now)...)
	return append(out, probedServers(s, now)...), nil
}

// hostIn returns the address with the given last byte in the network of the VLAN.
func hostIn(v compile.VLAN, last byte) netip.Addr {
	ip := v.CIDR.Addr().As4()
	ip[3] = last
	return netip.AddrFrom4(ip)
}

// searchingPlayers puts one searching PC on each of the first six rules, spread over the player networks.
func searchingPlayers(s *compile.Snapshot, now time.Time) []engineapi.Player {
	var out []engineapi.Player
	for i := 0; i < 6 && i < len(s.Rules); i++ {
		v := s.VLANs[1+i%(len(s.VLANs)-1)]
		out = append(out, engineapi.Player{Rule: s.Rules[i].ID, Src: hostIn(v, byte(100+i*7)), VID: v.VID,
			First: now.Add(-time.Duration(3+i*4) * time.Minute), Last: now.Add(-time.Duration(i) * time.Second), Count: uint64(40 + i*13)})
	}
	return out
}

// serverScanner is a game server scanner in the last network (like a Discord notifier): .99 queries the rules
// seven to twelve.
func serverScanner(s *compile.Snapshot, now time.Time) []engineapi.Player {
	var out []engineapi.Player
	last := s.VLANs[len(s.VLANs)-1]
	src := hostIn(last, 99)
	for i := 6; i < 12 && i < len(s.Rules); i++ {
		out = append(out, engineapi.Player{Rule: s.Rules[i].ID, Src: src, VID: last.VID,
			First: now.Add(-9 * time.Minute), Last: now.Add(-time.Duration(40+i) * time.Second), Count: 3})
	}
	return out
}

// probedServers are the servers found by the server probe in the server network: two Source servers on the
// first probed 27015 rule, one generic answer on the first probed 27960 rule (at most two rules in all).
func probedServers(s *compile.Snapshot, now time.Time) []engineapi.Player {
	gs := s.VLANs[0]
	var out []engineapi.Player
	n := 0
	for _, r := range s.Rules {
		if !r.Probe {
			continue
		}
		found := probedServersOf(r, gs, now)
		if found == nil {
			continue
		}
		out = append(out, found...)
		if n++; n == 2 {
			break
		}
	}
	return out
}

// probedServersOf invents the probe answers for one rule by its port, nil for ports without a demo server.
func probedServersOf(r compile.Rule, gs compile.VLAN, now time.Time) []engineapi.Player {
	switch {
	case strings.HasPrefix(r.Ports, "27015"):
		var out []engineapi.Player
		for i, srv := range []struct{ name, mp string }{{"Netsession CS2 #1", "de_dust2"}, {"Netsession CS2 #2 – Wingman", "de_inferno"}} {
			out = append(out, engineapi.Player{Rule: r.ID, Src: hostIn(gs, byte(20+i)), VID: gs.VID, First: now.Add(-50 * time.Minute),
				Last: now.Add(-time.Duration(12+i*20) * time.Second), Count: 48, Server: true, Port: 27015, Name: srv.name,
				Map: srv.mp, Game: "Counter-Strike 2", Num: 7 - i*5, Max: 10 - i*6})
		}
		return out
	case strings.HasPrefix(r.Ports, "27960"):
		return []engineapi.Player{{Rule: r.ID, Src: hostIn(gs, 30), VID: gs.VID, First: now.Add(-20 * time.Minute),
			Last: now.Add(-35 * time.Second), Count: 20, Server: true, Port: 27960}}
	}
	return nil
}
