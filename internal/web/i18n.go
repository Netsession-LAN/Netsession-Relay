package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// UI languages. German is the source language: every text is written in German in the templates and the Go code
// and looked up in the English catalog (i18n/en-*.json, German text -> English text) when the page is shown in
// English. A text missing from the catalog stays German, so a forgotten entry never breaks a page.
//
// In templates: {{t "Spieler"}}, with arguments {{t "%d PCs" .N}} (fmt verbs), also for German texts that come
// from Go: {{t .Role}}. Texts with markup: {{th `Das hat <em>nicht</em> geklappt`}} (arguments are escaped). In handlers: T := s.T(r); T("Server-Abfrage ist jetzt %s.", T("an")).
const (
	langDE     = "de"
	langEN     = "en"
	langCookie = "nsrelay_lang"
)

var langs = []string{langDE, langEN}

//go:embed i18n/*.json
var i18nFS embed.FS

// catalogEN maps German texts to English; loaded once from all i18n/en-*.json files.
var catalogEN = mustCatalog()

func mustCatalog() map[string]string {
	c, err := loadCatalog(i18nFS)
	if err != nil {
		panic(err)
	}
	return c
}

// loadCatalog merges the English catalog files. The same German text with two different translations is an error.
func loadCatalog(fsys fs.FS) (map[string]string, error) {
	files, err := fs.Glob(fsys, "i18n/en-*.json")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := map[string]string{}
	from := map[string]string{}
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for de, en := range m {
			if old, ok := out[de]; ok && old != en {
				return nil, fmt.Errorf("%q translated twice: %q (%s) and %q (%s)", de, old, from[de], en, f)
			}
			out[de], from[de] = en, f
		}
	}
	return out, nil
}

// translator returns the text in the page language; with arguments it formats like fmt.Sprintf.
type translator func(s string, args ...any) string

// missesEN collects German texts without English entry that were asked for (tests read it).
var (
	missMu   sync.Mutex
	missesEN = map[string]bool{}
)

func trFor(lang string) translator {
	return func(s string, args ...any) string {
		if lang == langEN && s != "" {
			if t, ok := catalogEN[s]; ok {
				s = t
			} else if strings.IndexFunc(s, unicode.IsLetter) >= 0 { // "–" or a number needs no entry
				missMu.Lock()
				missesEN[s] = true
				missMu.Unlock()
			}
		}
		if len(args) > 0 {
			return fmt.Sprintf(s, args...)
		}
		return s
	}
}

// lookupQuiet translates s when the catalog has it and keeps it otherwise, without recording a miss: for page
// titles and flash messages that may already be translated or contain names.
func lookupQuiet(lang, s string) string {
	if lang == langEN {
		if t, ok := catalogEN[s]; ok {
			return t
		}
	}
	return s
}

// langOf is the language of a request: the cookie set by the language switch, else the server default.
func (s *Server) langOf(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && (c.Value == langDE || c.Value == langEN) {
		return c.Value
	}
	if s.DefaultLang == langEN {
		return langEN
	}
	return langDE
}

// T returns the translator for the language of r.
func (s *Server) T(r *http.Request) translator { return trFor(s.langOf(r)) }

// setLang stores the chosen language in a cookie (a year) and goes back to the page the switch was used on.
func (s *Server) setLang(w http.ResponseWriter, r *http.Request) {
	l := r.PathValue("lang")
	if l != langDE && l != langEN {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: langCookie, Value: l, Path: "/", MaxAge: int((365 * 24 * time.Hour).Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secure(r)})
	http.Redirect(w, r, safeBack(r.URL.Query().Get("zurueck")), http.StatusSeeOther)
}

// jsCatalog is the part of the English catalog the browser script needs (keys listed in jsTexts), as JSON for
// a <script type="application/json"> block; empty for German.
func jsCatalog(lang string) string {
	if lang != langEN {
		return "{}"
	}
	m := map[string]string{}
	for _, k := range jsTexts {
		if v, ok := catalogEN[k]; ok {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// jsTexts are the German texts app.js shows (app.js looks them up with t()).
var jsTexts = []string{
	" Hinweis: ",
	" · gesehen von %s",
	" · seitdem ruhig",
	"%s Spiele gefunden",
	"%s× · zuletzt %s",
	"1 Spiel gefunden",
	"Alarme beendet.",
	"An",
	"Angewendet.",
	"Anmelden",
	"Aus",
	"Betriebsart jetzt: %s. %s.",
	"Betrifft %s Netze",
	"Betrifft 1 Netz",
	"Deine Sitzung ist abgelaufen. Melde dich neu an.",
	"Einschalten",
	"Gesehen",
	"Ja, weiter",
	"Keine Antwort vom Relay-Server. Der Haken wurde nicht geändert.",
	"Keine Antwort vom Relay-Server. Der Schalter wurde nicht geändert.",
	"Keine Verbindung zum Relay-Server. Die Zahlen sind von %s. Neuer Versuch läuft.",
	"Kopieren ging nicht. Der Text ist markiert: Strg+C drücken.",
	"Kopiert.",
	"Live: Stand von %s",
	"Neuer Alarm: ",
	"Sicher?",
	"Technische Details",
	"alles ruhig",
	"betrifft %s Netze",
	"betrifft 1 Netz",
	"getrennt",
	"siehe unten",
	"verbindet",
	"vorhin",
}
