package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/Netsession2024/Netsession-Relay/internal/outputs"
	"github.com/Netsession2024/Netsession-Relay/internal/snmp"
)

// The SNMP section of the monitoring page. The agent itself runs in internal/outputs.

func (s *Server) agentSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	T := s.T(r)
	v := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	cur, curSec := s.Outputs.AgentSettings(ctx)
	a := outputs.AgentSettings{Enabled: v("enabled") == "1", V2c: v("v2c") == "1", User: v("user"), AuthProto: v("auth_proto"),
		PrivProto: v("priv_proto"), Contact: v("contact"), Location: v("location")}
	a.Port, _ = strconv.Atoi(v("port")) // not a number: 0, refused below
	if a.Port < 1 || a.Port > 65535 {
		back(w, r, "/system/monitoring#snmp", "", errors.New(T("Port bitte zwischen 1 und 65535 (Standard 161).")))
		return
	}
	for _, p := range strings.FieldsFunc(v("allow"), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' }) {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			ad, err := netip.ParseAddr(p)
			if err != nil {
				back(w, r, "/system/monitoring", "", errors.New(T("Erlaubte Absender: %q ist keine Adresse oder kein Netz.", p)))
				return
			}
			pfx = netip.PrefixFrom(ad, ad.BitLen())
		}
		a.Allow = append(a.Allow, pfx.String())
	}
	sec := curSec
	if x := r.PostFormValue("community"); x != "" {
		sec.Community = x
	}
	if x := r.PostFormValue("auth_pass"); x != "" {
		sec.AuthPass = x
	}
	if x := r.PostFormValue("priv_pass"); x != "" {
		sec.PrivPass = x
	}
	if a.Enabled {
		// the address is a placeholder: only the options are checked here, the agent picks its address itself
		c := snmp.AgentConfig{Listen: "192.0.2.1:" + strconv.Itoa(a.Port), V2c: a.V2c, Community: sec.Community, V3User: a.User,
			AuthProto: a.AuthProto, AuthPass: sec.AuthPass, PrivProto: a.PrivProto, PrivPass: sec.PrivPass}
		if err := snmp.ValidateAgent(c); err != nil {
			back(w, r, "/system/monitoring", "", errors.New(agentConfigError(T, err)))
			return
		}
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
	running, listen, err := s.Outputs.AgentStatus(ctx)
	switch {
	case err != nil:
		back(w, r, "/system/monitoring", "", errors.New(T("SNMP-Agent: %s", agentError(T, err))))
	case running:
		back(w, r, "/system/monitoring", T("SNMP-Agent läuft auf %s.", listen), nil)
	default:
		back(w, r, "/system/monitoring", T("SNMP-Agent ist aus."), nil)
	}
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
