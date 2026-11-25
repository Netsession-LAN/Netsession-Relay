package web

import (
	"slices"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
	"github.com/Netsession-LAN/Netsession-Relay/internal/outputs"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The summaries of the push targets on the monitoring page: kind, options, events and the age of the last
// delivery.

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
