package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// Port finder limits.
const (
	FinderMaxDuration = 10 * time.Minute
	finderMaxGroups   = 512
	finderMaxSrcs     = 8
	finderSampleLen   = 64
	finderMaxFrames   = 4000            // frames kept for the pcap download
	finderMaxBytes    = 4 << 20         // and at most this many bytes
	finderNewAfter    = 5 * time.Second // groups first seen later than this after the start are "new"
)

// FinderRequest starts a sniffer session.
type FinderRequest struct {
	VIDs     []int         // empty: all VLANs
	Src      netip.Addr    // invalid: any sender
	Duration time.Duration // capped at FinderMaxDuration
}

// FinderGroup is all frames of one destination (broadcast or multicast group) and port.
type FinderGroup struct {
	Kind   string // "broadcast" | "multicast"
	Group  string // multicast group, empty for broadcast
	Port   uint16
	Rule   string // rule of the active snapshot, empty: no rule (unknown port)
	Count  uint64
	First  time.Time
	Last   time.Time
	MinLen int
	MaxLen int
	Srcs   []netip.Addr // first finderMaxSrcs senders
	VIDs   []int
	New    bool   // first seen more than finderNewAfter after the start
	Sample []byte // first bytes of the first payload
}

// FinderStatus is the state of the current (or last) session.
type FinderStatus struct {
	Active   bool
	Started  time.Time
	Until    time.Time
	VIDs     []int
	Src      netip.Addr
	Frames   uint64 // frames matching the filter
	Captured int    // frames kept for pcap
	Groups   []FinderGroup
}

type finderSession struct {
	req     FinderRequest
	started time.Time
	until   time.Time
	vids    map[int]bool
	frames  uint64
	groups  map[groupKey]*FinderGroup
	caps    []capFrame
	bytes   int
}

// Finder records discovery traffic for the port finder. The zero value is ready; Observe costs one atomic
// load while no session runs.
type Finder struct {
	active atomic.Bool
	mu     sync.Mutex
	s      *finderSession
}

// ErrFinderDuration is returned for a session length outside 1 s .. FinderMaxDuration.
var ErrFinderDuration = errors.New("duration must be between 1 s and 10 min")

// CheckFinderVIDs refuses a session for VLAN IDs the relay has no network for (have = the VLAN IDs of the active
// configuration): such a session would run its time and find nothing, which reads like "the game sends nothing".
func CheckFinderVIDs(want, have []int) error {
	var unknown []int
	for _, v := range want {
		if !slices.Contains(have, v) {
			unknown = append(unknown, v)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	if len(have) == 0 {
		return fmt.Errorf("VLAN %s: the relay has no network yet (set up the networks and apply first)", joinVIDs(unknown))
	}
	return fmt.Errorf("VLAN %s is not a network of this relay (configured: %s)", joinVIDs(unknown), joinVIDs(slices.Sorted(slices.Values(have))))
}

// joinVIDs lists VLAN IDs as "1101, 1102".
func joinVIDs(vids []int) string {
	s := make([]string, len(vids))
	for i, v := range vids {
		s[i] = strconv.Itoa(v)
	}
	return strings.Join(s, ", ")
}

// Start begins a new session and drops the results of the previous one.
func (f *Finder) Start(req FinderRequest, now time.Time) error {
	if req.Duration < time.Second || req.Duration > FinderMaxDuration {
		return ErrFinderDuration
	}
	s := &finderSession{req: req, started: now, until: now.Add(req.Duration), groups: map[groupKey]*FinderGroup{}}
	if len(req.VIDs) > 0 {
		s.vids = map[int]bool{}
		for _, v := range req.VIDs {
			s.vids[v] = true
		}
	}
	f.mu.Lock()
	f.s = s
	f.mu.Unlock()
	f.active.Store(true)
	return nil
}

// Stop ends the session early; the results stay readable.
func (f *Finder) Stop(now time.Time) {
	f.mu.Lock()
	if f.s != nil && now.Before(f.s.until) {
		f.s.until = now
	}
	f.mu.Unlock()
	f.active.Store(false)
}

// Status returns a copy of the session state, groups sorted by count.
func (f *Finder) Status(now time.Time) FinderStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.s
	if s == nil {
		return FinderStatus{}
	}
	st := FinderStatus{Active: now.Before(s.until), Started: s.started, Until: s.until, Src: s.req.Src,
		VIDs: slices.Clone(s.req.VIDs), Frames: s.frames, Captured: len(s.caps)}
	for _, g := range s.groups {
		c := *g
		c.Srcs, c.VIDs, c.Sample = slices.Clone(g.Srcs), slices.Clone(g.VIDs), slices.Clone(g.Sample)
		sort.Ints(c.VIDs)
		st.Groups = append(st.Groups, c)
	}
	sort.Slice(st.Groups, func(i, j int) bool {
		a, b := st.Groups[i], st.Groups[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Port < b.Port
	})
	return st
}

// WritePcap writes the captured frames as a classic pcap file (Ethernet link type), readable by Wireshark.
func (f *Finder) WritePcap(w io.Writer) error {
	f.mu.Lock()
	var caps []capFrame
	if f.s != nil {
		caps = slices.Clone(f.s.caps)
	}
	f.mu.Unlock()
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(hdr[4:], 2)
	binary.LittleEndian.PutUint16(hdr[6:], 4)
	binary.LittleEndian.PutUint32(hdr[16:], 65535) // snaplen
	binary.LittleEndian.PutUint32(hdr[20:], 1)     // LINKTYPE_ETHERNET
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	rec := make([]byte, 16)
	for _, c := range caps {
		if err := writePcapRecord(w, rec, c); err != nil {
			return err
		}
	}
	return nil
}

// writePcapRecord writes the record header (in rec, 16 bytes) and the data of one captured frame.
func writePcapRecord(w io.Writer, rec []byte, c capFrame) error {
	binary.LittleEndian.PutUint32(rec[0:], uint32(c.ts.Unix()))
	binary.LittleEndian.PutUint32(rec[4:], uint32(c.ts.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(rec[8:], uint32(len(c.data)))
	binary.LittleEndian.PutUint32(rec[12:], uint32(len(c.data)))
	if _, err := w.Write(rec); err != nil {
		return err
	}
	_, err := w.Write(c.data)
	return err
}

// FinderAPI converts a finder status for the engine API.
func FinderAPI(st FinderStatus) engineapi.FinderStatus {
	out := engineapi.FinderStatus{Active: st.Active, Started: st.Started, Until: st.Until, VIDs: st.VIDs,
		Frames: st.Frames, Captured: st.Captured, Groups: make([]engineapi.FinderGroup, len(st.Groups))}
	if st.Src.IsValid() {
		out.Src = st.Src.String()
	}
	for i, g := range st.Groups {
		out.Groups[i] = engineapi.FinderGroup(g)
	}
	return out
}
