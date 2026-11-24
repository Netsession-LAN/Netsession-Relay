package demo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The demo engine follows applied snapshots and its numbers move.
func TestEngine(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := control.Init(ctx, st, "test", "lab", "trunk0", false); err != nil {
		t.Fatal(err)
	}
	demo := NewEngine()
	if st, _ := demo.Status(ctx); st.Mode != "" || len(st.VLANStats) != 0 {
		t.Errorf("demo engine without snapshot: %+v", st)
	}
	res, err := control.Apply(ctx, st, demo, "test", "", false)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := demo.Status(ctx)
	time.Sleep(30 * time.Millisecond)
	b, _ := demo.Status(ctx)
	if a.SnapshotHash != res.Snapshot.Hash || a.Mode != res.Snapshot.Mode || len(a.VLANStats) != len(res.Snapshot.VLANs) || len(a.RuleStats) != len(res.Snapshot.Rules) {
		t.Errorf("demo status does not follow the snapshot: %+v", a)
	}
	if b.RX < a.RX || b.RXRate == 0 {
		t.Errorf("demo traffic does not move: %d -> %d, rate %d", a.RX, b.RX, b.RXRate)
	}
	players, err := demo.Players(ctx)
	if err != nil || len(players) == 0 {
		t.Errorf("demo players: %d, %v", len(players), err)
	}
}

func TestFinder(t *testing.T) {
	ctx := context.Background()
	demo := NewEngine()
	if st, _ := demo.Finder(ctx); !st.Started.IsZero() {
		t.Errorf("session before the start: %+v", st)
	}
	if err := demo.FinderStart(ctx, engineapiStart(0)); err == nil {
		t.Error("a session of 0 s accepted")
	}
	if err := demo.FinderStart(ctx, engineapiStart(60)); err != nil {
		t.Fatal(err)
	}
	if st, _ := demo.Finder(ctx); !st.Active || st.Started.IsZero() {
		t.Errorf("session not running: %+v", st)
	}
	if err := demo.FinderStop(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := demo.Finder(ctx); st.Active {
		t.Errorf("session still running after stop: %+v", st)
	}
}

func engineapiStart(seconds int) engineapi.FinderStart {
	return engineapi.FinderStart{VIDs: []int{1101}, Seconds: seconds}
}
