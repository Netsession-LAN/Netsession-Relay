package snmp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
)

// TrapConfig of one trap receiver.
type TrapConfig struct {
	Version   string // v2c | v3
	Inform    bool   // confirmed InformRequest instead of a trap
	Community string // v2c
	User      string // v3 USM user
	AuthProto string // sha | sha256 | sha512
	AuthPass  string
	PrivProto string // aes128 | aes256
	PrivPass  string
	EngineID  string // our authoritative engine id (hex), v3 traps
	Boots     uint32
	Bind      netip.Addr
}

// NewEngineID returns a random RFC 3411 engine id for our enterprise: 0x80 | PEN, format 5 (octets), 8 random
// bytes. Created once and stored.
func NewEngineID() string {
	b := make([]byte, 13)
	pen := uint32(notify.PEN) | 0x80000000
	b[0], b[1], b[2], b[3] = byte(pen>>24), byte(pen>>16), byte(pen>>8), byte(pen)
	b[4] = 5
	_, _ = rand.Read(b[5:])
	return hex.EncodeToString(b)
}

func authProto(s string) (gosnmp.SnmpV3AuthProtocol, error) {
	switch s {
	case "sha":
		return gosnmp.SHA, nil
	case "sha256", "":
		return gosnmp.SHA256, nil
	case "sha512":
		return gosnmp.SHA512, nil
	}
	return 0, fmt.Errorf("auth protocol %q", s)
}

func privProto(s string) (gosnmp.SnmpV3PrivProtocol, error) {
	switch s {
	case "aes128", "aes", "":
		return gosnmp.AES, nil
	case "aes256":
		return gosnmp.AES256C, nil // Cisco/net-snmp key extension, as net-snmp's AES-256
	}
	return 0, fmt.Errorf("privacy protocol %q", s)
}

// Validation errors of ValidateTrap and ValidateAgent a caller can tell apart with errors.Is.
var (
	ErrEndpoint         = errors.New("want host:port")
	ErrNoVersion        = errors.New("neither v2c nor v3 configured")
	ErrCommunityMissing = errors.New("community missing")
	ErrUserMissing      = errors.New("v3 user missing")
	ErrPasswordShort    = errors.New("v3 passwords: at least 8 characters")
)

// ValidateTrap checks the receiver address and config.
func ValidateTrap(endpoint string, c TrapConfig) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("trap receiver %q: %w", endpoint, ErrEndpoint)
	}
	return c.versionError()
}

// versionError checks the version and its credentials.
func (c TrapConfig) versionError() error {
	switch c.Version {
	case "v2c":
		if c.Community == "" {
			return ErrCommunityMissing
		}
		return nil
	case "v3":
		if c.User == "" {
			return ErrUserMissing
		}
		return validateUSM(c.AuthProto, c.AuthPass, c.PrivProto, c.PrivPass)
	}
	return fmt.Errorf("version %q", c.Version)
}

// validateUSM checks the protocols and passwords of a v3 user (authPriv).
func validateUSM(auth, authPass, priv, privPass string) error {
	if _, err := authProto(auth); err != nil {
		return err
	}
	if _, err := privProto(priv); err != nil {
		return err
	}
	if len(authPass) < 8 || len(privPass) < 8 {
		return ErrPasswordShort
	}
	return nil
}

// newUSM returns the security parameters of a v3 user whose protocols passed validateUSM.
func newUSM(user, auth, authPass, priv, privPass string) *gosnmp.UsmSecurityParameters {
	ap, _ := authProto(auth)
	pp, _ := privProto(priv)
	return &gosnmp.UsmSecurityParameters{UserName: user, AuthenticationProtocol: ap, AuthenticationPassphrase: authPass,
		PrivacyProtocol: pp, PrivacyPassphrase: privPass}
}

// TrapSender sends events as SNMP notifications to one receiver.
type TrapSender struct {
	endpoint string
	c        TrapConfig
	start    time.Time

	mu sync.Mutex
	g  *gosnmp.GoSNMP
}

// NewTrapSender validates and builds the sender (connection on first send).
func NewTrapSender(endpoint string, c TrapConfig) (*TrapSender, error) {
	if err := ValidateTrap(endpoint, c); err != nil {
		return nil, err
	}
	return &TrapSender{endpoint: endpoint, c: c, start: time.Now()}, nil
}

// Reliable reports whether delivery is confirmed: only informs are.
func (s *TrapSender) Reliable() bool { return s.c.Inform }

func (s *TrapSender) client() (*gosnmp.GoSNMP, error) {
	if s.g != nil {
		return s.g, nil
	}
	host, portStr, _ := net.SplitHostPort(s.endpoint)
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	g := &gosnmp.GoSNMP{Target: host, Port: uint16(port), Transport: "udp", Timeout: 2 * time.Second, Retries: 1,
		MaxOids: gosnmp.MaxOids}
	if s.c.Bind.IsValid() {
		g.LocalAddr = net.JoinHostPort(s.c.Bind.String(), "0")
	}
	s.setVersion(g)
	if err := g.Connect(); err != nil {
		return nil, err
	}
	s.g = g
	return g, nil
}

// setVersion configures g for v2c or for the v3 user.
func (s *TrapSender) setVersion(g *gosnmp.GoSNMP) {
	if s.c.Version == "v2c" {
		g.Version, g.Community = gosnmp.Version2c, s.c.Community
		return
	}
	eid, _ := hex.DecodeString(s.c.EngineID)
	g.Version, g.SecurityModel, g.MsgFlags = gosnmp.Version3, gosnmp.UserSecurityModel, gosnmp.AuthPriv
	sp := newUSM(s.c.User, s.c.AuthProto, s.c.AuthPass, s.c.PrivProto, s.c.PrivPass)
	if !s.c.Inform {
		// traps: we are the authoritative engine
		sp.AuthoritativeEngineID = string(eid)
		sp.AuthoritativeEngineBoots = s.c.Boots
		sp.AuthoritativeEngineTime = uint32(time.Since(s.start).Seconds())
	}
	g.SecurityParameters = sp
}

// notificationFor returns the notification OID and varbinds of an event.
func notificationFor(e notify.Event) (string, []gosnmp.SnmpPDU) {
	str := func(name, v string) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: notifyOID(name), Type: gosnmp.OctetString, Value: v}
	}
	vlan := ""
	if e.VLAN > 0 {
		vlan = strconv.Itoa(e.VLAN)
	}
	switch e.Type {
	case notify.TypeAlarm:
		trap := "0.1"
		if e.State == "clear" {
			trap = "0.2"
		}
		return OID(trap), []gosnmp.SnmpPDU{str("nsRelayEventKind", e.Kind), str("nsRelayEventKey", e.Key), str("nsRelayEventVlan", vlan), str("nsRelayEventText", e.Text)}
	case notify.TypeAudit:
		return OID("0.3"), []gosnmp.SnmpPDU{str("nsRelayEventAction", e.Action), str("nsRelayEventUser", e.User), str("nsRelayEventObject", e.Object), str("nsRelayEventText", e.Text)}
	}
	return OID("0.4"), []gosnmp.SnmpPDU{str("nsRelayEventUser", e.User), str("nsRelayEventText", e.Text)}
}

// Send sends one notification per event.
func (s *TrapSender) Send(ctx context.Context, batch []notify.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.client()
	if err != nil {
		return err
	}
	s.updateTiming(ctx, g)
	for _, e := range batch {
		if _, err := g.SendTrap(gosnmp.SnmpTrap{Variables: s.trapVariables(e), IsInform: s.c.Inform}); err != nil {
			_ = g.Conn.Close()
			s.g = nil
			return err
		}
	}
	return nil
}

// updateTiming fits the timeout into the deadline of ctx and, for v3 traps, brings our engine time up to date.
func (s *TrapSender) updateTiming(ctx context.Context, g *gosnmp.GoSNMP) {
	if dl, ok := ctx.Deadline(); ok {
		g.Timeout = min(2*time.Second, time.Until(dl))
	}
	if sp, ok := g.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok && !s.c.Inform {
		sp.AuthoritativeEngineTime = uint32(time.Since(s.start).Seconds())
	}
}

// trapVariables are the varbinds of the notification for e: sysUpTime, snmpTrapOID, then the event's objects,
// every name with a leading dot.
func (s *TrapSender) trapVariables(e notify.Event) []gosnmp.SnmpPDU {
	trapOID, vars := notificationFor(e)
	pdus := append([]gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(time.Since(s.start).Seconds() * 100)},
		{Name: ".1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.ObjectIdentifier, Value: "." + trapOID},
	}, vars...)
	for i := range pdus {
		if pdus[i].Name[0] != '.' {
			pdus[i].Name = "." + pdus[i].Name
		}
	}
	return pdus
}

// Close closes the socket.
func (s *TrapSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.g != nil && s.g.Conn != nil {
		err := s.g.Conn.Close()
		s.g = nil
		return err
	}
	return nil
}
