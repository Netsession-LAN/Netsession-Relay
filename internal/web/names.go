package web

import (
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Names for the templates: what the codes of modes, roles, network groups, the game database and the host
// preparation are called on the page.

// groupKeys are the key cap labels of the built-in groups other than the tables.
var groupKeys = map[string]string{
	compile.GroupGameserver: "GS",
	compile.GroupWiFi:       "WL",
	compile.GroupOrga:       "OR",
}

// tableKey is the short label on a VLAN key cap: table number, GS (gameserver), WL (WiFi), OR (orga).
func tableKey(name, group string) string {
	if k, ok := groupKeys[group]; ok {
		return k
	}
	if f := strings.Fields(name); len(f) > 1 {
		return f[len(f)-1]
	}
	if len(name) > 2 {
		return strings.ToUpper(name[:2])
	}
	return strings.ToUpper(name)
}

// modeNames are the words for the operating modes ("" is an engine without configuration).
var modeNames = map[string]string{
	compile.ModeActive:  "Aktiv",
	compile.ModeObserve: "Beobachten",
	compile.ModePaused:  "Pausiert",
	"standby":           "Standby",
	"":                  "Ohne Konfiguration",
}

func modeName(m string) string {
	if n, ok := modeNames[m]; ok {
		return n
	}
	return m
}

func roleName(r string) string {
	switch r {
	case store.RoleAdmin:
		return "Admin"
	case store.RoleOperator:
		return "Operator"
	case store.RoleViewer:
		return "Betrachter"
	}
	return r
}

// groupName is the display name of a built-in network group; any other group shows as it is named.
func groupName(g string) string {
	switch g {
	case compile.GroupTables:
		return "Tische"
	case compile.GroupGameserver:
		return "Gameserver"
	case compile.GroupWiFi:
		return "WLAN"
	case compile.GroupOrga:
		return "Orga"
	}
	return g
}

// kindName, dirName and confName put the game database vocabulary into words.
func kindName(k string) string {
	switch k {
	case "broadcast":
		return "Broadcast"
	case "multicast":
		return "Multicast"
	}
	return k
}

func dirName(d string) string {
	switch d {
	case "query":
		return "Suchende fragen"
	case "announce":
		return "Host meldet sich"
	case "both":
		return "Suchende und Host"
	}
	return "–"
}

func confName(c string) string {
	switch c {
	case "high":
		return "Belegt"
	case "medium":
		return "Wahrscheinlich"
	case "low":
		return "Unsicher"
	}
	return c
}

// hostIssueTexts put the codes of the host preparation of the engine (hostprep) into words.
var hostIssueTexts = map[string]string{
	"sysctl":     "Auf den Relay-Schnittstellen ließ sich das Routing nicht abschalten.",
	"ipv6":       "IPv6 ließ sich auf den Relay-Schnittstellen nicht abschalten.",
	"firewall":   "Die Host-Firewall für die Relay-Schnittstellen ließ sich nicht einrichten. Dienste des Hosts wie SSH sind damit aus den Spieler-Netzen erreichbar; die Weboberfläche weist solche Verbindungen selbst ab.",
	"mgmt_route": "Der Rückweg für die Verwaltungsadresse ließ sich nicht setzen. Aus einem Spieler-Netz kann die Weboberfläche dann hängen.",
	"forward":    "Der Host leitet IPv4 weiter (ip_forward) und die Engine konnte es für die Relay-Schnittstellen nicht verhindern: Der Host kann zwischen den Netzen routen.",
}

// hostIssueText puts a code of the host preparation into words; an unknown code stays as it is.
func hostIssueText(code string) string {
	if t, ok := hostIssueTexts[code]; ok {
		return t
	}
	return code
}
