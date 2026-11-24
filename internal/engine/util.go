package engine

import (
	"net"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
)

func parseMAC(s string) (pkt.MAC, error) {
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 {
		return pkt.MAC{}, &net.AddrError{Err: "invalid MAC", Addr: s}
	}
	return pkt.MAC(hw), nil
}

func parsePorts(spec string) ([]uint16, error) {
	s, err := portspec.Parse(spec)
	if err != nil {
		return nil, err
	}
	return s.Ports(), nil
}
