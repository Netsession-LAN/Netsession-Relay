package snmp

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"
)

// The agent answers GET, GETNEXT and GETBULK read-only (SET: notWritable) for the system group and the relay
// tree. Receiving, v3 authentication/decryption and engine discovery reports come from gosnmp's
// TrapListener; this file only resolves the variables.

// AgentConfig of the SNMP agent.
type AgentConfig struct {
	Listen    string // host:port, MGMT address only
	V2c       bool
	Community string
	V3User    string
	AuthProto string
	AuthPass  string
	PrivProto string
	PrivPass  string
	EngineID  string // hex
	Boots     uint32
	Allow     []netip.Prefix // empty = every source
	SysDescr  string
	SysName   string
	Contact   string
	Location  string
}

// ValidateAgent checks the configuration (at least one of v2c and v3).
func ValidateAgent(c AgentConfig) error {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil || port == "" {
		return fmt.Errorf("listen address %q", c.Listen)
	}
	if a, err := netip.ParseAddr(host); err != nil || a.IsUnspecified() {
		return errors.New("listen address must be the MGMT address, not 0.0.0.0")
	}
	if !c.V2c && c.V3User == "" {
		return ErrNoVersion
	}
	if c.V2c && c.Community == "" {
		return ErrCommunityMissing
	}
	if c.V3User != "" {
		if _, err := authProto(c.AuthProto); err != nil {
			return err
		}
		if _, err := privProto(c.PrivProto); err != nil {
			return err
		}
		if len(c.AuthPass) < 8 || len(c.PrivPass) < 8 {
			return ErrPasswordShort
		}
	}
	return nil
}

// Agent is a running SNMP agent.
type Agent struct {
	c     AgentConfig
	view  func() *View
	start time.Time
	tl    *gosnmp.TrapListener
	done  chan struct{}

	mu       sync.Mutex
	cache    []entry
	cachedAt time.Time

	rateMu sync.Mutex
	rate   map[netip.Addr]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	maxRepetitions = 50
	maxResponse    = 1400 // bytes of variable values, keeps GETBULK from amplifying
	ratePerSource  = 50   // requests per second
)

// StartAgent binds the UDP socket and serves until Close.
func StartAgent(c AgentConfig, view func() *View) (*Agent, error) {
	if err := ValidateAgent(c); err != nil {
		return nil, err
	}
	a := &Agent{c: c, view: view, start: time.Now(), done: make(chan struct{}), rate: map[netip.Addr]*bucket{}}
	params := &gosnmp.GoSNMP{Version: gosnmp.Version2c, Timeout: 2 * time.Second, Logger: gosnmp.NewLogger(log.New(io.Discard, "", 0))}
	if c.V3User != "" {
		ap, _ := authProto(c.AuthProto)
		pp, _ := privProto(c.PrivProto)
		eid, _ := hex.DecodeString(c.EngineID)
		params.Version, params.SecurityModel, params.MsgFlags = gosnmp.Version3, gosnmp.UserSecurityModel, gosnmp.AuthPriv
		params.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: c.V3User, AuthenticationProtocol: ap,
			AuthenticationPassphrase: c.AuthPass, PrivacyProtocol: pp, PrivacyPassphrase: c.PrivPass,
			AuthoritativeEngineID: string(eid), AuthoritativeEngineBoots: c.Boots}
	}
	tl := gosnmp.NewTrapListener()
	tl.Params = params
	tl.OnNewTrap = a.handle
	a.tl = tl
	errc := make(chan error, 1)
	go func() {
		defer close(a.done)
		errc <- tl.Listen(c.Listen)
	}()
	select {
	case <-tl.Listening():
		return a, nil
	case err := <-errc:
		if err == nil {
			err = errors.New("listener stopped")
		}
		return nil, err
	case <-time.After(3 * time.Second):
		tl.Close()
		return nil, errors.New("SNMP listener did not start")
	}
}

// Close stops the agent.
func (a *Agent) Close() {
	a.tl.Close()
	<-a.done
}

func (a *Agent) allowed(src netip.Addr) bool {
	if len(a.c.Allow) > 0 && !slices.ContainsFunc(a.c.Allow, func(p netip.Prefix) bool { return p.Contains(src) }) {
		return false
	}
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	now := time.Now()
	b := a.rate[src]
	if b == nil {
		if len(a.rate) > 1024 { // forget old sources
			clear(a.rate)
		}
		b = &bucket{tokens: ratePerSource, last: now}
		a.rate[src] = b
	}
	b.tokens = min(ratePerSource, b.tokens+now.Sub(b.last).Seconds()*ratePerSource)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (a *Agent) handle(p *gosnmp.SnmpPacket, remote *net.UDPAddr) {
	src, _ := netip.AddrFromSlice(remote.IP)
	if !a.allowed(src.Unmap()) || !a.authorized(p) {
		return
	}
	es := a.table()
	resp := *p
	resp.PDUType = gosnmp.GetResponse
	resp.Error, resp.ErrorIndex = gosnmp.NoError, 0
	if !a.answer(&resp, p, es) {
		return
	}
	_ = a.tl.SendUDP(&resp, remote)
}

// authorized checks the version and credentials of a request: v1/v2c need the configured community (a wrong
// one gets no answer), v3 needs a configured user. For v3 it also fills in the engine boots and time of the
// reply.
func (a *Agent) authorized(p *gosnmp.SnmpPacket) bool {
	switch p.Version {
	case gosnmp.Version2c, gosnmp.Version1:
		return a.c.V2c && subtle.ConstantTimeCompare([]byte(p.Community), []byte(a.c.Community)) == 1
	case gosnmp.Version3:
		if a.c.V3User == "" {
			return false
		}
		if sp, ok := p.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok {
			sp.AuthoritativeEngineBoots = a.c.Boots
			sp.AuthoritativeEngineTime = uint32(time.Since(a.start).Seconds())
		}
		return true
	}
	return false
}

// answer fills the variables (and for SET the error) of the response to request p; false for request types
// that get no answer.
func (a *Agent) answer(resp, p *gosnmp.SnmpPacket, es []entry) bool {
	switch p.PDUType {
	case gosnmp.GetRequest:
		resp.Variables = a.each(es, p.Variables, a.get)
	case gosnmp.GetNextRequest:
		resp.Variables = a.each(es, p.Variables, a.next)
	case gosnmp.GetBulkRequest:
		resp.Variables = a.bulk(es, p)
		resp.NonRepeaters, resp.MaxRepetitions = 0, 0
	case gosnmp.SetRequest:
		resp.Error, resp.ErrorIndex = gosnmp.NotWritable, 1
		resp.Variables = p.Variables
	default:
		return false
	}
	return true
}

// each resolves every requested variable with lookup (get or next).
func (a *Agent) each(es []entry, vars []gosnmp.SnmpPDU, lookup func([]entry, []int) gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
	var out []gosnmp.SnmpPDU
	for _, v := range vars {
		out = append(out, lookup(es, parseOID(v.Name)))
	}
	return out
}

// bulk answers GETBULK: the non-repeaters once, then the repeaters row by row, at most maxRepetitions rows and
// (after the first variable) at most maxResponse bytes.
func (a *Agent) bulk(es []entry, p *gosnmp.SnmpPacket) []gosnmp.SnmpPDU {
	nonRep := min(int(p.NonRepeaters), len(p.Variables))
	out := a.each(es, p.Variables[:nonRep], a.next)
	reps := min(int(p.MaxRepetitions), maxRepetitions)
	cursors := make([][]int, 0, len(p.Variables)-nonRep)
	for _, v := range p.Variables[nonRep:] {
		cursors = append(cursors, parseOID(v.Name))
	}
	size := 0
	for r := 0; r < reps && len(cursors) > 0; r++ {
		for i, c := range cursors {
			pdu := a.next(es, c)
			size += pduSize(pdu)
			if size > maxResponse && len(out) > 0 {
				return out
			}
			out = append(out, pdu)
			cursors[i] = parseOID(pdu.Name)
		}
	}
	return out
}
