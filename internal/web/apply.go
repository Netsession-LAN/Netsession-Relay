package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
)

// Applying the config after a change in the web part and answering the form that caused it.

type applyAnswer struct {
	OK       bool     `json:"ok"`
	Enabled  bool     `json:"enabled"`
	Message  string   `json:"message,omitempty"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Revision int64    `json:"revision,omitempty"`
}

// applyNow compiles and applies after a change; warnings except shared ports are reported.
func (s *Server) applyNow(r *http.Request, note string) applyAnswer {
	res, err := control.Apply(r.Context(), s.Store, s.Engine, s.actor(r), note, false)
	s.dropRuleIndex()
	a := applyAnswer{OK: err == nil, Revision: res.Revision}
	for _, w := range res.Warnings {
		if w.Code != compile.WarnSharedPort {
			a.Warnings = append(a.Warnings, w.Message)
		}
	}
	if err != nil {
		a.Error = string(s.applyError(r, res, err))
	}
	return a
}

// backPath is where a form wants to return to (hidden field "back"); only local paths are accepted.
func backPath(r *http.Request, def string) string {
	p := r.PostFormValue("back")
	if localPath(p) && !strings.ContainsAny(p, "?#") {
		return p
	}
	return def
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request, path string, a applyAnswer) {
	if wantsJSON(r) {
		code := http.StatusOK
		if a.Error != "" {
			code = http.StatusUnprocessableEntity
		}
		writeJSON(w, code, a)
		return
	}
	var err error
	if a.Error != "" {
		err = errors.New(a.Error)
	}
	back(w, r, path, a.Message, err)
}
