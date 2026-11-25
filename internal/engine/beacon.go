package engine

import (
	"bytes"
	"encoding/binary"
	"sort"
	"strconv"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
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

// PortRange is an inclusive range of UDP ports.
type PortRange struct{ Lo, Hi uint16 }

type beaconPorts struct {
	all    bool
	ranges []PortRange
}

// Beacon payload layout (after the Ethernet header). Version 1 ends after the hash; version 2 appends the flags,
// the number of port ranges and the ranges, optionally followed by the signature trailer (send time + tag).
const (
	beaconOffMagic    = 0  // 4 bytes "NSRL"
	beaconOffVersion  = 4  // 1 byte
	beaconOffInstance = 5  // 16 bytes
	beaconOffStarted  = 21 // 8 bytes, unix nanos
	beaconOffActive   = 29 // 1 byte, 1 = active
	beaconOffHash     = 30 // 8 bytes
	beaconOffFlags    = 38 // 1 byte (version 2)
	beaconOffCount    = 39 // 2 bytes, number of port ranges (version 2)
	beaconOffRanges   = 41 // beaconRangeBytes per range: lo, hi (version 2)

	beaconLen        = beaconOffFlags // version 1 payload
	beaconMaxRanges  = 300            // 300*4 bytes after the fixed part: fits a 1500-byte frame
	beaconFlagAll    = 1
	beaconFlagSigned = 2 // a signature trailer follows the port ranges (SignBeacon)
	beaconVersion    = 2
	beaconRangeBytes = 4
	beaconTimeLen    = 8                            // send time of a signed beacon, unix nanos
	beaconTagLen     = 16                           // truncated HMAC-SHA256
	beaconSigLen     = beaconTimeLen + beaconTagLen // signature trailer
	// BeaconMaxSkew is how far the send time of a signed beacon may be from this engine's clock. A recorded
	// beacon is worthless after it; two relays that guard each other need clocks closer than this (NTP).
	BeaconMaxSkew = 30 * time.Second
)

// Ethernet header offsets used for beacon frames.
const (
	ethOffSrc  = 6
	ethOffType = 12
)

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

// RelayedPorts returns the merged UDP destination port ranges of all rules of a snapshot.
func RelayedPorts(s *compile.Snapshot) (ranges []PortRange, all bool) {
	var ports []uint16
	for _, r := range s.Rules {
		ps, err := portspec.Parse(r.Ports)
		if err != nil {
			return nil, true // cannot tell: conflict with everybody
		}
		ports = append(ports, ps.Ports()...)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	ranges = mergePorts(ports)
	if len(ranges) > beaconMaxRanges {
		return nil, true
	}
	return ranges, false
}

// mergePorts turns sorted ports into ranges of consecutive ports.
func mergePorts(sorted []uint16) []PortRange {
	var ranges []PortRange
	for _, p := range sorted {
		n := len(ranges)
		if n == 0 || !ranges[n-1].reaches(p) {
			ranges = append(ranges, PortRange{p, p})
			continue
		}
		ranges[n-1].Hi = max(ranges[n-1].Hi, p)
	}
	return ranges
}

// reaches reports whether port p lies in the range or right after it (without wrapping past 65535).
func (r PortRange) reaches(p uint16) bool {
	return r.Hi != 65535 && p <= r.Hi+1
}

// portsOverlap reports whether two port sets share a port; "all" overlaps anything, also an empty set
// (an engine without rules sends nothing, but it may get rules any second).
func portsOverlap(aAll bool, a []PortRange, bAll bool, b []PortRange) bool {
	return aAll || bAll || rangesOverlap(a, b)
}

// rangesOverlap reports whether two sorted, merged range lists share a port.
func rangesOverlap(a, b []PortRange) bool {
	for i, j := 0, 0; i < len(a) && j < len(b); {
		if a[i].Hi < b[j].Lo {
			i++
		} else if b[j].Hi < a[i].Lo {
			j++
		} else {
			return true
		}
	}
	return false
}

// BuildBeacon returns a broadcast beacon frame sent from mac.
func BuildBeacon(mac pkt.MAC, b Beacon) []byte {
	n := len(b.Ports)
	if b.sendsAllPorts() {
		n = 0
	}
	f := make([]byte, pkt.EthHeaderLen+beaconOffRanges+n*beaconRangeBytes, 96+n*beaconRangeBytes) // padded by the NIC
	copy(f[0:ethOffSrc], pkt.BroadcastMAC[:])
	copy(f[ethOffSrc:ethOffType], mac[:])
	binary.BigEndian.PutUint16(f[ethOffType:], BeaconEtherType)
	p := f[pkt.EthHeaderLen:]
	copy(p[beaconOffMagic:], beaconMagic[:])
	p[beaconOffVersion] = beaconVersion
	copy(p[beaconOffInstance:beaconOffStarted], b.Instance[:])
	binary.BigEndian.PutUint64(p[beaconOffStarted:], uint64(b.Started))
	if b.Active {
		p[beaconOffActive] = 1
	}
	copy(p[beaconOffHash:beaconOffFlags], b.Hash[:])
	if b.sendsAllPorts() {
		p[beaconOffFlags] = beaconFlagAll
	}
	binary.BigEndian.PutUint16(p[beaconOffCount:], uint16(n))
	for i := 0; i < n; i++ {
		o := beaconOffRanges + i*beaconRangeBytes
		binary.BigEndian.PutUint16(p[o:o+2], b.Ports[i].Lo)
		binary.BigEndian.PutUint16(p[o+2:o+4], b.Ports[i].Hi)
	}
	return f
}

// sendsAllPorts reports whether the beacon goes out as "all ports": ports unknown, or more ranges than fit.
func (b *Beacon) sendsAllPorts() bool {
	return b.AllPorts || len(b.Ports) > beaconMaxRanges
}

// ParseBeacon decodes a beacon frame (version 1 or 2).
func ParseBeacon(f []byte) (Beacon, bool) {
	var b Beacon
	version, ok := beaconFrameVersion(f)
	if !ok {
		return b, false
	}
	p := f[pkt.EthHeaderLen:]
	copy(b.Instance[:], p[beaconOffInstance:beaconOffStarted])
	b.Started = int64(binary.BigEndian.Uint64(p[beaconOffStarted:beaconOffActive]))
	b.Active = p[beaconOffActive] == 1
	copy(b.Hash[:], p[beaconOffHash:beaconOffFlags])
	if version == 1 {
		b.AllPorts = true
		return b, true
	}
	if !b.parsePorts(p) {
		return Beacon{}, false
	}
	return b, true
}

// beaconFrameVersion returns the beacon version of frame f; false if f is no beacon of a known version.
func beaconFrameVersion(f []byte) (byte, bool) {
	if len(f) < pkt.EthHeaderLen+beaconLen || binary.BigEndian.Uint16(f[ethOffType:]) != BeaconEtherType {
		return 0, false
	}
	p := f[pkt.EthHeaderLen:]
	version := p[beaconOffVersion]
	known := version == 1 || version == beaconVersion
	return version, known && bytes.Equal(p[beaconOffMagic:beaconOffVersion], beaconMagic[:])
}

// parsePorts reads the port part of a version 2 beacon payload p.
func (b *Beacon) parsePorts(p []byte) bool {
	if len(p) < beaconOffRanges {
		return false
	}
	b.AllPorts = p[beaconOffFlags]&beaconFlagAll != 0
	n := int(binary.BigEndian.Uint16(p[beaconOffCount:beaconOffRanges]))
	if n > beaconMaxRanges || len(p) < beaconOffRanges+n*beaconRangeBytes {
		return false
	}
	for i := 0; i < n; i++ {
		o := beaconOffRanges + i*beaconRangeBytes
		b.Ports = append(b.Ports, PortRange{binary.BigEndian.Uint16(p[o : o+2]), binary.BigEndian.Uint16(p[o+2 : o+4])})
	}
	return true
}

// SetPorts records the ports of the applied snapshot (sent in the beacon, compared with peers).
func (g *Guard) SetPorts(s *compile.Snapshot) {
	r, all := RelayedPorts(s)
	g.ports.Store(&beaconPorts{all: all, ranges: r})
}

func (g *Guard) myPorts() *beaconPorts {
	if p := g.ports.Load(); p != nil {
		return p
	}
	return &beaconPorts{all: true} // before the first snapshot: conflict with everybody
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
