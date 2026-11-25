//go:build linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/hostprep"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

const readTimeout = 500 * time.Millisecond

// state is swapped atomically on every snapshot change.
type state struct {
	table *Table
	out   []*packetSock // out[i] = socket of table.VLANs[i]
	index map[*packetSock]int
}

// Engine runs the data plane from a snapshot file.
type Engine struct {
	Path      string // snapshot file (active.json)
	Socket    string // engine API socket; empty = no API
	SocketGID int    // group of the socket (-1 = unchanged)
	Version   string
	Log       *slog.Logger
	Counters  Counters
	// BeaconKey is NSRELAY_BEACON_KEY; empty = the key file beacon.key next to the snapshot (created on first use).
	BeaconKey string
	// AllowedIfaces is NSRELAY_ALLOWED_IFACES; empty = automatic rule (CheckIfaces).
	AllowedIfaces []string

	beaconAuth string     // "env" | "file" | "" (beacons unsigned: no key could be loaded)
	mu         sync.Mutex // serialises apply
	receivers  map[string]*receiver
	st         atomic.Pointer[state]
	wg         sync.WaitGroup
	guard      *Guard
	mcast      *memberships
	alarmLog   map[string]time.Time
	started    time.Time
	hostIssues atomic.Pointer[[]hostprep.Issue] // what the host preparation could not do at the last apply
	rates      atomic.Pointer[rateSample]
	prevRates  rateSample
}

// rateSample holds per-second rates computed in everySecond.
type rateSample struct {
	rx, sent uint64
	vlanIn   map[int]uint64 // VID -> cumulative in (for the next delta)
	rxRate   uint64
	sentRate uint64
	vlanRate map[int]uint64
	lastIn   map[int]time.Time // VID -> when the in counter last grew
}

// Guard returns the protection state (alarms, standby) for status output.
func (e *Engine) Guard() *Guard { return e.guard }

// Run loads the snapshot, sets up interfaces and sockets and relays until ctx is cancelled.
// SIGHUP reloads the snapshot file.
func (e *Engine) Run(ctx context.Context) error {
	e.init()
	e.loadBeaconKey()
	e.logIfacePolicy()
	if err := e.loadStored(); err != nil {
		return err
	}
	e.serveAPI(ctx)
	return e.loop(ctx)
}

func (e *Engine) init() {
	if e.Log == nil {
		e.Log = slog.Default()
	}
	e.receivers = map[string]*receiver{}
	e.guard = NewGuard(nil)
	e.mcast = newMemberships()
	e.alarmLog = map[string]time.Time{}
	e.started = time.Now()
}

// loadBeaconKey gives the guard the key that signs the beacons; without one the relay runs with unsigned beacons.
func (e *Engine) loadBeaconKey() {
	key, src, err := LoadBeaconKey(e.BeaconKey, filepath.Join(filepath.Dir(e.Path), "beacon.key"))
	if err != nil {
		e.Log.Warn("relay beacons are NOT signed: no key (set NSRELAY_BEACON_KEY or make the state directory writable); "+
			"a forged beacon can put this relay into standby", "err", err)
		return
	}
	e.guard.SetBeaconKey(key)
	e.beaconAuth = src
	e.Log.Info("relay beacons are signed", "key_from", src)
}

func (e *Engine) logIfacePolicy() {
	if len(e.AllowedIfaces) > 0 {
		e.Log.Info("interfaces limited to NSRELAY_ALLOWED_IFACES", "allowed", e.AllowedIfaces)
		return
	}
	e.Log.Info("NSRELAY_ALLOWED_IFACES not set: every interface a snapshot names is used, except the ones carrying the default route")
}

// loadStored applies the stored snapshot. A missing or refused one leaves the engine idle but running.
func (e *Engine) loadStored() error {
	switch err := e.Reload(); {
	case errors.Is(err, os.ErrNotExist):
		e.Log.Warn("no snapshot yet, waiting for apply via the engine API", "path", e.Path)
	case errors.Is(err, ErrIfaceNotAllowed):
		// keep running (API, status) instead of a restart loop; nothing is relayed until a snapshot is accepted
		e.Log.Error("stored snapshot NOT applied, the relay is idle", "path", e.Path, "err", err)
	case err != nil:
		return err
	}
	return nil
}

func (e *Engine) serveAPI(ctx context.Context) {
	if e.Socket == "" {
		return
	}
	go func() {
		if err := engineapi.Serve(ctx, e.Socket, e.SocketGID, e); err != nil {
			e.Log.Error("engine API stopped", "socket", e.Socket, "err", err)
		}
	}()
	e.Log.Info("engine API listening", "socket", e.Socket)
}

// loop runs the timers and SIGHUP reloads until ctx is cancelled.
func (e *Engine) loop(ctx context.Context) error {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	stats := time.NewTicker(30 * time.Second)
	defer stats.Stop()
	second := time.NewTicker(time.Second)
	defer second.Stop()
	e.sendBeacons()
	seconds := 0 // ticks of the one-second ticker: everySecond schedules by it, so nothing else may count
	for {
		select {
		case <-ctx.Done():
			e.shutdown()
			e.logStats()
			return nil
		case <-hup:
			if err := e.Reload(); err != nil {
				e.Log.Error("reload failed, keeping previous snapshot", "err", err)
			}
		case <-stats.C:
			e.logStats()
		case <-second.C:
			seconds++
			e.everySecond(seconds)
		}
	}
}

// Reload reads the snapshot file and applies it.
func (e *Engine) Reload() error {
	s, err := compile.ReadSnapshotFile(e.Path)
	if err != nil {
		return err
	}
	return e.apply(s)
}

// Validate checks a snapshot without touching interfaces (engine API).
func (e *Engine) Validate(s *compile.Snapshot) error {
	if s.Version != compile.SnapshotVersion {
		return fmt.Errorf("snapshot version %d, engine supports %d", s.Version, compile.SnapshotVersion)
	}
	if _, err := NewTable(s, nil); err != nil {
		return err
	}
	for _, v := range s.VLANs {
		if err := checkIfaceExists(v); err != nil {
			return err
		}
	}
	return e.checkIfaces(s)
}

// checkIfaceExists checks that the interface a VLAN is set up on (its trunk or device) exists on the host.
func checkIfaceExists(v compile.VLAN) error {
	if v.Attach == compile.AttachTrunk {
		if _, err := net.InterfaceByName(v.Parent); err != nil {
			return fmt.Errorf("VLAN %d: trunk interface %q: %w", v.VID, v.Parent, err)
		}
		return nil
	}
	if _, err := net.InterfaceByName(v.Device); err != nil {
		return fmt.Errorf("VLAN %d: interface %q: %w", v.VID, v.Device, err)
	}
	return nil
}

// checkIfaces applies the interface policy (CheckIfaces) with the host's current default routes.
func (e *Engine) checkIfaces(s *compile.Snapshot) error {
	var protected map[string]bool
	if len(e.AllowedIfaces) == 0 {
		var err error
		if protected, err = netif.DefaultRouteLinks(); err != nil {
			return fmt.Errorf("cannot read the routes of the host to protect the management interface: %w", err)
		}
	}
	return CheckIfaces(s, e.AllowedIfaces, protected)
}

// Apply applies a snapshot and persists it as the start configuration (engine API). If applying fails the
// previous snapshot stays active and on disk.
func (e *Engine) Apply(s *compile.Snapshot) error {
	if err := e.Validate(s); err != nil {
		return err
	}
	if err := e.apply(s); err != nil {
		return err
	}
	return compile.WriteSnapshotFile(e.Path, s)
}

func (e *Engine) shutdown() {
	e.mcast.close()
	e.mu.Lock()
	defer e.mu.Unlock()
	for name, r := range e.receivers {
		r.halt()
		delete(e.receivers, name)
	}
	e.wg.Wait()
	// interfaces stay: a restart re-adopts them without flapping the links
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
