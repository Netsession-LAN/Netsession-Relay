package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// The operating mode from the system page: mode, server probe, emergency stop and its release.

// emergencyConfirm is the confirmation page for the emergency stop (the header button opens it as a dialog
// when JavaScript runs).
// settingModeBeforeStop holds the mode that was set when the emergency stop was pressed. While it is set and
// the mode is still "paused", an operator may switch the relay back on.
const settingModeBeforeStop = "mode_before_stop"

// resumeMode returns the mode an operator may restore after an emergency stop, or "". When the settings cannot
// be read it is "" (the defaults): no release without knowing the state.
func (s *Server) resumeMode(r *http.Request) string {
	mode, _ := s.Store.Setting(r.Context(), store.SettingMode, compile.ModeActive)
	prev, _ := s.Store.Setting(r.Context(), settingModeBeforeStop, "")
	if mode != compile.ModePaused || (prev != compile.ModeActive && prev != compile.ModeObserve) {
		return ""
	}
	return prev
}

func (s *Server) emergencyConfirm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "notaus", "Not-Aus", "", map[string]any{"Resume": s.resumeMode(r), "ResumeName": modeName(s.resumeMode(r))})
}

// emergencyRelease switches the relay back to the mode it had before the emergency stop (operator and up).
func (s *Server) emergencyRelease(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	prev := s.resumeMode(r)
	if prev == "" {
		back(w, r, "/", "", errors.New(T("Das Relay ist nicht durch Not-Aus pausiert. Die Betriebsart ändert ein Admin unter System.")))
		return
	}
	if err := s.Store.SetMode(r.Context(), s.actor(r), prev); err != nil {
		back(w, r, "/", "", s.userError(r, "emergency release: mode", err))
		return
	}
	s.forgetModeBeforeStop(r.Context())
	s.audit(r.Context(), s.actor(r), "emergency_release", "engine", compile.ModePaused, prev)
	s.Log.Warn("emergency stop released", "user", s.actor(r), "mode", prev)
	a := s.applyNow(r, "Not-Aus aufgehoben")
	if a.OK {
		a.Message = T("Relay wieder an: %s.", T(modeName(prev)))
	}
	s.answer(w, r, "/", a)
}

// setProbe switches the server probe (engine/probe.go) on or off and applies the configuration.
func (s *Server) setProbe(w http.ResponseWriter, r *http.Request) {
	on := r.PostFormValue("on") == "1"
	val, action := "0", "probe.off"
	if on {
		val, action = "1", "probe.on"
	}
	if err := s.Store.SetSetting(r.Context(), store.SettingProbeServers, val); err != nil {
		back(w, r, "/system", "", s.internal(r, "server probe: store the setting", err))
		return
	}
	s.audit(r.Context(), s.actor(r), action, "", "", "")
	a := s.applyNow(r, "Server-Abfrage "+onOff(on))
	if a.OK {
		T := s.T(r)
		a.Message = T("Server-Abfrage ist jetzt %s.", T(onOff(on)))
	}
	s.answer(w, r, "/system#abfrage", a)
}

func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	mode := r.PostFormValue("mode")
	path := backPath(r, "/system")
	if err := s.Store.SetMode(r.Context(), s.actor(r), mode); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			err = uimsg.Error("Diese Betriebsart gibt es nicht.")
		}
		back(w, r, path, "", s.userError(r, "mode: set", err))
		return
	}
	s.forgetModeBeforeStop(r.Context()) // an admin decided: no operator release
	a := s.applyNow(r, "Betriebsart "+modeName(mode))
	if a.OK {
		T := s.T(r)
		a.Message = T("Betriebsart ist jetzt %s.", T(modeName(mode)))
	}
	s.answer(w, r, path, a)
}

// forgetModeBeforeStop ends the operator's right to switch the relay back on. If that cannot be stored the right
// stays, which is logged; the mode itself is not affected.
func (s *Server) forgetModeBeforeStop(ctx context.Context) {
	if err := s.Store.SetSetting(ctx, settingModeBeforeStop, ""); err != nil {
		s.Log.Error("mode before the emergency stop not cleared: an operator can still switch the relay back on", "err", err)
	}
}

// rememberModeBeforeStop stores the current mode for the release. Nothing here may keep the stop itself from
// happening: a mode that cannot be read or stored only means that an admin has to switch the relay back on.
func (s *Server) rememberModeBeforeStop(ctx context.Context) {
	prev, err := s.Store.Setting(ctx, store.SettingMode, compile.ModeActive)
	if err != nil {
		s.Log.Error("emergency stop: current mode not readable, only an admin can switch back on", "err", err)
		return
	}
	if prev == compile.ModePaused {
		return
	}
	if err := s.Store.SetSetting(ctx, settingModeBeforeStop, prev); err != nil {
		s.Log.Error("emergency stop: previous mode not stored, only an admin can switch back on", "err", err)
	}
}

func (s *Server) emergencyStop(w http.ResponseWriter, r *http.Request) {
	s.rememberModeBeforeStop(r.Context())
	if err := s.Store.SetMode(r.Context(), s.actor(r), compile.ModePaused); err != nil {
		back(w, r, "/", "", s.userError(r, "emergency stop: mode", err))
		return
	}
	s.audit(r.Context(), s.actor(r), "emergency_stop", "engine", "", "paused")
	s.Log.Warn("emergency stop", "user", s.actor(r))
	a := s.applyNow(r, "Not-Aus")
	if a.OK {
		a.Message = s.T(r)("Not-Aus: Relay pausiert. Wieder einschalten: Not-Aus-Seite, ab Operator.")
	}
	s.answer(w, r, "/", a)
}
