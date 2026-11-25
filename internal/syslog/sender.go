package syslog

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
)

// The sender of one syslog target: the connection over UDP, TCP or TLS and the framing of each message.

// Sender is one syslog target. TCP and TLS keep their connection and reconnect after an error.
type Sender struct {
	endpoint string
	c        Config
	host     string
	tlsConf  *tls.Config

	mu   sync.Mutex
	conn net.Conn
}

// NewSender builds the sender of a target (no connection yet).
func NewSender(endpoint string, c Config) (*Sender, error) {
	if err := Validate(endpoint, c); err != nil {
		return nil, err
	}
	s := &Sender{endpoint: endpoint, c: c, host: c.Host}
	if s.host == "" {
		s.host, _ = os.Hostname()
	}
	if c.Transport == "tls" {
		host, _, _ := net.SplitHostPort(endpoint)
		s.tlsConf = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.TLSSkipVerify} //nolint:gosec // explicit, shown in the UI
		if c.TLSCA != "" {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM([]byte(c.TLSCA))
			s.tlsConf.RootCAs = pool
		}
	}
	return s, nil
}

// Reliable reports whether delivery is confirmed: TCP and TLS confirm it to the kernel of the receiver, UDP
// does not.
func (s *Sender) Reliable() bool { return s.c.Transport != "udp" }

func (s *Sender) dialer() *net.Dialer {
	d := &net.Dialer{Timeout: 3 * time.Second}
	if s.c.Bind.IsValid() {
		if s.c.Transport == "udp" {
			d.LocalAddr = &net.UDPAddr{IP: s.c.Bind.AsSlice()}
		} else {
			d.LocalAddr = &net.TCPAddr{IP: s.c.Bind.AsSlice()}
		}
	}
	return d
}

func (s *Sender) connect(ctx context.Context) (net.Conn, error) {
	if s.conn != nil {
		return s.conn, nil
	}
	d := s.dialer()
	var conn net.Conn
	var err error
	switch s.c.Transport {
	case "tls":
		conn, err = (&tls.Dialer{NetDialer: d, Config: s.tlsConf}).DialContext(ctx, "tcp", s.endpoint)
	case "tcp":
		conn, err = d.DialContext(ctx, "tcp", s.endpoint)
	default:
		conn, err = d.DialContext(ctx, "udp", s.endpoint)
	}
	if err != nil {
		return nil, err
	}
	s.conn = conn
	return conn, nil
}

func (s *Sender) frame(e notify.Event) []byte {
	msg := s.message(e)
	switch s.c.Transport {
	case "tls":
		return []byte(fmt.Sprintf("%d %s", len(msg), msg)) // octet counting, RFC 5425
	case "tcp":
		return []byte(msg + "\n") // non-transparent framing, RFC 6587
	}
	return []byte(msg)
}

// message renders e in the configured format; an RFC 5424 message over UDP is cut to 2048 bytes.
func (s *Sender) message(e notify.Event) string {
	if s.c.Format == "rfc3164" {
		return Format3164(e, s.host, s.c.Facility)
	}
	msg := Format5424(e, s.host, s.c.Facility)
	if s.c.Transport == "udp" && len(msg) > 2048 {
		msg = msg[:2048]
	}
	return msg
}

// Send writes the batch; on a write error the connection is dropped and rebuilt with the next batch.
func (s *Sender) Send(ctx context.Context, batch []notify.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
	}
	for _, e := range batch {
		if _, err := conn.Write(s.frame(e)); err != nil {
			_ = conn.Close()
			s.conn = nil
			return err
		}
	}
	return nil
}

// Close drops the connection.
func (s *Sender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}
