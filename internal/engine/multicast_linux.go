//go:build linux

package engine

import (
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/unix"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// memberships keeps IGMP group memberships per VLAN interface so that IGMP snooping on the switches lets
// the relayed multicast groups through. One UDP helper socket per
// interface (Linux allows 20 memberships per socket by default; we need a handful).
type memberships struct {
	fds    map[int]int // ifindex -> socket
	joined groupSet    // ifindex -> groups
}

func newMemberships() *memberships {
	return &memberships{fds: map[int]int{}, joined: groupSet{}}
}

// sync joins every multicast group of the table on every VLAN interface and leaves groups/interfaces that
// are gone.
func (m *memberships) sync(t *Table) error {
	want := wantedGroups(t)
	errs := m.leave(want)
	errs = append(errs, m.join(want)...)
	return errors.Join(errs...)
}

// groupSet holds multicast groups per interface (ifindex).
type groupSet map[int]map[netip.Addr]bool

func (s groupSet) add(ifindex int, g netip.Addr) {
	if s[ifindex] == nil {
		s[ifindex] = map[netip.Addr]bool{}
	}
	s[ifindex][g] = true
}

// wantedGroups lists every multicast group of the table on every VLAN interface.
func wantedGroups(t *Table) groupSet {
	want := groupSet{}
	for _, r := range t.rules {
		if r.Kind != gamedb.KindMulticast {
			continue
		}
		for _, v := range t.VLANs {
			want.add(v.Ifindex, r.group)
		}
	}
	return want
}

// leave drops the memberships that are no longer wanted and closes the sockets of interfaces without any.
func (m *memberships) leave(want groupSet) []error {
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
	return errs
}

// join adds the wanted memberships that are missing.
func (m *memberships) join(want groupSet) []error {
	var errs []error
	for idx, groups := range want {
		for g := range groups {
			if m.joined[idx][g] {
				continue
			}
			if err := m.op(idx, g, unix.IP_ADD_MEMBERSHIP); err != nil {
				errs = append(errs, err)
				continue
			}
			m.joined.add(idx, g)
		}
	}
	return errs
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
	m.joined = groupSet{}
}
