package web

import (
	"fmt"
	"slices"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

func TestGameFormDefaults(t *testing.T) {
	f := gameForm{ID: "spiel", Name: "Spiel", Aliases: " Eins, ,Zwei ,", Specs: []specForm{
		{Kind: gamedb.KindBroadcast, Ports: "27015", Group: "239.1.1.1"},
		{Ports: ""}, // an empty row is skipped
		{Kind: gamedb.KindMulticast, Ports: "5000", Group: "239.1.1.1", Direction: "out"},
	}}
	g, msg := f.game(gamedb.Game{}, "op", false, fmt.Sprintf)
	if msg != "" {
		t.Fatal(msg)
	}
	if g.Category != gamedb.CategoryGame || g.Confidence != "low" || g.SourceIP != gamedb.SourceOriginal {
		t.Errorf("defaults: %q %q %q", g.Category, g.Confidence, g.SourceIP)
	}
	if !slices.Equal(g.Aliases, []string{"Eins", "Zwei"}) {
		t.Errorf("aliases %q", g.Aliases)
	}
	if len(g.Discovery) != 2 || g.Discovery[0].Group != "" || g.Discovery[0].Direction != "both" ||
		g.Discovery[1].Group != "239.1.1.1" || g.Discovery[1].Direction != "out" {
		t.Errorf("discovery %+v", g.Discovery)
	}
	if g.RelayNeeded != gamedb.RelayYes || len(g.Sources) != 1 {
		t.Errorf("relay %q sources %q", g.RelayNeeded, g.Sources)
	}

	base := gamedb.Game{Category: "tool", Confidence: "high", SourceIP: "relay", RelayNeeded: gamedb.RelayNo, DefaultEnabled: true}
	g, msg = gameForm{Name: "Werkzeug", From: "finder"}.game(base, "op", false, fmt.Sprintf)
	if msg != "" || g.Category != "tool" || g.Confidence != "high" || g.SourceIP != "relay" || g.DefaultEnabled ||
		g.Aliases != nil || g.Sources[0][:11] != "Port-Finder" {
		t.Errorf("kept from the stored game: %+v %q", g, msg)
	}
	if _, msg := (gameForm{Name: "Ohne"}).game(gamedb.Game{}, "op", false, fmt.Sprintf); msg == "" {
		t.Error("a game without ports and not marked as needing no relay is accepted")
	}
}
