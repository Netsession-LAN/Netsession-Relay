//go:build linux

package lab

import (
	"bytes"
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Netsession2024/Netsession-Relay/internal/pkt"
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
	f := pkt.Frame{SrcMAC: pkt.MAC(ifi.HardwareAddr), DstMAC: pkt.BroadcastMAC, TTL: 64, ID: 1,
		Src: o.Src, Dst: o.Dst, SrcPort: o.SrcPort, DstPort: o.DstPort, UDPChecksum: 1, Payload: o.Payload}
	if o.Dst.IsMulticast() {
		f.DstMAC = pkt.MulticastMAC(o.Dst)
		f.TTL = 1
	}
	for i := range max(o.Count, 1) {
		f.ID = uint16(i + 1)
		b, err := pkt.Build(nil, &f)
		if err != nil {
			return err
		}
		if _, err := unix.Write(fd, b); err != nil {
			return fmt.Errorf("lab send: %w", err)
		}
		if o.Interval > 0 && i < o.Count-1 {
			time.Sleep(time.Duration(o.Interval) * time.Millisecond)
		}
	}
	return nil
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
		if interval > 0 && i < count-1 {
			time.Sleep(time.Duration(interval) * time.Millisecond)
		}
	}
	return nil
}

// Expect waits for matching UDP frames on opts.Iface and returns them.
// With opts.None it returns an error if anything matches.
func Expect(o ExpectOptions) ([]pkt.Frame, error) {
	fd, _, err := openRaw(o.Iface)
	if err != nil {
		return nil, fmt.Errorf("lab expect: %w", err)
	}
	defer unix.Close(fd)
	want := max(o.Count, 1)
	deadline := time.Now().Add(time.Duration(max(o.Timeout, 100)) * time.Millisecond)
	tv := unix.NsecToTimeval((50 * time.Millisecond).Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	buf := make([]byte, 2048)
	var got []pkt.Frame
	for time.Now().Before(deadline) {
		n, err := unix.Read(fd, buf)
		if err != nil {
			continue
		}
		f, err := pkt.Parse(append([]byte{}, buf[:n]...))
		if err != nil || f.DstPort != o.DstPort {
			continue
		}
		if o.Src.IsValid() && f.Src != o.Src {
			continue
		}
		if o.Dst.IsValid() && f.Dst != o.Dst {
			continue
		}
		got = append(got, f)
		if !o.None && o.Max == 0 && len(got) >= want {
			return got, nil
		}
	}
	if o.Max > 0 {
		if len(got) < want || len(got) > o.Max {
			return got, fmt.Errorf("%s port %d: got %d frame(s), want %d..%d", o.Iface, o.DstPort, len(got), want, o.Max)
		}
		return got, nil
	}
	if o.None {
		if len(got) > 0 {
			return got, fmt.Errorf("expected nothing on %s port %d, got %d frame(s)", o.Iface, o.DstPort, len(got))
		}
		return nil, nil
	}
	return got, fmt.Errorf("timeout on %s: got %d of %d frame(s) for port %d", o.Iface, len(got), want, o.DstPort)
}

// A2SServer answers A2S_INFO queries like a Steam/Source server: a query without challenge gets the challenge
// 'A', a query with it gets the info 'I' (name, map de_lab, 3/16 players). It returns the number of info answers.
func A2SServer(o A2SOptions) (int, error) {
	fd, ifi, err := openRaw(o.Iface)
	if err != nil {
		return 0, fmt.Errorf("lab a2s-server: %w", err)
	}
	defer unix.Close(fd)
	port := o.Port
	if port == 0 {
		port = 27015
	}
	query := append([]byte{0xff, 0xff, 0xff, 0xff, 'T'}, []byte("Source Engine Query\x00")...)
	challenge := []byte{0x4c, 0x61, 0x62, 0x21}
	tv := unix.NsecToTimeval((50 * time.Millisecond).Nanoseconds())
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	deadline := time.Now().Add(time.Duration(max(o.Timeout, 100)) * time.Millisecond)
	buf := make([]byte, 2048)
	infos := 0
	for time.Now().Before(deadline) {
		n, err := unix.Read(fd, buf)
		if err != nil {
			continue
		}
		f, err := pkt.Parse(append([]byte{}, buf[:n]...))
		if err != nil || f.DstPort != port || !bytes.HasPrefix(f.Payload, query) {
			continue
		}
		var answer []byte
		switch {
		case len(f.Payload) == len(query):
			answer = append([]byte{0xff, 0xff, 0xff, 0xff, 'A'}, challenge...)
		case len(f.Payload) == len(query)+4 && bytes.Equal(f.Payload[len(query):], challenge):
			answer = []byte{0xff, 0xff, 0xff, 0xff, 'I', 17}
			for _, s := range []string{o.Name, "de_lab", "cs2", "Counter-Strike 2"} {
				answer = append(append(answer, s...), 0)
			}
			answer = append(answer, 0xda, 0x02, 3, 16, 0)
			infos++
		default:
			continue
		}
		out := pkt.Frame{DstMAC: f.SrcMAC, SrcMAC: pkt.MAC(ifi.HardwareAddr), TTL: 64, ID: uint16(infos + 1), Src: o.IP,
			Dst: f.Src, SrcPort: port, DstPort: f.SrcPort, UDPChecksum: 1, Payload: answer}
		b, err := pkt.Build(nil, &out)
		if err != nil {
			return infos, err
		}
		if _, err := unix.Write(fd, b); err != nil {
			return infos, fmt.Errorf("lab a2s-server: %w", err)
		}
	}
	return infos, nil
}
