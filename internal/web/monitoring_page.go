package web

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/notify"
	"github.com/Netsession2024/Netsession-Relay/internal/otlp"
	"github.com/Netsession2024/Netsession-Relay/internal/outputs"
	"github.com/Netsession2024/Netsession-Relay/internal/snmp"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
	"github.com/Netsession2024/Netsession-Relay/internal/syslog"
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

func targetSummary(t store.Target, T translator) string {
	switch t.Kind {
	case store.TargetSyslog:
		f := "RFC 5424"
		if outputs.ConfigString(t.Config, "format", "rfc5424") == "rfc3164" {
			f = "RFC 3164"
		}
		return strings.ToUpper(outputs.ConfigString(t.Config, "transport", "udp")) + " · " + f + " · " + outputs.ConfigString(t.Config, "facility", "local0")
	case store.TargetSNMPTrap:
		v := outputs.ConfigString(t.Config, "version", "v3")
		if outputs.ConfigBool(t.Config, "inform") {
			v += " · Inform"
		}
		return v
	case store.TargetOTLP:
		var sig []string
		if !outputs.ConfigBool(t.Config, "no_metrics") {
			sig = append(sig, T("Metriken"))
		}
		if !outputs.ConfigBool(t.Config, "no_logs") {
			sig = append(sig, T("Meldungen"))
		}
		p := "gRPC"
		if outputs.ConfigString(t.Config, "protocol", "grpc") == "http" {
			p = "HTTP"
		}
		if outputs.ConfigString(t.Config, "tls", "none") == "tls" {
			p += " + TLS"
		}
		return p + " · " + strings.Join(sig, ", ")
	}
	return ""
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

// ---- target form ----

type targetForm struct {
	ID       int64
	Kind     string
	Name     string
	Enabled  bool
	Endpoint string
	Alarm    bool
	Audit    bool
	// syslog
	Transport, Format, Facility string
	// otlp
	Protocol          string
	TLS               bool
	TLSCA             string
	SkipVerify        bool
	Metrics, Logs     bool
	Interval          int
	RuleMetrics, Gzip bool
	HeaderNames       []string // stored header names (values never shown)
	// snmptrap
	Version, User, AuthProto, PrivProto string
	Inform                              bool
	SecretsSet                          []string // names of stored secrets (community, auth_pass, priv_pass)
	Headers                             string   // new "Name: Wert" lines
	ClearHeaders                        bool
	Facilities                          []string
	Err                                 string
}

func formFromTarget(t store.Target) targetForm {
	f := targetForm{ID: t.ID, Kind: t.Kind, Name: t.Name, Enabled: t.Enabled, Endpoint: t.Endpoint,
		Alarm: slices.Contains(t.Events, "alarm"), Audit: slices.Contains(t.Events, "audit"),
		Transport: outputs.ConfigString(t.Config, "transport", "udp"), Format: outputs.ConfigString(t.Config, "format", "rfc5424"),
		Facility: outputs.ConfigString(t.Config, "facility", "local0"), Protocol: outputs.ConfigString(t.Config, "protocol", "grpc"),
		TLS: outputs.ConfigString(t.Config, "tls", "none") == "tls", TLSCA: outputs.ConfigString(t.Config, "tls_ca", ""),
		SkipVerify: outputs.ConfigBool(t.Config, "tls_skip_verify"), Metrics: !outputs.ConfigBool(t.Config, "no_metrics"), Logs: !outputs.ConfigBool(t.Config, "no_logs"),
		Interval: 30, RuleMetrics: outputs.ConfigBool(t.Config, "rule_metrics"), Gzip: outputs.ConfigBool(t.Config, "gzip"), Facilities: syslog.Facilities}
	if v, ok := t.Config["interval_s"].(float64); ok && v > 0 {
		f.Interval = int(v)
	}
	f.Version, f.User = outputs.ConfigString(t.Config, "version", "v3"), outputs.ConfigString(t.Config, "user", "")
	f.AuthProto, f.PrivProto = outputs.ConfigString(t.Config, "auth_proto", "sha256"), outputs.ConfigString(t.Config, "priv_proto", "aes128")
	f.Inform = outputs.ConfigBool(t.Config, "inform")
	for _, k := range []string{"community", "auth_pass", "priv_pass"} {
		if t.Secret[k] != "" {
			f.SecretsSet = append(f.SecretsSet, k)
		}
	}
	if f.Transport == "tls" {
		f.TLS = true
	}
	for k := range t.Secret {
		if name, ok := strings.CutPrefix(k, "header:"); ok {
			f.HeaderNames = append(f.HeaderNames, name)
		}
	}
	slices.Sort(f.HeaderNames)
	return f
}

func (s *Server) targetNew(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("art")
	if kind != store.TargetOTLP && kind != store.TargetSNMPTrap {
		kind = store.TargetSyslog
	}
	t := store.Target{Kind: kind, Enabled: true, Events: []string{"alarm", "audit"}, Config: map[string]any{}}
	if kind == store.TargetSNMPTrap {
		t.Events = []string{"alarm"} // audit as trap is possible but noise
	}
	f := formFromTarget(t)
	if kind == store.TargetOTLP {
		f.Name, f.Endpoint = "", ""
	}
	s.render(w, r, "monitoring-target", "Ziel hinzufügen", "system", f)
}

func (s *Server) targetEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	t, err := s.Store.Target(r.Context(), id)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Dieses Ziel gibt es nicht.")
		return
	}
	s.render(w, r, "monitoring-target", s.T(r)("%s bearbeiten", t.Name), "system", formFromTarget(t))
}

func (s *Server) targetSave(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	f := readTargetForm(r)
	// an existing target keeps its kind; its stored secrets are shown as "set" and used where the form left a
	// secret empty
	var stored map[string]string
	if f.ID != 0 {
		if cur, err := s.Store.Target(r.Context(), f.ID); err == nil {
			cf := formFromTarget(cur)
			f.HeaderNames, f.SecretsSet = cf.HeaderNames, cf.SecretsSet
			f.Kind = cur.Kind
			stored = cur.Secret
		}
	}
	title := "Ziel hinzufügen"
	if f.ID != 0 {
		title = T("%s bearbeiten", f.Name)
	}
	reject := func(msg string) {
		f.Err = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "monitoring-target", title, "system", f)
	}
	if f.Name == "" {
		reject(T("Bitte einen Namen eingeben."))
		return
	}
	t := store.Target{ID: f.ID, Kind: f.Kind, Name: f.Name, Enabled: f.Enabled, Endpoint: f.Endpoint}
	if f.Alarm {
		t.Events = append(t.Events, "alarm")
	}
	if f.Audit {
		t.Events = append(t.Events, "audit")
	}
	var msg string
	switch f.Kind {
	case store.TargetSyslog:
		t.Config, t.Secret, msg = syslogTargetOptions(f, T)
	case store.TargetOTLP:
		t.Config, t.Secret, msg = otlpTargetOptions(&f, T)
	case store.TargetSNMPTrap:
		secrets := map[string]string{"community": r.PostFormValue("community"), "auth_pass": r.PostFormValue("auth_pass"), "priv_pass": r.PostFormValue("priv_pass")}
		t.Config, t.Secret, msg = trapTargetOptions(f, secrets, stored, T)
	default:
		msg = T("Diese Art gibt es nicht.")
	}
	if msg != "" {
		reject(msg)
		return
	}
	if f.ClearHeaders {
		// SaveTarget merges secrets; to drop old headers the stored ones are replaced explicitly
		for k := range stored {
			if _, keep := t.Secret[k]; strings.HasPrefix(k, "header:") && !keep {
				t.Secret[k] = store.SecretDelete
			}
		}
	}
	if _, err := s.Store.SaveTarget(r.Context(), s.actor(r), t); err != nil {
		reject(s.targetStoreError(r, err))
		return
	}
	s.Outputs.Reload(r.Context())
	back(w, r, "/system/monitoring", T("%s gespeichert. Schick eine Testmeldung.", t.Name), nil)
}

// readTargetForm reads the fields of the target form as they were sent (secrets are read where they are used).
func readTargetForm(r *http.Request) targetForm {
	v := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	on := func(k string) bool { return r.PostFormValue(k) == "1" }
	id, _ := strconv.ParseInt(v("id"), 10, 64)
	f := targetForm{ID: id, Kind: v("kind"), Name: v("name"), Enabled: on("enabled"), Endpoint: v("endpoint"),
		Alarm: on("alarm"), Audit: on("audit"), Transport: v("transport"), Format: v("format"), Facility: v("facility"),
		Protocol: v("protocol"), TLS: on("tls"), TLSCA: v("tls_ca"), SkipVerify: on("tls_skip_verify"),
		Metrics: on("metrics"), Logs: on("logs"), RuleMetrics: on("rule_metrics"), Gzip: on("gzip"),
		Headers: r.PostFormValue("headers"), ClearHeaders: on("clear_headers"), Facilities: syslog.Facilities,
		Version: v("version"), User: v("user"), AuthProto: v("auth_proto"), PrivProto: v("priv_proto"), Inform: on("inform")}
	f.Interval, _ = strconv.Atoi(v("interval_s"))
	return f
}

// syslogTargetOptions, otlpTargetOptions and trapTargetOptions check the fields of one kind of target and
// return what is stored: the options, the secrets and "" - or what is wrong, in the language of T.
func syslogTargetOptions(f targetForm, T translator) (map[string]any, map[string]string, string) {
	c := syslog.Config{Transport: f.Transport, Format: f.Format, Facility: f.Facility, TLSCA: f.TLSCA, TLSSkipVerify: f.SkipVerify}
	if err := syslog.Validate(f.Endpoint, c); err != nil {
		return nil, nil, targetError(err, T)
	}
	config := map[string]any{"transport": f.Transport, "format": f.Format, "facility": f.Facility}
	if f.Transport == "tls" {
		config["tls_ca"], config["tls_skip_verify"] = f.TLSCA, f.SkipVerify
	}
	return config, map[string]string{}, ""
}

// otlpTargetOptions also puts the interval into its bounds in the form, so a rejected form shows what would be
// stored.
func otlpTargetOptions(f *targetForm, T translator) (map[string]any, map[string]string, string) {
	if f.Interval < 10 || f.Interval > 300 {
		f.Interval = 30
	}
	headers, msg := parseHeaders(f.Headers)
	if msg != "" {
		return nil, nil, T(msg)
	}
	c := otlp.Config{Protocol: f.Protocol, TLS: f.TLS, TLSCA: f.TLSCA, SkipVerify: f.SkipVerify, Metrics: f.Metrics, Logs: f.Logs, Headers: headers}
	if err := otlp.Validate(f.Endpoint, c); err != nil {
		return nil, nil, targetError(err, T)
	}
	tls := "none"
	if f.TLS {
		tls = "tls"
	}
	config := map[string]any{"protocol": f.Protocol, "tls": tls, "no_metrics": !f.Metrics, "no_logs": !f.Logs,
		"interval_s": f.Interval, "rule_metrics": f.RuleMetrics, "gzip": f.Gzip}
	if f.TLS {
		config["tls_ca"], config["tls_skip_verify"] = f.TLSCA, f.SkipVerify
	}
	secret := map[string]string{}
	for k, val := range headers {
		secret["header:"+k] = val
	}
	return config, secret, ""
}

// trapTargetOptions validates with the secrets that will be in effect: the new value, or the stored one where
// the form left the field empty.
func trapTargetOptions(f targetForm, secrets, stored map[string]string, T translator) (map[string]any, map[string]string, string) {
	effective := func(k string) string { return cmp.Or(secrets[k], stored[k]) }
	c := snmp.TrapConfig{Version: f.Version, Inform: f.Inform, User: f.User, AuthProto: f.AuthProto, PrivProto: f.PrivProto,
		Community: effective("community"), AuthPass: effective("auth_pass"), PrivPass: effective("priv_pass")}
	if err := snmp.ValidateTrap(f.Endpoint, c); err != nil {
		return nil, nil, targetError(err, T)
	}
	config := map[string]any{"version": f.Version, "inform": f.Inform}
	if f.Version == "v3" {
		config["user"], config["auth_proto"], config["priv_proto"] = f.User, f.AuthProto, f.PrivProto
	}
	return config, secrets, ""
}

// parseHeaders reads "Name: value" lines; the error is a German UI text (the caller translates it).
func parseHeaders(text string) (map[string]string, string) {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" || strings.ContainsAny(k, " \t") {
			return nil, "Header bitte als „Name: Wert“, einer pro Zeile."
		}
		out[k] = v
	}
	return out, ""
}

// targetError words a validation error of a target in the language of T. The problems without an identity of
// their own (an unknown transport, a bad header name) are described by their text.
func targetError(err error, T translator) string {
	is := func(targets ...error) bool {
		return slices.ContainsFunc(targets, func(t error) bool { return errors.Is(err, t) })
	}
	switch {
	case is(syslog.ErrEndpoint, otlp.ErrEndpoint, snmp.ErrEndpoint):
		return T("Adresse bitte als Adresse:Port, z. B. 10.0.0.5:514.")
	case is(otlp.ErrEndpointURL):
		return T("Adresse bitte als URL, z. B. http://10.0.0.5:4318.")
	case is(otlp.ErrNoSignal):
		return T("Bitte Metriken, Meldungen oder beides wählen.")
	case is(snmp.ErrCommunityMissing):
		return T("Bitte die Community eintragen.")
	case is(snmp.ErrUserMissing):
		return T("Bitte den SNMPv3-Benutzer eintragen.")
	case is(snmp.ErrPasswordShort):
		return T("Die SNMPv3-Passwörter brauchen mindestens 8 Zeichen.")
	case is(syslog.ErrTLSCA, otlp.ErrTLSCA):
		return T("Im CA-Feld steht kein Zertifikat (PEM).")
	}
	return T("Das passt nicht: %s", err.Error())
}

// targetStoreError words why a target could not be stored; anything unexpected is an internal error.
func (s *Server) targetStoreError(r *http.Request, err error) string {
	T := s.T(r)
	switch {
	case errors.Is(err, store.ErrExists):
		return T("Ein Ziel mit diesem Namen gibt es schon.")
	case errors.Is(err, store.ErrTooManyTargets):
		return T("Höchstens %d Ziele.", store.MaxTargets)
	}
	return string(s.userError(r, "monitoring target: store", err))
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
	if (t.Kind == store.TargetSyslog && outputs.ConfigString(t.Config, "transport", "udp") == "udp") || (t.Kind == store.TargetSNMPTrap && !outputs.ConfigBool(t.Config, "inform")) {
		msg += " " + T("Ohne Bestätigung: Kommt sie dort an?")
	}
	back(w, r, "/system/monitoring", msg, nil)
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
