package web

import (
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"
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
