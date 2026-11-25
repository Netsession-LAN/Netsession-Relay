package web

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// The hub of the live stream: one engine poll per interval for all open pages, each sample sent in the languages
// of the pages.

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
	ctx, cancel := s.hubContext()
	defer cancel()
	for {
		select {
		case <-s.done:
			h.stop()
			return
		case <-t.C:
		}
		if !h.keepRunning() {
			return
		}
		s.publishSample(ctx)
	}
}

// hubContext is the context of the hub's polls: they end with the server (Close), not with the request that
// happened to start the hub.
func (s *Server) hubContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// stop marks the hub as stopped; the next subscriber starts it again.
func (h *liveHub) stop() {
	h.mu.Lock()
	h.running = false
	h.mu.Unlock()
}

// keepRunning reports whether a page is still open. When none is, the hub stops under the same lock, so a page
// that subscribes right after starts a new one.
func (h *liveHub) keepRunning() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) > 0 {
		return true
	}
	h.running = false
	return false
}

// publishSample polls the engine once and sends the sample, with the rates of the busy games, to every open page.
func (s *Server) publishSample(ctx context.Context) {
	h := s.hub
	v := s.liveNow(ctx)
	h.sample(v)
	for i := range v.Busy {
		v.Busy[i].Rate = h.rate[v.Busy[i].Rule]
	}
	h.send(v)
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
