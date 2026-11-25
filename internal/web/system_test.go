package web

import (
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

func TestAuditOf(t *testing.T) {
	T := trFor(langDE)
	cases := []struct {
		in   store.AuditEntry
		want auditView
	}{
		{store.AuditEntry{Action: "login", Object: "10.0.0.5"}, auditView{Text: "Angemeldet", Object: "10.0.0.5", Mono: true}},
		{store.AuditEntry{Action: "login.failed", Object: "10.0.0.5"}, auditView{Text: "Anmeldung fehlgeschlagen", Object: "10.0.0.5", Mono: true, Tone: "warn"}},
		{store.AuditEntry{Action: "game.sources", Object: "flatout2"}, auditView{Text: "Absender-Filter eines Spiels geändert", Object: "flatout2: alle", Mono: true}},
		{store.AuditEntry{Action: "vlan.update", Object: "1101"}, auditView{Text: "Netz geändert", Object: "VLAN 1101", Mono: true}},
		{store.AuditEntry{Action: "mode.set", Before: "observe", After: "active"}, auditView{Text: "Betriebsart geändert", Object: "Beobachten → Aktiv"}},
		{store.AuditEntry{Action: "emergency_stop", Object: "engine"}, auditView{Text: "Not-Aus ausgelöst", Object: "Relay pausiert", Tone: "danger"}},
		{store.AuditEntry{Action: "emergency_release", After: "active"}, auditView{Text: "Not-Aus aufgehoben", Object: "zurück auf Aktiv"}},
		{store.AuditEntry{Action: "rollback", Object: "revision 7"}, auditView{Text: "Zurückgerollt", Object: "Revision 7", Tone: "warn"}},
		{store.AuditEntry{Action: "user.set_password", Object: "dana"}, auditView{Text: "Passwort gesetzt", Object: "Konto dana"}},
		{store.AuditEntry{Action: "something.new", Object: "x"}, auditView{Text: "something.new", Object: "x"}},
	}
	for _, c := range cases {
		if got := auditOf(c.in, T); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.in.Action, got, c.want)
		}
	}
}
