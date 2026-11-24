package engine

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// Server probe: many games (Steam/Source, Quake 3, Unreal, ...) only search by broadcast; the servers answer by
// unicast to the client, which the relay never sees. The probe learns the query packets that real clients
// broadcast on rules where every game only queries (compile.Rule.Probe), replays them every ProbeInterval from the
// relay's own address in every VLAN and records who answers. A2S servers (Steam/Source) also get the follow-up
// request with the challenge, so name, map and player count are known.
//
// Replies are unicast UDP to the relay address with destination port ProbePort; the packet filter passes exactly
// those (afpacket_linux.go). The host's nftables drop them afterwards, so the kernel never answers them.

const (
	// ProbePort is the UDP source port of probe packets (outside all known game ranges).
	ProbePort     = 47998
	ProbeInterval = 60 * time.Second
	probeTTL      = 3*ProbeInterval + 10*time.Second // a server is listed until it missed three rounds
	maxProbeQuery = 512                              // learned query payloads are at most this long
	maxProbePorts = 64                               // learned (rule, port) pairs; oldest replaced
	maxServers    = 1024
	// queryTTL: a learned query nobody sent again for this long is no longer replayed (seeds come back)
	queryTTL = 24 * time.Hour
	// answerWindow: answers are only accepted this long after a probe round, so unsolicited packets to the
	// probe port cannot list fake servers between rounds
	answerWindow = 5 * time.Second
)

// a2sInfo is the A2S_INFO request without challenge; it is also the seed for rules on the Source ports, so
// servers are found before any client searched.
var a2sInfo = append([]byte{0xff, 0xff, 0xff, 0xff, 'T'}, []byte("Source Engine Query\x00")...)

// ProbeQuery is one learned query: replayed to Port for Rule.
type ProbeQuery struct {
	Rule    string
	Port    uint16
	Payload []byte
	Learned time.Time
}

// Server is a host that answered a probe.
type Server struct {
	Rule      string
	Src       netip.Addr
	Port      uint16 // the server's UDP port
	VID       int
	First     time.Time
	Last      time.Time
	Count     uint64
	Name, Map string // A2S only
	Game      string // A2S only: the game as the server reports it ("Counter-Strike 2")
	Num, Max  int    // players, A2S only
	MAC       pkt.MAC
	asked     uint64 // probe round of the last challenge follow-up (at most one per round)
}

type serverKey struct {
	src  netip.Addr
	port uint16
	vid  int
}

// Prober keeps the learned queries and the servers that answered. Safe for concurrent use.
type Prober struct {
	mu      sync.Mutex
	queries map[queryKey]*ProbeQuery
	servers map[serverKey]*Server
	// byPort says which rule a probed port belongs to (servers answer from that port); rebuilt every round
	byPort map[uint16]string
	// round counts probe rounds (Queries calls). A server gets one challenge follow-up per round, so a host
	// that answers every request with a new challenge cannot keep the relay in a unicast ping-pong.
	round   uint64
	roundAt time.Time // start of the last round
}

type queryKey struct {
	rule string
	port uint16
}

// Learn stores the payload of a client query on a probe rule (called for every frame of such a rule; cheap when
// the payload is unchanged).
func (p *Prober) Learn(rule string, port uint16, payload []byte, now time.Time) {
	if len(payload) == 0 || len(payload) > maxProbeQuery {
		return
	}
	k := queryKey{rule, port}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.queries == nil {
		p.queries = map[queryKey]*ProbeQuery{}
	}
	if q := p.queries[k]; q != nil {
		q.Learned = now
		if !bytes.Equal(q.Payload, payload) && !isA2SChallenged(payload) {
			q.Payload = append(q.Payload[:0], payload...)
		}
		return
	}
	if isA2SChallenged(payload) { // a client's second request carries its own challenge: keep the plain one
		payload = a2sInfo
	}
	if len(p.queries) >= maxProbePorts {
		var oldest queryKey
		var at time.Time
		for kk, q := range p.queries {
			if at.IsZero() || q.Learned.Before(at) {
				oldest, at = kk, q.Learned
			}
		}
		delete(p.queries, oldest)
	}
	p.queries[k] = &ProbeQuery{Rule: rule, Port: port, Payload: append([]byte(nil), payload...), Learned: now}
}

// q3GetInfo is the id Tech 3 LAN query (Quake 3, Wolfenstein ET, RtCW, Urban Terror, OpenArena, Call of Duty 1-4:
// "getinfo xxx" to the broadcast address); servers answer with "infoResponse".
var q3GetInfo = []byte("\xff\xff\xff\xffgetinfo xxx")

// seeds are built-in queries per port range: servers are found right after a start, before any client searched
// (learned queries live in memory only).
var seeds = []struct {
	lo, hi  uint16
	payload []byte
}{
	{27015, 27020, a2sInfo},   // Steam/Source (CS2, CS:GO, TF2, L4D, ...)
	{27960, 27963, q3GetInfo}, // Quake 3 engine
	{28960, 28963, q3GetInfo}, // Call of Duty 1-4
}

// Seed adds the built-in query for probe rules on known ports that have no learned query yet.
func (p *Prober) Seed(rule string, ports []uint16, now time.Time) {
	for _, port := range ports {
		for _, s := range seeds {
			if port < s.lo || port > s.hi {
				continue
			}
			p.mu.Lock()
			_, ok := p.queries[queryKey{rule, port}]
			p.mu.Unlock()
			if !ok {
				p.Learn(rule, port, s.payload, now)
			}
		}
	}
}

// Forget drops learned queries of rules that are no longer probe rules.
func (p *Prober) Forget(keep map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.queries {
		if !keep[k.rule] {
			delete(p.queries, k)
		}
	}
	for k, s := range p.servers {
		if !keep[s.Rule] {
			delete(p.servers, k)
		}
	}
	for port, rule := range p.byPort { // answers from ports that are no longer probed are ignored again
		if !keep[rule] {
			delete(p.byPort, port)
		}
	}
}

// Queries returns the learned queries (sorted by rule and port) and starts a probe round: for answerWindow,
// answers are accepted from exactly these ports. Queries not seen for queryTTL are dropped.
func (p *Prober) Queries(now time.Time) []ProbeQuery {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ProbeQuery, 0, len(p.queries))
	p.byPort = make(map[uint16]string, len(p.queries)) // fresh: evicted or forgotten ports drop out
	p.round++
	p.roundAt = now
	for k, q := range p.queries {
		if now.Sub(q.Learned) > queryTTL {
			delete(p.queries, k)
			continue
		}
		out = append(out, ProbeQuery{Rule: q.Rule, Port: q.Port, Payload: append([]byte(nil), q.Payload...), Learned: q.Learned})
		p.byPort[q.Port] = q.Rule
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// BuildProbe returns the broadcast frame of query q from the relay address of v.
func BuildProbe(buf []byte, v *VLANRuntime, q ProbeQuery, id uint16) ([]byte, error) {
	f := pkt.Frame{DstMAC: pkt.MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, SrcMAC: v.MAC, ID: id, TTL: 64,
		Src: v.RelayIP, Dst: pkt.LimitedBroadcast, SrcPort: ProbePort, DstPort: q.Port, UDPChecksum: 1, Payload: q.Payload}
	return pkt.Build(buf, &f)
}

// Reply handles a unicast frame to ProbePort on VLAN v. It records the sender as a server and returns a follow-up
// frame (A2S request with the server's challenge) to send on the same VLAN, or nil.
func (p *Prober) Reply(v *VLANRuntime, frame []byte, now time.Time) []byte {
	f, err := pkt.Parse(frame)
	if err != nil || f.DstPort != ProbePort || f.Dst != v.RelayIP || f.DstMAC != v.MAC || !v.CIDR.Contains(f.Src) {
		return nil
	}
	k := serverKey{f.Src, f.SrcPort, v.VID}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.roundAt.IsZero() || now.Sub(p.roundAt) > answerWindow {
		return nil // no round running: unsolicited
	}
	rule := p.byPort[f.SrcPort]
	if rule == "" {
		return nil // not a port we probed
	}
	if p.servers == nil {
		p.servers = map[serverKey]*Server{}
	}
	s := p.servers[k]
	if s == nil {
		if len(p.servers) >= maxServers {
			return nil
		}
		s = &Server{Rule: rule, Src: f.Src, Port: f.SrcPort, VID: v.VID, First: now}
		p.servers[k] = s
	}
	s.Last, s.MAC = now, f.SrcMAC
	s.Count++
	pl := f.Payload
	switch {
	case len(pl) >= 9 && bytes.Equal(pl[:5], []byte{0xff, 0xff, 0xff, 0xff, 'A'}):
		if s.asked == p.round {
			return nil // already asked in this round
		}
		s.asked = p.round
		req := append(append([]byte(nil), a2sInfo...), pl[5:9]...)
		out := pkt.Frame{DstMAC: f.SrcMAC, SrcMAC: v.MAC, TTL: 64, Src: v.RelayIP, Dst: f.Src, SrcPort: ProbePort,
			DstPort: f.SrcPort, UDPChecksum: 1, Payload: req}
		b, err := pkt.Build(nil, &out)
		if err != nil {
			return nil
		}
		return b
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

func isA2SChallenged(b []byte) bool {
	return len(b) == len(a2sInfo)+4 && bytes.Equal(b[:len(a2sInfo)], a2sInfo)
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
