package netif

// HostIface describes one network interface of the host.
type HostIface struct {
	Name         string
	Kind         string // link type as the kernel names it: "device", "vlan", "bridge", "veth", ...
	Up           bool   // administratively up
	Carrier      bool   // link detected
	Loopback     bool
	DefaultRoute bool     // carries a default route: the management side of the host
	Relay        bool     // created by the engine (alias "nsrelay")
	Addrs        []string // addresses with prefix length, without IPv6 link-local ones
}
