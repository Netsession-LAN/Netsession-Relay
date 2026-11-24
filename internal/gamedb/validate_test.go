package gamedb

import "testing"

// Every problem of Validate keeps its exact wording and its order: the web UI and the CLI show these texts.
func TestValidateMessages(t *testing.T) {
	bad := Game{ID: "Bad ID", Category: "x", RelayNeeded: "maybe", DefaultEnabled: true, Confidence: "x", SourceIP: "x",
		GamePorts: map[string]string{"sctp": "0"},
		Discovery: []Spec{
			{Kind: KindMulticast, Ports: "0", Direction: "x", SourceMode: "x", DstMode: "x", TTLMode: "x", RatePPS: -1, PayloadMax: 2000},
			{Kind: KindBroadcast, Group: "224.0.2.60", Ports: "60-70", RatePPS: 10, RateBurst: 5, PayloadMax: -1},
			{Kind: "anycast", Ports: "1234"},
		}}
	noSpec := Game{ID: "y", Name: "Y", Category: CategoryGame, RelayNeeded: RelayYes, Confidence: "high", SourceIP: SourceOriginal}
	err := Validate([]Game{bad, bad, noSpec})
	if err == nil {
		t.Fatal("Validate accepted invalid games")
	}
	if got := err.Error(); got != validateWant {
		t.Errorf("Validate:\n%s\nwant:\n%s", got, validateWant)
	}
}

func TestValidRate(t *testing.T) {
	for _, tt := range []struct {
		pps, burst int
		want       bool
	}{
		{0, 0, true}, {5, 0, true}, {5, 5, true}, {5, 10, true},
		{10, 5, false}, {-1, 0, false}, {0, -1, false},
	} {
		if got := validRate(tt.pps, tt.burst); got != tt.want {
			t.Errorf("validRate(%d, %d) = %v, want %v", tt.pps, tt.burst, got, tt.want)
		}
	}
}

const validateWant = `game "Bad ID": invalid id
game "Bad ID": name missing
game "Bad ID": category "x" invalid
game "Bad ID": relay_needed "maybe" invalid
game "Bad ID": confidence "x" invalid
game "Bad ID": source_ip "x" invalid
game "Bad ID": discovery specs present but relay_needed=maybe
game "Bad ID": default_enabled requires relay_needed=yes
game "Bad ID": game_ports: unknown protocol "sctp"
game "Bad ID": game_ports.sctp: portspec "0": invalid port "0" (want 1..65535)
game "Bad ID": discovery[0]: portspec "0": invalid port "0" (want 1..65535)
game "Bad ID": discovery[0]: group "" is not an IPv4 multicast address
game "Bad ID": discovery[0]: direction "x" invalid
game "Bad ID": discovery[0]: source_mode "x" invalid
game "Bad ID": discovery[0]: dst_mode "x" invalid
game "Bad ID": discovery[0]: ttl_mode "x" invalid
game "Bad ID": discovery[0]: rate_pps/rate_burst invalid (-1/0)
game "Bad ID": discovery[0]: payload_max 2000 out of range 1..1472
game "Bad ID": discovery[1]: port 67: blocked port (DHCP is never relayed)
game "Bad ID": discovery[1]: port 68: blocked port (DHCP is never relayed)
game "Bad ID": discovery[1]: broadcast spec must not have a group
game "Bad ID": discovery[1]: rate_pps/rate_burst invalid (10/5)
game "Bad ID": discovery[1]: payload_max -1 out of range 1..1472
game "Bad ID": discovery[2]: kind "anycast" invalid
game "Bad ID": invalid id
game "Bad ID": duplicate id
game "Bad ID": name missing
game "Bad ID": category "x" invalid
game "Bad ID": relay_needed "maybe" invalid
game "Bad ID": confidence "x" invalid
game "Bad ID": source_ip "x" invalid
game "Bad ID": discovery specs present but relay_needed=maybe
game "Bad ID": default_enabled requires relay_needed=yes
game "Bad ID": game_ports: unknown protocol "sctp"
game "Bad ID": game_ports.sctp: portspec "0": invalid port "0" (want 1..65535)
game "Bad ID": discovery[0]: portspec "0": invalid port "0" (want 1..65535)
game "Bad ID": discovery[0]: group "" is not an IPv4 multicast address
game "Bad ID": discovery[0]: direction "x" invalid
game "Bad ID": discovery[0]: source_mode "x" invalid
game "Bad ID": discovery[0]: dst_mode "x" invalid
game "Bad ID": discovery[0]: ttl_mode "x" invalid
game "Bad ID": discovery[0]: rate_pps/rate_burst invalid (-1/0)
game "Bad ID": discovery[0]: payload_max 2000 out of range 1..1472
game "Bad ID": discovery[1]: port 67: blocked port (DHCP is never relayed)
game "Bad ID": discovery[1]: port 68: blocked port (DHCP is never relayed)
game "Bad ID": discovery[1]: broadcast spec must not have a group
game "Bad ID": discovery[1]: rate_pps/rate_burst invalid (10/5)
game "Bad ID": discovery[1]: payload_max -1 out of range 1..1472
game "Bad ID": discovery[2]: kind "anycast" invalid
game "y": relay_needed=yes but no discovery spec`
