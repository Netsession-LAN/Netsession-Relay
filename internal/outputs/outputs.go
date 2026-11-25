// Package outputs runs the monitoring outputs of the web part: the push targets (syslog, SNMP traps,
// OpenTelemetry) behind one dispatcher, and the read-only SNMP agent. Targets and settings live in the database;
// the web UI edits them and tells the Service to reload. Events (audit entries, alarms) are handed in with
// Publish; a slow or dead target never blocks the caller.
package outputs

import (
	"context"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/monitor"
	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
	"github.com/Netsession-LAN/Netsession-Relay/internal/otlp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/syslog"
)

// Setting keys of the monitoring outputs.
const (
	// SettingHostName is the host name used in all outputs (syslog HOSTNAME, OTLP host.name, SNMP sysName);
	// empty = the host name of the machine.
	SettingHostName = "monitor_host_name"

	settingInstanceID   = "monitor_instance_id" // random id created once (OTLP service.instance.id)
	settingSNMPEngineID = "snmp_engine_id"      // SNMPv3 engine id, created once
	settingSNMPBoots    = "snmp_engine_boots"   // SNMPv3 boot counter, +1 per start of the web part
)

// ReportFunc returns the current monitoring report and the per-rule counters (OTLP metrics, SNMP agent).
type ReportFunc func(context.Context) (monitor.Report, []engineapi.RuleStat)

// Options are the dependencies of a Service.
type Options struct {
	Store   *store.Store
	Log     *slog.Logger
	Version string
	// Report feeds the outputs that publish numbers. It may call Service.Status.
	Report ReportFunc
	// MgmtAddr returns the management address of the host (the SNMP agent binds it when nothing else is set);
	// nil = unknown.
	MgmtAddr func() netip.Addr
}

// Service owns the dispatcher of the push targets and the SNMP agent.
type Service struct {
	store    *store.Store
	log      *slog.Logger
	version  string
	report   ReportFunc
	mgmtAddr func() netip.Addr
	started  time.Time
	notify   *notify.Dispatcher

	addrMu   sync.RWMutex
	bind     netip.Addr // source address of outgoing connections (SetBindAddr)
	snmpAddr string     // overrides the address the SNMP agent binds (SetSNMPAddr)

	agentMu  sync.Mutex
	agent    *snmp.Agent
	agentErr error
}

// New builds the dispatcher and loads the targets from the database. It counts one start for SNMPv3 (USM needs a
// growing boot counter next to the engine time).
func New(o Options) *Service {
	s := &Service{store: o.Store, log: o.Log, version: o.Version, report: o.Report, mgmtAddr: o.MgmtAddr, started: time.Now()}
	if s.log == nil {
		s.log = slog.Default()
	}
	ctx := context.Background()
	s.bumpSNMPBoots(ctx)
	s.notify = notify.New(s.log, map[string]notify.Factory{
		store.TargetSyslog: func(t store.Target) (notify.Sender, error) {
			return syslog.NewSender(t.Endpoint, syslogConfig(t, s.HostName(context.Background()), s.BindAddr()))
		},
		store.TargetSNMPTrap: func(t store.Target) (notify.Sender, error) {
			return snmp.NewTrapSender(t.Endpoint, s.trapConfig(context.Background(), t))
		},
		store.TargetOTLP: func(t store.Target) (notify.Sender, error) {
			c := otlpConfig(t, s.otlpResource(context.Background()), s.BindAddr())
			return otlp.NewSender(t.Endpoint, c, func(ctx context.Context) []otlp.Metric {
				rep, rules := s.report(ctx)
				return otlp.FromReport(rep, rules, s.notify.Status(), time.Now(), s.started, c.RuleMetrics)
			})
		},
	})
	s.Reload(ctx)
	return s
}

// Close flushes and stops all senders and the SNMP agent (shutdown).
func (s *Service) Close() {
	s.notify.Close()
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if s.agent != nil {
		s.agent.Close()
		s.agent = nil
	}
}

// Reload re-reads the targets after a change; senders of unchanged targets keep running.
func (s *Service) Reload(ctx context.Context) {
	ts, err := s.store.Targets(ctx)
	if err != nil {
		s.log.Error("monitoring targets", "err", err)
		return
	}
	s.notify.Reload(ts)
}

// Restart rebuilds every sender, for a change that is part of each sender's configuration (host name, source
// address).
func (s *Service) Restart(ctx context.Context) {
	ts, err := s.store.Targets(ctx)
	if err != nil {
		s.log.Error("monitoring targets", "err", err)
		return
	}
	s.notify.Rebuild(ts)
}

// Publish hands an event to every target that takes its type. It never blocks.
func (s *Service) Publish(ev notify.Event) { s.notify.Publish(ev) }

// Test sends one event to one target right away and returns the result.
func (s *Service) Test(ctx context.Context, id int64, ev notify.Event) error {
	return s.notify.Test(ctx, id, ev)
}

// Status reports every target.
func (s *Service) Status() []notify.Status { return s.notify.Status() }

// Stats counts the targets that are switched on, the failing ones and the events dropped.
func (s *Service) Stats() (total, failing int, dropped uint64) {
	return notify.Summary(s.notify.Status())
}

// SetBindAddr sets the source address for outgoing monitoring connections from the listen address of the web
// part (loopback and "every address" mean "any"), rebuilds the senders and restarts the SNMP agent.
func (s *Service) SetBindAddr(ctx context.Context, listen string) {
	bind := BindFrom(listen)
	s.addrMu.Lock()
	changed := s.bind != bind
	s.bind = bind
	s.addrMu.Unlock()
	if changed {
		// the source address is built into every sender, and no target's own stamp changed
		s.Restart(ctx)
	}
	s.RestartAgent(ctx)
}

// SetSNMPAddr overrides the address the SNMP agent binds (NSRELAY_SNMP_ADDR); call it before SetBindAddr.
func (s *Service) SetSNMPAddr(addr string) {
	s.addrMu.Lock()
	s.snmpAddr = addr
	s.addrMu.Unlock()
}

// BindAddr is the source address of outgoing monitoring connections; the zero value means "any".
func (s *Service) BindAddr() netip.Addr {
	s.addrMu.RLock()
	defer s.addrMu.RUnlock()
	return s.bind
}

// BindFrom returns the host part of a listen address as source address for outgoing connections; loopback and
// unspecified addresses give the zero value.
func BindFrom(listen string) netip.Addr {
	host := listen
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		host = listen[:i]
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || a.IsUnspecified() || a.IsLoopback() {
		return netip.Addr{}
	}
	return a
}

// HostName is the name the outputs report: the setting, else the host name of the machine.
func (s *Service) HostName(ctx context.Context) string {
	// a failed read falls back to the machine's name: the outputs keep working with a wrong label
	if h, _ := s.store.Setting(ctx, SettingHostName, ""); h != "" {
		return h
	}
	h, _ := os.Hostname()
	return h
}

// instanceID returns the random id of this installation, created at the first use.
func (s *Service) instanceID(ctx context.Context) string {
	if id, _ := s.store.Setting(ctx, settingInstanceID, ""); id != "" {
		return id
	}
	id := auth.Token(12)
	if err := s.store.SetSetting(ctx, settingInstanceID, id); err != nil {
		s.log.Warn("monitoring: instance id not stored, it changes with the next start", "err", err)
	}
	return id
}

// snmpEngine returns the SNMPv3 engine id (created at the first use) and the boot counter.
func (s *Service) snmpEngine(ctx context.Context) (string, uint32) {
	id, _ := s.store.Setting(ctx, settingSNMPEngineID, "")
	if id == "" {
		id = snmp.NewEngineID()
		if err := s.store.SetSetting(ctx, settingSNMPEngineID, id); err != nil {
			s.log.Warn("snmp: engine id not stored, it changes with the next start", "err", err)
		}
	}
	b, _ := s.store.Setting(ctx, settingSNMPBoots, "0")
	n, _ := strconv.Atoi(b) // an unreadable counter starts again at 0
	return id, uint32(n)
}

func (s *Service) bumpSNMPBoots(ctx context.Context) {
	b, _ := s.store.Setting(ctx, settingSNMPBoots, "0")
	n, _ := strconv.Atoi(b)
	if err := s.store.SetSetting(ctx, settingSNMPBoots, strconv.Itoa(n+1)); err != nil {
		s.log.Warn("snmp: boot counter not stored, SNMPv3 managers may reject answers as replays", "err", err)
	}
}
