package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// ---- system, history ----

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "system", "System", "system", s.systemData(r))
}

// systemData is the view model of the system page (also used when a new monitoring token is shown).
func (s *Server) systemData(r *http.Request) map[string]any {
	// settings: Store.Setting returns the default when it cannot read, the page still renders
	mode, _ := s.Store.Setting(r.Context(), store.SettingMode, compile.ModeActive)
	tok, _ := s.Store.Setting(r.Context(), settingMonitorToken, "")
	probe, _ := s.Store.Setting(r.Context(), store.SettingProbeServers, "1")
	services, err := s.Store.Services(r.Context())
	s.degraded("system: service hosts", err)
	total, failing, _ := s.Outputs.Stats()
	var lastBackup time.Time
	if s.BackupDir != "" {
		lastBackup = control.LastBackup(s.BackupDir)
	}
	return map[string]any{"BackupAuto": s.BackupDir != "", "BackupKeep": s.BackupKeep, "BackupLast": lastBackup, "Mode": mode, "MonitorSet": tok != "", "Targets": total, "TargetsFailing": failing,
		"Services": services, "ServiceIP": r.URL.Query().Get("dienst"),
		"Resume": s.resumeMode(r), "ResumeName": modeName(s.resumeMode(r)), "Probe": probe != "0"}
}

// auditView is one line of the log; Text and Object are in the language of the reader.
type auditView struct {
	TS     string
	User   string
	Text   string
	Object string
	Mono   bool // object is technical (IP, id)
	Tone   string
}

var auditTexts = map[string]string{ //nolint:gosec // G101: a setting or message name, not a credential
	"login":                 "Angemeldet",
	"probe.on":              "Server-Abfrage eingeschaltet",
	"probe.off":             "Server-Abfrage ausgeschaltet",
	"login.denied":          "Anmeldung ohne Relay-Gruppe abgelehnt",
	"login.failed":          "Anmeldung fehlgeschlagen",
	"login.locked":          "Konto gesperrt (zu viele Fehlversuche)", // only in logs of old versions: accounts are no longer locked
	"game.enable":           "Spiel eingeschaltet",
	"game.disable":          "Spiel ausgeschaltet",
	"vlan.enable":           "Netz eingeschaltet",
	"vlan.disable":          "Netz ausgeschaltet",
	"vlan.create":           "Netz angelegt",
	"vlan.update":           "Netz geändert",
	"vlan.delete":           "Netz gelöscht",
	"mode.set":              "Betriebsart geändert",
	"apply":                 "Stand angewendet",
	"apply.rejected":        "Stand von der Engine abgelehnt",
	"rollback":              "Zurückgerollt",
	"config.import":         "Konfiguration importiert",
	"user.set_password":     "Passwort gesetzt",
	"user.setup":            "Erstes Admin-Konto angelegt",
	"user.logout":           "Überall abgemeldet",
	"emergency_stop":        "Not-Aus ausgelöst",
	"emergency_release":     "Not-Aus aufgehoben",
	"backup.download":       "Backup heruntergeladen",
	"monitor.token":         "Monitoring-Token neu erzeugt",
	"alarm.ack":             "Alarm als gesehen markiert",
	"finder.start":          "Port-Finder gestartet",
	"finder.pcap":           "Mitschnitt heruntergeladen",
	"game.create":           "Spiel angelegt",
	"game.update":           "Spiel geändert",
	"game.delete":           "Spiel gelöscht",
	"game.profile":          "Profil eines Spiels gewählt",
	"game.sources":          "Absender-Filter eines Spiels geändert",
	"profile.edges":         "Profil geändert",
	"profile.create":        "Profil angelegt",
	"profile.delete":        "Profil gelöscht",
	"profile.default":       "Standardprofil gesetzt",
	"profile.rename":        "Profil umbenannt",
	"game.verify":           "Spiel geprüft",
	"game.verify_delete":    "Prüfung gelöscht",
	"config.export":         "Konfiguration exportiert",
	"syslog.set":            "Syslog-Ziel geändert", // only in logs of old versions: written by a form that is gone
	"monitor.target.create": "Monitoring-Ziel angelegt",
	"monitor.target.update": "Monitoring-Ziel geändert",
	"monitor.target.delete": "Monitoring-Ziel gelöscht",
	"monitor.target.test":   "Testmeldung gesendet",
	"snmp.agent.set":        "SNMP-Agent geändert",
	"monitor.host":          "Name für das Monitoring geändert",
	"service.set":           "Als Dienst markiert",
	"service.delete":        "Dienst-Markierung entfernt",
	"event.logo.set":        "Logo der LAN gesetzt",
	"event.logo.remove":     "Logo der LAN entfernt",
}

// auditMono marks the actions whose object is technical (user name, game ID, VLAN ID) and shown as such.
var auditMono = map[string]bool{
	"login": true, "login.failed": true, "login.locked": true, "game.enable": true, "game.disable": true,
	"backup.download": true, "game.sources": true,
	"vlan.enable": true, "vlan.disable": true, "vlan.create": true, "vlan.update": true, "vlan.delete": true,
}

// auditTones colours the actions an operator should notice.
var auditTones = map[string]string{
	"emergency_stop": "danger", "apply.rejected": "danger", "login.locked": "danger",
	"login.failed": "warn", "rollback": "warn",
}

// auditObjects words the object of the actions that store more than a plain name; any other action shows its
// object as stored.
var auditObjects = map[string]func(a store.AuditEntry, T translator) string{
	"game.sources": func(a store.AuditEntry, T translator) string { return a.Object + ": " + cmp.Or(a.After, T("alle")) },
	"vlan.enable":  auditVLAN,
	"vlan.disable": auditVLAN,
	"vlan.create":  auditVLAN,
	"vlan.update":  auditVLAN,
	"vlan.delete":  auditVLAN,
	"mode.set": func(a store.AuditEntry, T translator) string {
		return T(modeName(a.Before)) + " → " + T(modeName(a.After))
	},
	"emergency_stop":    func(_ store.AuditEntry, T translator) string { return T("Relay pausiert") },
	"emergency_release": func(a store.AuditEntry, T translator) string { return T("zurück auf %s", T(modeName(a.After))) },
	"apply":             auditRevision,
	"apply.rejected":    auditRevision,
	"rollback":          auditRevision,
	"user.set_password": func(a store.AuditEntry, T translator) string { return T("Konto %s", a.Object) },
}

func auditVLAN(a store.AuditEntry, _ translator) string { return "VLAN " + a.Object }

func auditRevision(a store.AuditEntry, _ translator) string {
	return strings.Replace(a.Object, "revision", "Revision", 1)
}

// auditOf puts a stored audit entry into words. The stored data stays as it is, T translates the words.
func auditOf(a store.AuditEntry, T translator) auditView {
	v := auditView{TS: a.TS, User: a.User, Text: cmp.Or(T(auditTexts[a.Action]), a.Action), Object: a.Object,
		Mono: auditMono[a.Action], Tone: auditTones[a.Action]}
	if object := auditObjects[a.Action]; object != nil {
		v.Object = object(a, T)
	}
	return v
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	revs, err := s.Store.Revisions(ctx, 30)
	s.degraded("history: revisions", err)
	withLogins := r.URL.Query().Get("anmeldungen") == "1"
	raw, err := s.Store.AuditLog(ctx, 400)
	s.degraded("history: audit log", err)
	var audit []auditView
	hidden := 0
	T := s.T(r)
	for _, a := range raw {
		if !withLogins && a.Action == "login" {
			hidden++
			continue
		}
		if len(audit) < 100 {
			audit = append(audit, auditOf(a, T))
		}
	}
	s.render(w, r, "history", "Verlauf", "history", map[string]any{"Revisions": revs, "Audit": audit, "Logins": withLogins, "Hidden": hidden})
}

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

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		back(w, r, "/verlauf", "", errors.New(s.T(r)("Revision ungültig.")))
		return
	}
	res, err := control.Rollback(r.Context(), s.Store, s.Engine, s.actor(r), id)
	s.dropRuleIndex()
	if err != nil {
		back(w, r, "/verlauf", "", s.applyError(r, res, err))
		return
	}
	back(w, r, "/verlauf", s.T(r)("Auf Revision %d zurückgerollt (neu: %d).", id, res.Revision), nil)
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	// the temporary copy goes next to the backups or the database (volumes); the root file system of the
	// container is read-only, /tmp is only the last resort
	path, cleanup, err := control.BackupTemp(r.Context(), s.Store, s.ScratchDirs...)
	if err != nil {
		s.failInternal(w, r, "backup download", err)
		return
	}
	defer cleanup()
	s.audit(r.Context(), s.actor(r), "backup.download", filepath.Base(path), "", "")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	http.ServeFile(w, r, path)
}
