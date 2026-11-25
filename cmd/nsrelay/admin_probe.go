package main

import (
	"fmt"
	"io"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// The server probe (nsrelay admin probe): like the port finder it talks to the engine socket only.

// adminProbe starts a server probe round and lists the servers that answered.
func adminProbe(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	eng := c.engine()
	if err := eng.ProbeNow(c.ctx); err != nil {
		return c.fail(err)
	}
	time.Sleep(1500 * time.Millisecond)
	list, err := eng.Players(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "%d server(s)\n", printServers(c.stdout, list))
	return 0
}

// printServers prints the servers among the players, one per line, and returns how many there are.
func printServers(w io.Writer, list []engineapi.Player) int {
	n := 0
	for _, p := range list {
		if !p.Server {
			continue
		}
		n++
		fmt.Fprintf(w, "%-5d %-21s %-18s %q %q %d/%d\n", p.VID, fmt.Sprintf("%s:%d", p.Src, p.Port), p.Rule, p.Name, p.Map, p.Num, p.Max)
	}
	return n
}
