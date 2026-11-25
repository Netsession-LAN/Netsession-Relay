package engine

import (
	"bytes"
	"strconv"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// BeaconEtherType is the local-experimental EtherType used for relay beacons (IEEE 802 "local experimental 1").
const BeaconEtherType = 0x88B5

var beaconMagic = [4]byte{'N', 'S', 'R', 'L'}

// Beacon is the payload of a relay beacon frame.
//
// Version 2 adds the UDP ports the engine relays: two relays only conflict when their ports overlap, so a second
// instance for other ports (an mDNS-only relay next to the game relay) may run on the same VLANs. A version 1
// beacon, or one with more ranges than fit into a frame, counts as "all ports" (the safe side).
type Beacon struct {
	Instance [16]byte
	Started  int64 // unix nanos
	Active   bool
	Hash     [8]byte     // first bytes of the snapshot hash
	AllPorts bool        // ports unknown (version 1) or too many ranges: overlaps everything
	Ports    []PortRange // relayed UDP destination ports, sorted, merged
}

// BeaconFrame builds this engine's beacon frame for a VLAN interface, signed when a key is set.
func (g *Guard) BeaconFrame(mac pkt.MAC, s *compile.Snapshot, now time.Time) []byte {
	f := BuildBeacon(mac, g.MyBeacon(s))
	if g.beaconKey != nil {
		f = SignBeacon(f, g.beaconKey, now)
	}
	return f
}

// SeeFrame handles a received beacon frame from VLAN vid. With a key, only beacons signed with the same key
// take part in the dual-active guard; any other beacon for ports this engine relays is ignored and raises the
// alarm unknown_beacon (a relay without the same key, or a forged frame meant to silence this relay). Without a
// key every beacon counts, as before signatures existed. It returns true when this engine enters standby.
func (g *Guard) SeeFrame(f []byte, vid int, now time.Time) bool {
	b, ok := ParseBeacon(f)
	if !ok || b.Instance == g.Instance || !g.trusted(f, &b, vid, now) {
		return false
	}
	return g.SeeBeacon(b, now)
}

// trusted reports whether the beacon frame f (decoded: b) may take part in the dual-active guard: always without
// a key, with a key only when it is signed with it. An untrusted beacon for this engine's ports raises the alarm.
func (g *Guard) trusted(f []byte, b *Beacon, vid int, now time.Time) bool {
	if g.beaconKey == nil {
		return true
	}
	why := BeaconAuth(f, g.beaconKey, now)
	if why == "" {
		return true
	}
	if mp := g.myPorts(); portsOverlap(mp.all, mp.ranges, b.AllPorts, b.Ports) {
		g.Alarms.Raise(AlarmUnknownBeacon, strconv.Itoa(vid), now,
			"VLAN %d: relay signal from %s without a valid signature (%s), ignored: another relay without the same key, or a forged frame",
			vid, pkt.MAC(f[ethOffSrc:ethOffType]), why)
	}
	return false
}

// MyBeacon returns this engine's beacon for the given snapshot.
func (g *Guard) MyBeacon(s *compile.Snapshot) Beacon {
	mp := g.myPorts()
	b := Beacon{Instance: g.Instance, Started: g.Started.UnixNano(),
		Active: s.Mode == compile.ModeActive && !g.standby.Load(), AllPorts: mp.all, Ports: mp.ranges}
	copy(b.Hash[:], s.Hash)
	return b
}

// SeeBeacon processes a received beacon. If an active engine that started earlier and relays at least one of
// this engine's ports is seen, this engine goes to standby (returns true when that state is entered).
// Engines with disjoint ports (game relay and mDNS relay) ignore each other.
func (g *Guard) SeeBeacon(b Beacon, now time.Time) bool {
	if b.Instance == g.Instance || !b.Active {
		return false
	}
	if !g.yieldsTo(&b) {
		return false
	}
	g.lastPeerActive.Store(now.UnixNano())
	if g.standby.CompareAndSwap(false, true) {
		g.Alarms.Raise(AlarmDualActive, "peer", now, "another active relay (started %s) relays the same ports, going to standby",
			time.Unix(0, b.Started).UTC().Format(time.RFC3339))
		return true
	}
	return false
}

// yieldsTo reports whether this engine gives way to the active engine of beacon b: b relays at least one of this
// engine's ports and started earlier (ties broken by instance ID). Otherwise the other one yields.
func (g *Guard) yieldsTo(b *Beacon) bool {
	mp := g.myPorts()
	if !portsOverlap(mp.all, mp.ranges, b.AllPorts, b.Ports) {
		return false
	}
	mine := g.Started.UnixNano()
	return b.Started < mine || (b.Started == mine && bytes.Compare(b.Instance[:], g.Instance[:]) < 0)
}

// PeerTick leaves standby when the earlier engine has been silent for the peer timeout (10 s).
// Returns true when standby is left.
func (g *Guard) PeerTick(now time.Time) bool {
	if !g.standby.Load() {
		return false
	}
	if now.UnixNano()-g.lastPeerActive.Load() > g.peerTimeout.Nanoseconds() {
		g.standby.Store(false)
		return true
	}
	return false
}
