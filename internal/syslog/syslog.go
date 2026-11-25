// Package syslog sends relay events to syslog servers: UDP, TCP or TLS, RFC 5424 (with structured data) or
// RFC 3164. One Sender per target.
package syslog

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
)

// Severities (RFC 5424 numbers).
const (
	Warning = 4
	Notice  = 5
	Info    = 6
)

var facilities = map[string]int{"kern": 0, "user": 1, "daemon": 3, "auth": 4, "local0": 16, "local1": 17, "local2": 18,
	"local3": 19, "local4": 20, "local5": 21, "local6": 22, "local7": 23}

// Facilities lists the selectable facility names in UI order.
var Facilities = []string{"local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7", "daemon", "user", "auth"}

// Config of one syslog target.
type Config struct {
	Transport     string // udp | tcp | tls
	Format        string // rfc5424 | rfc3164
	Facility      string // local0 ...
	TLSCA         string // PEM, optional
	TLSSkipVerify bool
	Host          string     // HOSTNAME field; "" = os.Hostname()
	Bind          netip.Addr // source address (MGMT); invalid = any
}

// Validation errors a caller can tell apart with errors.Is.
var (
	ErrEndpoint = errors.New("want host:port")
	ErrTLSCA    = errors.New("TLS CA: no certificate in PEM")
)

// Validate checks the endpoint and the config.
func Validate(endpoint string, c Config) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("syslog target %q: %w", endpoint, ErrEndpoint)
	}
	return c.validate()
}

// validate checks transport, format, facility and the TLS CA.
func (c Config) validate() error {
	_, knownFacility := facilities[c.Facility]
	switch {
	case !slices.Contains([]string{"udp", "tcp", "tls"}, c.Transport):
		return fmt.Errorf("transport %q", c.Transport)
	case !slices.Contains([]string{"rfc5424", "rfc3164"}, c.Format):
		return fmt.Errorf("format %q", c.Format)
	case !knownFacility:
		return fmt.Errorf("facility %q", c.Facility)
	case !validCA(c.TLSCA):
		return ErrTLSCA
	}
	return nil
}

// validCA reports whether pem is empty (system roots) or holds at least one certificate.
func validCA(pem string) bool {
	return pem == "" || x509.NewCertPool().AppendCertsFromPEM([]byte(pem))
}

func severity(s notify.Severity) int {
	switch s {
	case notify.Warn:
		return Warning
	case notify.Notice:
		return Notice
	}
	return Info
}

// sdEscape escapes an SD-PARAM value (RFC 5424 6.3.3).
var sdEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`)

// Format5424 renders one RFC 5424 message with structured data [nsrelay@PEN ...].
func Format5424(e notify.Event, host, facility string) string {
	if host == "" {
		host = "-"
	}
	params := []string{"msgid=\"" + e.MsgID() + "\""}
	add := func(k, v string) {
		if v != "" {
			params = append(params, k+"=\""+sdEscape.Replace(v)+"\"")
		}
	}
	add("state", e.State)
	add("kind", e.Kind)
	add("key", e.Key)
	if e.VLAN > 0 {
		add("vlan", fmt.Sprint(e.VLAN))
	}
	add("action", e.Action)
	add("user", e.User)
	add("object", e.Object)
	sd := fmt.Sprintf("[nsrelay@%d %s]", notify.PEN, strings.Join(params, " "))
	msg := strings.ReplaceAll(e.Text, "\n", " ")
	return fmt.Sprintf("<%d>1 %s %s nsrelay %d %s %s %s", facilities[facility]*8+severity(e.Severity),
		e.Time.UTC().Format(time.RFC3339Nano), host, os.Getpid(), e.MsgID(), sd, msg)
}

// Format3164 renders one BSD-style message (local time, no structured data), at most 1024 bytes.
func Format3164(e notify.Event, host, facility string) string {
	if host == "" {
		host = "-"
	}
	msg := fmt.Sprintf("<%d>%s %s nsrelay[%d]: %s: %s", facilities[facility]*8+severity(e.Severity),
		e.Time.Local().Format(time.Stamp), host, os.Getpid(), e.MsgID(), strings.ReplaceAll(e.Text, "\n", " "))
	if len(msg) > 1024 {
		msg = msg[:1024]
	}
	return msg
}
