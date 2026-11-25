package snmp

import (
	"context"
	"encoding/hex"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
)

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
