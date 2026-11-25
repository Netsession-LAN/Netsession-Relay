// Package notify delivers relay events (alarm opened/cleared, audit entries, test messages) to the monitoring
// targets (syslog, OTLP, SNMP traps). Each target has its own bounded queue and goroutine: Publish never blocks,
// a slow or dead target never slows down the web UI or the other targets.
package notify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// PEN is the IANA Private Enterprise Number used in syslog structured data and the SNMP tree. 32473 is the
// number IANA reserved for examples in documentation (RFC 5612): it never collides with a real vendor, but it
// is not this project's own. Who has a number of their own changes it here and regenerates the MIB and the
// Zabbix SNMP template (`nsrelay snmp-mib`, `nsrelay snmp-mib -zabbix`); the tests keep both files in step.
const PEN = 32473

// Severity of an event; each output maps it to its own scale (syslog severity, OTLP SeverityNumber).
type Severity int

const (
	Info   Severity = iota // test
	Notice                 // audit, alarm cleared
	Warn                   // alarm opened, audit of a dangerous action
)

// Event types.
const (
	TypeAlarm = "alarm"
	TypeAudit = "audit"
	TypeTest  = "test"
)

// Event is one message for the targets.
type Event struct {
	Time     time.Time
	Type     string // alarm | audit | test
	State    string // alarm: open | clear
	Severity Severity
	Kind     string // alarm kind (foreign_relay, loop, storm, dual_active, vlan_silent, engine_down, unknown_beacon)
	Key      string // alarm key (rule/VLAN)
	VLAN     int    // 0 = no VLAN
	Action   string // audit action, e.g. game.enable
	User     string
	Object   string
	Text     string // finished German sentence
}

// MsgID is the short message id used by syslog and as an OTLP attribute.
func (e Event) MsgID() string { return e.Type }

// Sender delivers events to one target.
type Sender interface {
	Send(ctx context.Context, batch []Event) error
	// Reliable: the transport confirms delivery (TCP, TLS, gRPC, inform). Unreliable senders get one try.
	Reliable() bool
	Close() error
}

// Factory builds the sender of a target.
type Factory func(t store.Target) (Sender, error)

const (
	queueSize  = 256
	batchSize  = 50
	sendTime   = 5 * time.Second
	maxBackoff = 60 * time.Second
	maxAge     = 10 * time.Minute
	failAfter  = 2 * time.Minute // "Fehler" when the last try failed and nothing succeeded for this long
)

// Status is the runtime state of a target (memory only).
type Status struct {
	ID        int64
	Name      string
	Kind      string
	Enabled   bool
	State     string // ok | unconfirmed | error | off | waiting
	LastOK    time.Time
	LastErr   string
	LastErrAt time.Time
	Sent      uint64
	Dropped   uint64
	Queued    int
}

// Failing reports whether the target counts as failing (for the UI and the outputs_failing metric).
func (s Status) Failing() bool { return s.State == "error" }

// Publish hands an event to every enabled target that wants its type. It never blocks.
func (d *Dispatcher) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	for _, w := range d.subscribers(ev.Type) {
		w.push(ev)
	}
}

// subscribers returns the workers whose targets take events of type typ (test events go to no one).
func (d *Dispatcher) subscribers(typ string) []*worker {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws := make([]*worker, 0, len(d.workers))
	for _, w := range d.workers {
		if w.t.Wants(typ) && typ != TypeTest {
			ws = append(ws, w)
		}
	}
	return ws
}

// ErrUnknownTarget is returned by Test for a target that is not running.
var ErrUnknownTarget = errors.New("target not active")

// Test sends one event synchronously to a single target (the "Testen" button) and returns the result.
func (d *Dispatcher) Test(ctx context.Context, id int64, ev Event) error {
	d.mu.Lock()
	w := d.workers[id]
	setup := d.setupErr[id]
	d.mu.Unlock()
	if setup != "" {
		return errors.New(setup)
	}
	if w == nil {
		return ErrUnknownTarget
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	ctx, cancel := context.WithTimeout(ctx, sendTime)
	defer cancel()
	err := w.sender.Send(ctx, []Event{ev})
	w.mu.Lock()
	if err != nil {
		w.lastErr, w.errAt = err.Error(), time.Now()
	} else {
		w.lastOK = time.Now()
		w.sent++
	}
	w.mu.Unlock()
	return err
}

// Status returns the state of every configured target (disabled ones as "off").
func (d *Dispatcher) Status() []Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Status, 0, len(d.targets))
	for _, t := range d.targets {
		switch w := d.workers[t.ID]; {
		case !t.Enabled:
			out = append(out, Status{ID: t.ID, Name: t.Name, Kind: t.Kind, State: "off"})
		case d.setupErr[t.ID] != "":
			out = append(out, Status{ID: t.ID, Name: t.Name, Kind: t.Kind, Enabled: true, State: "error", LastErr: d.setupErr[t.ID]})
		case w != nil:
			out = append(out, w.status())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Summary counts targets for the monitoring report.
func Summary(st []Status) (total, failing int, dropped uint64) {
	for _, s := range st {
		if s.State == "off" {
			continue
		}
		total++
		if s.Failing() {
			failing++
		}
		dropped += s.Dropped
	}
	return
}

// String is used in logs only.
func (e Event) String() string { return fmt.Sprintf("%s/%s %s", e.Type, e.State, e.Text) }
