package web

import (
	"cmp"
	"net/http"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/otlp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/syslog"
)

// The options of a push target per kind, checked from its form: syslog, OpenTelemetry or SNMP trap.

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
	return otlpTargetConfig(*f), otlpHeaderSecrets(headers), ""
}

// otlpHeaderSecrets stores the headers of an OTLP target as secrets ("header:Name").
func otlpHeaderSecrets(headers map[string]string) map[string]string {
	secret := map[string]string{}
	for k, val := range headers {
		secret["header:"+k] = val
	}
	return secret
}

// otlpTargetConfig is the stored options of a checked OTLP target (the headers are secrets).
func otlpTargetConfig(f targetForm) map[string]any {
	tls := "none"
	if f.TLS {
		tls = "tls"
	}
	config := map[string]any{"protocol": f.Protocol, "tls": tls, "no_metrics": !f.Metrics, "no_logs": !f.Logs,
		"interval_s": f.Interval, "rule_metrics": f.RuleMetrics, "gzip": f.Gzip}
	if f.TLS {
		config["tls_ca"], config["tls_skip_verify"] = f.TLSCA, f.SkipVerify
	}
	return config
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
