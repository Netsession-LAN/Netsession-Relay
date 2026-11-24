package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Step 2 of the setup ("Netz"): the networks on the trunk, checked by the engine before anything is stored.

// setupExample is a generic LAN party: a server network, four tables, an orga network.
func setupExample(T translator) []setupRow {
	rows := []setupRow{{"10", "Server", "10.10.0.0/24", compile.GroupGameserver}}
	for n := 1; n <= 4; n++ {
		rows = append(rows, setupRow{strconv.Itoa(10 + n), T("Tisch %d", n), fmt.Sprintf("10.10.%d.0/24", n), compile.GroupTables})
	}
	return append(rows, setupRow{"99", "Orga", "10.10.99.0/24", compile.GroupOrga})
}

// readSetupNetworkForm reads the posted form of step 2, at most setupRowsMax network rows.
func readSetupNetworkForm(r *http.Request) setupForm {
	f := setupForm{Event: strings.TrimSpace(r.PostFormValue("event")), Trunk: r.PostFormValue("trunk"),
		TrunkOther: strings.TrimSpace(r.PostFormValue("trunk_other")), Host: strings.TrimSpace(r.PostFormValue("host")),
		Mode: r.PostFormValue("mode")}
	vids, names, cidrs, groups := r.PostForm["vid"], r.PostForm["name"], r.PostForm["cidr"], r.PostForm["group"]
	for i := range min(len(vids), len(names), len(cidrs), len(groups), setupRowsMax) {
		f.Rows = append(f.Rows, setupRow{strings.TrimSpace(vids[i]), strings.TrimSpace(names[i]), strings.TrimSpace(cidrs[i]), groups[i]})
	}
	return f
}

// setupFormAction applies the buttons that only change the form: "beispiel" fills in the example, "mehr" adds
// up to four empty rows (at most setupRowsMax in all). It reports whether action was one of them.
func (f *setupForm) setupFormAction(action string, T translator) bool {
	switch action {
	case "beispiel":
		f.Rows = setupExample(T)
	case "mehr":
		for range min(4, setupRowsMax-len(f.Rows)) {
			f.Rows = append(f.Rows, setupRow{Group: compile.GroupTables})
		}
	default:
		return false
	}
	return true
}

// setupNetwork handles step 2: the buttons that only change the form (example, more rows) and the finish.
func (s *Server) setupNetwork(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.needsNetwork(ctx) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	T := s.T(r)
	f := readSetupNetworkForm(r)
	show := func(code int) {
		s.setupDefaults(ctx, &f)
		w.WriteHeader(code)
		s.render(w, r, "setup", "Einrichten", "setup", f)
	}
	reject := func(msg string) {
		f.Err = msg
		show(http.StatusUnprocessableEntity)
	}
	if f.setupFormAction(r.PostFormValue("aktion"), T) {
		show(http.StatusOK)
		return
	}
	cfg, trunk, msg := s.setupConfig(r, &f)
	if msg != "" {
		reject(msg)
		return
	}
	if err := s.Store.ImportConfig(ctx, s.actor(r), cfg); err != nil {
		reject(string(s.userError(r, "setup: store the configuration", err)))
		return
	}
	a := s.applyNow(r, "Einrichtung")
	s.Log.Info("setup: network configured", "user", s.actor(r), "trunk", trunk, "vlans", len(cfg.VLANs), "mode", f.Mode, "ok", a.OK)
	if !a.OK {
		back(w, r, "/", "", errors.New(T("Die Einrichtung ist gespeichert, aber das Anwenden ist fehlgeschlagen: %s", a.Error)))
		return
	}
	back(w, r, "/", setupDoneMessage(f.Mode, len(cfg.VLANs), len(cfg.Games), T), nil)
}

// setupConfig checks the finished form of step 2 and builds the configuration from it (cfg.VLANs are the
// networks of the rows). msg is "" or what is wrong, in the language of T; a missing second network also sets
// f.ErrNetwork.
func (s *Server) setupConfig(r *http.Request, f *setupForm) (cfg store.Config, trunk, msg string) {
	T := s.T(r)
	trunk, host, msg := s.setupBasics(r.Context(), *f, T)
	if msg != "" {
		return cfg, trunk, msg
	}
	vlans, msg := setupVLANs(f.Rows, trunk, host, T)
	if msg == "" && len(vlans) < 2 {
		f.ErrNetwork = true
		msg = T("Bitte mindestens zwei Netze eintragen. Das Relay verbindet Netze miteinander.")
	}
	if msg != "" {
		return cfg, trunk, msg
	}
	cfg, msg = s.setupCheck(r, *f, vlans)
	return cfg, trunk, msg
}

// setupDoneMessage is the message after a successful setup with n networks and the given number of games.
func setupDoneMessage(mode string, n, games int, T translator) string {
	if mode == compile.ModeActive {
		return T("Fertig eingerichtet: %d Netze, %d Spiele. Das Relay leitet jetzt weiter.", n, games)
	}
	return T("Fertig eingerichtet: %d Netze, %d Spiele. Das Relay beobachtet jetzt. Auf „Aktiv“ stellst du unter System.", n, games)
}

// setupBasics checks the fields above the list of networks: trunk interface, mode, name of the event and the
// host part of the relay address. msg is "" or what is wrong, in the language of T.
func (s *Server) setupBasics(ctx context.Context, f setupForm, T translator) (trunk string, host int, msg string) {
	// trunk: the typed name wins over the list
	trunk = cmp.Or(f.TrunkOther, f.Trunk)
	if !ifName.MatchString(trunk) {
		return "", 0, T("Bitte die Schnittstelle wählen, an der die Netze als Trunk anliegen.")
	}
	if msg := s.setupTrunkProblem(ctx, trunk, T); msg != "" {
		return "", 0, msg
	}
	if f.Mode != compile.ModeObserve && f.Mode != compile.ModeActive {
		return "", 0, T("Bitte eine Betriebsart wählen.")
	}
	if f.Event == "" || len(f.Event) > 60 {
		return "", 0, T("Bitte einen Namen für die LAN eingeben (höchstens 60 Zeichen).")
	}
	host, err := strconv.Atoi(f.Host)
	if err != nil || host < 1 || host > 65534 {
		return "", 0, T("Die Stelle der Relay-Adresse muss eine Zahl sein, z. B. 250.")
	}
	return trunk, host, ""
}

// setupVLANs turns the filled rows into networks on the trunk; the relay address of each is the host-th address
// of its network. msg is "" or what is wrong with which row.
func setupVLANs(rows []setupRow, trunk string, host int, T translator) (vlans []store.VLANConfig, msg string) {
	for n, row := range rows {
		if row.empty() {
			continue
		}
		v, msg := setupVLAN(row, trunk, host, vlans, T)
		if msg != "" {
			return nil, T("Netz in Zeile %d: %s", n+1, msg)
		}
		vlans = append(vlans, v)
	}
	return vlans, ""
}

// setupVLAN turns one row into a network on the trunk; before are the networks of the rows above. msg is "" or
// what is wrong with the row.
func setupVLAN(row setupRow, trunk string, host int, before []store.VLANConfig, T translator) (store.VLANConfig, string) {
	relay, msg := setupRelayAddr(row.CIDR, host, T)
	if msg != "" {
		return store.VLANConfig{}, msg
	}
	v, msg := vlanForm{VID: row.VID, Name: row.Name, Group: row.Group, CIDR: row.CIDR, RelayIP: relay,
		Attach: compile.AttachTrunk, Parent: trunk, Enable: true}.parse(T)
	switch {
	case msg != "":
		return v, msg
	case !slices.Contains(defaultGroups, v.Group):
		return v, T("Bitte eine der Gruppen wählen.")
	case slices.ContainsFunc(before, func(x store.VLANConfig) bool { return x.VID == v.VID }):
		return v, T("VLAN %d gibt es schon.", v.VID)
	}
	return v, ""
}

// setupRelayAddr is the host-th address of the network cidr. It is "" when cidr is no IPv4 network with room
// for hosts (/30 or larger), which the check of the row then reports; msg is set when host does not fit.
func setupRelayAddr(cidr string, host int, T translator) (relay, msg string) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() || p.Bits() > 30 {
		return "", ""
	}
	size := 1 << (32 - p.Bits())
	if host > size-2 {
		return "", T("Die Stelle %d passt nicht in %s (höchstens %d).", host, p.Masked(), size-2)
	}
	b := p.Masked().Addr().As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v += uint32(host)
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}).String(), ""
}

// setupCheck compiles the configuration of the setup and lets the engine check it, before anything is stored.
// msg is "" or why it cannot be used.
func (s *Server) setupCheck(r *http.Request, f setupForm, vlans []store.VLANConfig) (cfg store.Config, msg string) {
	ctx := r.Context()
	T := s.T(r)
	db, err := s.Store.GameDB(ctx)
	if err != nil {
		return cfg, string(s.internal(r, "setup: read the games", err))
	}
	cfg = control.SetupConfig(f.Event, f.Mode, vlans, db)
	in, err := store.ConfigInput(cfg, db, true)
	if err != nil {
		return cfg, T("Das Netz passt nicht zur Konfiguration: %s", err.Error())
	}
	snap, _, err := compile.Compile(in)
	switch {
	case errors.Is(err, compile.ErrOverlap):
		return cfg, T("Zwei Netze überschneiden sich: %s", err.Error())
	case err != nil:
		return cfg, T("Das Netz passt nicht zur Konfiguration: %s", err.Error())
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := s.Engine.Validate(vctx, snap)
	switch {
	case err != nil:
		s.Log.Error("setup: engine not reachable", "err", err)
		return cfg, T("Die Engine antwortet nicht, deshalb wurde nichts gespeichert. Läuft der Container der Engine?")
	case !res.OK:
		return cfg, T("Die Engine lehnt diese Netze ab: %s", res.Error)
	}
	return cfg, ""
}
