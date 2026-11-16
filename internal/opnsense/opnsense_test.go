package opnsense

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
)

func load(t *testing.T) []Box {
	t.Helper()
	var boxes []Box
	for _, n := range []string{"51", "52", "53"} {
		f, err := os.Open(filepath.Join("testdata", "box"+n+".xml"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := Parse("."+n, f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		boxes = append(boxes, b)
	}
	return boxes
}

func TestParse(t *testing.T) {
	boxes := load(t)
	want := []int{63, 49, 6}
	for i, b := range boxes {
		if len(b.Relays) != want[i] {
			t.Errorf("%s: %d relays, want %d", b.Name, len(b.Relays), want[i])
		}
	}
	// .53 is a clone of .51 (same hostname) - visible in the import
	if boxes[0].Hostname != boxes[2].Hostname {
		t.Errorf("hostnames %q / %q", boxes[0].Hostname, boxes[2].Hostname)
	}
	var fo Relay
	for _, r := range boxes[2].Relays {
		if r.Port == 23757 {
			fo = r
		}
	}
	if !fo.Enabled || fo.Source != "1.1.1.2" || len(fo.Interfaces) != 15 || !slices.Contains(fo.Interfaces, "1254_Orga") {
		t.Errorf("FlatOut entry on .53: %+v", fo)
	}
	if _, err := Parse("x", strings.NewReader("<opnsense><system/></opnsense>")); err == nil {
		t.Error("config without relays accepted")
	}
}

func TestReportFindsTheKnownProblems(t *testing.T) {
	db, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	rep := BuildReport(load(t), db)
	find := func(port int, sub string) bool {
		for _, p := range rep.Ports {
			if p.Port == port {
				for _, f := range p.Findings {
					if strings.Contains(f, sub) {
						return true
					}
				}
			}
		}
		return false
	}
	// the loop of 2026-11-16: 28960 active on .51 and .52, 27036 on all three
	if !find(28960, "DOPPELT") || !find(27036, "DOPPELT") {
		t.Error("duplicates 28960/27036 not reported")
	}
	// ARMA 2 relayed with -s 1.1.1.2 although it needs the original IP
	if !find(2302, "-s 1.1.1.2") {
		t.Error("source rewrite on 2302 (ARMA 2) not reported")
	}
	// Minecraft Java uses multicast 224.0.2.60:4445; .52 relays 4445 as plain broadcast without the group
	if !find(4445, "Multicast 224.0.2.60") || !slices.Contains(rep.Suggest, "minecraft_java") {
		t.Error("Minecraft entry without multicast group not explained")
	}
	if !find(137, "Netzwerkdienst") {
		t.Error("NetBIOS not reported as noise")
	}
	if !slices.Contains(rep.Suggest, "flatout2") || slices.Contains(rep.Suggest, "netbios_ns") {
		t.Errorf("suggestions %v", rep.Suggest)
	}
	s := rep.Summary()
	if s.Duplicates < 2 || s.SourceRewrite == 0 || s.Active == 0 {
		t.Errorf("summary %+v", s)
	}
	var buf bytes.Buffer
	rep.WriteText(&buf)
	if !strings.Contains(buf.String(), "Lief bisher auf den Boxen") {
		t.Error("text report without suggestion line")
	}
}
