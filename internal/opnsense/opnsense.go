// Package opnsense imports relay settings from OPNsense config.xml exports (plugin os-udpbroadcastrelay) and
// compares them with the game database. The database (internet research) is the reference; the box configuration
// may be wrong (decision 2026-11-16). It turns them into a report (docs/02-plan.md 5.4): which game each port belongs to, duplicates across boxes,
// disabled entries, source rewriting for games that need the original IP, noise services, unknown ports.
// The files contain secrets; they are only parsed, never stored.
package opnsense

import (
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Iface is an OPNsense interface (optN).
type Iface struct {
	Name   string // optN / lan
	Device string // vtnet2
	Descr  string // 1110_Tisch_1
	IP     string
	Subnet string
}

// Relay is one udpbroadcastrelay entry.
type Relay struct {
	UUID        string
	Enabled     bool
	InstanceID  int
	Port        int
	Interfaces  []string // descriptions (or opt names if unknown)
	Multicast   string
	Source      string // -s value: "" = original, 1.1.1.1 / 1.1.1.2 = relay address
	Description string
}

// Box is one parsed config.xml.
type Box struct {
	Name       string // file label or hostname
	Hostname   string
	Interfaces map[string]Iface
	Relays     []Relay
}

type xmlConfig struct {
	System struct {
		Hostname string `xml:"hostname"`
		Domain   string `xml:"domain"`
	} `xml:"system"`
	Interfaces struct {
		Items []struct {
			XMLName xml.Name
			If      string `xml:"if"`
			Descr   string `xml:"descr"`
			IPAddr  string `xml:"ipaddr"`
			Subnet  string `xml:"subnet"`
		} `xml:",any"`
	} `xml:"interfaces"`
	Relays []struct {
		UUID        string `xml:"uuid,attr"`
		Enabled     string `xml:"enabled"`
		Interfaces  string `xml:"interfaces"`
		Multicast   string `xml:"multicastaddress"`
		Source      string `xml:"sourceaddress"`
		ListenPort  string `xml:"listenport"`
		InstanceID  string `xml:"InstanceID"`
		Description string `xml:"description"`
	} `xml:"OPNsense>udpbroadcastrelays>udpbroadcastrelay"`
}

// Parse reads a config.xml (full export or sanitised excerpt).
func Parse(name string, r io.Reader) (Box, error) {
	var c xmlConfig
	dec := xml.NewDecoder(io.LimitReader(r, 64<<20))
	if err := dec.Decode(&c); err != nil {
		return Box{}, fmt.Errorf("%s: %w", name, err)
	}
	b := Box{Name: name, Hostname: c.System.Hostname, Interfaces: map[string]Iface{}}
	if c.System.Domain != "" && b.Hostname != "" {
		b.Hostname += "." + c.System.Domain
	}
	for _, it := range c.Interfaces.Items {
		b.Interfaces[it.XMLName.Local] = Iface{Name: it.XMLName.Local, Device: it.If, Descr: it.Descr, IP: it.IPAddr, Subnet: it.Subnet}
	}
	for _, x := range c.Relays {
		port, err := strconv.Atoi(strings.TrimSpace(x.ListenPort))
		if err != nil || port < 1 || port > 65535 {
			return Box{}, fmt.Errorf("%s: relay %s: invalid port %q", name, x.UUID, x.ListenPort)
		}
		id, _ := strconv.Atoi(strings.TrimSpace(x.InstanceID))
		rel := Relay{UUID: x.UUID, Enabled: x.Enabled == "1", InstanceID: id, Port: port,
			Multicast: strings.TrimSpace(x.Multicast), Source: strings.TrimSpace(x.Source), Description: strings.TrimSpace(x.Description)}
		for _, opt := range strings.Split(x.Interfaces, ",") {
			if opt = strings.TrimSpace(opt); opt == "" {
				continue
			}
			label := opt
			if i, ok := b.Interfaces[opt]; ok && i.Descr != "" {
				label = i.Descr
			}
			rel.Interfaces = append(rel.Interfaces, label)
		}
		slices.Sort(rel.Interfaces)
		b.Relays = append(b.Relays, rel)
	}
	if len(c.Relays) == 0 {
		return b, fmt.Errorf("%s: no udpbroadcastrelay entries (is this an OPNsense config.xml with the plugin?)", name)
	}
	return b, nil
}

// RewritesSource reports whether the entry replaces the sender address (-s 1.1.1.1 / 1.1.1.2).
func (r Relay) RewritesSource() bool { return r.Source != "" }
