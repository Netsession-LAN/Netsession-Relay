package web

import (
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// icon renders an inline reference to the icon sprite.
func icon(name string) template.HTML {
	return template.HTML(`<svg class="ic" aria-hidden="true"><use href="/static/icons.svg#i-` + //nolint:gosec // G203: every variable part is escaped (HTMLEscapeString / escapeArgs)
		template.HTMLEscapeString(name) + `"></use></svg>`)
}

// icon98 is a colour icon of the joke theme "98" (static/icons98.svg).
func icon98(name string) template.HTML {
	return template.HTML(`<svg class="ic98" aria-hidden="true"><use href="/static/icons98.svg#w-` + //nolint:gosec // G203: every variable part is escaped (HTMLEscapeString / escapeArgs)
		template.HTMLEscapeString(name) + `"></use></svg>`)
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "–"
	}
	return h
}

// formatNum groups thousands with a thin space ("120 431").
func formatNum(v any) string {
	n, ok := numberOf(v)
	if !ok {
		return fmt.Sprint(v)
	}
	return groupThousands(n)
}

// numberOf converts the integer types the templates pass to formatNum.
func numberOf(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case uint64:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint16:
		return int64(x), true
	}
	return 0, false
}

// groupThousands writes n with a narrow space between groups of three digits.
func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	digits := strings.TrimPrefix(s, "-")
	var b strings.Builder
	b.WriteString(s[:len(s)-len(digits)]) // the sign
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(c)
	}
	return b.String()
}

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

// onOff is the word for a switch position ("an"/"aus"); callers translate it.
func onOff(on bool) string {
	if on {
		return "an"
	}
	return "aus"
}

func urlQuery(s string) string { return url.QueryEscape(s) }

// timeTag renders a point in time as <time>. The text is the server's zone (UTC in the container); the
// script replaces it with the browser's local time. kind: "datetime" | "time".
func timeTag(v any, kind string) template.HTML {
	t := timeOf(v)
	if t.IsZero() {
		return "–"
	}
	layout := "02.01. 15:04"
	if kind == "time" {
		layout = "15:04:05"
	}
	text := t.Local().Format(layout)
	if name, off := t.Local().Zone(); off == 0 {
		text += " " + name
	}
	return template.HTML(`<time datetime="` + t.UTC().Format(time.RFC3339) + `" data-fmt="` + template.HTMLEscapeString(kind) + //nolint:gosec // G203: every variable part is escaped (HTMLEscapeString / escapeArgs)
		`">` + template.HTMLEscapeString(text) + `</time>`)
}

// timeOf reads the point in time a template passes to timeTag: a time.Time or an RFC 3339 text (zero if neither).
func timeOf(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case string:
		t, _ := time.Parse(time.RFC3339Nano, x)
		return t
	}
	return time.Time{}
}

// dict builds a map for passing several values to a sub-template.
func dict(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return m
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
