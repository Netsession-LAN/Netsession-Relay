package lab

import (
	"testing"
)

func TestLabSnapshot(t *testing.T) {
	s, _, err := Snapshot("trunk0", "active", 55)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.VLANs) != 4 || s.VLANs[1].VID != 1101 || s.VLANs[1].Parent != "trunk0" {
		t.Fatalf("vlans = %+v", s.VLANs)
	}
	found := false
	for _, r := range s.Rules {
		if r.Ports == "23757" {
			found = true
		}
		for _, e := range r.Flows {
			if e.Src == "orga" || e.Dst == "orga" {
				t.Errorf("lab snapshot references missing group orga in %s", r.ID)
			}
		}
	}
	if !found {
		t.Error("FlatOut rule missing in lab snapshot")
	}
}
