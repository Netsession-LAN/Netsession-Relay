package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// noisePorts are broadcasts every LAN has; the port finder can hide them.
var noisePorts = map[uint16]string{
	137: "NetBIOS", 138: "NetBIOS", 1900: "SSDP", 3702: "WS-Discovery", 5353: "mDNS", 5355: "LLMNR",
	17500: "Dropbox", 27036: "Steam", 57621: "Spotify", 6646: "McAfee", 1947: "Sentinel",
}

// gameHint is a game of the database that uses a port, with its state in the active event.
type gameHint struct {
	ID, Name string
	On       bool
}

type finderRow struct {
	Dest     string // "Broadcast" (translated by the template) or the multicast group
	Kind     string
	Group    string
	Port     uint16
	Rule     string
	Games    []gameHint // games of the active rule, or of the database for an unknown port
	Noise    string
	Count    uint64
	Interval string
	Size     string
	Srcs     string
	Nets     string
	New      bool
	Offset   string
	Hex      string
	ASCII    string
}

type unknownRow struct {
	Port  uint16
	Count uint32
	Noise string
	Games []gameHint
}

type finderView struct {
	Status    engineapi.FinderStatus
	Err       bool
	Rows      []finderRow
	Hidden    int
	ShowNoise bool
	Left      string
	Unknown   []unknownRow
	VLANs     []store.VLANConfig
	Picked    map[int]bool
}

// portGames finds database games whose discovery uses kind/group/port.
func portGames(games []store.GameRow, kind, group string, port uint16) []gameHint {
	uses := func(d gamedb.Spec) bool { return specUsesPort(d, kind, group, port) }
	var out []gameHint
	for _, g := range games {
		if slices.ContainsFunc(g.Discovery, uses) {
			out = append(out, gameHint{ID: g.ID, Name: g.Name, On: g.Enabled})
		}
	}
	return out
}

// specUsesPort reports whether a discovery spec covers kind/port (and, for multicast, the group).
func specUsesPort(d gamedb.Spec, kind, group string, port uint16) bool {
	if d.Kind != kind || (kind == gamedb.KindMulticast && d.Group != group) {
		return false
	}
	ps, err := portspec.Parse(d.Ports)
	return err == nil && ps.Contains(port)
}

func hexASCII(b []byte) (string, string) {
	var h, a strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			h.WriteByte(' ')
		}
		fmt.Fprintf(&h, "%02x", c)
		if c >= 0x20 && c < 0x7f {
			a.WriteByte(c)
		} else {
			a.WriteByte('.')
		}
	}
	return h.String(), a.String()
}

// finderRowCtx is what turning the engine's finder groups into table rows needs besides the group itself.
type finderRowCtx struct {
	games     []store.GameRow
	gameByID  map[string]gameHint
	idx       *ruleIndex
	vlanName  map[int]string
	started   time.Time
	showNoise bool
}

func (s *Server) finderData(ctx context.Context, showNoise bool) finderView {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	v := finderView{ShowNoise: showNoise, Picked: map[int]bool{}}
	fs, err := s.Engine.Finder(ctx)
	v.Err = err != nil
	v.Status = fs
	games, err := s.Store.Games(ctx)
	s.degraded("port finder: games", err)
	rc := finderRowCtx{games: games, gameByID: finderGameHints(games), started: fs.Started, showNoise: showNoise}
	st, _ := s.Engine.Status(ctx)
	rc.idx = s.rules(ctx, st.SnapshotHash)
	rc.vlanName = map[int]string{}
	if c, err := s.Store.ExportConfig(ctx); err == nil {
		v.VLANs = c.VLANs
		for _, x := range c.VLANs {
			rc.vlanName[x.VID] = x.Name
		}
	}
	for _, id := range fs.VIDs {
		v.Picked[id] = true
	}
	if fs.Active {
		v.Left = finderLeft(time.Until(fs.Until))
	}
	for _, g := range fs.Groups {
		if rc.hidden(g) {
			v.Hidden++
			continue
		}
		v.Rows = append(v.Rows, rc.row(g))
	}
	for _, p := range st.TopUnknown {
		v.Unknown = append(v.Unknown, unknownRow{Port: p.Port, Count: p.Count, Noise: noisePorts[p.Port],
			Games: portGames(games, gamedb.KindBroadcast, "", p.Port)})
	}
	return v
}

// finderGameHints indexes the games of the database by ID.
func finderGameHints(games []store.GameRow) map[string]gameHint {
	out := map[string]gameHint{}
	for _, g := range games {
		out[g.ID] = gameHint{ID: g.ID, Name: g.Name, On: g.Enabled}
	}
	return out
}

// finderLeft shows the time left of a running session as m:ss.
func finderLeft(left time.Duration) string {
	secs := int(left.Seconds())
	return fmt.Sprintf("%d:%02d", secs/60, secs%60)
}

// hidden reports whether a group is background noise of every LAN and noise is not shown. Multicast groups are
// never noise.
func (rc finderRowCtx) hidden(g engineapi.FinderGroup) bool {
	return g.Kind != gamedb.KindMulticast && noisePorts[g.Port] != "" && !rc.showNoise
}

// row turns one group the finder saw into a table row.
func (rc finderRowCtx) row(g engineapi.FinderGroup) finderRow {
	row := finderRow{Kind: g.Kind, Group: g.Group, Port: g.Port, Rule: g.Rule, Count: g.Count, New: g.New,
		Noise: noisePorts[g.Port], Dest: "Broadcast"}
	if g.Kind == gamedb.KindMulticast {
		row.Dest, row.Noise = g.Group, ""
	}
	row.Games = rc.rowGames(g)
	if g.Count > 1 {
		row.Interval = fmt.Sprintf("%.1f s", g.Last.Sub(g.First).Seconds()/float64(g.Count-1))
	}
	row.Size = finderSize(g.MinLen, g.MaxLen)
	row.Srcs = finderSrcs(g.Srcs)
	row.Nets = rc.nets(g.VIDs)
	row.Offset = fmt.Sprintf("+%d s", int(g.First.Sub(rc.started).Seconds()))
	row.Hex, row.ASCII = hexASCII(g.Sample)
	return row
}

// rowGames gives the games of the active rule that forwards a group, or, when no rule does, the games of the
// database that use its port.
func (rc finderRowCtx) rowGames(g engineapi.FinderGroup) []gameHint {
	r, ok := rc.idx.byRule[g.Rule]
	if !ok {
		return portGames(rc.games, g.Kind, g.Group, g.Port)
	}
	var out []gameHint
	for _, id := range r.Specs {
		if h, ok := rc.gameByID[id]; ok {
			out = append(out, h)
		}
	}
	return out
}

// nets names the networks a group was seen in, "VLAN <id>" for one without a name.
func (rc finderRowCtx) nets(vids []int) string {
	nets := make([]string, len(vids))
	for i, id := range vids {
		nets[i] = rc.vlanName[id]
		if nets[i] == "" {
			nets[i] = fmt.Sprintf("VLAN %d", id)
		}
	}
	return strings.Join(nets, ", ")
}

// finderSize shows the payload size of a group, as a range when it varies.
func finderSize(minLen, maxLen int) string {
	if maxLen != minLen {
		return fmt.Sprintf("%d–%d B", minLen, maxLen)
	}
	return fmt.Sprintf("%d B", minLen)
}

// finderSrcs lists the sender addresses of a group.
func finderSrcs(srcs []netip.Addr) string {
	out := make([]string, len(srcs))
	for i, a := range srcs {
		out[i] = a.String()
	}
	return strings.Join(out, ", ")
}

// finder is the port finder page; ?teil=ergebnis returns only the live part.
func (s *Server) finder(w http.ResponseWriter, r *http.Request) {
	v := s.finderData(r.Context(), r.URL.Query().Get("laerm") == "1")
	if r.URL.Query().Get("teil") == "ergebnis" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.page(r, "finder").ExecuteTemplate(w, "finder-result", v); err != nil {
			s.Log.Error("render finder fragment", "err", err)
		}
		return
	}
	s.render(w, r, "finder", "Port-Finder", "finder", v)
}

func (s *Server) finderStart(w http.ResponseWriter, r *http.Request) {
	req := engineapi.FinderStart{Src: strings.TrimSpace(r.PostFormValue("src"))}
	for _, x := range r.PostForm["vid"] {
		if id, err := strconv.Atoi(x); err == nil {
			req.VIDs = append(req.VIDs, id)
		}
	}
	T := s.T(r)
	if !finderSrcValid(req.Src) {
		back(w, r, "/finder", "", errors.New(T("„%s“ ist keine IPv4-Adresse", req.Src)))
		return
	}
	mins, _ := strconv.Atoi(r.PostFormValue("minuten"))
	req.Seconds = max(1, min(mins, 10)) * 60
	if err := s.Engine.FinderStart(r.Context(), req); err != nil {
		s.Log.Error("port finder not started: engine not reachable or refused", "err", err)
		back(w, r, "/finder", "", errors.New(T("Der Port-Finder startet nicht: Die Engine antwortet nicht oder lehnt ab. Einzelheiten stehen im Log des Relays.")))
		return
	}
	s.audit(r.Context(), s.actor(r), "finder.start", fmt.Sprint(req.VIDs), "", req.Src)
	back(w, r, "/finder", T("Port-Finder läuft. Jetzt das Spiel starten und den LAN-Browser öffnen."), nil)
}

// finderSrcValid reports whether the sender filter of the port finder is empty (every sender) or an IPv4 address.
func finderSrcValid(src string) bool {
	if src == "" {
		return true
	}
	a, err := netip.ParseAddr(src)
	return err == nil && a.Is4()
}

func (s *Server) finderStop(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.FinderStop(r.Context()); err != nil {
		s.Log.Error("port finder not stopped: engine not reachable", "err", err)
		back(w, r, "/finder", "", errors.New(s.T(r)("Die Engine antwortet nicht. Der Port-Finder läuft in der Engine.")))
		return
	}
	back(w, r, "/finder", s.T(r)("Port-Finder gestoppt. Die Ergebnisse bleiben bis zum nächsten Start."), nil)
}

func (s *Server) finderPcap(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	s.audit(ctx, s.actor(r), "finder.pcap", "", "", "")
	w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nsrelay-finder-%s.pcap"`, time.Now().Format("20060102-150405")))
	if err := s.Engine.FinderPcap(ctx, w); err != nil {
		s.Log.Warn("finder pcap", "err", err)
	}
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	r := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")
	s := strings.Trim(slugRE.ReplaceAllString(r.Replace(strings.ToLower(name)), "_"), "_")
	if s == "" || s[0] == '_' {
		s = "spiel_" + s
	}
	return s
}

// gameError puts an error of storing a game into words, in the language of r: what is wrong with the entry, or
// (anything that is not about the entry) an internal error.
func (s *Server) gameError(r *http.Request, err error) string {
	T := s.T(r)
	switch {
	case errors.Is(err, store.ErrExists):
		return T("Diese Kennung gibt es schon. Bitte eine andere wählen.")
	case errors.Is(err, gamedb.ErrInvalidID):
		return T("Die Kennung darf nur Kleinbuchstaben, Ziffern und _ enthalten.")
	case errors.Is(err, gamedb.ErrNameMissing):
		return T("Bitte einen Namen eingeben.")
	case errors.Is(err, gamedb.ErrBlockedPort):
		return T("DHCP (67/68) wird nie weitergeleitet.")
	}
	if errors.Is(err, gamedb.ErrInvalid) {
		return T("Das Spiel passt nicht: %s", err.Error())
	}
	return string(s.userError(r, "game: store", err))
}
