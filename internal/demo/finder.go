package demo

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// finderSession fakes a port finder session: a known game, Steam noise and, after a few seconds, an unknown port.
type finderSession struct {
	req            engineapi.FinderStart
	started, until time.Time
}

// FinderStart starts a fake session.
func (d *Engine) FinderStart(_ context.Context, req engineapi.FinderStart) error {
	if req.Seconds < 1 || req.Seconds > 600 {
		return errors.New("duration must be between 1 s and 10 min")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	d.finder = finderSession{req: req, started: now, until: now.Add(time.Duration(req.Seconds) * time.Second)}
	return nil
}

// FinderStop ends the fake session.
func (d *Engine) FinderStop(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if now := time.Now(); now.Before(d.finder.until) {
		d.finder.until = now
	}
	return nil
}

// Finder reports invented results that grow with the session time.
func (d *Engine) Finder(context.Context) (engineapi.FinderStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.finder
	if f.started.IsZero() {
		return engineapi.FinderStatus{}, nil
	}
	now := time.Now()
	end := now
	if f.until.Before(end) {
		end = f.until
	}
	st := engineapi.FinderStatus{Active: now.Before(f.until), Started: f.started, Until: f.until, VIDs: f.req.VIDs, Src: f.req.Src}
	fs := findings{st: &st, started: f.started, elapsed: end.Sub(f.started).Seconds(), src: f.sender(), vid: f.vid()}
	fs.add(engineapi.FinderGroup{Kind: "broadcast", Port: 27036, MinLen: 42, MaxLen: 42, Sample: []byte("\xff\xff\xff\xff!L_\xa0\x00\x00\x00\x00\x08")}, 0.5, 5)
	fs.add(engineapi.FinderGroup{Kind: "broadcast", Port: 23757, Rule: d.ruleForPorts("23757"), MinLen: 24, MaxLen: 24, Sample: []byte("FO2\x00query\x00\x00\x00\x01")}, 9, 1)
	fs.add(engineapi.FinderGroup{Kind: "broadcast", Port: 41234, MinLen: 64, MaxLen: 88, Sample: []byte("ZOOM-LAN\x01\x00\x00\x00UC-Host\x00")}, 14, 2)
	st.Captured = int(min(st.Frames, 4000))
	return st, nil
}

// vid is the VLAN the invented frames arrive on: the first of the session's filter, else table 1.
func (f finderSession) vid() int {
	if len(f.req.VIDs) > 0 {
		return f.req.VIDs[0]
	}
	return 1101
}

// sender is the invented sender: the session's sender filter, else a host on table 1.
func (f finderSession) sender() netip.Addr {
	if a, err := netip.ParseAddr(f.req.Src); err == nil {
		return a
	}
	return netip.MustParseAddr("10.10.1.23")
}

// ruleForPorts is the ID of the last rule of the snapshot with exactly these ports, "" without one.
func (d *Engine) ruleForPorts(ports string) string {
	if d.snap == nil {
		return ""
	}
	rule := ""
	for _, r := range d.snap.Rules {
		if r.Ports == ports {
			rule = r.ID
		}
	}
	return rule
}

// findings collects the invented groups of a session that has run for elapsed seconds.
type findings struct {
	st      *engineapi.FinderStatus
	started time.Time
	elapsed float64
	src     netip.Addr
	vid     int
}

// add reports group g once the session has run for after seconds, with one frame every "every" seconds from then on.
func (fs findings) add(g engineapi.FinderGroup, after, every float64) {
	if fs.elapsed < after {
		return
	}
	g.Count = uint64((fs.elapsed-after)/every) + 1
	g.First = fs.started.Add(time.Duration(after * float64(time.Second)))
	g.Last = g.First.Add(time.Duration(float64(g.Count-1) * every * float64(time.Second)))
	g.Srcs, g.VIDs, g.New = []netip.Addr{fs.src}, []int{fs.vid}, after > 5
	fs.st.Groups = append(fs.st.Groups, g)
	fs.st.Frames += g.Count
}

// FinderPcap writes an empty capture (pcap header only).
func (d *Engine) FinderPcap(_ context.Context, w io.Writer) error {
	_, err := w.Write([]byte{0xd4, 0xc3, 0xb2, 0xa1, 2, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0, 0, 1, 0, 0, 0})
	return err
}
