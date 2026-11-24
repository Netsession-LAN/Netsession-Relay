package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// The live stream: one engine poll per interval for all open pages (the hub) and the server-sent events that
// carry each sample to the browser in the page's language.

// The engine has its own event stream on the Unix socket. The web part does not relay it but asks for the
// status once per interval while at least one page is open: the same code path as the page render, it works
// with every Engine implementation (tests, demo), and the stream also carries database state (games on).
type liveHub struct {
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	langs   map[chan []byte]string // language of each subscriber's page
	running bool
	prevRX  map[string]uint64
	prevAt  time.Time
	rate    map[string]uint64
	rateAt  time.Time
}

func (h *liveHub) rates() map[string]uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.rateAt) > 10*time.Second {
		return nil
	}
	return h.rate
}

// sample stores the per-rule rates derived from two consecutive polls.
func (h *liveHub) sample(v *liveView) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := v.TS
	if dt := now.Sub(h.prevAt).Seconds(); h.prevRX != nil && dt > 0.01 && dt < 30 && v.EngineOK {
		rate := map[string]uint64{}
		for id, rx := range v.ruleRX {
			if p, ok := h.prevRX[id]; ok && rx > p {
				rate[id] = uint64(float64(rx-p)/dt + 0.5)
			}
		}
		h.rate, h.rateAt = rate, now
	}
	if v.EngineOK {
		h.prevRX, h.prevAt = v.ruleRX, now
	}
}

// subscribe adds an open page to the hub; lang is the language its samples are sent in.
func (s *Server) subscribe(lang string) chan []byte {
	h := s.hub
	ch := make(chan []byte, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	if h.langs == nil {
		h.langs = map[chan []byte]string{}
	}
	h.langs[ch] = lang
	start := !h.running
	h.running = true
	h.mu.Unlock()
	if start {
		go s.runHub()
	}
	return ch
}

func (s *Server) unsubscribe(ch chan []byte) {
	s.hub.mu.Lock()
	delete(s.hub.subs, ch)
	delete(s.hub.langs, ch)
	s.hub.mu.Unlock()
}

func (s *Server) runHub() {
	h := s.hub
	t := time.NewTicker(s.interval())
	defer t.Stop()
	// the polls end with the server (Close), not with the request that happened to start the hub
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		select {
		case <-s.done:
			h.mu.Lock()
			h.running = false
			h.mu.Unlock()
			return
		case <-t.C:
		}
		h.mu.Lock()
		if len(h.subs) == 0 {
			h.running = false
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		v := s.liveNow(ctx)
		h.sample(v)
		for i := range v.Busy {
			v.Busy[i].Rate = h.rate[v.Busy[i].Rule]
		}
		h.send(v)
	}
}

// send hands one sample to every open page, as one JSON text per language that is open right now.
func (h *liveHub) send(v *liveView) {
	byLang := h.encodeForLangs(v)
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		if b := byLang[h.langs[ch]]; b != nil { // nil: subscribed after the sample was made, or not serializable
			hubOfferLatest(ch, b)
		}
	}
}

// encodeForLangs encodes the sample once for every language an open page uses; the lock is not held while
// encoding.
func (h *liveHub) encodeForLangs(v *liveView) map[string][]byte {
	byLang := map[string][]byte{}
	h.mu.Lock()
	for ch := range h.subs {
		byLang[h.langs[ch]] = nil
	}
	h.mu.Unlock()
	for lang := range byLang {
		if b, err := json.Marshal(v.localized(lang)); err == nil {
			byLang[lang] = b
		}
	}
	return byLang
}

// hubOfferLatest hands b to a subscriber without blocking. A slow reader loses its old sample and gets the new one.
func hubOfferLatest(ch chan []byte, b []byte) {
	select {
	case ch <- b:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- b:
	default:
	}
}

func (s *Server) interval() time.Duration {
	if s.LiveInterval > 0 {
		return s.LiveInterval
	}
	return 2 * time.Second
}

// apiEvents is the live stream for open pages (server-sent events): one "status" event at once, then one per
// interval. The browser reconnects by itself; "retry" sets the pause.
func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// a bounded number of streams per browser session and per account; more are refused (the page retries)
	se := sessionFrom(r)
	if !s.streams.take(se.ID, se.User.ID) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many live streams"})
		return
	}
	defer s.streams.release(se.ID, se.User.ID)
	rc := http.NewResponseController(w)
	deadline := func() { _ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second)) } // a reader that is gone
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // reverse proxies must not buffer the stream
	lang := s.langOf(r)
	ch := s.subscribe(lang)
	defer s.unsubscribe(ch)
	first, _ := json.Marshal(s.liveNow(r.Context()).localized(lang))
	deadline()
	fmt.Fprintf(w, "retry: 3000\nevent: status\ndata: %s\n\n", first)
	fl.Flush()
	// the session can end while the stream is open
	check := time.NewTicker(time.Minute)
	defer check.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done: // the server shuts down; the page reconnects by itself
			return
		case <-check.C:
			if _, err := s.Store.SessionByToken(r.Context(), s.sessionToken(r)); err != nil {
				deadline()
				fmt.Fprint(w, "event: bye\ndata: {}\n\n")
				fl.Flush()
				return
			}
		case b := <-ch:
			deadline()
			if _, err := fmt.Fprintf(w, "event: status\ndata: %s\n\n", b); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
