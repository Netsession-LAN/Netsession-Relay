package web

import (
	"testing"
	"time"
)

func TestRoleOf(t *testing.T) {
	dirs := map[string]map[string]bool{
		"announcer": {"announce": true},
		"seeker":    {"query": true},
		"flatout":   {"both": true},
		"unknown":   {"": true},
	}
	cases := []struct {
		ids  []string
		want string
	}{
		{[]string{"announcer"}, roleHost},
		{[]string{"seeker"}, roleSearch},
		{[]string{"announcer", "seeker"}, roleActive},
		{[]string{"announcer", "flatout"}, roleActive},
		{[]string{"seeker", "unknown"}, roleActive},
		{[]string{"missing"}, roleActive},
		{nil, roleActive},
	}
	for _, c := range cases {
		if got := roleOf(c.ids, dirs); got != c.want {
			t.Errorf("roleOf(%v) = %q, want %q", c.ids, got, c.want)
		}
	}
}

// The same PC on two ports of a game is one line with the widest time span; two servers of one host stay apart.
func TestAddPlayerMerges(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	var g playerGroup
	g.addPlayer(playerRow{IP: "10.10.1.10", VID: 1101, Role: roleActive, First: t0, Last: t0.Add(time.Minute), Ago: "a", Count: 2})
	g.addPlayer(playerRow{IP: "10.10.1.10", VID: 1101, Role: roleSearch, First: t0.Add(-time.Minute), Last: t0.Add(2 * time.Minute), Ago: "b", Count: 3})
	g.addPlayer(playerRow{IP: "10.10.1.10", VID: 1102, Role: roleSearch, First: t0, Last: t0, Count: 1}) // other network
	if len(g.Players) != 2 {
		t.Fatalf("%d lines, want 2", len(g.Players))
	}
	p := g.Players[0]
	if !p.First.Equal(t0.Add(-time.Minute)) || !p.Last.Equal(t0.Add(2*time.Minute)) || p.Ago != "b" || p.Count != 5 || p.Role != roleSearch {
		t.Errorf("merged line %+v", p)
	}
	// a host stays a host
	g.addPlayer(playerRow{IP: "10.10.1.10", VID: 1101, Role: roleHost, First: t0, Last: t0, Count: 1})
	g.addPlayer(playerRow{IP: "10.10.1.10", VID: 1101, Role: roleSearch, First: t0, Last: t0, Count: 1})
	if g.Players[0].Role != roleHost {
		t.Errorf("role %q, want host", g.Players[0].Role)
	}
	// servers of one host on different ports are separate lines; a server joins the line of its PC
	g.addPlayer(playerRow{IP: "10.10.1.20", VID: 1101, Server: true, Port: 27015, SrvName: "a"})
	g.addPlayer(playerRow{IP: "10.10.1.20", VID: 1101, Server: true, Port: 27016, SrvName: "b"})
	g.addPlayer(playerRow{IP: "10.10.1.10", VID: 1101, Role: roleHost, Server: true, Port: 27015, SrvName: "c", Slots: "1/8"})
	if len(g.Players) != 4 {
		t.Fatalf("%d lines, want 4", len(g.Players))
	}
	if p := g.Players[0]; !p.Server || p.Port != 27015 || p.SrvName != "c" || p.Slots != "1/8" {
		t.Errorf("server details not taken over: %+v", p)
	}
}
