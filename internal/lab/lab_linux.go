//go:build linux

package lab

import (
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func openRaw(iface string) (int, *net.Interface, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return -1, nil, err
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, nil, err
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_PACKET, unix.PACKET_IGNORE_OUTGOING, 1); err != nil {
		unix.Close(fd)
		return -1, nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_IP), Ifindex: ifi.Index}); err != nil {
		unix.Close(fd)
		return -1, nil, err
	}
	return fd, ifi, nil
}

// Send transmits opts.Count raw frames built from opts (source MAC = interface MAC).
func Send(o SendOptions) error {
	fd, ifi, err := openRaw(o.Iface)
	if err != nil {
		return fmt.Errorf("lab send: %w", err)
	}
	defer unix.Close(fd)
	f := sendFrame(o, pkt.MAC(ifi.HardwareAddr))
	for i := range max(o.Count, 1) {
		f.ID = uint16(i + 1)
		b, err := pkt.Build(nil, &f)
		if err != nil {
			return err
		}
		if _, err := unix.Write(fd, b); err != nil {
			return fmt.Errorf("lab send: %w", err)
		}
		pauseBetween(i, o.Count, o.Interval)
	}
	return nil
}

// sendFrame is the frame Send transmits: to the broadcast MAC, or to the group's MAC with TTL 1 for multicast.
func sendFrame(o SendOptions, mac pkt.MAC) pkt.Frame {
	f := pkt.Frame{SrcMAC: mac, DstMAC: pkt.BroadcastMAC, TTL: 64, ID: 1,
		Src: o.Src, Dst: o.Dst, SrcPort: o.SrcPort, DstPort: o.DstPort, UDPChecksum: 1, Payload: o.Payload}
	if o.Dst.IsMulticast() {
		f.DstMAC = pkt.MulticastMAC(o.Dst)
		f.TTL = 1
	}
	return f
}

// pauseBetween waits interval milliseconds after frame i of count, except after the last one.
func pauseBetween(i, count, interval int) {
	if interval > 0 && i < count-1 {
		time.Sleep(time.Duration(interval) * time.Millisecond)
	}
}

// SendRaw transmits count Ethernet frames built by build (which gets the interface MAC as source address),
// interval milliseconds apart. For frames that are not IPv4/UDP, e.g. relay beacons.
func SendRaw(iface string, count, interval int, build func(mac pkt.MAC) []byte) error {
	fd, ifi, err := openRaw(iface)
	if err != nil {
		return fmt.Errorf("lab send: %w", err)
	}
	defer unix.Close(fd)
	for i := range max(count, 1) {
		if _, err := unix.Write(fd, build(pkt.MAC(ifi.HardwareAddr))); err != nil {
			return fmt.Errorf("lab send: %w", err)
		}
		pauseBetween(i, count, interval)
	}
	return nil
}

// receiveFrames reads IPv4 frames from fd for timeout milliseconds (at least 100) and hands every frame that
// parses to handle. It stops early when handle reports done or an error, and returns that error.
func receiveFrames(fd, timeout int, handle func(pkt.Frame) (done bool, err error)) error {
	deadline := time.Now().Add(time.Duration(max(timeout, 100)) * time.Millisecond)
	tv := unix.NsecToTimeval((50 * time.Millisecond).Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		f, ok := readFrame(fd, buf)
		if !ok {
			continue
		}
		if done, err := handle(f); done || err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads one frame into buf and parses a copy of it; false on a read timeout or a frame that does not
// parse.
func readFrame(fd int, buf []byte) (pkt.Frame, bool) {
	n, err := unix.Read(fd, buf)
	if err != nil {
		return pkt.Frame{}, false
	}
	f, err := pkt.Parse(append([]byte{}, buf[:n]...))
	return f, err == nil
}

// Expect waits for matching UDP frames on opts.Iface and returns them.
// With opts.None it returns an error if anything matches.
func Expect(o ExpectOptions) ([]pkt.Frame, error) {
	fd, _, err := openRaw(o.Iface)
	if err != nil {
		return nil, fmt.Errorf("lab expect: %w", err)
	}
	defer unix.Close(fd)
	var got []pkt.Frame
	_ = receiveFrames(fd, o.Timeout, func(f pkt.Frame) (bool, error) {
		if !o.matches(f) {
			return false, nil
		}
		got = append(got, f)
		return o.enough(got), nil
	})
	return o.result(got)
}

// A2SServer answers A2S_INFO queries like a Steam/Source server: a query without challenge gets the challenge
// 'A', a query with it gets the info 'I' (name, map de_lab, 3/16 players). It returns the number of info answers.
func A2SServer(o A2SOptions) (int, error) {
	fd, ifi, err := openRaw(o.Iface)
	if err != nil {
		return 0, fmt.Errorf("lab a2s-server: %w", err)
	}
	defer unix.Close(fd)
	if o.Port == 0 {
		o.Port = 27015
	}
	s := a2sResponder{fd: fd, mac: pkt.MAC(ifi.HardwareAddr), o: o}
	err = receiveFrames(fd, o.Timeout, s.handle)
	return s.infos, err
}

// a2sResponder is the state of one A2SServer run; o.Port is set.
type a2sResponder struct {
	fd    int
	mac   pkt.MAC
	o     A2SOptions
	infos int // info answers sent
}

// handle answers a query to the server's port; it stops the run when an answer cannot be sent.
func (s *a2sResponder) handle(f pkt.Frame) (bool, error) {
	if f.DstPort != s.o.Port {
		return false, nil
	}
	answer, info, ok := a2sAnswer(f.Payload, s.o.Name)
	if !ok {
		return false, nil
	}
	if info {
		s.infos++
	}
	if err := s.send(f, answer); err != nil {
		return true, err
	}
	return false, nil
}

// send transmits answer to the sender of the query f.
func (s *a2sResponder) send(f pkt.Frame, answer []byte) error {
	out := pkt.Frame{DstMAC: f.SrcMAC, SrcMAC: s.mac, TTL: 64, ID: uint16(s.infos + 1), Src: s.o.IP,
		Dst: f.Src, SrcPort: s.o.Port, DstPort: f.SrcPort, UDPChecksum: 1, Payload: answer}
	b, err := pkt.Build(nil, &out)
	if err != nil {
		return err
	}
	if _, err := unix.Write(s.fd, b); err != nil {
		return fmt.Errorf("lab a2s-server: %w", err)
	}
	return nil
}
