package web

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/outputs"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
)

// The SNMP section of the monitoring page. The agent itself runs in internal/outputs.

func (s *Server) agentSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	T := s.T(r)
	cur, curSec := s.Outputs.AgentSettings(ctx)
	a, ok := s.readAgentForm(w, r)
	if !ok {
		return
	}
	sec := curSec // a secret left empty in the form stays as it is
	sec.Community = cmp.Or(r.PostFormValue("community"), sec.Community)
	sec.AuthPass = cmp.Or(r.PostFormValue("auth_pass"), sec.AuthPass)
	sec.PrivPass = cmp.Or(r.PostFormValue("priv_pass"), sec.PrivPass)
	if err := validateAgentSettings(a, sec); err != nil {
		back(w, r, "/system/monitoring", "", errors.New(agentConfigError(T, err)))
		return
	}
	if err := s.Outputs.SaveAgentSettings(ctx, a, sec); err != nil {
		back(w, r, "/system/monitoring", "", s.internal(r, "SNMP agent: store the settings", err))
		return
	}
	// the audit entry holds the options, never the secrets
	before, _ := json.Marshal(cur)
	after, _ := json.Marshal(a)
	s.audit(ctx, s.actor(r), "snmp.agent.set", "snmp", string(before), string(after))
	s.Outputs.RestartAgent(ctx)
	s.answerAgentStatus(w, r)
}

// readAgentForm reads the options of the agent form. On a wrong field it sends the browser back with what is
// wrong and ok is false.
func (s *Server) readAgentForm(w http.ResponseWriter, r *http.Request) (a outputs.AgentSettings, ok bool) {
	T := s.T(r)
	v := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	a = outputs.AgentSettings{Enabled: v("enabled") == "1", V2c: v("v2c") == "1", User: v("user"), AuthProto: v("auth_proto"),
		PrivProto: v("priv_proto"), Contact: v("contact"), Location: v("location")}
	a.Port, _ = strconv.Atoi(v("port")) // not a number: 0, refused below
	if a.Port < 1 || a.Port > 65535 {
		back(w, r, "/system/monitoring#snmp", "", errors.New(T("Port bitte zwischen 1 und 65535 (Standard 161).")))
		return a, false
	}
	allow, bad := parseAgentAllow(v("allow"))
	if bad != "" {
		back(w, r, "/system/monitoring", "", errors.New(T("Erlaubte Absender: %q ist keine Adresse oder kein Netz.", bad)))
		return a, false
	}
	a.Allow = allow
	return a, true
}

// answerAgentStatus goes back to the monitoring page with the state of the restarted agent.
func (s *Server) answerAgentStatus(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	running, listen, err := s.Outputs.AgentStatus(r.Context())
	switch {
	case err != nil:
		back(w, r, "/system/monitoring", "", errors.New(T("SNMP-Agent: %s", agentError(T, err))))
	case running:
		back(w, r, "/system/monitoring", T("SNMP-Agent läuft auf %s.", listen), nil)
	default:
		back(w, r, "/system/monitoring", T("SNMP-Agent ist aus."), nil)
	}
}

// parseAgentAllow reads the allowed senders (addresses or networks, separated by commas, blanks or lines) as
// networks. bad is the first entry that is neither, "" when all are fine.
func parseAgentAllow(text string) (allow []string, bad string) {
	for _, p := range strings.FieldsFunc(text, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' }) {
		pfx, ok := agentAllowPrefix(p)
		if !ok {
			return nil, p
		}
		allow = append(allow, pfx.String())
	}
	return allow, ""
}

// agentAllowPrefix reads one allowed sender: a network, or an address as a network of its own.
func agentAllowPrefix(p string) (netip.Prefix, bool) {
	if pfx, err := netip.ParsePrefix(p); err == nil {
		return pfx, true
	}
	ad, err := netip.ParseAddr(p)
	if err != nil {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(ad, ad.BitLen()), true
}

// validateAgentSettings checks the options of a switched-on agent with the secrets that will be in effect (a
// switched-off agent is not checked). The address is a placeholder: only the options are checked here, the
// agent picks its address itself.
func validateAgentSettings(a outputs.AgentSettings, sec outputs.AgentSecret) error {
	if !a.Enabled {
		return nil
	}
	c := snmp.AgentConfig{Listen: "192.0.2.1:" + strconv.Itoa(a.Port), V2c: a.V2c, Community: sec.Community, V3User: a.User,
		AuthProto: a.AuthProto, AuthPass: sec.AuthPass, PrivProto: a.PrivProto, PrivPass: sec.PrivPass}
	return snmp.ValidateAgent(c)
}

// agentConfigError puts a validation error of the agent settings into words.
func agentConfigError(T translator, err error) string {
	switch {
	case errors.Is(err, snmp.ErrNoVersion):
		return T("Bitte v3 (Benutzer) oder v2c (Community) einrichten.")
	case errors.Is(err, snmp.ErrCommunityMissing):
		return T("Bitte die Community für v2c eintragen.")
	case errors.Is(err, snmp.ErrPasswordShort):
		return T("Die SNMPv3-Passwörter brauchen mindestens 8 Zeichen.")
	}
	return "SNMP: " + err.Error()
}

// agentError puts into words why the agent does not run. A reason without a sentence of its own is shown as the
// operating system gave it ("bind: address already in use"): it is what the admin needs to get the agent going,
// and only the admin sees this page.
func agentError(T translator, err error) string {
	switch {
	case errors.Is(err, outputs.ErrNoAgentAddr):
		return T("Keine Verwaltungsadresse gefunden (keine Schnittstelle mit Standardroute). Setz NSRELAY_SNMP_ADDR oder NSRELAY_HTTP_ADDR auf die Adresse, der Agent startet sonst nicht.")
	case errors.Is(err, outputs.ErrAgentPortDenied):
		return T("Der Port ist auf diesem Host nicht freigegeben. Nimm 1161 oder führe setup.sh neu aus (sysctl net.ipv4.ip_unprivileged_port_start=161).")
	}
	return err.Error()
}

// agentForm is the view of the SNMP section.
func (s *Server) agentForm(ctx context.Context, T translator) map[string]any {
	a, sec := s.Outputs.AgentSettings(ctx)
	running, listen, err := s.Outputs.AgentStatus(ctx)
	errText := ""
	if err != nil {
		errText = agentError(T, err)
	}
	host := s.Outputs.AgentHost()
	return map[string]any{"S": a, "Allow": strings.Join(a.Allow, ", "), "CommunitySet": sec.Community != "",
		"AuthSet": sec.AuthPass != "", "PrivSet": sec.PrivPass != "", "Running": running, "Listen": listen, "Err": errText,
		"Host": host.String(), "HostValid": host.IsValid()}
}
