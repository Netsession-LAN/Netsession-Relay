//go:build linux

package lab

import (
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
