package web

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/otlp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/snmp"
	"github.com/Netsession-LAN/Netsession-Relay/internal/syslog"
)

func TestParseHeaders(t *testing.T) {
	got, msg := parseHeaders("Authorization: Bearer x:y\n\n  X-Scope-OrgID :  relay \n")
	if want := map[string]string{"Authorization": "Bearer x:y", "X-Scope-OrgID": "relay"}; msg != "" || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v %q, want %v", got, msg, want)
	}
	for _, bad := range []string{"no colon", ": value", "Name:", "Two words: x", "Tab\tName: x"} {
		if h, msg := parseHeaders(bad); h != nil || msg == "" {
			t.Errorf("%q accepted: %v", bad, h)
		}
	}
}

func TestTargetError(t *testing.T) {
	T := trFor(langDE)
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", snmp.ErrEndpoint), "Adresse bitte als Adresse:Port, z. B. 10.0.0.5:514."},
		{otlp.ErrEndpointURL, "Adresse bitte als URL, z. B. http://10.0.0.5:4318."},
		{syslog.ErrTLSCA, "Im CA-Feld steht kein Zertifikat (PEM)."},
		{snmp.ErrUserMissing, "Bitte den SNMPv3-Benutzer eintragen."},
		{errors.New("unknown transport"), "Das passt nicht: unknown transport"},
	}
	for _, c := range cases {
		if got := targetError(c.err, T); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}
