// Package pkt parses and builds Ethernet, IPv4 and UDP frames, computes checksums and broadcast addresses.
// It is pure logic and runs on every OS, including Windows in tests.
//
// Frames are untagged Ethernet II: the engine reads from VLAN subinterfaces, where the kernel has already
// removed the 802.1Q tag.
package pkt

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// Header sizes and protocol numbers.
const (
	EthHeaderLen  = 14
	IPv4HeaderLen = 20 // without options
	UDPHeaderLen  = 8

	EtherTypeIPv4 = 0x0800
	ProtoUDP      = 17

	// MaxUDPPayload is the largest UDP payload in a 1500-byte MTU without IP options.
	MaxUDPPayload = 1500 - IPv4HeaderLen - UDPHeaderLen
)

// MAC is an Ethernet hardware address.
type MAC [6]byte

// BroadcastMAC is ff:ff:ff:ff:ff:ff.
var BroadcastMAC = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// IsGroup reports whether the I/G bit is set (broadcast or multicast).
func (m MAC) IsGroup() bool { return m[0]&1 == 1 }

func (m MAC) String() string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
}

// Errors returned by Parse. Callers count drops by these reasons.
var (
	ErrShort       = errors.New("pkt: frame too short")
	ErrNotIPv4     = errors.New("pkt: not IPv4")
	ErrNotUDP      = errors.New("pkt: not UDP")
	ErrFragment    = errors.New("pkt: IPv4 fragment")
	ErrBadIPHeader = errors.New("pkt: bad IPv4 header")
	ErrBadUDP      = errors.New("pkt: bad UDP length")
)

// Frame is a parsed Ethernet/IPv4/UDP frame. Payload aliases the input buffer of Parse.
type Frame struct {
	DstMAC, SrcMAC MAC
	TOS            uint8
	ID             uint16
	DontFragment   bool
	TTL            uint8
	Src, Dst       netip.Addr
	SrcPort        uint16
	DstPort        uint16
	// UDPChecksum is the checksum as received. Zero means the sender did not compute one; Build keeps it zero.
	UDPChecksum uint16
	Payload     []byte
}

// Parse decodes an Ethernet II frame carrying an unfragmented IPv4/UDP datagram. Trailing Ethernet padding
// is ignored. IPv4 options are skipped (and dropped by Build).
func Parse(b []byte) (Frame, error) {
	var f Frame
	if len(b) < EthHeaderLen+IPv4HeaderLen+UDPHeaderLen {
		return f, ErrShort
	}
	copy(f.DstMAC[:], b[0:6])
	copy(f.SrcMAC[:], b[6:12])
	if binary.BigEndian.Uint16(b[12:14]) != EtherTypeIPv4 {
		return f, ErrNotIPv4
	}
	ip := b[EthHeaderLen:]
	ihl, total, err := ipv4Lengths(ip)
	if err != nil {
		return f, err
	}
	flagsFrag := binary.BigEndian.Uint16(ip[6:8])
	if flagsFrag&0x3fff != 0 { // MF set or offset > 0
		return f, ErrFragment
	}
	if ip[9] != ProtoUDP {
		return f, ErrNotUDP
	}
	f.TOS = ip[1]
	f.ID = binary.BigEndian.Uint16(ip[4:6])
	f.DontFragment = flagsFrag&0x4000 != 0
	f.TTL = ip[8]
	f.Src = netip.AddrFrom4([4]byte(ip[12:16]))
	f.Dst = netip.AddrFrom4([4]byte(ip[16:20]))
	err = f.parseUDP(ip[ihl:total])
	return f, err
}

// ipv4Lengths returns the header length and the total length of an IPv4 packet after checking that both fit.
func ipv4Lengths(ip []byte) (ihl, total int, err error) {
	if ip[0]>>4 != 4 {
		return 0, 0, ErrNotIPv4
	}
	ihl = int(ip[0]&0x0f) * 4
	total = int(binary.BigEndian.Uint16(ip[2:4]))
	if ihl < IPv4HeaderLen || total < ihl+UDPHeaderLen || total > len(ip) {
		return 0, 0, ErrBadIPHeader
	}
	return ihl, total, nil
}

// parseUDP fills the UDP header fields and the payload of the segment udp into f.
func (f *Frame) parseUDP(udp []byte) error {
	ulen := int(binary.BigEndian.Uint16(udp[4:6]))
	if ulen < UDPHeaderLen || ulen > len(udp) {
		return ErrBadUDP
	}
	f.SrcPort = binary.BigEndian.Uint16(udp[0:2])
	f.DstPort = binary.BigEndian.Uint16(udp[2:4])
	f.UDPChecksum = binary.BigEndian.Uint16(udp[6:8])
	f.Payload = udp[UDPHeaderLen:ulen]
	return nil
}

// Len returns the size of the frame Build produces.
func (f *Frame) Len() int { return EthHeaderLen + IPv4HeaderLen + UDPHeaderLen + len(f.Payload) }

// Build serialises f into buf (grown if too small) and returns the frame. The IPv4 header is written without
// options and with a fresh header checksum. The UDP checksum is recomputed, unless f.UDPChecksum is zero
// (sender opted out), in which case it stays zero.
func Build(buf []byte, f *Frame) ([]byte, error) {
	if !f.Src.Is4() || !f.Dst.Is4() {
		return nil, errors.New("pkt: Build needs IPv4 addresses")
	}
	if len(f.Payload) > 0xffff-IPv4HeaderLen-UDPHeaderLen {
		return nil, errors.New("pkt: payload too large")
	}
	n := f.Len()
	if cap(buf) < n {
		buf = make([]byte, n)
	}
	b := buf[:n]

	copy(b[0:6], f.DstMAC[:])
	copy(b[6:12], f.SrcMAC[:])
	binary.BigEndian.PutUint16(b[12:14], EtherTypeIPv4)

	ip := b[EthHeaderLen : EthHeaderLen+IPv4HeaderLen]
	ip[0] = 0x45
	ip[1] = f.TOS
	binary.BigEndian.PutUint16(ip[2:4], uint16(IPv4HeaderLen+UDPHeaderLen+len(f.Payload)))
	binary.BigEndian.PutUint16(ip[4:6], f.ID)
	var flags uint16
	if f.DontFragment {
		flags = 0x4000
	}
	binary.BigEndian.PutUint16(ip[6:8], flags)
	ip[8] = f.TTL
	ip[9] = ProtoUDP
	ip[10], ip[11] = 0, 0
	src, dst := f.Src.As4(), f.Dst.As4()
	copy(ip[12:16], src[:])
	copy(ip[16:20], dst[:])
	binary.BigEndian.PutUint16(ip[10:12], Checksum(ip))

	udp := b[EthHeaderLen+IPv4HeaderLen:]
	binary.BigEndian.PutUint16(udp[0:2], f.SrcPort)
	binary.BigEndian.PutUint16(udp[2:4], f.DstPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(UDPHeaderLen+len(f.Payload)))
	udp[6], udp[7] = 0, 0
	copy(udp[UDPHeaderLen:], f.Payload)
	if f.UDPChecksum != 0 {
		binary.BigEndian.PutUint16(udp[6:8], UDPChecksum(f.Src, f.Dst, udp))
	}
	return b, nil
}

// LimitedBroadcast is 255.255.255.255.
var LimitedBroadcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})

// SubnetBroadcast returns the directed broadcast address of an IPv4 prefix (all host bits set).
func SubnetBroadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	v := binary.BigEndian.Uint32(a[:]) | ^uint32(0)>>p.Bits()
	if p.Bits() == 32 {
		v = binary.BigEndian.Uint32(a[:])
	}
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], v)
	return netip.AddrFrom4(out)
}

// IsBroadcastFor reports whether a is the limited broadcast or the directed broadcast of p.
func IsBroadcastFor(a netip.Addr, p netip.Prefix) bool {
	return a == LimitedBroadcast || (p.Bits() < 31 && a == SubnetBroadcast(p))
}

// MulticastMAC returns the Ethernet address for an IPv4 multicast group (01:00:5e + low 23 bits, RFC 1112).
func MulticastMAC(group netip.Addr) MAC {
	g := group.As4()
	return MAC{0x01, 0x00, 0x5e, g[1] & 0x7f, g[2], g[3]}
}
