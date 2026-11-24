// Package notify delivers relay events (alarm opened/cleared, audit entries, test messages) to the monitoring
// targets (syslog, OTLP, SNMP traps). Each target has its own bounded queue and goroutine: Publish never blocks,
// a slow or dead target never slows down the web UI or the other targets.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
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

// Dispatcher owns one worker per enabled target.
type Dispatcher struct {
	log       *slog.Logger
	factories map[string]Factory

	mu       sync.Mutex
	workers  map[int64]*worker
	targets  []store.Target
	setupErr map[int64]string // targets whose sender could not be built
}

// New creates a dispatcher; factories maps a target kind to its sender factory.
func New(log *slog.Logger, factories map[string]Factory) *Dispatcher {
	return &Dispatcher{log: log, factories: factories, workers: map[int64]*worker{}, setupErr: map[int64]string{}}
}

// Reload starts, restarts and stops workers so they match targets. A target is restarted when it changed
// (Updated); unchanged targets keep their sender, queue and counters.
func (d *Dispatcher) Reload(targets []store.Target) { d.reload(targets, false) }

// Rebuild is Reload for a change outside the targets that every sender has built in (source address, host
// name): each enabled target gets a new sender, whether its own configuration changed or not.
func (d *Dispatcher) Rebuild(targets []store.Target) { d.reload(targets, true) }

func (d *Dispatcher) reload(targets []store.Target, force bool) {
	var stopping []*worker
	// workers are stopped outside the lock: a sender may read the status while it shuts down (OTLP sends
	// a last metrics export)
	defer func() {
		for _, w := range stopping {
			d.stopWorker(w)
		}
	}()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.targets = targets
	keep := map[int64]bool{}
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		keep[t.ID] = true
		w := d.workers[t.ID]
		if w != nil && !force && w.t.Updated == t.Updated {
			continue
		}
		if w != nil {
			stopping = append(stopping, w)
		}
		d.start(t)
	}
	for id, w := range d.workers {
		if !keep[id] {
			stopping = append(stopping, w)
			delete(d.workers, id)
		}
	}
}

// start builds the sender of a target and runs a new worker for it; a target whose sender cannot be built is
// remembered with the reason instead. The caller holds d.mu and stops the old worker.
func (d *Dispatcher) start(t store.Target) {
	delete(d.workers, t.ID)
	delete(d.setupErr, t.ID)
	f := d.factories[t.Kind]
	if f == nil {
		d.setupErr[t.ID] = "no sender for kind " + t.Kind
		return
	}
	s, err := f(t)
	if err != nil {
		d.setupErr[t.ID] = err.Error()
		return
	}
	w := &worker{t: t, sender: s, log: d.log, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	d.workers[t.ID] = w
	go w.run()
}

func (d *Dispatcher) stopWorker(w *worker) {
	close(w.stop)
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
	}
	_ = w.sender.Close()
}

// Publish hands an event to every enabled target that wants its type. It never blocks.
func (d *Dispatcher) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	d.mu.Lock()
	ws := make([]*worker, 0, len(d.workers))
	for _, w := range d.workers {
		if w.t.Wants(ev.Type) && ev.Type != TypeTest {
			ws = append(ws, w)
		}
	}
	d.mu.Unlock()
	for _, w := range ws {
		w.push(ev)
	}
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

// Close stops all workers; queued events get up to 3 s to go out.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	ws := d.workers
	d.workers = map[int64]*worker{}
	d.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for _, w := range ws {
		for time.Now().Before(deadline) {
			w.mu.Lock()
			n := len(w.queue)
			w.mu.Unlock()
			if n == 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		close(w.stop)
		<-w.done
		_ = w.sender.Close()
	}
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
