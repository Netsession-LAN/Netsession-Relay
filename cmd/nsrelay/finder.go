package main

import (
	"fmt"
	"io"
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

func adminFinderStart(c *adminCall) int {
	vlans := c.fs.String("vlans", "", "VLAN IDs of networks of the relay, comma separated (empty: all)")
	src := c.fs.String("src", "", "only this sender")
	secs := c.fs.Int("seconds", 300, "session length (max 600)")
	if !c.parse() {
		return 2
	}
	vids, err := parseVLANList(*vlans)
	if err != nil {
		return c.fail(err)
	}
	req := engineapi.FinderStart{Src: *src, Seconds: *secs, VIDs: vids}
	if err := c.engine().FinderStart(c.ctx, req); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "port finder running for %d s (VLANs %v, sender %q)\n", req.Seconds, req.VIDs, req.Src)
	return 0
}

// parseVLANList reads the -vlans flag of finder-start: VLAN IDs separated by commas, empty entries skipped.
func parseVLANList(s string) ([]int, error) {
	var vids []int
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x == "" {
			continue
		}
		id, err := strconv.Atoi(x)
		if err != nil {
			return nil, fmt.Errorf("admin finder-start: -vlans: %q is not a VLAN ID", x)
		}
		vids = append(vids, id)
	}
	return vids, nil
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
	fmt.Fprintf(c.stdout, "%s, started %s, %d frames, %d captured\n", finderState(st), st.Started.Format(time.TimeOnly), st.Frames, st.Captured)
	for _, g := range st.Groups {
		printFinderGroup(c.stdout, g)
	}
	return 0
}

// finderState is "finished" or how long a running session has left.
func finderState(st engineapi.FinderStatus) string {
	if st.Active {
		return fmt.Sprintf("running, %s left", time.Until(st.Until).Round(time.Second))
	}
	return "finished"
}

// printFinderGroup prints one line of the findings: destination, port, rule (UNKNOWN without one), frame count
// and sizes, VLANs, senders, NEW for a group that showed up late, and the start of the first payload.
func printFinderGroup(w io.Writer, g engineapi.FinderGroup) {
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
	fmt.Fprintf(w, "%-15s %5d  %-22s x%-6d %d-%d B  vlans %v  srcs %v%s  %q\n", dst, g.Port, rule, g.Count,
		g.MinLen, g.MaxLen, g.VIDs, g.Srcs, newMark, printable(g.Sample))
}

func adminFinderPcap(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	file := c.fs.Arg(0)
	if file == "" {
		return c.usage("finder-pcap <file|->   (- = standard output)")
	}
	if err := saveFinderPcap(c, file); err != nil {
		return c.fail(err)
	}
	if file != "-" {
		fmt.Fprintln(c.stdout, "written", file)
	}
	return 0
}

// saveFinderPcap writes the capture to file, or to standard output for "-".
func saveFinderPcap(c *adminCall, file string) error {
	if file == "-" { // the capture and nothing else on standard output, so a redirect gives a valid pcap
		return c.engine().FinderPcap(c.ctx, c.stdout)
	}
	f, err := os.Create(file)
	if err != nil {
		return writeHint(err)
	}
	if err := c.engine().FinderPcap(c.ctx, f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
