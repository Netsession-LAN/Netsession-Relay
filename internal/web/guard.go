package web

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
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

// hostIfaces lists the interfaces of the host (HostIfaces in tests and the demo).
func (s *Server) hostIfaces() ([]netif.HostIface, error) {
	if s.HostIfaces != nil {
		return s.HostIfaces()
	}
	return netif.Interfaces()
}

func addrOf(cidr string) (netip.Addr, bool) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return netip.Addr{}, false
	}
	return p.Addr().Unmap(), true
}

// relayAddrs computes the addresses the UI is not served on.
func (s *Server) relayAddrs(ctx context.Context) map[netip.Addr]bool {
	out, names := s.configuredRelaySide(ctx)
	ifs, err := s.hostIfaces()
	if err != nil {
		return out // not on Linux: the configured relay IPs are all that is known
	}
	relay := func(i netif.HostIface) bool { return guardRelayIface(i, names) }
	for _, a := range guardAddrsWhere(ifs, relay) {
		out[a] = true
	}
	for _, a := range guardAddrsWhere(ifs, guardMgmtIface) {
		delete(out, a)
	}
	return out
}

// configuredRelaySide reads the configured networks: their relay IPs and the names of the interfaces they use.
// An unreadable configuration gives two empty sets.
func (s *Server) configuredRelaySide(ctx context.Context) (ips map[netip.Addr]bool, ifaceNames map[string]bool) {
	ips, ifaceNames = map[netip.Addr]bool{}, map[string]bool{}
	c, err := s.Store.ExportConfig(ctx)
	if err != nil {
		return ips, ifaceNames
	}
	for _, v := range c.VLANs {
		if a, err := netip.ParseAddr(v.RelayIP); err == nil {
			ips[a.Unmap()] = true
		}
		if v.Attach == compile.AttachDevice {
			ifaceNames[v.Device] = true
		} else {
			ifaceNames[fmt.Sprintf("%s.%d", v.Parent, v.VID)] = true
		}
	}
	return ips, ifaceNames
}

// guardMgmtIface reports whether an interface belongs to the management side (default route or loopback); its
// addresses are never refused.
func guardMgmtIface(i netif.HostIface) bool { return i.DefaultRoute || i.Loopback }

// guardRelayIface reports whether an interface is on the relay side: used by a configured network or created by
// the engine, and not on the management side.
func guardRelayIface(i netif.HostIface, configured map[string]bool) bool {
	return (configured[i.Name] || i.Relay) && !guardMgmtIface(i)
}

// guardAddrsWhere gives the parseable addresses (without prefix length) of the interfaces that match, in the
// order of the interfaces.
func guardAddrsWhere(ifs []netif.HostIface, match func(netif.HostIface) bool) []netip.Addr {
	var out []netip.Addr
	for _, i := range ifs {
		if !match(i) {
			continue
		}
		for _, c := range i.Addrs {
			if a, ok := addrOf(c); ok {
				out = append(out, a)
			}
		}
	}
	return out
}

// isRelayAddr reports whether a local address belongs to the relay side (cached for a few seconds).
func (s *Server) isRelayAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() {
		return false
	}
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.blocked == nil || time.Since(g.at) > guardTTL {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		g.blocked = s.relayAddrs(ctx)
		cancel()
		g.at = time.Now()
	}
	if !g.blocked[a] {
		return false
	}
	if g.logged == nil {
		g.logged = map[netip.Addr]time.Time{}
	}
	if t, ok := g.logged[a]; !ok || time.Since(t) > time.Minute { // one line a minute per address
		g.logged[a] = time.Now()
		s.Log.Warn("connection closed: the web UI is not served on relay addresses", "local", a.String())
	}
	return true
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
		if ta, ok := c.LocalAddr().(*net.TCPAddr); ok {
			if a, ok := netip.AddrFromSlice(ta.IP); ok && l.s.isRelayAddr(a) {
				_ = c.Close()
				continue
			}
		}
		return c, nil
	}
}
