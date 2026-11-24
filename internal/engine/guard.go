package engine

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/maphash"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/pkt"
	"github.com/Netsession2024/Netsession-Relay/internal/portspec"
)

// Guard holds the protection state shared by all receive goroutines:
// loop detection (dedup), rate limits, storm brake, alarms and the dual-active guard.
// It survives snapshot changes.
type Guard struct {
	Now func() time.Time // injectable clock (tests)

	Instance [16]byte  // random per process
	Started  time.Time // process start, decides who yields on dual-active
	// Holdoff: nothing is sent before Started+Holdoff, so a second engine can see the first one's
	// beacon before it ever relays (default 1.5 s, beacons every second).
	Holdoff time.Duration

	Alarms  Alarms
	Players Players // who searches/hosts which game
	Finder  Finder  // port finder sessions
	Probe   Prober  // server probe: learned queries and servers that answered

	dedup   [dedupShards]dedupShard
	buckets [bucketShards]bucketShard
	seed    maphash.Seed

	standby        atomic.Bool
	ports          atomic.Pointer[beaconPorts] // UDP ports this engine relays (SetPorts), compared with peers
	lastPeerActive atomic.Int64                // unix nanos
	peerTimeout    time.Duration
	beaconKey      []byte // shared secret for beacons (SetBeaconKey); nil = beacons are neither signed nor checked

	stormOverSecs int
	lastSent      uint64
	lastCopies    map[stormKey]uint64
	pauseMu       sync.RWMutex
	paused        map[stormKey]int64 // storm brake: (rule, ingress VLAN) -> paused until (unix nanos)
	pausedN       atomic.Int32       // len(paused), so the packet path skips the lock while nothing is paused
}

// stormKey names what the storm brake pauses: one rule for the frames arriving on one VLAN.
type stormKey struct {
	rule string
	vid  int
}

// NewGuard returns a guard with a fresh instance ID.
func NewGuard(now func() time.Time) *Guard {
	if now == nil {
		now = time.Now
	}
	g := &Guard{Now: now, Started: now(), Holdoff: 1500 * time.Millisecond, seed: maphash.MakeSeed(),
		peerTimeout: 10 * time.Second, lastCopies: map[stormKey]uint64{}, paused: map[stormKey]int64{}}
	_, _ = rand.Read(g.Instance[:])
	for i := range g.dedup {
		g.dedup[i].m = map[uint64]dedupEntry{}
	}
	for i := range g.buckets {
		g.buckets[i].m = map[bucketKey]*bucket{}
	}
	return g
}

// Sending reports whether the engine may send now (holdoff over and not in standby).
func (g *Guard) Sending(now time.Time) bool {
	return !g.standby.Load() && !now.Before(g.Started.Add(g.Holdoff))
}

// Standby reports whether the dual-active guard has put this engine into standby.
func (g *Guard) Standby() bool { return g.standby.Load() }

// ---- step 7 of the pipeline (core.go): dedup across VLANs ----

const dedupShards = 32

type dedupEntry struct {
	vlan int
	at   int64
}

type dedupShard struct {
	mu sync.Mutex
	m  map[uint64]dedupEntry
}

// SeenElsewhere records the frame and reports whether the same datagram (source, ports, payload) arrived on
// a different VLAN within window - a loop or a second relay without original-IP marking.
func (g *Guard) SeenElsewhere(f *pkt.Frame, vlan int, now time.Time, window time.Duration) bool {
	var h maphash.Hash
	h.SetSeed(g.seed)
	a := f.Src.As4()
	h.Write(a[:])
	var ports [4]byte
	binary.BigEndian.PutUint16(ports[0:], f.SrcPort)
	binary.BigEndian.PutUint16(ports[2:], f.DstPort)
	h.Write(ports[:])
	h.Write(f.Payload)
	key := h.Sum64()
	sh := &g.dedup[key%dedupShards]
	ts := now.UnixNano()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if e, ok := sh.m[key]; ok && ts-e.at <= window.Nanoseconds() && e.vlan != vlan {
		return true
	}
	sh.m[key] = dedupEntry{vlan: vlan, at: ts}
	if len(sh.m) > 4096 { // bounded memory: drop expired entries
		for k, e := range sh.m {
			if ts-e.at > window.Nanoseconds() {
				delete(sh.m, k)
			}
		}
	}
	return false
}

// ---- step 9: token buckets ----

const bucketShards = 32

type bucketKey struct {
	rule string
	src  netip.Addr
	port uint16
	vlan int  // >0: per-VLAN bucket (rule/src/port empty)
	out  bool // per-VLAN bucket of copies sent (fan-out), not of frames received
}

type bucket struct {
	tokens float64
	last   int64
}

type bucketShard struct {
	mu sync.Mutex
	m  map[bucketKey]*bucket
}

func (g *Guard) take(k bucketKey, rate, burst int, now time.Time) bool {
	return g.takeN(k, 1, rate, burst, now)
}

// takeN takes n tokens at once (all or nothing).
func (g *Guard) takeN(k bucketKey, n, rate, burst int, now time.Time) bool {
	if rate <= 0 {
		return true
	}
	var h maphash.Hash
	h.SetSeed(g.seed)
	h.WriteString(k.rule)
	if k.src.IsValid() { // per-VLAN buckets have no source
		a := k.src.As4()
		h.Write(a[:])
	}
	sh := &g.buckets[(h.Sum64()+uint64(k.port)+uint64(k.vlan))%bucketShards]
	ts := now.UnixNano()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	b := sh.m[k]
	if b == nil {
		b = &bucket{tokens: float64(burst), last: ts}
		sh.m[k] = b
	}
	b.tokens += float64(ts-b.last) / 1e9 * float64(rate)
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = ts
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// AllowFlow applies the per (rule, source IP, destination port) bucket.
func (g *Guard) AllowFlow(rule string, src netip.Addr, port uint16, rate, burst int, now time.Time) bool {
	return g.take(bucketKey{rule: rule, src: src, port: port}, rate, burst, now)
}

// AllowVLAN applies the per ingress VLAN bucket.
func (g *Guard) AllowVLAN(vid, rate, burst int, now time.Time) bool {
	return g.take(bucketKey{vlan: vid}, rate, burst, now)
}

// AllowCopies applies the per ingress VLAN budget of copies: a frame that goes out to n VLANs costs n. It keeps
// one VLAN from filling the engine-wide storm budget by itself, however many sources it invents.
func (g *Guard) AllowCopies(vid, n, rate, burst int, now time.Time) bool {
	return g.takeN(bucketKey{vlan: vid, out: true}, n, rate, max(burst, n), now)
}

// GC removes idle buckets (call about once a minute).
func (g *Guard) GC(now time.Time) {
	for i := range g.buckets {
		sh := &g.buckets[i]
		sh.mu.Lock()
		for k, b := range sh.m {
			if now.UnixNano()-b.last > int64(time.Minute) {
				delete(sh.m, k)
			}
		}
		sh.mu.Unlock()
	}
}

// ---- step 8: storm brake (checked before the buckets of step 9) ----

// StormPause is how long the storm brake pauses a rule for one ingress VLAN.
const StormPause = 60 * time.Second

// StormTick is called once per second. If the engine sent more than storm_pps for three consecutive seconds,
// the loudest pair (rule, ingress VLAN) - the one that caused most copies in the last second - is paused for
// 60 seconds: only frames of that rule arriving on that VLAN are dropped, every other VLAN keeps the game. If
// the engine stays over the limit, the next loudest pair follows three seconds later. It returns the paused
// rule ID and VLAN ID ("" and 0 when nothing was paused).
func (g *Guard) StormTick(t *Table, c *Counters, now time.Time) (string, int) {
	sent := c.Sent.Load()
	delta := sent - g.lastSent
	if sent < g.lastSent {
		delta = sent
	}
	g.lastSent = sent
	var top stormKey
	var topRate uint64
	last := g.lastCopies
	g.lastCopies = make(map[stormKey]uint64, len(last))
	for _, r := range t.rules {
		for in := range r.copies {
			v := r.copies[in].Load()
			if v == 0 {
				continue
			}
			k := stormKey{r.ID, t.VLANs[in].VID}
			d := v
			if p, ok := last[k]; ok && p <= v { // counters start at 0 with every new table
				d = v - p
			}
			g.lastCopies[k] = v
			if d > topRate {
				top, topRate = k, d
			}
		}
	}
	g.expirePauses(now)
	if delta <= uint64(t.limits.StormPPS) {
		g.stormOverSecs = 0
		return "", 0
	}
	g.stormOverSecs++
	if g.stormOverSecs < 3 || topRate == 0 {
		return "", 0
	}
	g.stormOverSecs = 0
	g.pauseMu.Lock()
	g.paused[top] = now.Add(StormPause).UnixNano()
	g.pausedN.Store(int32(len(g.paused)))
	g.pauseMu.Unlock()
	g.Alarms.Raise(AlarmStorm, top.rule, now, "paused for 60 s for frames from VLAN %d only: %d frames/s sent in total, %d/s of them by this rule from that VLAN",
		top.vid, delta, topRate)
	return top.rule, top.vid
}

// Paused reports whether the storm brake currently drops frames of the rule arriving on the VLAN.
func (g *Guard) Paused(rule string, vid int, now time.Time) bool {
	if g.pausedN.Load() == 0 {
		return false
	}
	g.pauseMu.RLock()
	until := g.paused[stormKey{rule, vid}]
	g.pauseMu.RUnlock()
	return until > now.UnixNano()
}

func (g *Guard) expirePauses(now time.Time) {
	if g.pausedN.Load() == 0 {
		return
	}
	g.pauseMu.Lock()
	for k, until := range g.paused {
		if until <= now.UnixNano() {
			delete(g.paused, k)
		}
	}
	g.pausedN.Store(int32(len(g.paused)))
	g.pauseMu.Unlock()
}

// ---- dual-active guard ----

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

// beaconDomain separates the beacon MAC from any other use of the same secret.
const beaconDomain = "nsrelay-beacon-v1\x00"

// SignBeacon appends the signature trailer to a frame from BuildBeacon: the send time and an HMAC-SHA256
// (truncated to 16 bytes) over the whole payload including the time, keyed with the shared secret. The version
// stays 2, so engines without signature support still read the beacon.
func SignBeacon(f, key []byte, now time.Time) []byte {
	p := f[pkt.EthHeaderLen:]
	p[beaconOffFlags] |= beaconFlagSigned
	var ts [beaconTimeLen]byte
	binary.BigEndian.PutUint64(ts[:], uint64(now.UnixNano()))
	f = append(f, ts[:]...)
	return append(f, beaconTag(key, f[pkt.EthHeaderLen:])...)
}

func beaconTag(key, signed []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(beaconDomain))
	m.Write(signed)
	return m.Sum(nil)[:beaconTagLen]
}

// BeaconAuth checks the signature of a beacon frame that ParseBeacon accepted. It returns "" when the beacon is
// signed with key and fresh, otherwise why not: "unsigned" (an engine without key, an old version, or a forged
// frame), "signature" (another key, or forged) or "clock" (older or newer than BeaconMaxSkew: a replay, or the
// clocks of the two relays differ).
func BeaconAuth(f, key []byte, now time.Time) string {
	p := f[pkt.EthHeaderLen:]
	if len(p) < beaconOffRanges || p[beaconOffVersion] != beaconVersion || p[beaconOffFlags]&beaconFlagSigned == 0 {
		return "unsigned"
	}
	end := beaconOffRanges + int(binary.BigEndian.Uint16(p[beaconOffCount:beaconOffRanges]))*beaconRangeBytes
	if len(p) < end+beaconSigLen {
		return "unsigned"
	}
	if !hmac.Equal(beaconTag(key, p[:end+beaconTimeLen]), p[end+beaconTimeLen:end+beaconSigLen]) {
		return "signature"
	}
	sent := time.Unix(0, int64(binary.BigEndian.Uint64(p[end:end+beaconTimeLen])))
	if d := now.Sub(sent); d > BeaconMaxSkew || d < -BeaconMaxSkew {
		return "clock"
	}
	return ""
}

// LoadBeaconKey returns the shared secret for beacons and where it came from: the environment value
// (NSRELAY_BEACON_KEY) when set, else the content of file, which is created with a random key on first use
// (mode 0600, next to the snapshot in the engine's state directory). So beacons are signed without any
// configuration; a cloned VM carries the same file and is still recognised. Relays that are installed
// separately and must recognise each other get the same value through the environment.
func LoadBeaconKey(env, file string) (key []byte, source string, err error) {
	if env = strings.TrimSpace(env); env != "" {
		if len(env) < 16 {
			return nil, "", errors.New("NSRELAY_BEACON_KEY is shorter than 16 characters")
		}
		return []byte(env), "env", nil
	}
	if file == "" {
		return nil, "", errors.New("no state directory for the beacon key")
	}
	for range 2 {
		b, err := os.ReadFile(file)
		if err == nil {
			if k := strings.TrimSpace(string(b)); len(k) >= 16 {
				return []byte(k), "file", nil
			}
			return nil, "", fmt.Errorf("%s: too short for a key (delete it to get a new one)", file)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, "", err
		}
		// O_EXCL: when two engines start at once, one creates the file and the other reads it
		fh, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			time.Sleep(50 * time.Millisecond) // let the other one finish writing
			continue
		}
		if err != nil {
			return nil, "", err
		}
		_, werr := fh.WriteString(hex.EncodeToString(raw) + "\n")
		if cerr := fh.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			_ = os.Remove(file)
			return nil, "", werr
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, "", err
	}
	if k := strings.TrimSpace(string(b)); len(k) >= 16 {
		return []byte(k), "file", nil
	}
	return nil, "", fmt.Errorf("%s: too short for a key", file)
}

// SetBeaconKey switches beacon signing and checking on (before the engine starts receiving).
func (g *Guard) SetBeaconKey(key []byte) { g.beaconKey = key }

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
	if !ok || b.Instance == g.Instance {
		return false
	}
	if g.beaconKey != nil {
		if why := BeaconAuth(f, g.beaconKey, now); why != "" {
			if mp := g.myPorts(); portsOverlap(mp.all, mp.ranges, b.AllPorts, b.Ports) {
				g.Alarms.Raise(AlarmUnknownBeacon, strconv.Itoa(vid), now,
					"VLAN %d: relay signal from %s without a valid signature (%s), ignored: another relay without the same key, or a forged frame",
					vid, pkt.MAC(f[ethOffSrc:ethOffType]), why)
			}
			return false
		}
	}
	return g.SeeBeacon(b, now)
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
	for _, p := range ports {
		if n := len(ranges); n > 0 && p <= ranges[n-1].Hi+1 && ranges[n-1].Hi != 65535 {
			if p > ranges[n-1].Hi {
				ranges[n-1].Hi = p
			}
			continue
		}
		ranges = append(ranges, PortRange{p, p})
	}
	if len(ranges) > beaconMaxRanges {
		return nil, true
	}
	return ranges, false
}

// portsOverlap reports whether two port sets share a port; "all" overlaps anything, also an empty set
// (an engine without rules sends nothing, but it may get rules any second).
func portsOverlap(aAll bool, a []PortRange, bAll bool, b []PortRange) bool {
	if aAll || bAll {
		return true
	}
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
	if b.AllPorts || n > beaconMaxRanges {
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
	if b.AllPorts || len(b.Ports) > beaconMaxRanges {
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

// ParseBeacon decodes a beacon frame (version 1 or 2).
func ParseBeacon(f []byte) (Beacon, bool) {
	var b Beacon
	if len(f) < pkt.EthHeaderLen+beaconLen || binary.BigEndian.Uint16(f[ethOffType:]) != BeaconEtherType {
		return b, false
	}
	p := f[pkt.EthHeaderLen:]
	version := p[beaconOffVersion]
	if !bytes.Equal(p[beaconOffMagic:beaconOffVersion], beaconMagic[:]) || (version != 1 && version != beaconVersion) {
		return b, false
	}
	copy(b.Instance[:], p[beaconOffInstance:beaconOffStarted])
	b.Started = int64(binary.BigEndian.Uint64(p[beaconOffStarted:beaconOffActive]))
	b.Active = p[beaconOffActive] == 1
	copy(b.Hash[:], p[beaconOffHash:beaconOffFlags])
	if version == 1 {
		b.AllPorts = true
		return b, true
	}
	if len(p) < beaconOffRanges {
		return Beacon{}, false
	}
	b.AllPorts = p[beaconOffFlags]&beaconFlagAll != 0
	n := int(binary.BigEndian.Uint16(p[beaconOffCount:beaconOffRanges]))
	if n > beaconMaxRanges || len(p) < beaconOffRanges+n*beaconRangeBytes {
		return Beacon{}, false
	}
	for i := 0; i < n; i++ {
		o := beaconOffRanges + i*beaconRangeBytes
		b.Ports = append(b.Ports, PortRange{binary.BigEndian.Uint16(p[o : o+2]), binary.BigEndian.Uint16(p[o+2 : o+4])})
	}
	return b, true
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
	mp := g.myPorts()
	if !portsOverlap(mp.all, mp.ranges, b.AllPorts, b.Ports) {
		return false
	}
	mine := g.Started.UnixNano()
	earlier := b.Started < mine || (b.Started == mine && bytes.Compare(b.Instance[:], g.Instance[:]) < 0)
	if !earlier {
		return false // the other one yields
	}
	g.lastPeerActive.Store(now.UnixNano())
	if g.standby.CompareAndSwap(false, true) {
		g.Alarms.Raise(AlarmDualActive, "peer", now, "another active relay (started %s) relays the same ports, going to standby",
			time.Unix(0, b.Started).UTC().Format(time.RFC3339))
		return true
	}
	return false
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

// ---- alarms ----

// Alarm kinds.
const (
	AlarmForeignRelay = "foreign_relay"
	AlarmLoop         = "loop"
	AlarmStorm        = "storm"
	AlarmDualActive   = "dual_active"
	// AlarmUnknownBeacon: a relay beacon without a valid signature for ports this engine relays (key: VLAN ID).
	AlarmUnknownBeacon = "unknown_beacon"
)

// Alarm is an aggregated alarm (same kind + key). An acknowledged alarm stays acknowledged while it keeps
// occurring; after a quiet gap of ackResetAfter it counts as a new incident and is open again.
type Alarm struct {
	Kind    string    `json:"kind"`
	Key     string    `json:"key"`
	Detail  string    `json:"detail"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	Count   uint64    `json:"count"`
	AckedAt time.Time `json:"acked_at"`
	AckedBy string    `json:"acked_by,omitempty"`
}

const ackResetAfter = 60 * time.Second

// Alarms aggregates alarms in memory; the web part reads them through the engine API and writes them to the
// history.
type Alarms struct {
	mu sync.Mutex
	m  map[string]*Alarm
}

// Raise records an alarm and reports whether it is new.
func (a *Alarms) Raise(kind, key string, now time.Time, format string, args ...any) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string]*Alarm{}
	}
	id := kind + "|" + key
	if x, ok := a.m[id]; ok {
		if !x.AckedAt.IsZero() && now.Sub(x.Last) > ackResetAfter {
			x.AckedAt, x.AckedBy = time.Time{}, "" // quiet gap: a new incident
		}
		x.Last = now
		x.Count++
		return false
	}
	a.m[id] = &Alarm{Kind: kind, Key: key, Detail: fmt.Sprintf(format, args...), First: now, Last: now, Count: 1}
	return true
}

// Ack acknowledges all alarms of a kind and returns how many were affected.
func (a *Alarms) Ack(kind, by string, now time.Time) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, x := range a.m {
		if x.Kind == kind {
			x.AckedAt, x.AckedBy = now, by
			n++
		}
	}
	return n
}

// List returns all alarms, newest first.
func (a *Alarms) List() []Alarm {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Alarm, 0, len(a.m))
	for _, x := range a.m {
		out = append(out, *x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}
