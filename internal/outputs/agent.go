package outputs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"strconv"

	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
)

// The read-only SNMP agent of the web part. Its configuration is stored as two settings (JSON): the options and,
// separately, the secrets (community, passwords). The secrets are in the database and therefore in the backup;
// they are never shown in the UI, written to the audit log or exported.

const (
	settingSNMPAgent       = "snmp_agent"
	settingSNMPAgentSecret = "snmp_agent_secret" //nolint:gosec // G101: a setting or message name, not a credential
)

// AgentSettings are the options of the SNMP agent.
type AgentSettings struct {
	Enabled   bool     `json:"enabled"`
	Port      int      `json:"port"`
	V2c       bool     `json:"v2c"`
	User      string   `json:"v3_user"`
	AuthProto string   `json:"auth_proto"`
	PrivProto string   `json:"priv_proto"`
	Allow     []string `json:"allow"` // networks that may ask; empty = everybody who reaches the port
	Contact   string   `json:"contact"`
	Location  string   `json:"location"`
}

// AgentSecret holds the secrets of the SNMP agent.
type AgentSecret struct {
	Community string `json:"community"`
	AuthPass  string `json:"auth_pass"`
	PrivPass  string `json:"priv_pass"`
}

// Why the agent does not run (AgentStatus); any other error comes from the listener or the configuration.
var (
	// ErrNoAgentAddr: no address to bind. The agent never binds every address, it must not answer in the
	// participant networks.
	ErrNoAgentAddr = errors.New("snmp agent: no management address found")
	// ErrAgentPortDenied: the host does not let the web part bind the port (ports below 1024 need a sysctl).
	ErrAgentPortDenied = errors.New("snmp agent: binding the port is not permitted")
)

// AgentSettings reads the stored configuration; without one the defaults (off, port 161, SHA-256, AES-128).
func (s *Service) AgentSettings(ctx context.Context) (AgentSettings, AgentSecret) {
	a := AgentSettings{Port: 161, AuthProto: "sha256", PrivProto: "aes128"}
	var sec AgentSecret
	s.readJSON(ctx, settingSNMPAgent, &a)
	s.readJSON(ctx, settingSNMPAgentSecret, &sec)
	return a, sec
}

// readJSON fills v from a setting. A missing setting leaves v as it is; one that cannot be read is logged and
// also leaves v (the defaults: agent off).
func (s *Service) readJSON(ctx context.Context, key string, v any) {
	raw, err := s.store.Setting(ctx, key, "")
	if err == nil && raw != "" {
		err = json.Unmarshal([]byte(raw), v)
	}
	if err != nil {
		s.log.Error("snmp agent: stored configuration not readable", "setting", key, "err", err)
	}
}

// SaveAgentSettings stores the configuration; RestartAgent makes it take effect.
func (s *Service) SaveAgentSettings(ctx context.Context, a AgentSettings, sec AgentSecret) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	bs, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, settingSNMPAgent, string(b)); err != nil {
		return err
	}
	return s.store.SetSetting(ctx, settingSNMPAgentSecret, string(bs))
}

// AgentHost is the address the agent binds: the override (SetSNMPAddr), else the address the web part listens
// on, else (web part on every address) the management address of the host. The zero value means there is none.
func (s *Service) AgentHost() netip.Addr {
	s.addrMu.RLock()
	bind, override := s.bind, s.snmpAddr
	s.addrMu.RUnlock()
	if a, err := netip.ParseAddr(override); err == nil && !a.IsUnspecified() {
		return a
	}
	if bind.IsValid() {
		return bind
	}
	if s.mgmtAddr != nil {
		return s.mgmtAddr()
	}
	return netip.Addr{}
}

// RestartAgent stops a running agent and starts it again with the stored settings.
func (s *Service) RestartAgent(ctx context.Context) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if s.agent != nil {
		s.agent.Close()
		s.agent = nil
	}
	s.agentErr = nil
	a, sec := s.AgentSettings(ctx)
	if !a.Enabled {
		return
	}
	host := s.AgentHost()
	if !host.IsValid() {
		s.agentErr = ErrNoAgentAddr
		return
	}
	s.agent, s.agentErr = s.startAgent(s.agentConfig(ctx, a, sec, host))
}

// agentConfig is the configuration of the agent listening on host.
func (s *Service) agentConfig(ctx context.Context, a AgentSettings, sec AgentSecret, host netip.Addr) snmp.AgentConfig {
	eid, boots := s.snmpEngine(ctx)
	return snmp.AgentConfig{Listen: net.JoinHostPort(host.String(), strconv.Itoa(a.Port)), V2c: a.V2c, Community: sec.Community,
		V3User: a.User, AuthProto: a.AuthProto, AuthPass: sec.AuthPass, PrivProto: a.PrivProto, PrivPass: sec.PrivPass,
		EngineID: eid, Boots: boots, SysDescr: "Netsession Relay " + s.version, SysName: s.HostName(ctx), Contact: a.Contact, Location: a.Location,
		Allow: allowPrefixes(a.Allow)}
}

// allowPrefixes parses the allowed source networks; they were validated when saved, a broken one is skipped.
func allowPrefixes(list []string) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range list {
		if pfx, err := netip.ParsePrefix(p); err == nil {
			out = append(out, pfx)
		}
	}
	return out
}

// startAgent starts the agent with c; a port it may not bind is reported as ErrAgentPortDenied.
func (s *Service) startAgent(c snmp.AgentConfig) (*snmp.Agent, error) {
	ag, err := snmp.StartAgent(c, func() *snmp.View {
		rep, _ := s.report(context.Background())
		return &snmp.View{Report: rep, OutputsFailing: rep.OutputsFailing}
	})
	if err != nil {
		s.log.Warn("snmp agent", "err", err)
		if errors.Is(err, fs.ErrPermission) {
			err = fmt.Errorf("%w: %w", ErrAgentPortDenied, err)
		}
		return nil, err
	}
	return ag, nil
}

// AgentStatus reports whether the agent runs and where it listens, or why it does not (nil when it is merely
// switched off).
func (s *Service) AgentStatus(ctx context.Context) (running bool, listen string, err error) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if s.agent == nil {
		return false, "", s.agentErr
	}
	a, _ := s.AgentSettings(ctx)
	return true, net.JoinHostPort(s.AgentHost().String(), strconv.Itoa(a.Port)), nil
}
