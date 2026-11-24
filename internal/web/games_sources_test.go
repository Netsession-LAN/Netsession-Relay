package web

import (
	"slices"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

func TestSourcesOutside(t *testing.T) {
	vlans := []store.VLANConfig{
		{Name: "Tisch 1", CIDR: "10.10.1.0/24", Enabled: true},
		{Name: "Aus", CIDR: "10.10.2.0/24"},
		{Name: "Kaputt", CIDR: "kein Netz", Enabled: true},
	}
	sources := []string{"10.10.1.20", "10.10.1.0/28", "10.10.2.5", "10.10.0.0/16", "10.99.0.1"}
	if got, want := sourcesOutside(sources, vlans), []string{"10.10.2.5", "10.10.0.0/16", "10.99.0.1"}; !slices.Equal(got, want) {
		t.Errorf("outside: %v, want %v", got, want)
	}
	if got := sourceNet("10.10.1.0/28", vlans); got != "Tisch 1" {
		t.Errorf("network of 10.10.1.0/28: %q", got)
	}
	if got := sourceNet("10.10.1.1, 10.10.1.2", vlans); got != "" {
		t.Errorf("two entries in one: %q", got)
	}
	if got := sourcesOutside(nil, vlans); got != nil {
		t.Errorf("no filter: %v", got)
	}
}
