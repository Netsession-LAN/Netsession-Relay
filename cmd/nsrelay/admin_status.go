package main

import (
	"cmp"
	"fmt"
	"io"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// adminStatus asks the engine for its status (via the socket; no database needed) and prints it.
func adminStatus(c *adminCall) int {
	if !c.parse() {
		return 2
	}
	st, err := c.engine().Status(c.ctx)
	if err != nil {
		return c.fail(err)
	}
	printStatus(c.stdout, st)
	return 0
}

// printStatus writes the engine status as text: summary, traffic per network and rule, then the problems.
func printStatus(out io.Writer, st engineapi.Status) {
	fmt.Fprintf(out, "engine %s, started %s\n", st.Version, st.Started.Format(time.RFC3339))
	fmt.Fprintf(out, "mode %s, sending %v, standby %v, snapshot %s, %d VLANs, %d rules\n",
		cmp.Or(st.Mode, "(none)"), st.Sending, st.Standby, shortHash(st.SnapshotHash), st.VLANs, st.Rules)
	fmt.Fprintf(out, "rx %d, sent %d, reasons %v\n", st.RX, st.Sent, st.Reasons)
	printStatusTraffic(out, st)
	printStatusProblems(out, st)
}

// printStatusTraffic answers per network: does its interface deliver frames at all? (A silent one points at the
// trunk, the VLAN tag or the interface, not at the rules.) And per rule: did it see frames, did it relay them?
func printStatusTraffic(out io.Writer, st engineapi.Status) {
	for _, v := range st.VLANStats {
		fmt.Fprintf(out, "vlan %d %s (%s): in %d, out %d, last in %s\n", v.VID, v.Name, v.Interface, v.In, v.Out, lastIn(v.LastIn))
	}
	for _, r := range st.RuleStats {
		if r.RX > 0 {
			fmt.Fprintf(out, "rule %s: rx %d, relayed %d\n", r.ID, r.RX, r.Relayed)
		}
	}
}

// lastIn is when a network last received a frame, or "never".
func lastIn(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Format(time.RFC3339)
}

// printStatusProblems lists open alarms, unknown broadcast ports and what the engine could not prepare on the
// host (no routing on relay interfaces, firewall, management routes).
func printStatusProblems(out io.Writer, st engineapi.Status) {
	for _, a := range st.Alarms {
		fmt.Fprintf(out, "ALARM %s %s x%d: %s\n", a.Kind, a.Key, a.Count, a.Detail)
	}
	for _, p := range st.TopUnknown {
		fmt.Fprintf(out, "unknown broadcast port %d: %d\n", p.Port, p.Count)
	}
	printHostIssues(out, st)
}

// printHostIssues lists what the engine could not prepare on the host, or that it prepared everything.
func printHostIssues(out io.Writer, st engineapi.Status) {
	for _, is := range st.HostIssues {
		fmt.Fprintf(out, "HOST %s: %s\n", is.Code, is.Detail)
	}
	if len(st.HostIssues) == 0 && st.Mode != "" {
		fmt.Fprintln(out, "host prepared")
	}
}

// shortHash is the first 12 characters of a snapshot hash, "-" without one.
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return cmp.Or(h, "-")
}
