package web

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// The web part listens on every address of the host by default, so a fresh installation needs no setting. The
// relay has an address in every participant network, though, and the UI must not be reachable there. The
// engine installs a host firewall for that; this guard is the second line that needs no privileges: a
// connection that arrives on a relay address is closed before a single byte is read.
//
// Relay addresses are the relay IPs of the configured networks and every address on a relay interface (the
// VLAN interfaces the engine created, interfaces attached as a device). Addresses on an interface that carries
// a default route are never refused: that is the management side, even when the relay uses it as a device.

const guardTTL = 3 * time.Second

type relayGuard struct {
	mu      sync.Mutex
	at      time.Time
	blocked map[netip.Addr]bool
	logged  map[netip.Addr]time.Time
}

// isRelayAddr reports whether a local address belongs to the relay side (cached for a few seconds).
func (s *Server) isRelayAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() {
		return false
	}
	s.guard.mu.Lock()
	defer s.guard.mu.Unlock()
	s.refreshRelayAddrs()
	if !s.guard.blocked[a] {
		return false
	}
	s.logRelayAddrRefused(a)
	return true
}

// refreshRelayAddrs reads the relay addresses again when the cached ones are older than guardTTL. s.guard.mu is
// held.
func (s *Server) refreshRelayAddrs() {
	g := &s.guard
	if g.blocked != nil && time.Since(g.at) <= guardTTL {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	g.blocked = s.relayAddrs(ctx)
	cancel()
	g.at = time.Now()
}

// logRelayAddrRefused logs a connection closed on relay address a, one line a minute per address. s.guard.mu is
// held.
func (s *Server) logRelayAddrRefused(a netip.Addr) {
	g := &s.guard
	if g.logged == nil {
		g.logged = map[netip.Addr]time.Time{}
	}
	if t, ok := g.logged[a]; ok && time.Since(t) <= time.Minute {
		return
	}
	g.logged[a] = time.Now()
	s.Log.Warn("connection closed: the web UI is not served on relay addresses", "local", a.String())
}

// mgmtAddr is the IPv4 address of the management side: the first one on an interface with a default route.
func (s *Server) mgmtAddr() netip.Addr {
	ifs, err := s.hostIfaces()
	if err != nil {
		return netip.Addr{}
	}
	for _, a := range guardAddrsWhere(ifs, func(i netif.HostIface) bool { return i.DefaultRoute }) {
		if a.Is4() {
			return a
		}
	}
	return netip.Addr{}
}

// Listener wraps the listener of the web server with the guard.
func (s *Server) Listener(ln net.Listener) net.Listener { return &guardListener{Listener: ln, s: s} }

type guardListener struct {
	net.Listener
	s *Server
}

func (l *guardListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !l.onRelayAddr(c) {
			return c, nil
		}
		_ = c.Close()
	}
}

// onRelayAddr reports whether a connection came in on an address of the relay side.
func (l *guardListener) onRelayAddr(c net.Conn) bool {
	ta, ok := c.LocalAddr().(*net.TCPAddr)
	if !ok {
		return false
	}
	a, ok := netip.AddrFromSlice(ta.IP)
	return ok && l.s.isRelayAddr(a)
}
