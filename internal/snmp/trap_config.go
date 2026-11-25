package snmp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/gosnmp/gosnmp"

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
)

// The configuration of a trap receiver: version, community or USM user with its protocols, checked before it is
// stored.

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
