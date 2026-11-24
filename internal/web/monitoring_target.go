package web

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/otlp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/outputs"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/syslog"
)

// The form of a push target (System → Monitoring): syslog, OpenTelemetry or SNMP trap.

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
	f.SecretsSet = storedTrapSecrets(t.Secret)
	if f.Transport == "tls" {
		f.TLS = true
	}
	f.HeaderNames = storedHeaderNames(t.Secret)
	return f
}

// storedTrapSecrets names the SNMP secrets that are set (community, auth_pass, priv_pass).
func storedTrapSecrets(secret map[string]string) []string {
	var set []string
	for _, k := range []string{"community", "auth_pass", "priv_pass"} {
		if secret[k] != "" {
			set = append(set, k)
		}
	}
	return set
}

// storedHeaderNames lists the names of the stored OTLP headers, sorted (values are never shown).
func storedHeaderNames(secret map[string]string) []string {
	var names []string
	for k := range secret {
		if name, ok := strings.CutPrefix(k, "header:"); ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
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
	stored := s.adoptStoredTarget(r, &f)
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
	t := store.Target{ID: f.ID, Kind: f.Kind, Name: f.Name, Enabled: f.Enabled, Endpoint: f.Endpoint, Events: f.events()}
	var msg string
	t.Config, t.Secret, msg = targetOptions(r, &f, stored, T)
	if msg != "" {
		reject(msg)
		return
	}
	if f.ClearHeaders {
		dropStoredHeaders(t.Secret, stored)
	}
	if _, err := s.Store.SaveTarget(r.Context(), s.actor(r), t); err != nil {
		reject(s.targetStoreError(r, err))
		return
	}
	s.Outputs.Reload(r.Context())
	back(w, r, "/system/monitoring", T("%s gespeichert. Schick eine Testmeldung.", t.Name), nil)
}

// adoptStoredTarget takes over what an existing target keeps: its kind, and the names of its stored secrets
// (shown as "set"; they are used where the form left a secret empty). It returns the stored secrets, nil for a
// new target.
func (s *Server) adoptStoredTarget(r *http.Request, f *targetForm) map[string]string {
	if f.ID == 0 {
		return nil
	}
	cur, err := s.Store.Target(r.Context(), f.ID)
	if err != nil {
		return nil
	}
	cf := formFromTarget(cur)
	f.HeaderNames, f.SecretsSet = cf.HeaderNames, cf.SecretsSet
	f.Kind = cur.Kind
	return cur.Secret
}

// events lists the kinds of messages the target gets ("alarm", "audit"; nil = only tests).
func (f targetForm) events() []string {
	var ev []string
	if f.Alarm {
		ev = append(ev, "alarm")
	}
	if f.Audit {
		ev = append(ev, "audit")
	}
	return ev
}

// targetOptions checks the fields of the kind of target and returns the options, the secrets and "" - or what
// is wrong, in the language of T. The SNMP secrets come from the request; stored fills in those left empty.
func targetOptions(r *http.Request, f *targetForm, stored map[string]string, T translator) (map[string]any, map[string]string, string) {
	switch f.Kind {
	case store.TargetSyslog:
		return syslogTargetOptions(*f, T)
	case store.TargetOTLP:
		return otlpTargetOptions(f, T)
	case store.TargetSNMPTrap:
		secrets := map[string]string{"community": r.PostFormValue("community"), "auth_pass": r.PostFormValue("auth_pass"), "priv_pass": r.PostFormValue("priv_pass")}
		return trapTargetOptions(*f, secrets, stored, T)
	}
	return nil, nil, T("Diese Art gibt es nicht.")
}

// dropStoredHeaders marks the stored headers the new secrets do not set again for deletion: SaveTarget merges
// secrets, so old headers are only dropped when they are replaced explicitly.
func dropStoredHeaders(secret, stored map[string]string) {
	for k := range stored {
		if _, keep := secret[k]; strings.HasPrefix(k, "header:") && !keep {
			secret[k] = store.SecretDelete
		}
	}
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
