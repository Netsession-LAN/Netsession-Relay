package web

import (
	"context"
	"net/http"
	"time"
)

// ---- overview ----

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	// an admin without any network continues with the setup (step 2); a message in the URL is shown first
	if s.isAdmin(r) && r.URL.RawQuery == "" && s.needsNetwork(r.Context()) {
		http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
		return
	}
	ev, _ := s.Store.ActiveEvent(r.Context()) // no event yet (before the setup): the page says so itself
	s.render(w, r, "overview", "Übersicht", "overview", map[string]any{"Event": ev})
}

// ---- design reference and API ----

func (s *Server) components(w http.ResponseWriter, r *http.Request) {
	if !s.Demo { // a page for working on the design, not part of a running relay
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "components", "Komponenten", "", nil)
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	st, err := s.Engine.Status(ctx)
	out := map[string]any{"version": s.Version, "engine": st}
	if err != nil {
		// the page only needs to know that the engine is away; why is in the log
		s.Log.Debug("status: engine not reachable", "err", err)
		out["engine_error"] = "engine not reachable"
	}
	if ev, err := s.Store.ActiveEvent(ctx); err == nil {
		out["event"] = ev
	}
	writeJSON(w, http.StatusOK, out)
}
