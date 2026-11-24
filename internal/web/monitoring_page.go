package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
	"github.com/Netsession-LAN/Netsession-Relay/internal/outputs"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Page System → Monitoring (admin): the token for Zabbix/Prometheus, the host name, and the push targets.

// targetRow is one target in the list.
type targetRow struct {
	store.Target
	Status   notify.Status
	KindName string
	Summary  string // "UDP · RFC 5424" / "gRPC · Metriken, Meldungen"
	What     string // "Alarme, Protokoll"
	LastOK   string
	LastErr  string
}

func targetKindName(k string) string {
	switch k {
	case store.TargetSyslog:
		return "Syslog"
	case store.TargetOTLP:
		return "OpenTelemetry"
	case store.TargetSNMPTrap:
		return "SNMP-Trap"
	}
	return k
}

// targetSummary is the short description of how a target is reached ("UDP · RFC 5424 · local0").
func targetSummary(t store.Target, T translator) string {
	switch t.Kind {
	case store.TargetSyslog:
		return syslogSummary(t.Config)
	case store.TargetSNMPTrap:
		return trapSummary(t.Config)
	case store.TargetOTLP:
		return otlpSummary(t.Config, T)
	}
	return ""
}

func syslogSummary(c map[string]any) string {
	f := "RFC 5424"
	if outputs.ConfigString(c, "format", "rfc5424") == "rfc3164" {
		f = "RFC 3164"
	}
	return strings.ToUpper(outputs.ConfigString(c, "transport", "udp")) + " · " + f + " · " + outputs.ConfigString(c, "facility", "local0")
}

func trapSummary(c map[string]any) string {
	v := outputs.ConfigString(c, "version", "v3")
	if outputs.ConfigBool(c, "inform") {
		v += " · Inform"
	}
	return v
}

// otlpSummary names the protocol and the signals sent ("gRPC + TLS · Metriken, Meldungen").
func otlpSummary(c map[string]any, T translator) string {
	var sig []string
	if !outputs.ConfigBool(c, "no_metrics") {
		sig = append(sig, T("Metriken"))
	}
	if !outputs.ConfigBool(c, "no_logs") {
		sig = append(sig, T("Meldungen"))
	}
	p := "gRPC"
	if outputs.ConfigString(c, "protocol", "grpc") == "http" {
		p = "HTTP"
	}
	if outputs.ConfigString(c, "tls", "none") == "tls" {
		p += " + TLS"
	}
	return p + " · " + strings.Join(sig, ", ")
}

func eventsName(ev []string, T translator) string {
	var out []string
	if slices.Contains(ev, "alarm") {
		out = append(out, T("Alarme"))
	}
	if slices.Contains(ev, "audit") {
		out = append(out, T("Verlauf"))
	}
	if len(out) == 0 {
		return T("nur Tests")
	}
	return strings.Join(out, ", ")
}

func agoText(t time.Time, T translator) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 5*time.Second:
		return T("gerade")
	case d < 90*time.Second:
		return T("vor %d s", int(d.Seconds()))
	case d < 90*time.Minute:
		return T("vor %d min", int(d.Minutes()))
	case d < 36*time.Hour:
		return T("vor %d h", int(d.Hours()))
	}
	return t.Local().Format("02.01. 15:04")
}

func (s *Server) monitoringData(r *http.Request) map[string]any {
	ctx := r.Context()
	T := s.T(r)
	tok, _ := s.Store.Setting(ctx, settingMonitorToken, "") // settings fall back to their default
	ts, err := s.Store.Targets(ctx)
	s.degraded("monitoring: targets", err)
	status := map[int64]notify.Status{}
	for _, st := range s.Outputs.Status() {
		status[st.ID] = st
	}
	var rows []targetRow
	failing := 0
	for _, t := range ts {
		st := status[t.ID]
		row := targetRow{Target: t, Status: st, KindName: T(targetKindName(t.Kind)), Summary: targetSummary(t, T), What: eventsName(t.Events, T),
			LastOK: agoText(st.LastOK, T)}
		if st.LastErr != "" {
			row.LastErr = st.LastErr
		}
		if st.Failing() {
			failing++
		}
		rows = append(rows, row)
	}
	host, _ := s.Store.Setting(ctx, outputs.SettingHostName, "")
	return map[string]any{"MonitorSet": tok != "", "Targets": rows, "Failing": failing, "Host": host,
		"HostDefault": s.Outputs.HostName(ctx), "Max": store.MaxTargets, "Agent": s.agentForm(ctx, T)}
}

func (s *Server) monitoringPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "monitoring", "Monitoring", "system", s.monitoringData(r))
}

func (s *Server) monitoringHost(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	h := strings.TrimSpace(r.PostFormValue("host"))
	if len(h) > 64 || strings.ContainsAny(h, " \t\r\n") {
		back(w, r, "/system/monitoring", "", errors.New(T("Der Name darf keine Leerzeichen enthalten (höchstens 64 Zeichen).")))
		return
	}
	old, _ := s.Store.Setting(r.Context(), outputs.SettingHostName, "")
	if err := s.Store.SetSetting(r.Context(), outputs.SettingHostName, h); err != nil {
		back(w, r, "/system/monitoring", "", s.internal(r, "monitoring: store the host name", err))
		return
	}
	s.audit(r.Context(), s.actor(r), "monitor.host", "", old, h)
	s.Outputs.Restart(r.Context())
	msg := T("Name gespeichert.")
	if h == "" {
		msg = T("Name zurückgesetzt, es gilt der Hostname der VM.")
	}
	back(w, r, "/system/monitoring", msg, nil)
}

func (s *Server) targetTest(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	t, err := s.Store.Target(r.Context(), id)
	if err != nil {
		back(w, r, "/system/monitoring", "", errors.New(T("Dieses Ziel gibt es nicht.")))
		return
	}
	if !t.Enabled {
		back(w, r, "/system/monitoring", "", errors.New(T("%s ist ausgeschaltet.", t.Name)))
		return
	}
	ev := notify.Event{Type: notify.TypeTest, Severity: notify.Info, User: s.actor(r), Text: s.outT()("Testmeldung von %s", s.actor(r))}
	if err := s.Outputs.Test(r.Context(), id, ev); err != nil {
		back(w, r, "/system/monitoring", "", errors.New(T("%s: Senden fehlgeschlagen: %v", t.Name, err)))
		return
	}
	s.audit(r.Context(), s.actor(r), "monitor.target.test", t.Name, "", "")
	msg := T("Testmeldung an %s gesendet.", t.Name)
	if unconfirmedDelivery(t) {
		msg += " " + T("Ohne Bestätigung: Kommt sie dort an?")
	}
	back(w, r, "/system/monitoring", msg, nil)
}

// unconfirmedDelivery reports whether a target receives messages without confirming them (syslog over UDP, SNMP
// traps that are no informs), so a sent test says nothing about its arrival.
func unconfirmedDelivery(t store.Target) bool {
	switch t.Kind {
	case store.TargetSyslog:
		return outputs.ConfigString(t.Config, "transport", "udp") == "udp"
	case store.TargetSNMPTrap:
		return !outputs.ConfigBool(t.Config, "inform")
	}
	return false
}

func (s *Server) targetDelete(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	t, err := s.Store.Target(r.Context(), id)
	if err != nil {
		back(w, r, "/system/monitoring", "", errors.New(T("Dieses Ziel gibt es nicht.")))
		return
	}
	if err := s.Store.DeleteTarget(r.Context(), s.actor(r), id); err != nil {
		back(w, r, "/system/monitoring", "", s.userError(r, "monitoring target: delete", err))
		return
	}
	s.Outputs.Reload(r.Context())
	back(w, r, "/system/monitoring", T("%s gelöscht.", t.Name), nil)
}

// mibFile serves NETSESSION-RELAY-MIB for SNMP managers.
func (s *Server) mibFile(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="NETSESSION-RELAY-MIB.txt"`)
	_, _ = w.Write([]byte(snmp.MIB()))
}
