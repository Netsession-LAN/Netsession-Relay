package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/outputs"
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

// parseHeaders reads "Name: value" lines; the error is a German UI text (the caller translates it).
func parseHeaders(text string) (map[string]string, string) {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := parseHeaderLine(line)
		if !ok {
			return nil, "Header bitte als „Name: Wert“, einer pro Zeile."
		}
		out[k] = v
	}
	return out, ""
}

// parseHeaderLine splits one "Name: value" line; ok is false without a name, a value, or with blanks in the name.
func parseHeaderLine(line string) (name, value string, ok bool) {
	name, value, ok = strings.Cut(line, ":")
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if !ok || name == "" || value == "" || strings.ContainsAny(name, " \t") {
		return "", "", false
	}
	return name, value, true
}
