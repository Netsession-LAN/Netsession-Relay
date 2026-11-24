package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Admin subcommands that list what the database holds: games, VLANs, revisions, audit log.

func adminGames(c *adminCall) int {
	onlyEnabled := c.fs.Bool("enabled", false, "only enabled games")
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	games, err := st.Games(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ON\tID\tNAME\tRELAY\tCONF\tPORTS")
	for _, g := range games {
		if *onlyEnabled && !g.Enabled {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", mark(g.Enabled), g.ID, g.Name, g.RelayNeeded, g.Confidence, discoveryPorts(g.Discovery))
	}
	return flush(c, tw)
}

// discoveryPorts is the PORTS column of the game list: the ports of every discovery entry, a multicast one as
// group:ports, separated by spaces.
func discoveryPorts(specs []gamedb.Spec) string {
	var ports []string
	for _, d := range specs {
		p := d.Ports
		if d.Group != "" {
			p = d.Group + ":" + p
		}
		ports = append(ports, p)
	}
	return strings.Join(ports, " ")
}

// mark is the "ON" column of the tables.
func mark(on bool) string {
	if on {
		return "x"
	}
	return ""
}

// flush writes a table out; a write error fails the command.
func flush(c *adminCall, tw *tabwriter.Writer) int {
	if err := tw.Flush(); err != nil {
		return c.fail(err)
	}
	return 0
}

func adminVLANs(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	cfg, err := st.ExportConfig(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ON\tVID\tNAME\tGROUP\tNETWORK\tRELAY IP\tINTERFACE")
	for _, v := range cfg.VLANs {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n", mark(v.Enabled), v.VID, v.Name, v.Group, v.CIDR, v.RelayIP, vlanInterface(v))
	}
	return flush(c, tw)
}

// vlanInterface is the interface the relay uses for a network: parent.VID on a trunk, else the device itself.
func vlanInterface(v store.VLANConfig) string {
	if v.Attach == "trunk" {
		return fmt.Sprintf("%s.%d", v.Parent, v.VID)
	}
	return v.Device
}

func adminRevisions(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	revs, err := st.Revisions(c.ctx, 30)
	if err != nil {
		return c.fail(err)
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTIME\tUSER\tOK\tHASH\tNOTE")
	for _, r := range revs {
		applied := "yes"
		if !r.AppliedOK {
			applied = "NO: " + r.EngineMessage
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.TS[:19], r.User, applied, r.Hash[:12], r.Note)
	}
	return flush(c, tw)
}

func adminAudit(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, ok := c.store()
	if !ok {
		return 1
	}
	defer st.Close()
	log, err := st.AuditLog(c.ctx, 50)
	if err != nil {
		return c.fail(err)
	}
	for _, e := range log {
		fmt.Fprintf(c.stdout, "%s  %-18s %-16s %s\n", e.TS[:19], e.User, e.Action, e.Object)
	}
	return 0
}
