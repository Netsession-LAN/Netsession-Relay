package web

import (
	"fmt"
	"testing"
)

func TestSetupRelayAddr(t *testing.T) {
	for _, c := range []struct {
		cidr  string
		host  int
		relay string
		bad   bool
	}{
		{"10.10.1.0/24", 250, "10.10.1.250", false},
		{"10.10.1.77/24", 250, "10.10.1.250", false}, // host bits are ignored
		{"10.10.0.0/16", 300, "10.10.1.44", false},
		{"10.10.1.0/24", 254, "10.10.1.254", false},
		{"10.10.1.0/24", 255, "", true}, // the broadcast address
		{"10.10.1.0/30", 2, "10.10.1.2", false},
		{"10.10.1.0/30", 3, "", true},
		{"10.10.1.0/31", 1, "", false}, // too small: the check of the row reports it
		{"fd00::/64", 1, "", false},
		{"kaputt", 1, "", false},
	} {
		relay, msg := setupRelayAddr(c.cidr, c.host, fmt.Sprintf)
		if relay != c.relay || (msg != "") != c.bad {
			t.Errorf("%s host %d: %q %q, want %q (error %v)", c.cidr, c.host, relay, msg, c.relay, c.bad)
		}
	}
}

func TestSetupFormAction(t *testing.T) {
	var f setupForm
	if f.setupFormAction("fertig", fmt.Sprintf) || f.setupFormAction("", fmt.Sprintf) {
		t.Error("the finish is not a form action")
	}
	if !f.setupFormAction("beispiel", fmt.Sprintf) || len(f.Rows) != 6 || f.Rows[1].Name != "Tisch 1" {
		t.Fatalf("example: %+v", f.Rows)
	}
	if !f.setupFormAction("mehr", fmt.Sprintf) || len(f.Rows) != 10 {
		t.Errorf("more rows: %d", len(f.Rows))
	}
	f.Rows = make([]setupRow, setupRowsMax-2)
	f.setupFormAction("mehr", fmt.Sprintf)
	if len(f.Rows) != setupRowsMax {
		t.Errorf("more rows near the limit: %d, want %d", len(f.Rows), setupRowsMax)
	}
	f.setupFormAction("mehr", fmt.Sprintf)
	if len(f.Rows) != setupRowsMax {
		t.Errorf("more rows at the limit: %d, want %d", len(f.Rows), setupRowsMax)
	}
}
