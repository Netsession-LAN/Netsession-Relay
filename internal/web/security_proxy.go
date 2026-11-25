package web

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// The client address behind reverse proxies: trusted proxies (NSRELAY_TRUSTED_PROXIES) and the forwarding
// headers they set.

// ParseProxies parses NSRELAY_TRUSTED_PROXIES: addresses or networks (CIDR), separated by commas or spaces.
func ParseProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		p, ok := proxyPrefix(f)
		if !ok {
			return nil, errors.New("trusted proxy " + f + ": not an address or network")
		}
		out = append(out, p)
	}
	return out, nil
}

// proxyPrefix reads one trusted proxy: a network, or an address as a network of its own.
func proxyPrefix(f string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(f); err == nil {
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(f)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
}

func (s *Server) trustedProxy(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// fromTrustedProxy reports whether the request was handed over by a configured reverse proxy.
func (s *Server) fromTrustedProxy(r *http.Request) bool {
	a, err := netip.ParseAddr(remoteHost(r))
	return err == nil && s.trustedProxy(a)
}

// clientIP is the address of the browser. Forwarding headers count only when the TCP peer is a trusted proxy
// (NSRELAY_TRUSTED_PROXIES); then the right-most address of X-Forwarded-For that is not a trusted proxy itself
// is the client (everything left of it can be invented by the client). Without such an entry CF-Connecting-IP
// is used. Without trusted proxies it is always the TCP peer.
func (s *Server) clientIP(r *http.Request) string {
	remote := remoteHost(r)
	if !s.fromTrustedProxy(r) {
		return remote
	}
	if a, ok := s.forwardedFor(r); ok {
		return a.Unmap().String()
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
		return a.Unmap().String()
	}
	return remote
}

// forwardedFor is the right-most address of X-Forwarded-For that is not a trusted proxy; ok is false when there is
// none, or when garbage comes first from the right.
func (s *Server) forwardedFor(r *http.Request) (netip.Addr, bool) {
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		if h == "" {
			continue
		}
		a, err := netip.ParseAddr(h)
		if err != nil {
			break // garbage: do not look further left
		}
		if !s.trustedProxy(a) {
			return a, true
		}
	}
	return netip.Addr{}, false
}
