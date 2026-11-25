package otlp

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// The configuration of an OTLP target and its checks: endpoint, signals, headers and the TLS CA.

// Config of one OTLP target.
type Config struct {
	Protocol    string // grpc | http
	TLS         bool
	TLSCA       string
	SkipVerify  bool
	Headers     map[string]string
	Metrics     bool
	Logs        bool
	Interval    time.Duration // metrics; 10 s .. 5 min
	RuleMetrics bool
	Gzip        bool
	Resource    Resource
	Bind        netip.Addr
}

// Validation errors a caller can tell apart with errors.Is.
var (
	ErrEndpoint    = errors.New("want host:port")           // gRPC
	ErrEndpointURL = errors.New("want http(s)://host:port") // HTTP
	ErrNoSignal    = errors.New("neither metrics nor logs")
	ErrTLSCA       = errors.New("TLS CA: no certificate in PEM")
)

// Validate checks endpoint and config.
func Validate(endpoint string, c Config) error {
	if err := validateEndpoint(c.Protocol, endpoint); err != nil {
		return err
	}
	if !c.Metrics && !c.Logs {
		return ErrNoSignal
	}
	if !validCA(c.TLSCA) {
		return ErrTLSCA
	}
	return validateHeaders(c.Headers)
}

// validCA reports whether pem is empty (system roots) or holds at least one certificate.
func validCA(pem string) bool {
	return pem == "" || x509.NewCertPool().AppendCertsFromPEM([]byte(pem))
}

// validateHeaders refuses empty header names and names with spaces, colons or line breaks.
func validateHeaders(headers map[string]string) error {
	for k := range headers {
		if k == "" || strings.ContainsAny(k, " :\r\n") {
			return fmt.Errorf("header name %q", k)
		}
	}
	return nil
}

// validateEndpoint checks the endpoint for the protocol: host:port for gRPC, an http(s) URL with a host for HTTP.
func validateEndpoint(protocol, endpoint string) error {
	switch protocol {
	case "grpc":
		if !isHostPort(endpoint) {
			return fmt.Errorf("OTLP gRPC endpoint %q: %w", endpoint, ErrEndpoint)
		}
	case "http":
		if !isHTTPURL(endpoint) {
			return fmt.Errorf("OTLP HTTP endpoint %q: %w", endpoint, ErrEndpointURL)
		}
	default:
		return fmt.Errorf("protocol %q", protocol)
	}
	return nil
}

// isHostPort reports whether endpoint is host:port with both parts set.
func isHostPort(endpoint string) bool {
	h, p, err := net.SplitHostPort(endpoint)
	return err == nil && h != "" && p != ""
}

// isHTTPURL reports whether endpoint is an http or https URL with a host.
func isHTTPURL(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// withDefaults bounds the metrics interval (outside 10 s .. 5 min: 30 s or 5 min) and fills in the service name.
func (c Config) withDefaults() Config {
	if c.Interval < 10*time.Second {
		c.Interval = 30 * time.Second
	}
	if c.Interval > 5*time.Minute {
		c.Interval = 5 * time.Minute
	}
	if c.Resource.ServiceName == "" {
		c.Resource.ServiceName = "nsrelay"
	}
	return c
}
