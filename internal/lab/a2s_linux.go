//go:build linux

package lab

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// The fake Steam/Source server of the lab on a UDP socket (the exchange itself is in a2s.go).

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
