package otlp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// The transports of a sender: TLS and source address of the connection, the gRPC client, and the HTTP/protobuf
// requests with the configured headers.

// tlsConfig is the client TLS configuration, nil without TLS. A configured CA replaces the system roots.
func (c Config) tlsConfig() *tls.Config {
	if !c.TLS {
		return nil
	}
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.SkipVerify} //nolint:gosec // explicit, shown in the UI
	if c.TLSCA != "" {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM([]byte(c.TLSCA))
		tlsConf.RootCAs = pool
	}
	return tlsConf
}

// dialer connects from the configured source address, if any.
func (c Config) dialer() *net.Dialer {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	if c.Bind.IsValid() {
		dialer.LocalAddr = &net.TCPAddr{IP: c.Bind.AsSlice()}
	}
	return dialer
}

// connectGRPC sets up the gRPC client connection (lazy, it reconnects by itself) and the service clients. With
// TLS the server name is the endpoint's host.
func (s *Sender) connectGRPC(tlsConf *tls.Config, dialer *net.Dialer) error {
	creds := insecure.NewCredentials()
	if tlsConf != nil {
		host, _, _ := net.SplitHostPort(s.endpoint)
		tlsConf.ServerName = host
		creds = credentials.NewTLS(tlsConf)
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", addr)
		})}
	if s.c.Gzip {
		opts = append(opts, grpc.WithDefaultCallOptions(grpc.UseCompressor("gzip")))
	}
	conn, err := grpc.NewClient("passthrough:///"+s.endpoint, opts...)
	if err != nil {
		return err
	}
	s.conn, s.logsC, s.metC = conn, collogs.NewLogsServiceClient(conn), colmetrics.NewMetricsServiceClient(conn)
	return nil
}

func (s *Sender) withHeaders(ctx context.Context) context.Context {
	if len(s.c.Headers) == 0 {
		return ctx
	}
	kv := make([]string, 0, 2*len(s.c.Headers))
	for k, v := range s.c.Headers {
		kv = append(kv, strings.ToLower(k), v)
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

func (s *Sender) postHTTP(ctx context.Context, path string, msg proto.Message) error {
	body, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	u := strings.TrimRight(s.endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	for k, v := range s.c.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.httpC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("collector answered %s", resp.Status)
	}
	return nil
}
