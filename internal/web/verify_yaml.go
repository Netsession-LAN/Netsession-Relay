package web

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The YAML exports: the games in the seed format of games/games.yaml (with their verifications as comments) and
// the active configuration.

// gamesYAML writes games in the seed format of games/games.yaml; the verifications of each game go into a
// comment above it (the seed format itself has no field for them).
func gamesYAML(games []gamedb.Game, ver map[string][]store.Verification) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Netsession Relay - export %s, seed format of games/games.yaml.\n", time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "# Paste the entries into games/games.yaml (replace the old entry with the same id) and commit.\n\n")
	for _, g := range games {
		for _, v := range ver[g.ID] {
			fmt.Fprintln(&b, verificationComment(v))
		}
		enc := yaml.NewEncoder(&b)
		enc.SetIndent(2)
		if err := enc.Encode([]gamedb.Game{g}); err != nil {
			return nil, err
		}
		enc.Close()
		b.WriteString("\n")
	}
	return b.Bytes(), nil
}

// verificationComment is the comment line of one verification above its game in the export.
func verificationComment(v store.Verification) string {
	line := fmt.Sprintf("# verified %s, %s: %s", v.TS[:min(10, len(v.TS))], v.Event, v.Result)
	if v.Variant != "" {
		line += " (" + v.Variant + ")"
	}
	if v.Note != "" {
		line += " - " + v.Note
	}
	return line + " [" + v.User + "]"
}

func sendYAML(w http.ResponseWriter, name string, body []byte) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// gameYAML exports one game.
func (s *Server) gameYAML(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.Game(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Dieses Spiel gibt es nicht.")
		return
	}
	vs, err := s.Store.Verifications(r.Context(), g.ID)
	if err != nil { // an export without its test results would look complete
		s.failInternal(w, r, "game export: test results", err)
		return
	}
	body, err := gamesYAML([]gamedb.Game{g.Game}, map[string][]store.Verification{g.ID: vs})
	if err != nil {
		s.failInternal(w, r, "game export: YAML", err)
		return
	}
	sendYAML(w, g.ID+".yaml", body)
}

// changedGamesYAML exports every game that was checked at an event or differs from the seed (own games,
// locally edited seed games): exactly what should go back into games/games.yaml.
func (s *Server) changedGamesYAML(w http.ResponseWriter, r *http.Request) {
	games, err := s.Store.Games(r.Context())
	if err != nil {
		s.failInternal(w, r, "games export: list", err)
		return
	}
	all, err := s.Store.Verifications(r.Context(), "")
	if err != nil {
		s.failInternal(w, r, "games export: test results", err)
		return
	}
	ver := map[string][]store.Verification{}
	for _, v := range all {
		ver[v.GameID] = append(ver[v.GameID], v)
	}
	body, err := gamesYAML(changedGames(games, ver), ver)
	if err != nil {
		s.failInternal(w, r, "games export: YAML", err)
		return
	}
	sendYAML(w, "spiele-geprueft-"+time.Now().Format("2006-01-02")+".yaml", body)
}

// changedGames are the games that were checked at an event (ver: verifications by game) or differ from the seed.
func changedGames(games []store.GameRow, ver map[string][]store.Verification) []gamedb.Game {
	var list []gamedb.Game
	for _, g := range games {
		if len(ver[g.ID]) > 0 || g.Origin != "seed" || g.LocallyModified {
			list = append(list, g.Game)
		}
	}
	return list
}

// configYAML exports the active configuration (VLANs, profiles, games of the event) as YAML.
func (s *Server) configYAML(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.ExportConfig(r.Context())
	if err != nil {
		s.failInternal(w, r, "configuration export", err)
		return
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Netsession Relay - configuration of event %q, exported %s.\n# Restore: nsrelay admin import <file>\n",
		c.Event, time.Now().Format("2006-01-02 15:04"))
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		s.failInternal(w, r, "configuration export: YAML", err)
		return
	}
	enc.Close()
	s.audit(r.Context(), s.actor(r), "config.export", c.Event, "", "yaml")
	sendYAML(w, "nsrelay-konfiguration-"+time.Now().Format("2006-01-02")+".yaml", b.Bytes())
}
