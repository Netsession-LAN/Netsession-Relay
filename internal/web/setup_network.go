package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
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
	s.applySetup(w, r, f.Mode, cfg, trunk)
}

// applySetup applies the configuration the setup stored and leaves the setup with the result.
func (s *Server) applySetup(w http.ResponseWriter, r *http.Request, mode string, cfg store.Config, trunk string) {
	T := s.T(r)
	a := s.applyNow(r, "Einrichtung")
	s.Log.Info("setup: network configured", "user", s.actor(r), "trunk", trunk, "vlans", len(cfg.VLANs), "mode", mode, "ok", a.OK)
	if !a.OK {
		back(w, r, "/", "", errors.New(T("Die Einrichtung ist gespeichert, aber das Anwenden ist fehlgeschlagen: %s", a.Error)))
		return
	}
	back(w, r, "/", setupDoneMessage(mode, len(cfg.VLANs), len(cfg.Games), T), nil)
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
