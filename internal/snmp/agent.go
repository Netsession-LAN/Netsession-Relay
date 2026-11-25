package snmp

import (
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
	if err := validateAgentListen(c.Listen); err != nil {
		return err
	}
	return c.versionError()
}

// validateAgentListen accepts a host:port on a concrete address: the agent must not listen on every interface.
func validateAgentListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return fmt.Errorf("listen address %q", listen)
	}
	if a, err := netip.ParseAddr(host); err != nil || a.IsUnspecified() {
		return errors.New("listen address must be the MGMT address, not 0.0.0.0")
	}
	return nil
}

// versionError checks that v2c or v3 is configured, each with its credentials.
func (c AgentConfig) versionError() error {
	if c.V2c && c.Community == "" {
		return ErrCommunityMissing
	}
	if c.V3User != "" {
		return validateUSM(c.AuthProto, c.AuthPass, c.PrivProto, c.PrivPass)
	}
	if !c.V2c {
		return ErrNoVersion
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
	tl := gosnmp.NewTrapListener()
	tl.Params = listenerParams(c)
	tl.OnNewTrap = a.handle
	a.tl = tl
	errc := make(chan error, 1)
	go func() {
		defer close(a.done)
		errc <- tl.Listen(c.Listen)
	}()
	if err := waitListening(tl, errc); err != nil {
		return nil, err
	}
	return a, nil
}

// listenerParams are the protocol settings of the agent's listener: v2c, and v3 with the agent as the
// authoritative engine when a v3 user is configured.
func listenerParams(c AgentConfig) *gosnmp.GoSNMP {
	params := &gosnmp.GoSNMP{Version: gosnmp.Version2c, Timeout: 2 * time.Second, Logger: gosnmp.NewLogger(log.New(io.Discard, "", 0))}
	if c.V3User != "" {
		eid, _ := hex.DecodeString(c.EngineID)
		sp := newUSM(c.V3User, c.AuthProto, c.AuthPass, c.PrivProto, c.PrivPass)
		sp.AuthoritativeEngineID, sp.AuthoritativeEngineBoots = string(eid), c.Boots
		params.Version, params.SecurityModel, params.MsgFlags = gosnmp.Version3, gosnmp.UserSecurityModel, gosnmp.AuthPriv
		params.SecurityParameters = sp
	}
	return params
}

// waitListening waits until the listener has bound its socket, failed, or not started within 3 s.
func waitListening(tl *gosnmp.TrapListener, errc <-chan error) error {
	select {
	case <-tl.Listening():
		return nil
	case err := <-errc:
		if err == nil {
			err = errors.New("listener stopped")
		}
		return err
	case <-time.After(3 * time.Second):
		tl.Close()
		return errors.New("SNMP listener did not start")
	}
}

// Close stops the agent.
func (a *Agent) Close() {
	a.tl.Close()
	<-a.done
}

// allowed reports whether a request from src is answered: the source is in the allow list and within its rate.
func (a *Agent) allowed(src netip.Addr) bool {
	return a.inAllowList(src) && a.takeToken(src)
}

func (a *Agent) inAllowList(src netip.Addr) bool {
	return len(a.c.Allow) == 0 || slices.ContainsFunc(a.c.Allow, func(p netip.Prefix) bool { return p.Contains(src) })
}

// takeToken takes one request from the token bucket of src (ratePerSource per second).
func (a *Agent) takeToken(src netip.Addr) bool {
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
