package opnsense

import (
	"fmt"
	"io"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
	"github.com/Netsession2024/Netsession-Relay/internal/portspec"
)

// Use is one relay entry for a port on one box.
type Use struct {
	Box string
	Relay
}

// PortEntry collects everything configured for one (port, multicast group).
type PortEntry struct {
	Port      int
	Multicast string
	Uses      []Use
	Games     []string // matching game ids from the database
	Findings  []string // German, for the report
}

// Active returns the enabled uses.
func (p PortEntry) Active() []Use {
	var out []Use
	for _, u := range p.Uses {
		if u.Enabled {
			out = append(out, u)
		}
	}
	return out
}

// Report is the import result.
type Report struct {
	Boxes   []Box
	Ports   []PortEntry
	Suggest []string // games with an active entry on the boxes: hint what was played, NOT a source of truth
	Unknown []int    // active ports without a game in the database
}

// Finding codes summarised in the header.
type Summary struct {
	Ports, Active, Duplicates, Disabled, SourceRewrite, Noise, Unknown int
}

// BuildReport matches the boxes against the game database.
func BuildReport(boxes []Box, db *gamedb.DB) Report {
	type key struct {
		port int
		mc   string
	}
	idx := map[key]*PortEntry{}
	for _, b := range boxes {
		for _, r := range b.Relays {
			k := key{r.Port, r.Multicast}
			pe := idx[k]
			if pe == nil {
				pe = &PortEntry{Port: r.Port, Multicast: r.Multicast}
				idx[k] = pe
			}
			pe.Uses = append(pe.Uses, Use{Box: b.Name, Relay: r})
		}
	}
	rep := Report{Boxes: boxes}
	suggest := map[string]bool{}
	for _, pe := range idx {
		games, needOriginal, noise := matchGames(db, pe.Port, pe.Multicast)
		pe.Games = games
		active := pe.Active()
		boxesActive := map[string]bool{}
		for _, u := range active {
			boxesActive[u.Box] = true
		}
		switch {
		case len(active) == 0:
			pe.Findings = append(pe.Findings, "nur deaktivierte Einträge")
		case len(boxesActive) > 1:
			names := keys(boxesActive)
			pe.Findings = append(pe.Findings, fmt.Sprintf("DOPPELT aktiv auf %s: Pakete kommen mehrfach an, Schleifengefahr", strings.Join(names, ", ")))
		}
		for _, u := range active {
			if u.RewritesSource() && needOriginal {
				pe.Findings = append(pe.Findings, fmt.Sprintf("%s schreibt die Absender-IP um (-s %s), das Spiel braucht laut Datenbank die Original-IP", u.Box, u.Source))
				break
			}
		}
		if noise && len(active) > 0 {
			pe.Findings = append(pe.Findings, "Netzwerkdienst statt Spiel (im neuen Relay standardmäßig aus)")
		}
		if len(games) == 0 && len(active) > 0 {
			if mcGames, group := multicastOwner(db, pe.Port); pe.Multicast == "" && len(mcGames) > 0 {
				pe.Findings = append(pe.Findings, fmt.Sprintf("Port gehört zu %s als Multicast %s; ohne Multicast-Gruppe erreicht der Eintrag das Spiel vermutlich nie",
					strings.Join(mcGames, ", "), group))
				for _, g := range mcGames {
					suggest[g] = true
				}
			} else {
				pe.Findings = append(pe.Findings, "kein Spiel in der Datenbank: als eigenes Spiel anlegen oder weglassen")
				rep.Unknown = append(rep.Unknown, pe.Port)
			}
		}
		if len(active) > 0 && !noise {
			for _, g := range games {
				suggest[g] = true
			}
		}
		rep.Ports = append(rep.Ports, *pe)
	}
	sort.Slice(rep.Ports, func(i, j int) bool {
		if rep.Ports[i].Port != rep.Ports[j].Port {
			return rep.Ports[i].Port < rep.Ports[j].Port
		}
		return rep.Ports[i].Multicast < rep.Ports[j].Multicast
	})
	rep.Suggest = keys(suggest)
	slices.Sort(rep.Unknown)
	rep.Unknown = slices.Compact(rep.Unknown)
	return rep
}

// matchGames returns games whose discovery covers the port (and group for multicast), whether any of them
// needs the original sender IP, and whether all matches are noise services.
func matchGames(db *gamedb.DB, port int, mc string) (ids []string, needOriginal, noise bool) {
	noise = true
	for _, g := range db.Games {
		for _, s := range g.Discovery {
			ps, err := portspec.Parse(s.Ports)
			if err != nil || !ps.Contains(uint16(port)) {
				continue
			}
			if s.Kind == gamedb.KindMulticast {
				if mc == "" || s.Group != mc {
					continue
				}
			} else if mc != "" {
				if a, err := netip.ParseAddr(mc); err == nil && a.IsMulticast() {
					continue
				}
			}
			ids = append(ids, g.ID)
			if g.SourceIP == gamedb.SourceOriginal {
				needOriginal = true
			}
			if g.Category != gamedb.CategoryNoise {
				noise = false
			}
			break
		}
	}
	if len(ids) == 0 {
		noise = false
	}
	slices.Sort(ids)
	return slices.Compact(ids), needOriginal, noise
}

// multicastOwner returns games that use the port with a multicast group (and the group).
func multicastOwner(db *gamedb.DB, port int) ([]string, string) {
	var ids []string
	group := ""
	for _, g := range db.Games {
		for _, s := range g.Discovery {
			if s.Kind != gamedb.KindMulticast {
				continue
			}
			if ps, err := portspec.Parse(s.Ports); err == nil && ps.Contains(uint16(port)) {
				ids = append(ids, g.ID)
				group = s.Group
			}
		}
	}
	return ids, group
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Summary counts findings.
func (r Report) Summary() Summary {
	var s Summary
	for _, p := range r.Ports {
		s.Ports++
		if len(p.Active()) > 0 {
			s.Active++
		}
		for _, f := range p.Findings {
			switch {
			case strings.HasPrefix(f, "DOPPELT"):
				s.Duplicates++
			case strings.HasPrefix(f, "nur deaktivierte"):
				s.Disabled++
			case strings.Contains(f, "-s "):
				s.SourceRewrite++
			case strings.HasPrefix(f, "Netzwerkdienst"):
				s.Noise++
			case strings.HasPrefix(f, "kein Spiel"):
				s.Unknown++
			}
		}
	}
	return s
}

// WriteText prints the report in German.
func (r Report) WriteText(w io.Writer) {
	fmt.Fprintln(w, "OPNsense-Import: Bericht")
	for _, b := range r.Boxes {
		on := 0
		for _, rel := range b.Relays {
			if rel.Enabled {
				on++
			}
		}
		fmt.Fprintf(w, "  %-14s %-28s %3d Einträge, %3d aktiv\n", b.Name, b.Hostname, len(b.Relays), on)
	}
	s := r.Summary()
	fmt.Fprintf(w, "\n%d Ports (%d aktiv): %d doppelt, %d nur deaktiviert, %d mit Absender-Umschreibung bei Original-IP-Spielen, %d Netzwerkdienste, %d ohne Spiel\n\n",
		s.Ports, s.Active, s.Duplicates, s.Disabled, s.SourceRewrite, s.Noise, s.Unknown)
	for _, p := range r.Ports {
		label := fmt.Sprint(p.Port)
		if p.Multicast != "" {
			label = p.Multicast + ":" + label
		}
		var uses []string
		for _, u := range p.Uses {
			st := "an"
			if !u.Enabled {
				st = "aus"
			}
			src := ""
			if u.Source != "" {
				src = " -s " + u.Source
			}
			uses = append(uses, fmt.Sprintf("%s#%d %s%s", u.Box, u.InstanceID, st, src))
		}
		games := "–"
		if len(p.Games) > 0 {
			games = strings.Join(p.Games, ", ")
		}
		fmt.Fprintf(w, "%-22s %-40s %s\n", label, strings.Join(uses, " · "), games)
		for _, f := range p.Findings {
			fmt.Fprintf(w, "%22s ! %s\n", "", f)
		}
	}
	if len(r.Suggest) > 0 {
		fmt.Fprintf(w, "\nLief bisher auf den Boxen (%d Spiele, nur als Hinweis; maßgeblich ist die Spiele-Datenbank aus der Recherche):\n  %s\n",
			len(r.Suggest), strings.Join(r.Suggest, " "))
	}
}
