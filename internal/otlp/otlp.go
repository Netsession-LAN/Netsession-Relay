// Package otlp exports the relay's metrics and events with the OpenTelemetry protocol (OTLP), over gRPC or
// HTTP/protobuf, without the OTel SDK: the monitoring report is converted directly into OTLP messages. One
// Sender per target.
package otlp

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip" // registers the gzip compressor

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
)

// Resource identifies this relay in every export.
type Resource struct {
	ServiceName string // always "nsrelay"
	Version     string
	InstanceID  string
	HostName    string
}

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

// Source builds the current metrics (called once per interval).
type Source func(ctx context.Context) []Metric

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

// Sender is one OTLP target: logs through the notify queue, metrics on its own ticker.
type Sender struct {
	endpoint string
	c        Config
	source   Source

	conn  *grpc.ClientConn
	logsC collogs.LogsServiceClient
	metC  colmetrics.MetricsServiceClient
	httpC *http.Client
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
}

// NewSender builds the sender and starts the metrics ticker (if metrics are enabled).
func NewSender(endpoint string, c Config, source Source) (*Sender, error) {
	if err := Validate(endpoint, c); err != nil {
		return nil, err
	}
	c = c.withDefaults()
	s := &Sender{endpoint: endpoint, c: c, source: source, stop: make(chan struct{}), done: make(chan struct{})}
	if err := s.setupTransport(); err != nil {
		return nil, err
	}
	if c.Metrics && source != nil {
		go s.loop()
	} else {
		close(s.done)
	}
	return s, nil
}

// setupTransport connects the gRPC client or builds the HTTP client of the configured protocol.
func (s *Sender) setupTransport() error {
	tlsConf, dialer := s.c.tlsConfig(), s.c.dialer()
	if s.c.Protocol == "grpc" {
		return s.connectGRPC(tlsConf, dialer)
	}
	tr := &http.Transport{DialContext: dialer.DialContext, TLSClientConfig: tlsConf, MaxIdleConns: 2, IdleConnTimeout: time.Minute}
	s.httpC = &http.Client{Transport: tr, Timeout: 5 * time.Second}
	return nil
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

// Reliable reports true: OTLP answers every request.
func (s *Sender) Reliable() bool { return true }

func (s *Sender) loop() {
	defer close(s.done)
	t := time.NewTicker(s.c.Interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.ExportMetrics(ctx) // last state on shutdown
			cancel()
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.ExportMetrics(ctx)
			cancel()
		}
	}
}

// ExportMetrics sends the current metrics once.
func (s *Sender) ExportMetrics(ctx context.Context) error {
	req := &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: resourceMetrics(s.c.Resource, s.source(ctx), s.c.RuleMetrics)}
	if s.metC == nil {
		return s.postHTTP(ctx, "/v1/metrics", req)
	}
	resp, err := s.metC.Export(s.withHeaders(ctx), req)
	if err != nil {
		return err
	}
	if n := resp.GetPartialSuccess().GetRejectedDataPoints(); n > 0 {
		return fmt.Errorf("collector rejected %d data points: %s", n, resp.GetPartialSuccess().GetErrorMessage())
	}
	return nil
}

// Send exports a batch of events as log records (notify.Sender).
func (s *Sender) Send(ctx context.Context, batch []notify.Event) error {
	if !s.c.Logs {
		return nil
	}
	req := &collogs.ExportLogsServiceRequest{ResourceLogs: resourceLogs(s.c.Resource, batch)}
	if s.logsC != nil {
		resp, err := s.logsC.Export(s.withHeaders(ctx), req)
		if err != nil {
			return err
		}
		if n := resp.GetPartialSuccess().GetRejectedLogRecords(); n > 0 {
			return fmt.Errorf("collector rejected %d log records: %s", n, resp.GetPartialSuccess().GetErrorMessage())
		}
		return nil
	}
	return s.postHTTP(ctx, "/v1/logs", req)
}

// Close stops the metrics ticker (after a last export) and the connection.
func (s *Sender) Close() error {
	s.once.Do(func() { close(s.stop) })
	<-s.done
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}
