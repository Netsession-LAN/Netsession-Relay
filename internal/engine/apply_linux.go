//go:build linux

package engine

import (
	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/hostprep"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// apply sets up interfaces and sockets for the snapshot and swaps the lookup table. VLANs that stay are not
// interrupted; removed VLANs lose their receiver and engine-created interface.
func (e *Engine) apply(s *compile.Snapshot) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.checkIfaces(s); err != nil { // also for the stored snapshot at start and on SIGHUP
		return err
	}
	links, err := ensureLinks(s)
	if err != nil {
		return err
	}
	tb, err := NewTable(s, links.macs())
	if err != nil {
		return err
	}
	// The interfaces exist now: shield them before anything is received or sent on them (no routing, host
	// firewall, management routes). What cannot be done is reported, the relay works without it.
	e.prepareHost(s)
	for _, c := range tb.Clamped {
		e.Log.Warn("snapshot limit outside the engine's bounds, corrected", "limit", c)
	}
	for i := range tb.VLANs {
		tb.VLANs[i].Ifindex = links[tb.VLANs[i].VID].Index
	}
	starting, replaced, err := e.openReceivers(s, links)
	if err != nil {
		return err
	}
	e.guard.SetPorts(s)
	old := e.st.Swap(e.newState(tb, links))
	e.guard.Probe.Forget(probedRules(s))
	for _, r := range starting {
		e.wg.Add(1)
		go e.receive(r)
	}
	if err := e.mcast.sync(tb); err != nil {
		e.Log.Warn("multicast membership", "err", err)
	}
	deleted := e.retire(replaced, links.names())
	oldHash := ""
	if old != nil {
		oldHash = old.table.Snapshot.Hash
	}
	e.Log.Info("snapshot applied", "hash", short(s.Hash), "previous", short(oldHash), "mode", s.Mode,
		"vlans", len(s.VLANs), "rules", len(s.Rules), "removed_links", deleted)
	return nil
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

// openReceivers opens sockets for new interfaces; their receivers (starting) start only after the new state is in
// place. replaced holds receivers whose interface was recreated under the same name (new ifindex): their socket is
// bound to the interface that is gone, so they are stopped once the new state is in place. If a socket cannot be
// opened, the receivers stay as they were.
func (e *Engine) openReceivers(s *compile.Snapshot, links vlanLinks) (starting, replaced []*receiver, err error) {
	for _, v := range s.VLANs {
		l := links[v.VID]
		old, exists := e.receivers[l.Name]
		if exists && old.sock.ifindex == l.Index {
			old.vid.Store(int32(v.VID))
			continue
		}
		sock, err := openPacketSock(l.Name, l.Index, readTimeout)
		if err != nil {
			e.undoReceivers(starting, replaced)
			return nil, nil, err
		}
		if exists {
			replaced = append(replaced, old)
		}
		r := newReceiver(sock, v.VID)
		e.receivers[l.Name] = r
		starting = append(starting, r)
	}
	return starting, replaced, nil
}

// undoReceivers reverts openReceivers: the new receivers are not started yet and are just closed, the replaced
// ones are still running and part of the current state, so they are kept.
func (e *Engine) undoReceivers(starting, replaced []*receiver) {
	for _, r := range starting {
		r.sock.close()
		delete(e.receivers, r.sock.name)
	}
	for _, r := range replaced {
		e.receivers[r.sock.name] = r
	}
}

// newState maps the table's VLANs to the sockets of their receivers.
func (e *Engine) newState(tb *Table, links vlanLinks) *state {
	st := &state{table: tb, out: make([]*packetSock, len(tb.VLANs)), index: map[*packetSock]int{}}
	for i, v := range tb.VLANs {
		r := e.receivers[links[v.VID].Name]
		st.out[i] = r.sock
		st.index[r.sock] = i
	}
	return st
}

// probedRules lists the rules the server probe keeps queries for.
func probedRules(s *compile.Snapshot) map[string]bool {
	keep := map[string]bool{}
	for _, r := range s.Rules {
		if r.Probe && s.Probe {
			keep[r.ID] = true
		}
	}
	return keep
}

// retire stops the receivers of removed and of recreated interfaces (after the swap, so nothing sends to them any
// more) and deletes the engine-created interfaces that are no longer used. It returns the deleted interfaces.
func (e *Engine) retire(replaced []*receiver, keep map[string]bool) []string {
	for _, r := range replaced {
		r.halt()
	}
	for name, r := range e.receivers {
		if !keep[name] {
			r.halt()
			delete(e.receivers, name)
		}
	}
	deleted, err := netif.Cleanup(keep)
	if err != nil {
		e.Log.Warn("interface cleanup", "err", err)
	}
	return deleted
}
