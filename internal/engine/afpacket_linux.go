//go:build linux

package engine

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// packetSock is an AF_PACKET/SOCK_RAW socket bound to one interface.
//
// One goroutine reads (its receiver); every receiver may write. A socket is closed while others can still hold
// it: an apply swaps in the new state and then closes the sockets of interfaces that are gone, but a receiver
// that loaded the old state just before the swap may be about to send to one of them. The kernel hands a closed
// descriptor number to the next socket or file that is opened, so such a late write would go to something else.
// The lock makes close wait for writes in flight and lets later writes fail with errSockClosed. It is a read lock
// on the hot path: writers do not wait for each other and nothing is allocated.
type packetSock struct {
	fd      int
	ifindex int
	name    string

	mu     sync.RWMutex // write: read lock; close: write lock
	closed bool
}

// errSockClosed is returned by write after close.
var errSockClosed = errors.New("packet socket closed")

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

// read returns the next frame; ok=false on timeout. Only the receiver that owns the socket reads, and close is
// called after that receiver stopped (receiver.halt), so read does not take the lock.
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

// write sends a complete Ethernet frame out of the bound interface; errSockClosed after close.
func (s *packetSock) write(frame []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errSockClosed
	}
	_, err := unix.Write(s.fd, frame)
	return err
}

// close closes the socket once; it waits for writes in flight.
func (s *packetSock) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return unix.Close(s.fd)
}
