package engineapi

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
)

type fake struct{ applied string }

func (f *fake) Status() Status {
	return Status{Mode: "active", SnapshotHash: f.applied, Alarms: []Alarm{{Kind: "foreign_relay", Key: "1101", Count: 1}}}
}
func (f *fake) Validate(s *compile.Snapshot) error {
	if s.Mode == "broken" {
		return errors.New("broken snapshot")
	}
	return nil
}
func (f *fake) Apply(s *compile.Snapshot) error {
	if err := f.Validate(s); err != nil {
		return err
	}
	f.applied = s.Hash
	return nil
}

func TestClientServerOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "e.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fake{}
	go Serve(ctx, sock, -1, f)
	c := NewClient(sock)
	var st Status
	var err error
	for range 50 { // wait for the listener
		if st, err = c.Status(ctx); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || st.Mode != "active" {
		t.Fatalf("status: %+v %v", st, err)
	}
	res, err := c.Apply(ctx, &compile.Snapshot{Version: 1, Mode: "active", Hash: "abc"})
	if err != nil || !res.OK || f.applied != "abc" {
		t.Fatalf("apply: %+v %v", res, err)
	}
	res, err = c.Validate(ctx, &compile.Snapshot{Mode: "broken"})
	if err != nil || res.OK || !strings.Contains(res.Error, "broken") {
		t.Fatalf("validate broken: %+v %v", res, err)
	}

	// event stream delivers an alarm and a status event
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine/v1/events", nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var events []string
	for sc.Scan() && len(events) < 2 {
		if strings.HasPrefix(sc.Text(), "event: ") {
			events = append(events, strings.TrimPrefix(sc.Text(), "event: "))
		}
	}
	if len(events) != 2 || events[0] != "alarm" || events[1] != "status" {
		t.Errorf("events = %v", events)
	}
}

func TestClientEngineDown(t *testing.T) {
	c := NewClient(filepath.Join(t.TempDir(), "none.sock"))
	if _, err := c.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("err = %v", err)
	}
}
