package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// The live stream: the server-sent events that carry each sample of the hub (live_hub.go) to the browser in the
// page's language.

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
	ev := &eventWriter{w: w, fl: fl, rc: http.NewResponseController(w)}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // reverse proxies must not buffer the stream
	lang := s.langOf(r)
	ch := s.subscribe(lang)
	defer s.unsubscribe(ch)
	first, _ := json.Marshal(s.liveNow(r.Context()).localized(lang))
	_ = ev.event("retry: 3000\nevent: status\ndata: %s\n\n", first)
	s.streamEvents(r, ev, ch)
}

// streamEvents sends every sample of the hub until the page goes away, the server shuts down or the session
// ends (which the page is told with "bye").
func (s *Server) streamEvents(r *http.Request, ev *eventWriter, ch chan []byte) {
	// the session can end while the stream is open
	check := time.NewTicker(time.Minute)
	defer check.Stop()
	for open := true; open; {
		select {
		case <-r.Context().Done():
			return
		case <-s.done: // the server shuts down; the page reconnects by itself
			return
		case <-check.C:
			open = s.streamSessionValid(r, ev)
		case b := <-ch:
			open = ev.event("event: status\ndata: %s\n\n", b) == nil
		}
	}
}

// streamSessionValid reports whether the session of a live stream still exists; if not, it says "bye" to the page.
func (s *Server) streamSessionValid(r *http.Request, ev *eventWriter) bool {
	if _, err := s.Store.SessionByToken(r.Context(), s.sessionToken(r)); err != nil {
		_ = ev.event("event: bye\ndata: {}\n\n")
		return false
	}
	return true
}

// eventWriter writes server-sent events. Every event gets a fresh write deadline, so a reader that is gone does
// not hold the stream open.
type eventWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
	rc *http.ResponseController
}

// event writes one event and flushes it to the browser.
func (e *eventWriter) event(format string, args ...any) error {
	_ = e.rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err := fmt.Fprintf(e.w, format, args...)
	if err != nil {
		return err
	}
	e.fl.Flush()
	return nil
}
