package compile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotFileRoundTrip(t *testing.T) {
	snap, _, err := Compile(exampleInput(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "active.json")
	if err := WriteSnapshotFile(path, snap); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshotFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash != snap.Hash || len(got.Rules) != len(snap.Rules) || len(got.VLANs) != len(snap.VLANs) {
		t.Fatalf("round trip changed snapshot")
	}
	if got.VLANs[1].CIDR != snap.VLANs[1].CIDR || got.VLANs[1].RelayIP != snap.VLANs[1].RelayIP {
		t.Errorf("vlan fields lost: %+v", got.VLANs[1])
	}
	if hashOf(got) != snap.Hash {
		t.Error("hash not stable over JSON round trip")
	}

	// tampering is detected
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), `"23757"`, `"23758"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshotFile(path); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("tampered file: err = %v", err)
	}
}
