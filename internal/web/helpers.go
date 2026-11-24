package web

import (
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

// icon renders an inline reference to the icon sprite.
func icon(name string) template.HTML {
	return template.HTML(`<svg class="ic" aria-hidden="true"><use href="/static/icons.svg#i-` +
		template.HTMLEscapeString(name) + `"></use></svg>`)
}

// icon98 is a colour icon of the joke theme "98" (static/icons98.svg).
func icon98(name string) template.HTML {
	return template.HTML(`<svg class="ic98" aria-hidden="true"><use href="/static/icons98.svg#w-` +
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
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int64:
		n = x
	case uint64:
		n = int64(x)
	case uint32:
		n = int64(x)
	case uint16:
		n = int64(x)
	default:
		return fmt.Sprint(v)
	}
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// tableKey is the short label on a VLAN key cap: table number, GS (gameserver), WL (WiFi), OR (orga).
func tableKey(name, group string) string {
	switch group {
	case compile.GroupGameserver:
		return "GS"
	case compile.GroupWiFi:
		return "WL"
	case compile.GroupOrga:
		return "OR"
	}
	if f := strings.Fields(name); len(f) > 1 {
		return f[len(f)-1]
	}
	if len(name) > 2 {
		return strings.ToUpper(name[:2])
	}
	return strings.ToUpper(name)
}

func modeName(m string) string {
	switch m {
	case compile.ModeActive:
		return "Aktiv"
	case compile.ModeObserve:
		return "Beobachten"
	case compile.ModePaused:
		return "Pausiert"
	case "standby":
		return "Standby"
	case "":
		return "Ohne Konfiguration"
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
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case string:
		t, _ = time.Parse(time.RFC3339Nano, x)
	}
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
	return template.HTML(`<time datetime="` + t.UTC().Format(time.RFC3339) + `" data-fmt="` + template.HTMLEscapeString(kind) +
		`">` + template.HTMLEscapeString(text) + `</time>`)
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

// hostIssueText puts a code of the host preparation of the engine (hostprep) into words.
func hostIssueText(code string) string {
	switch code {
	case "sysctl":
		return "Auf den Relay-Schnittstellen ließ sich das Routing nicht abschalten."
	case "ipv6":
		return "IPv6 ließ sich auf den Relay-Schnittstellen nicht abschalten."
	case "firewall":
		return "Die Host-Firewall für die Relay-Schnittstellen ließ sich nicht einrichten. Dienste des Hosts wie SSH sind damit aus den Spieler-Netzen erreichbar; die Weboberfläche weist solche Verbindungen selbst ab."
	case "mgmt_route":
		return "Der Rückweg für die Verwaltungsadresse ließ sich nicht setzen. Aus einem Spieler-Netz kann die Weboberfläche dann hängen."
	case "forward":
		return "Der Host leitet IPv4 weiter (ip_forward) und die Engine konnte es für die Relay-Schnittstellen nicht verhindern: Der Host kann zwischen den Netzen routen."
	}
	return code
}
