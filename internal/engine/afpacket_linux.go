//go:build linux

package engine

import (
	"fmt"
	"time"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// rxFilter passes only frames the relay may care about: destination MAC with group bit (broadcast or
// multicast) and either a relay beacon (EtherType 0x88B5) or IPv4/UDP that is not a fragment. Offsets are
// for untagged Ethernet II (the socket is bound to a VLAN subinterface, the kernel removed the 802.1Q tag).
var rxFilter = []bpf.Instruction{
	/* 0 */ bpf.LoadAbsolute{Off: 0, Size: 1}, // dst MAC byte 0
	/* 1 */ bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x01, SkipFalse: 8},
	/* 2 */ bpf.LoadAbsolute{Off: 12, Size: 2}, // EtherType
	/* 3 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: BeaconEtherType, SkipTrue: 5},
	/* 4 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x0800, SkipFalse: 5},
	/* 5 */ bpf.LoadAbsolute{Off: 23, Size: 1}, // IPv4 protocol
	/* 6 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: 17, SkipFalse: 3},
	/* 7 */ bpf.LoadAbsolute{Off: 20, Size: 2}, // flags + fragment offset
	/* 8 */ bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x3fff, SkipTrue: 1},
	/* 9 */ bpf.RetConstant{Val: 65535},
	/* 10 */ bpf.RetConstant{Val: 0},
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// packetSock is an AF_PACKET/SOCK_RAW socket bound to one interface.
type packetSock struct {
	fd      int
	ifindex int
	name    string
}

// openPacketSock opens a receive+send socket on ifindex. Outgoing frames of this host are not received
// (PACKET_IGNORE_OUTGOING), reads time out every readTimeout so receivers can stop.
func openPacketSock(name string, ifindex int, readTimeout time.Duration) (*packetSock, error) {
	// protocol 0: receive nothing until bind, so no frames of other interfaces slip in before the filter
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: socket: %w (needs CAP_NET_RAW)", name, err)
	}
	fail := func(step string, err error) (*packetSock, error) {
		unix.Close(fd)
		return nil, fmt.Errorf("%s: %s: %w", name, step, err)
	}
	raw, err := bpf.Assemble(rxFilter)
	if err != nil {
		return fail("assemble filter", err)
	}
	filter := make([]unix.SockFilter, len(raw))
	for i, r := range raw {
		filter[i] = unix.SockFilter{Code: r.Op, Jt: r.Jt, Jf: r.Jf, K: r.K}
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &prog); err != nil {
		return fail("attach filter", err)
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_PACKET, unix.PACKET_IGNORE_OUTGOING, 1); err != nil {
		return fail("PACKET_IGNORE_OUTGOING", err)
	}
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20)
	tv := unix.NsecToTimeval(readTimeout.Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return fail("SO_RCVTIMEO", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: ifindex}); err != nil {
		return fail("bind", err)
	}
	return &packetSock{fd: fd, ifindex: ifindex, name: name}, nil
}

// read returns the next frame; ok=false on timeout.
func (s *packetSock) read(buf []byte) (n int, ok bool, err error) {
	n, err = unix.Read(s.fd, buf)
	if err == unix.EAGAIN || err == unix.EINTR {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// write sends a complete Ethernet frame out of the bound interface.
func (s *packetSock) write(frame []byte) error {
	_, err := unix.Write(s.fd, frame)
	return err
}

func (s *packetSock) close() error { return unix.Close(s.fd) }
