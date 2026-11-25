package pkt

import (
	"net/netip"
)

// The Internet checksum (RFC 1071) of IPv4 headers and UDP datagrams.

// Checksum is the Internet checksum (RFC 1071) of b.
func Checksum(b []byte) uint16 {
	return ^fold(sum(0, b))
}

// UDPChecksum computes the UDP checksum over the IPv4 pseudo header and the UDP segment (header + payload)
// with its checksum field taken as zero. A result of zero is transmitted as 0xffff (RFC 768).
func UDPChecksum(src, dst netip.Addr, segment []byte) uint16 {
	s4, d4 := src.As4(), dst.As4()
	var acc uint32
	acc = sum(acc, s4[:])
	acc = sum(acc, d4[:])
	acc += ProtoUDP
	acc += uint32(len(segment))
	acc = sum(acc, segment[:6])
	acc = sum(acc, segment[8:])
	c := ^fold(acc)
	if c == 0 {
		c = 0xffff
	}
	return c
}

func sum(acc uint32, b []byte) uint32 {
	for len(b) >= 2 {
		acc += uint32(b[0])<<8 | uint32(b[1])
		b = b[2:]
	}
	if len(b) == 1 {
		acc += uint32(b[0]) << 8
	}
	return acc
}

func fold(acc uint32) uint16 {
	for acc>>16 != 0 {
		acc = acc&0xffff + acc>>16
	}
	return uint16(acc)
}
