package outputs

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/otlp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/syslog"
)

// The sender configuration of a target: the options stored with it turned into the config of its syslog, OTLP or
// SNMP trap sender.

// ConfigString reads a string option of a target; a missing or empty one gives def.
func ConfigString(m map[string]any, k, def string) string {
	if v, ok := m[k].(string); ok && v != "" {
		return v
	}
	return def
}

// ConfigBool reads a boolean option of a target; a missing one is false.
func ConfigBool(m map[string]any, k string) bool {
	v, _ := m[k].(bool)
	return v
}

func (s *Service) trapConfig(ctx context.Context, t store.Target) snmp.TrapConfig {
	eid, boots := s.snmpEngine(ctx)
	return snmp.TrapConfig{Version: ConfigString(t.Config, "version", "v3"), Inform: ConfigBool(t.Config, "inform"),
		Community: t.Secret["community"], User: ConfigString(t.Config, "user", ""), AuthProto: ConfigString(t.Config, "auth_proto", "sha256"),
		AuthPass: t.Secret["auth_pass"], PrivProto: ConfigString(t.Config, "priv_proto", "aes128"), PrivPass: t.Secret["priv_pass"],
		EngineID: eid, Boots: boots, Bind: s.BindAddr()}
}

func (s *Service) otlpResource(ctx context.Context) otlp.Resource {
	return otlp.Resource{ServiceName: "nsrelay", Version: s.version, InstanceID: s.instanceID(ctx), HostName: s.HostName(ctx)}
}

func otlpConfig(t store.Target, res otlp.Resource, bind netip.Addr) otlp.Config {
	c := otlp.Config{Protocol: ConfigString(t.Config, "protocol", "grpc"), TLS: ConfigString(t.Config, "tls", "none") == "tls",
		TLSCA: ConfigString(t.Config, "tls_ca", ""), SkipVerify: ConfigBool(t.Config, "tls_skip_verify"),
		Metrics: !ConfigBool(t.Config, "no_metrics"), Logs: !ConfigBool(t.Config, "no_logs"),
		RuleMetrics: ConfigBool(t.Config, "rule_metrics"), Gzip: ConfigBool(t.Config, "gzip"), Resource: res, Bind: bind,
		Interval: 30 * time.Second, Headers: map[string]string{}}
	if v, ok := t.Config["interval_s"].(float64); ok && v > 0 {
		c.Interval = time.Duration(v) * time.Second
	}
	addOTLPHeaders(c.Headers, t.Secret)
	return c
}

// addOTLPHeaders copies the headers stored as secrets ("header:<name>") into headers; empty values are left out.
func addOTLPHeaders(headers, secret map[string]string) {
	for k, v := range secret {
		if name, ok := strings.CutPrefix(k, "header:"); ok && v != "" {
			headers[name] = v
		}
	}
}

func syslogConfig(t store.Target, host string, bind netip.Addr) syslog.Config {
	return syslog.Config{Transport: ConfigString(t.Config, "transport", "udp"), Format: ConfigString(t.Config, "format", "rfc5424"),
		Facility: ConfigString(t.Config, "facility", "local0"), TLSCA: ConfigString(t.Config, "tls_ca", ""),
		TLSSkipVerify: ConfigBool(t.Config, "tls_skip_verify"), Host: host, Bind: bind}
}
