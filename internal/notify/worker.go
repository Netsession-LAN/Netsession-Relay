package notify

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// worker delivers the events of one target: a bounded queue, filled by push, emptied in batches by run.
type worker struct {
	t      store.Target
	sender Sender
	log    *slog.Logger

	mu      sync.Mutex
	queue   []Event
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	lastOK  time.Time
	lastErr string
	errAt   time.Time
	sent    uint64
	dropped uint64
}

func (w *worker) push(ev Event) {
	w.mu.Lock()
	if len(w.queue) >= queueSize {
		w.queue = w.queue[1:] // drop the oldest
		w.dropped++
	}
	w.queue = append(w.queue, ev)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *worker) run() {
	defer close(w.done)
	backoff := time.Second
	for {
		select {
		case <-w.stop:
			return
		case <-w.wake:
		}
		if !w.drain(&backoff) {
			return
		}
	}
}

// drain sends the queue in batches until it is empty. A failed batch on a reliable transport is retried after
// the backoff, which doubles up to maxBackoff and starts again at one second after a success. It returns false
// when the worker was stopped while waiting for a retry.
func (w *worker) drain(backoff *time.Duration) bool {
	for {
		batch := w.nextBatch()
		if len(batch) == 0 {
			return true
		}
		ctx, cancel := context.WithTimeout(context.Background(), sendTime)
		err := w.sender.Send(ctx, batch)
		cancel()
		w.record(len(batch), err)
		if err == nil {
			*backoff = time.Second
			continue
		}
		if w.sender.Reliable() && !w.waitRetry(err, backoff) {
			return false
		}
	}
}

// nextBatch drops the events that waited too long (a dead target for 10 minutes: they are worthless) and returns
// a copy of the oldest batchSize events; they stay queued until record.
func (w *worker) nextBatch() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	for len(w.queue) > 0 && now.Sub(w.queue[0].Time) > maxAge {
		w.queue = w.queue[1:]
		w.dropped++
	}
	n := min(len(w.queue), batchSize)
	return append([]Event(nil), w.queue[:n]...)
}

// record books the result of sending the n oldest events: they leave the queue when they were sent or when
// the transport is unreliable (never retried, counted as dropped on an error).
func (w *worker) record(n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == nil || !w.sender.Reliable() {
		w.queue = w.queue[min(n, len(w.queue)):]
	}
	if err == nil {
		w.lastOK = time.Now()
		w.sent += uint64(n)
		return
	}
	w.lastErr, w.errAt = err.Error(), time.Now()
	if !w.sender.Reliable() {
		w.dropped += uint64(n)
	}
}

// waitRetry waits the jittered backoff before the next try and doubles it; false when the worker was stopped.
func (w *worker) waitRetry(err error, backoff *time.Duration) bool {
	w.log.Debug("monitoring target", "target", w.t.Name, "err", err)
	wait := *backoff + time.Duration(rand.Int64N(int64(*backoff/2)+1)) //nolint:gosec // G404: jitter for retry backoff, not security relevant
	*backoff = min(*backoff*2, maxBackoff)
	select {
	case <-w.stop:
		return false
	case <-time.After(wait):
		return true
	}
}

// failing reports whether the last try failed and nothing succeeded for failAfter.
func (w *worker) failing() bool {
	return !w.errAt.IsZero() && w.errAt.After(w.lastOK) && time.Since(w.lastOK) > failAfter
}

func (w *worker) status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := Status{ID: w.t.ID, Name: w.t.Name, Kind: w.t.Kind, Enabled: true, LastOK: w.lastOK, LastErr: w.lastErr,
		LastErrAt: w.errAt, Sent: w.sent, Dropped: w.dropped, Queued: len(w.queue)}
	switch {
	case w.failing():
		st.State = "error"
	case w.lastOK.IsZero() && w.errAt.IsZero():
		st.State = "waiting"
	case !w.sender.Reliable():
		st.State = "unconfirmed"
	default:
		st.State = "ok"
	}
	return st
}
