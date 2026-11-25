package web

import (
	"net/netip"
	"slices"
	"strconv"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The network rows of step 2 of the setup: each row becomes a network on the trunk with the relay address at the
// same host part.

// setupHostPart reads the host part of the relay address (the same in every network), or says what is wrong.
func setupHostPart(field string, T translator) (int, string) {
	host, err := strconv.Atoi(field)
	if err != nil || host < 1 || host > 65534 {
		return 0, T("Die Stelle der Relay-Adresse muss eine Zahl sein, z. B. 250.")
	}
	return host, ""
}

// setupVLANs turns the filled rows into networks on the trunk; the relay address of each is the host-th address
// of its network. msg is "" or what is wrong with which row.
func setupVLANs(rows []setupRow, trunk string, host int, T translator) (vlans []store.VLANConfig, msg string) {
	for n, row := range rows {
		if row.empty() {
			continue
		}
		v, msg := setupVLAN(row, trunk, host, vlans, T)
		if msg != "" {
			return nil, T("Netz in Zeile %d: %s", n+1, msg)
		}
		vlans = append(vlans, v)
	}
	return vlans, ""
}

// setupVLAN turns one row into a network on the trunk; before are the networks of the rows above. msg is "" or
// what is wrong with the row.
func setupVLAN(row setupRow, trunk string, host int, before []store.VLANConfig, T translator) (store.VLANConfig, string) {
	relay, msg := setupRelayAddr(row.CIDR, host, T)
	if msg != "" {
		return store.VLANConfig{}, msg
	}
	v, msg := vlanForm{VID: row.VID, Name: row.Name, Group: row.Group, CIDR: row.CIDR, RelayIP: relay,
		Attach: compile.AttachTrunk, Parent: trunk, Enable: true}.parse(T)
	switch {
	case msg != "":
		return v, msg
	case !slices.Contains(defaultGroups, v.Group):
		return v, T("Bitte eine der Gruppen wählen.")
	case slices.ContainsFunc(before, func(x store.VLANConfig) bool { return x.VID == v.VID }):
		return v, T("VLAN %d gibt es schon.", v.VID)
	}
	return v, ""
}

// setupRelayAddr is the host-th address of the network cidr. It is "" when cidr is no IPv4 network with room
// for hosts (/30 or larger), which the check of the row then reports; msg is set when host does not fit.
func setupRelayAddr(cidr string, host int, T translator) (relay, msg string) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() || p.Bits() > 30 {
		return "", ""
	}
	size := 1 << (32 - p.Bits())
	if host > size-2 {
		return "", T("Die Stelle %d passt nicht in %s (höchstens %d).", host, p.Masked(), size-2)
	}
	b := p.Masked().Addr().As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v += uint32(host)
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}).String(), ""
}
