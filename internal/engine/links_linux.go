//go:build linux

package engine

import (
	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/hostprep"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// The interfaces of a snapshot: created or adopted, shielded, with their MAC addresses and interface indexes.

// setUpLinks checks the interface policy, creates or adopts the interfaces of the snapshot, shields them and
// builds the lookup table for them.
func (e *Engine) setUpLinks(s *compile.Snapshot) (*Table, vlanLinks, error) {
	if err := e.checkIfaces(s); err != nil { // also for the stored snapshot at start and on SIGHUP
		return nil, nil, err
	}
	links, err := ensureLinks(s)
	if err != nil {
		return nil, nil, err
	}
	tb, err := NewTable(s, links.macs())
	if err != nil {
		return nil, nil, err
	}
	// The interfaces exist now: shield them before anything is received or sent on them (no routing, host
	// firewall, management routes). What cannot be done is reported, the relay works without it.
	e.prepareHost(s)
	for _, c := range tb.Clamped {
		e.Log.Warn("snapshot limit outside the engine's bounds, corrected", "limit", c)
	}
	links.setIfindexes(tb)
	return tb, links, nil
}

// vlanLinks maps VLAN ID -> the interface of the VLAN.
type vlanLinks map[int]netif.Link

// ensureLinks creates or adopts the interface of every VLAN of the snapshot.
func ensureLinks(s *compile.Snapshot) (vlanLinks, error) {
	links := vlanLinks{}
	for _, v := range s.VLANs {
		l, err := netif.Ensure(v)
		if err != nil {
			return nil, err
		}
		links[v.VID] = l
	}
	return links, nil
}

func (links vlanLinks) macs() map[int]pkt.MAC {
	out := make(map[int]pkt.MAC, len(links))
	for vid, l := range links {
		out[vid] = l.MAC
	}
	return out
}

// setIfindexes records the interface index of every VLAN of the table.
func (links vlanLinks) setIfindexes(tb *Table) {
	for i := range tb.VLANs {
		tb.VLANs[i].Ifindex = links[tb.VLANs[i].VID].Index
	}
}

func (links vlanLinks) names() map[string]bool {
	out := make(map[string]bool, len(links))
	for _, l := range links {
		out[l.Name] = true
	}
	return out
}

// prepareHost shields the relay interfaces (hostprep) and logs what could not be done.
func (e *Engine) prepareHost(s *compile.Snapshot) {
	issues := hostprep.Apply(s)
	e.hostIssues.Store(&issues)
	for _, is := range issues {
		e.Log.Warn("host preparation incomplete (optional by hand: deploy/host/setup.sh)", "what", is.Code, "detail", is.Detail)
	}
	if len(issues) == 0 {
		e.Log.Info("host prepared: relay interfaces do not route, host firewall and management routes are in place")
	}
}
