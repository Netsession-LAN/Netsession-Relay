package web

import (
	"strings"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

func TestFormatNum(t *testing.T) {
	const sp = " "
	for _, c := range []struct {
		in   any
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1" + sp + "000"},
		{int64(120431), "120" + sp + "431"},
		{-1234567, "-1" + sp + "234" + sp + "567"},
		{-999, "-999"},
		{uint64(1 << 40), "1" + sp + "099" + sp + "511" + sp + "627" + sp + "776"},
		{uint32(65536), "65" + sp + "536"},
		{uint16(4711), "4" + sp + "711"},
		{int32(4711), "4711"}, // not one of the template types: printed as it is
		{"x", "x"},
	} {
		if got := formatNum(c.in); got != c.want {
			t.Errorf("formatNum(%T %v) = %q, want %q", c.in, c.in, got, c.want)
		}
	}
}

func TestTableKey(t *testing.T) {
	for _, c := range []struct{ name, group, want string }{
		{"Tisch 12", compile.GroupTables, "12"},
		{"Server", compile.GroupGameserver, "GS"},
		{"WLAN", compile.GroupWiFi, "WL"},
		{"Orga", compile.GroupOrga, "OR"},
		{"lounge", "", "LO"},
		{"x", "", "X"},
	} {
		if got := tableKey(c.name, c.group); got != c.want {
			t.Errorf("tableKey(%q, %q) = %q, want %q", c.name, c.group, got, c.want)
		}
	}
}

func TestModeAndHostIssueNames(t *testing.T) {
	if modeName("") != "Ohne Konfiguration" || modeName(compile.ModeActive) != "Aktiv" || modeName("other") != "other" {
		t.Error("modeName")
	}
	if !strings.HasPrefix(hostIssueText("ipv6"), "IPv6 ") || hostIssueText("new_code") != "new_code" {
		t.Error("hostIssueText")
	}
}

func TestTimeTag(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 30, 5, 0, time.UTC)
	want := `<time datetime="2026-11-16T12:30:05Z" data-fmt="time">`
	for _, v := range []any{at, at.Format(time.RFC3339Nano)} {
		if got := string(timeTag(v, "time")); !strings.HasPrefix(got, want) {
			t.Errorf("timeTag(%T) = %s", v, got)
		}
	}
	for _, v := range []any{time.Time{}, "", "no time", 42} {
		if got := timeTag(v, "datetime"); got != "–" {
			t.Errorf("timeTag(%#v) = %s, want –", v, got)
		}
	}
}
