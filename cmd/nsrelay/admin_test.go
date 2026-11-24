package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engine"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// testPcap is what the fake engine hands out as the capture: the pcap magic and bytes a text stream would mangle.
var testPcap = []byte{0xd4, 0xc3, 0xb2, 0xa1, 0x02, 0x00, 0x04, 0x00, 0x0d, 0x0a, 0x00, 0xff}

// fakeEngine answers on the engine socket for the CLI tests; it knows the VLANs 100-102.
type fakeEngine struct{ finder engineapi.FinderStart }

func (f *fakeEngine) Status() engineapi.Status         { return engineapi.Status{Mode: "observe"} }
func (f *fakeEngine) Validate(*compile.Snapshot) error { return nil }
func (f *fakeEngine) Apply(*compile.Snapshot) error    { return nil }
func (f *fakeEngine) Players() []engineapi.Player      { return nil }
func (f *fakeEngine) Ack(string, string) int           { return 0 }
func (f *fakeEngine) ProbeNow()                        {}
func (f *fakeEngine) FinderStop()                      {}
func (f *fakeEngine) Finder() engineapi.FinderStatus   { return engineapi.FinderStatus{} }
func (f *fakeEngine) FinderPcap(w io.Writer) error     { _, err := w.Write(testPcap); return err }
func (f *fakeEngine) FinderStart(r engineapi.FinderStart) error {
	if err := engine.CheckFinderVIDs(r.VIDs, []int{100, 101, 102}); err != nil {
		return err
	}
	f.finder = r
	return nil
}

// adminEnv is a database with the lan preset and a fake engine on a socket; admin runs one admin command.
type adminEnv struct {
	t          *testing.T
	db, socket string
	eng        *fakeEngine
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	// a short path: the path of a Unix socket is limited to about 100 bytes
	dir, err := os.MkdirTemp("", "nsr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := &adminEnv{t: t, db: filepath.Join(dir, "relay.db"), socket: filepath.Join(dir, "e.sock"), eng: &fakeEngine{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = engineapi.Serve(ctx, e.socket, -1, e.eng) }()
	t.Cleanup(func() { cancel(); <-done })
	c := engineapi.NewClient(e.socket)
	for range 100 { // wait for the listener
		if _, err = c.Status(ctx); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.admin("", "init", "-preset", "lan"); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	return e
}

func (e *adminEnv) admin(stdin string, args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	old := adminStdin
	adminStdin = strings.NewReader(stdin)
	defer func() { adminStdin = old }()
	var out, errOut bytes.Buffer
	full := append([]string{"admin", args[0], "-db", e.db, "-socket", e.socket, "-user", "test"}, args[1:]...)
	code = run(full, &out, &errOut)
	return code, out.String(), errOut.String()
}

// "-" is standard output for export and standard input for import: in the container only /backups is writable,
// `docker compose exec -T web nsrelay admin export - > relay.yaml` works from the host.
func TestAdminExportImportDash(t *testing.T) {
	e := newAdminEnv(t)
	code, yamlOut, stderr := e.admin("", "export", "-")
	if code != 0 || stderr != "" || !strings.HasPrefix(yamlOut, "version: 1\n") || !strings.Contains(yamlOut, "vid: 101") {
		t.Fatalf("export -: code %d, stderr %q, stdout starts %q", code, stderr, yamlOut[:min(len(yamlOut), 80)])
	}
	if _, plain, _ := e.admin("", "export"); plain != yamlOut {
		t.Error("export without a file differs from export -")
	}
	file := filepath.Join(filepath.Dir(e.db), "out.yaml")
	if code, out, _ := e.admin("", "export", file); code != 0 || !strings.Contains(out, "written:") {
		t.Errorf("export to a file: code %d, %q", code, out)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != yamlOut {
		t.Errorf("exported file differs from standard output: %v", err)
	}

	// change a name, import from standard input, export again
	changed := strings.Replace(yamlOut, "name: Tisch 1\n", "name: Reihe A\n", 1)
	if changed == yamlOut {
		t.Fatal("test input: network name not found in the export")
	}
	code, out, stderr := e.admin(changed, "import", "-")
	if code != 0 || !strings.Contains(out, "imported event") {
		t.Fatalf("import -: code %d, %q, %q", code, out, stderr)
	}
	if _, again, _ := e.admin("", "export", "-"); again != changed {
		t.Error("export after import - does not give back what was imported")
	}
	if code, _, stderr := e.admin("not: [yaml", "import", "-"); code == 0 || !strings.Contains(stderr, "admin import") {
		t.Errorf("import - of broken YAML: code %d, %q", code, stderr)
	}
	if code, _, stderr := e.admin("", "import", "-"); code == 0 || !strings.Contains(stderr, "standard input") {
		t.Errorf("import - of nothing: code %d, %q", code, stderr)
	}
}

// finder-pcap - writes the capture and nothing else to standard output, byte for byte.
func TestAdminFinderPcapDash(t *testing.T) {
	e := newAdminEnv(t)
	code, out, stderr := e.admin("", "finder-pcap", "-")
	if code != 0 || stderr != "" || !bytes.Equal([]byte(out), testPcap) {
		t.Errorf("finder-pcap -: code %d, stderr %q, stdout % x", code, stderr, out)
	}
	file := filepath.Join(filepath.Dir(e.db), "c.pcap")
	if code, out, _ := e.admin("", "finder-pcap", file); code != 0 || !strings.Contains(out, "written") {
		t.Errorf("finder-pcap to a file: code %d, %q", code, out)
	}
	if b, _ := os.ReadFile(file); !bytes.Equal(b, testPcap) {
		t.Errorf("capture file: % x", b)
	}
	if code, _, stderr := e.admin("", "finder-pcap"); code != 2 || !strings.Contains(stderr, "finder-pcap <file|->") {
		t.Errorf("finder-pcap without a target: code %d, %q", code, stderr)
	}
}

// A session for a VLAN the relay has no network for is refused, and the message names the ones it has.
func TestAdminFinderStartUnknownVLAN(t *testing.T) {
	e := newAdminEnv(t)
	code, _, stderr := e.admin("", "finder-start", "-vlans", "1101")
	if code != 1 || !strings.Contains(stderr, "VLAN 1101 is not a network of this relay (configured: 100, 101, 102)") {
		t.Errorf("unknown VLAN: code %d, %q", code, stderr)
	}
	if code, out, stderr := e.admin("", "finder-start", "-vlans", "101,102", "-seconds", "30"); code != 0 || !strings.Contains(out, "running for 30 s") {
		t.Errorf("known VLANs: code %d, %q %q", code, out, stderr)
	}
	if len(e.eng.finder.VIDs) != 2 {
		t.Errorf("engine got %+v", e.eng.finder)
	}
	if code, _, stderr := e.admin("", "finder-start", "-vlans", "abc"); code != 1 || !strings.Contains(stderr, "not a VLAN ID") {
		t.Errorf("bad VLAN ID: code %d, %q", code, stderr)
	}
}

// Two backups within one second both succeed (control.Backup gives the second one a counter).
func TestBackupTwiceInOneSecond(t *testing.T) {
	e := newAdminEnv(t)
	dir := filepath.Join(filepath.Dir(e.db), "backups")
	for i := range 3 {
		var out, errOut bytes.Buffer
		if code := run([]string{"backup", "-db", e.db, "-dir", dir}, &out, &errOut); code != 0 {
			t.Fatalf("backup %d: %s", i+1, errOut.String())
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "nsrelay-*.db")); len(m) != 3 {
		t.Errorf("backups: %v", m)
	}
}
