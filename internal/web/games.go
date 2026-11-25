package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// ---- games ----

type gameRow struct {
	store.GameRow
	Ports   string
	Pinned  bool
	RuleIDs string // space separated, for the live counters
	RX      uint64
	Unsure  bool
	Checked string // newest verification result (works|fails|partial) or ""
}

func gameRowOf(g store.GameRow, idx *ruleIndex, v *liveView) gameRow {
	var ports []string
	for _, d := range g.Discovery {
		ports = append(ports, d.Ports)
	}
	row := gameRow{GameRow: g, Ports: strings.Join(ports, ", "), Pinned: g.Pinned,
		Unsure: g.Confidence == "low" || g.RelayNeeded == "unknown"}
	var ids []string
	for _, rv := range rulesFor(idx, g.ID, v) {
		ids = append(ids, rv.ID)
		row.RX += rv.RX
	}
	row.RuleIDs = strings.Join(ids, " ")
	return row
}

// gameGroup is one block of the games list.
type gameGroup struct {
	Key, Title, Sub string
	Rows            []gameRow
}

type gamesData struct {
	Groups         []gameGroup // with traffic, games, platforms, network services
	NoPorts        []gameRow   // ports unknown: nothing to switch yet
	NoLAN          []gameRow   // no LAN broadcast at all
	On, Off, Total int
	Checked        int    // games with a test entry
	View           string // viewOn | viewOff | viewAll
	CanSwitch      bool
}

// Which games the list shows.
const (
	viewOn  = "on" // the default: the working list during a LAN
	viewOff = "off"
	viewAll = "all"
)

// gamesView reads the view from the query (?zeige=aus|alle, like the other German paths of the UI; the older
// ?filter= still works).
func gamesView(r *http.Request) string {
	q := r.URL.Query()
	switch cmp.Or(q.Get("zeige"), q.Get("filter")) {
	case "aus":
		return viewOff
	case "alle", "all":
		return viewAll
	}
	return viewOn
}

func (s *Server) games(w http.ResponseWriter, r *http.Request) {
	games, err := s.Store.Games(r.Context())
	if err != nil {
		s.failInternal(w, r, "games: list", err)
		return
	}
	live := s.liveNow(r.Context())
	idx := s.rules(r.Context(), live.Hash)
	d := gamesData{View: gamesView(r), CanSwitch: store.RoleRank(sessionFrom(r).User.Role) >= store.RoleRank(store.RoleOperator)}
	// pinned games (games.yaml: pinned) first inside their group, then by name
	sort.SliceStable(games, func(i, j int) bool { return gameListedBefore(games[i], games[j]) })
	latest, err := s.Store.LatestVerifications(r.Context())
	s.degraded("games: test results", err)
	d.Checked = len(latest)
	b := newGameListBlocks(s.T(r))
	for _, g := range games {
		row := gameRowOf(g, idx, live)
		row.Checked = latest[g.ID].Result
		d.add(&b, row)
	}
	d.Groups = b.groups()
	s.renderLive(w, r, "games", "Spiele", "games", &d, live)
}

// gameListedBefore orders the games list: pinned games first, then by name.
func gameListedBefore(a, b store.GameRow) bool {
	if a.Pinned != b.Pinned {
		return a.Pinned
	}
	return strings.ToLower(a.Name) < strings.ToLower(b.Name)
}

// gameListBlocks are the blocks of the games list while they are filled.
type gameListBlocks struct {
	hot, plain, platform, service gameGroup
}

func newGameListBlocks(T translator) gameListBlocks {
	return gameListBlocks{
		hot:      gameGroup{Key: "hot", Title: T("Mit Verkehr"), Sub: T("Für diese Spiele kam seit dem Start der Engine etwas an.")},
		plain:    gameGroup{Key: "game", Title: T("Spiele")},
		platform: gameGroup{Key: "platform", Title: T("Plattformen"), Sub: T("Gemeinsame Technik, deckt viele Spiele ab (Steam, DirectPlay, GameSpy …).")},
		service:  gameGroup{Key: "service", Title: T("Netzdienste"), Sub: T("Kein Spiel. Nur einschalten, wenn du weißt warum.")},
	}
}

// add counts a game and files it: games without LAN broadcast and switched-off games without ports get their
// own lists, the others go into a block.
func (d *gamesData) add(b *gameListBlocks, row gameRow) {
	if row.RelayNeeded == "no" {
		d.NoLAN = append(d.NoLAN, row)
		return
	}
	d.Total++
	if row.Enabled {
		d.On++
	} else {
		d.Off++
	}
	if len(row.Discovery) == 0 && !row.Enabled {
		d.NoPorts = append(d.NoPorts, row)
		return
	}
	b.place(row)
}

// place puts a game into its block: switched on with traffic, else by category.
func (b *gameListBlocks) place(row gameRow) {
	switch {
	case row.Enabled && row.RX > 0:
		b.hot.Rows = append(b.hot.Rows, row)
	case row.Category == gamedb.CategoryPlatform:
		b.platform.Rows = append(b.platform.Rows, row)
	case row.Category == gamedb.CategoryGame || row.Category == "":
		b.plain.Rows = append(b.plain.Rows, row)
	default:
		b.service.Rows = append(b.service.Rows, row)
	}
}

// groups gives the blocks that have games, the busiest games first in the traffic block.
func (b *gameListBlocks) groups() []gameGroup {
	sort.SliceStable(b.hot.Rows, func(i, j int) bool { return b.hot.Rows[i].RX > b.hot.Rows[j].RX })
	var out []gameGroup
	for _, g := range []gameGroup{b.hot, b.plain, b.platform, b.service} {
		if len(g.Rows) > 0 {
			out = append(out, g)
		}
	}
	return out
}

func (s *Server) toggleGame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	on := r.PostFormValue("on") == "1"
	path := backPath(r, "/spiele")
	name := s.gameName(r.Context(), id)
	T := s.T(r)
	if err := s.Store.SetGameEnabled(r.Context(), s.actor(r), id, on); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = uimsg.Error("Dieses Spiel gibt es nicht.")
		}
		s.answer(w, r, path, applyAnswer{Error: string(s.userError(r, "game: switch", err))})
		return
	}
	a := s.applyNow(r, fmt.Sprintf("%s %s", id, onOff(on)))
	a.Enabled = on
	if a.OK {
		a.Message = T("%s ist %s (Revision %d).", name, T(onOff(on)), a.Revision)
	}
	s.Log.Info("game toggled", "user", s.actor(r), "game", id, "on", on, "ok", a.OK)
	s.answer(w, r, path, a)
}

// gameName is the display name of a game; its ID when the game is unknown or the database cannot be read.
func (s *Server) gameName(ctx context.Context, id string) string {
	games, err := s.Store.Games(ctx)
	if err != nil {
		return id
	}
	if i := slices.IndexFunc(games, func(g store.GameRow) bool { return g.ID == id }); i >= 0 {
		return games[i].Name
	}
	return id
}
