// Package otlp exports the relay's metrics and events with the OpenTelemetry protocol (OTLP), over gRPC or
// HTTP/protobuf, without the OTel SDK: the monitoring report is converted directly into OTLP messages. One
// Sender per target.
package otlp

import (
	"context"
	"fmt"
	"net/http"
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

// Source builds the current metrics (called once per interval).
type Source func(ctx context.Context) []Metric

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
