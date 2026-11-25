package notify

import (
	"log/slog"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The dispatcher: one worker per enabled target, started, restarted and stopped as the targets change.

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
		if old := d.restart(t, force); old != nil {
			stopping = append(stopping, old)
		}
	}
	stopping = append(stopping, d.removeWorkersExcept(keep)...)
}

// restart starts a new worker for t unless one for the unchanged target runs already (force: in any case). It
// returns the replaced worker, which the caller stops, or nil. The caller holds d.mu.
func (d *Dispatcher) restart(t store.Target, force bool) *worker {
	w := d.workers[t.ID]
	if w != nil && !force && w.t.Updated == t.Updated {
		return nil
	}
	d.start(t)
	return w
}

// removeWorkersExcept removes the workers of the targets not in keep and returns them for the caller to stop.
// The caller holds d.mu.
func (d *Dispatcher) removeWorkersExcept(keep map[int64]bool) []*worker {
	var removed []*worker
	for id, w := range d.workers {
		if !keep[id] {
			removed = append(removed, w)
			delete(d.workers, id)
		}
	}
	return removed
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
