package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
	"unicode"

	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
)

// adminProbe starts a server probe round and lists the servers that answered (via socket).
func adminProbe(ctx context.Context, af adminFlags, stdout, stderr io.Writer) int {
	eng := engineapi.NewClient(af.socket)
	if err := eng.ProbeNow(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	time.Sleep(1500 * time.Millisecond)
	list, err := eng.Players(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	n := 0
	for _, p := range list {
		if !p.Server {
			continue
		}
		n++
		fmt.Fprintf(stdout, "%-5d %-21s %-18s %q %q %d/%d\n", p.VID, fmt.Sprintf("%s:%d", p.Src, p.Port), p.Rule, p.Name, p.Map, p.Num, p.Max)
	}
	fmt.Fprintf(stdout, "%d server(s)\n", n)
	return 0
}

// adminFinder runs the port finder commands against the engine socket (no database needed).
func adminFinder(ctx context.Context, af adminFlags, cmd string, req engineapi.FinderStart, file string, stdout, stderr io.Writer) int {
	eng := engineapi.NewClient(af.socket)
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 1 }
	switch cmd {
	case "finder-start":
		if err := eng.FinderStart(ctx, req); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "port finder running for %d s (VLANs %v, sender %q)\n", req.Seconds, req.VIDs, req.Src)
	case "finder-stop":
		if err := eng.FinderStop(ctx); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "port finder stopped")
	case "finder":
		st, err := eng.Finder(ctx)
		if err != nil {
			return fail(err)
		}
		if st.Started.IsZero() {
			fmt.Fprintln(stdout, "no session")
			return 0
		}
		state := "finished"
		if st.Active {
			state = fmt.Sprintf("running, %s left", time.Until(st.Until).Round(time.Second))
		}
		fmt.Fprintf(stdout, "%s, started %s, %d frames, %d captured\n", state, st.Started.Format(time.TimeOnly), st.Frames, st.Captured)
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
			fmt.Fprintf(stdout, "%-15s %5d  %-22s x%-6d %d-%d B  vlans %v  srcs %v%s  %q\n", dst, g.Port, rule, g.Count,
				g.MinLen, g.MaxLen, g.VIDs, g.Srcs, newMark, printable(g.Sample))
		}
	case "finder-pcap":
		if file == "" {
			return fail(fmt.Errorf("usage: nsrelay admin finder-pcap <file>"))
		}
		f, err := os.Create(file)
		if err != nil {
			return fail(err)
		}
		if err := eng.FinderPcap(ctx, f); err != nil {
			f.Close()
			return fail(err)
		}
		if err := f.Close(); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "written", file)
	default:
		fmt.Fprintln(stderr, adminUsage)
		return 2
	}
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
