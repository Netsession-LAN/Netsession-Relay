//go:build linux

package engine

import (
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/unix"

	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
)

// memberships keeps IGMP group memberships per VLAN interface so that IGMP snooping on the switches lets
// the relayed multicast groups through (docs/02-plan.md 4.3 "Multicast"). One UDP helper socket per
// interface (Linux allows 20 memberships per socket by default; we need a handful).
type memberships struct {
	fds    map[int]int                 // ifindex -> socket
	joined map[int]map[netip.Addr]bool // ifindex -> groups
}

func newMemberships() *memberships {
	return &memberships{fds: map[int]int{}, joined: map[int]map[netip.Addr]bool{}}
}

// sync joins every multicast group of the table on every VLAN interface and leaves groups/interfaces that
// are gone.
func (m *memberships) sync(t *Table) error {
	want := map[int]map[netip.Addr]bool{}
	for _, r := range t.rules {
		if r.Kind != gamedb.KindMulticast {
			continue
		}
		for _, v := range t.VLANs {
			idx := v.Ifindex
			if want[idx] == nil {
				want[idx] = map[netip.Addr]bool{}
			}
			want[idx][r.group] = true
		}
	}
	var errs []error
	for idx, groups := range m.joined {
		for g := range groups {
			if !want[idx][g] {
				errs = append(errs, m.op(idx, g, unix.IP_DROP_MEMBERSHIP))
				delete(groups, g)
			}
		}
		if len(groups) == 0 {
			unix.Close(m.fds[idx])
			delete(m.fds, idx)
			delete(m.joined, idx)
		}
	}
	for idx, groups := range want {
		for g := range groups {
			if m.joined[idx][g] {
				continue
			}
			if err := m.op(idx, g, unix.IP_ADD_MEMBERSHIP); err != nil {
				errs = append(errs, err)
				continue
			}
			if m.joined[idx] == nil {
				m.joined[idx] = map[netip.Addr]bool{}
			}
			m.joined[idx][g] = true
		}
	}
	return errors.Join(errs...)
}

func (m *memberships) op(ifindex int, group netip.Addr, opt int) error {
	fd, ok := m.fds[ifindex]
	if !ok {
		var err error
		fd, err = unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return err
		}
		m.fds[ifindex] = fd
	}
	mreq := &unix.IPMreqn{Multiaddr: group.As4(), Ifindex: int32(ifindex)}
	if err := unix.SetsockoptIPMreqn(fd, unix.IPPROTO_IP, opt, mreq); err != nil {
		return fmt.Errorf("ifindex %d group %s: %w", ifindex, group, err)
	}
	return nil
}

func (m *memberships) close() {
	for _, fd := range m.fds {
		unix.Close(fd) // closing drops all memberships
	}
	m.fds = map[int]int{}
	m.joined = map[int]map[netip.Addr]bool{}
}
