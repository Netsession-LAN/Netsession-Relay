package engine

import (
	"crypto/rand"
	"encoding/binary"
	"hash/maphash"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
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
		sh.dropExpired(ts, window)
	}
	return false
}

// dropExpired removes the entries older than window at ts. sh.mu is held.
func (sh *dedupShard) dropExpired(ts int64, window time.Duration) {
	for k, e := range sh.m {
		if ts-e.at > window.Nanoseconds() {
			delete(sh.m, k)
		}
	}
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
	b.tokens = min(b.tokens+float64(ts-b.last)/1e9*float64(rate), float64(burst))
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
