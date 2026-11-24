package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// The port finder and the server probe talk to the engine socket only; they need no database.

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
	n := 0
	for _, p := range list {
		if !p.Server {
			continue
		}
		n++
		fmt.Fprintf(c.stdout, "%-5d %-21s %-18s %q %q %d/%d\n", p.VID, fmt.Sprintf("%s:%d", p.Src, p.Port), p.Rule, p.Name, p.Map, p.Num, p.Max)
	}
	fmt.Fprintf(c.stdout, "%d server(s)\n", n)
	return 0
}

func adminFinderStart(c *adminCall) int {
	vlans := c.fs.String("vlans", "", "VLAN IDs, comma separated (empty: all)")
	src := c.fs.String("src", "", "only this sender")
	secs := c.fs.Int("seconds", 300, "session length (max 600)")
	if !c.parse() {
		return 2
	}
	req := engineapi.FinderStart{Src: *src, Seconds: *secs}
	for _, x := range strings.Split(*vlans, ",") {
		if x = strings.TrimSpace(x); x == "" {
			continue
		}
		id, err := strconv.Atoi(x)
		if err != nil {
			return c.fail(fmt.Errorf("admin finder-start: -vlans: %q is not a VLAN ID", x))
		}
		req.VIDs = append(req.VIDs, id)
	}
	if err := c.engine().FinderStart(c.ctx, req); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "port finder running for %d s (VLANs %v, sender %q)\n", req.Seconds, req.VIDs, req.Src)
	return 0
}

func adminFinderStop(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	if err := c.engine().FinderStop(c.ctx); err != nil {
		return c.fail(err)
	}
	fmt.Fprintln(c.stdout, "port finder stopped")
	return 0
}

// adminFinderShow prints the state and the findings of the current or last session.
func adminFinderShow(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, err := c.engine().Finder(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	if st.Started.IsZero() {
		fmt.Fprintln(c.stdout, "no session")
		return 0
	}
	state := "finished"
	if st.Active {
		state = fmt.Sprintf("running, %s left", time.Until(st.Until).Round(time.Second))
	}
	fmt.Fprintf(c.stdout, "%s, started %s, %d frames, %d captured\n", state, st.Started.Format(time.TimeOnly), st.Frames, st.Captured)
	for _, g := range st.Groups {
		dst := "broadcast"
		if g.Group != "" {
			dst = g.Group
		}
		rule := g.Rule
		if rule == "" {
			rule = "UNKNOWN"
		}
		newMark := ""
		if g.New {
			newMark = " NEW"
		}
		fmt.Fprintf(c.stdout, "%-15s %5d  %-22s x%-6d %d-%d B  vlans %v  srcs %v%s  %q\n", dst, g.Port, rule, g.Count,
			g.MinLen, g.MaxLen, g.VIDs, g.Srcs, newMark, printable(g.Sample))
	}
	return 0
}

func adminFinderPcap(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	file := c.fs.Arg(0)
	if file == "" {
		return c.fail(fmt.Errorf("usage: nsrelay admin finder-pcap <file>"))
	}
	f, err := os.Create(file)
	if err != nil {
		return c.fail(err)
	}
	if err := c.engine().FinderPcap(c.ctx, f); err != nil {
		f.Close()
		return c.fail(err)
	}
	if err := f.Close(); err != nil {
		return c.fail(err)
	}
	fmt.Fprintln(c.stdout, "written", file)
	return 0
}

func printable(b []byte) string {
	r := []rune(string(b))
	for i, c := range r {
		if !unicode.IsPrint(c) || c > 0x7e {
			r[i] = '.'
		}
	}
	return string(r)
}
