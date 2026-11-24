package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The configuration of the first-run setup compiles, and its matrix only names groups that have a network.
func TestSetupConfig(t *testing.T) {
	db, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	vlans := []store.VLANConfig{
		{VID: 11, Name: "Tisch 1", Group: "tische", CIDR: "10.10.1.0/24", RelayIP: "10.10.1.250", Attach: compile.AttachTrunk, Parent: "eth1", Enabled: true},
		{VID: 10, Name: "Server", Group: "gameserver", CIDR: "10.10.0.0/24", RelayIP: "10.10.0.250", Attach: compile.AttachTrunk, Parent: "eth1", Enabled: true},
	}
	c := SetupConfig("LAN", compile.ModeObserve, vlans, db)
	if len(c.Profiles) != 1 || !c.Profiles[0].Default || len(c.Profiles[0].Edges) != 4 {
		t.Fatalf("profiles: %+v (want tische and gameserver in all four directions)", c.Profiles)
	}
	for _, e := range c.Profiles[0].Edges {
		if e.Src == "orga" || e.Dst == "orga" {
			t.Errorf("edge %v names a group without a network", e)
		}
	}
	if len(c.Games) == 0 {
		t.Fatal("no default games")
	}
	in, err := store.ConfigInput(c, db, true)
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := compile.Compile(in)
	if err != nil || snap.Mode != compile.ModeObserve || len(snap.VLANs) != 2 || len(snap.Rules) == 0 {
		t.Fatalf("setup configuration does not compile: %v", err)
	}
}

func TestLastBackupAndAuto(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := Init(ctx, st, "test", "lab", "trunk0", false); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if !LastBackup(dir).IsZero() {
		t.Error("empty directory has a last backup")
	}
	now := time.Date(2026, 11, 23, 3, 30, 0, 0, time.UTC)
	st.Now = func() time.Time { return now }
	if _, err := Backup(ctx, st, dir, 14); err != nil {
		t.Fatal(err)
	}
	if got := LastBackup(dir); !got.Equal(now) {
		t.Errorf("last backup %v, want %v", got, now)
	}
	if backupDue(dir, now.Add(23*time.Hour)) {
		t.Error("backup due after 23 h")
	}
	if !backupDue(dir, now.Add(24*time.Hour)) || !backupDue(t.TempDir(), now) {
		t.Error("backup not due after a day or in an empty directory")
	}
}
