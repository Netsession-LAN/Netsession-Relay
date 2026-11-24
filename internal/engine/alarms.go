package engine

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

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
