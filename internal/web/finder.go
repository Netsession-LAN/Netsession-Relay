package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

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
	v.addRows(rc, fs.Groups)
	v.Unknown = finderUnknownRows(st.TopUnknown, games)
	return v
}

// addRows turns the groups the finder saw into table rows; noise that is not shown is only counted.
func (v *finderView) addRows(rc finderRowCtx, groups []engineapi.FinderGroup) {
	for _, g := range groups {
		if rc.hidden(g) {
			v.Hidden++
			continue
		}
		v.Rows = append(v.Rows, rc.row(g))
	}
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
