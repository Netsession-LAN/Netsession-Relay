package web

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// udpSink is a syslog receiver on loopback that collects datagrams.
type udpSink struct {
	pc  net.PacketConn
	got chan string
}

func newSink(t *testing.T) *udpSink {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	s := &udpSink{pc: pc, got: make(chan string, 100)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			s.got <- string(buf[:n])
		}
	}()
	return s
}

func (s *udpSink) addr() string { return s.pc.LocalAddr().String() }

// next waits for a message containing want.
func (s *udpSink) next(t *testing.T, want string) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case m := <-s.got:
			if strings.Contains(m, want) {
				return m
			}
		case <-deadline:
			t.Fatalf("no message with %q", want)
			return ""
		}
	}
}

func syslogTarget(name, ep string, events ...string) store.Target {
	return store.Target{Kind: store.TargetSyslog, Name: name, Enabled: true, Endpoint: ep, Events: events,
		Config: map[string]any{"transport": "udp", "format": "rfc5424", "facility": "local0"}}
}

func TestOutputsSeveralTargets(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a, b := newSink(t), newSink(t)
	if _, err := e.st.SaveTarget(ctx, "test", syslogTarget("A", a.addr(), "alarm", "audit")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SaveTarget(ctx, "test", syslogTarget("B", b.addr(), "alarm")); err != nil {
		t.Fatal(err)
	}
	e.ui.Outputs.Reload(ctx)

	// audit goes to A only (B takes alarms only)
	op, csrf := e.client("op")
	r, _ := op.PostForm(e.srv.URL+"/spiele/flatout2", url.Values{"csrf": {csrf}, "on": {"0"}})
	r.Body.Close()
	m := a.next(t, "Spiel ausgeschaltet")
	if !strings.Contains(m, `msgid="audit"`) || !strings.Contains(m, `action="game.disable"`) || !strings.Contains(m, `user="op"`) {
		t.Errorf("audit message %q", m)
	}

	// alarms: foreign_relay on 1101 is active in the fake engine -> open to both, once
	var down time.Time
	now := time.Now()
	e.ui.watchOnce(ctx, &down, now)
	for _, s := range []*udpSink{a, b} {
		m := s.next(t, `state="open"`)
		if !strings.HasPrefix(m, "<132>1 ") || !strings.Contains(m, `kind="foreign_relay" key="1101" vlan="1101"`) {
			t.Errorf("open message %q", m)
		}
	}
	e.ui.watchOnce(ctx, &down, now.Add(10*time.Second))
	select {
	case m := <-b.got:
		t.Errorf("open sent twice: %q", m)
	case <-time.After(300 * time.Millisecond):
	}
	if open, _ := e.st.OpenAlarms(ctx); len(open) != 1 {
		t.Errorf("open alarms in the db: %v", open)
	}

	// engine gone: engine_down only after 30 s, the other alarm stays open
	e.eng.down = true
	e.ui.watchOnce(ctx, &down, now.Add(20*time.Second))
	e.ui.watchOnce(ctx, &down, now.Add(55*time.Second))
	if m := b.next(t, `kind="engine_down"`); !strings.Contains(m, `state="open"`) {
		t.Errorf("engine_down %q", m)
	}
	e.eng.down = false
	e.ui.watchOnce(ctx, &down, now.Add(60*time.Second))
	if m := b.next(t, `kind="engine_down"`); !strings.Contains(m, `state="clear"`) || !strings.Contains(m, "wieder") {
		t.Errorf("engine back %q", m)
	}

	// status, report and prometheus know the targets
	rep, _ := e.ui.monitorReport(ctx)
	if rep.OutputsTotal != 2 || rep.OutputsFailing != 0 {
		t.Errorf("report outputs %d/%d", rep.OutputsTotal, rep.OutputsFailing)
	}
}
