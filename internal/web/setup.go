package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// First-run setup in the browser (/einrichten), so a fresh installation needs no command line:
//
//	step 1 "Konto": no account can log in yet (no local account, no single sign-on) - the first visitor
//	        creates the admin account and is logged in. There is no setup token; the page closes for good with
//	        the first account, and the log says while it is open.
//	step 2 "Netz":  an admin is logged in and there is no network yet - trunk interface, networks, mode. The
//	        configuration is compiled and checked by the engine before anything is stored.
//
// Installations that have accounts and networks never see it. Step 1 is in setup_account.go, step 2 in
// setup_network.go with the trunk choice in setup_trunk.go.

const (
	setupMinPassword = 10
	setupRowsMin     = 6
	setupRowsMax     = 64
	setupRelayHost   = "250"
)

var setupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,31}$`)

// needsAccount reports whether nobody can log in yet. Once an account exists the answer stays "no" for the
// life of the process.
func (s *Server) needsAccount(ctx context.Context) bool {
	if s.oidc != nil || s.Demo || s.hasAccount.Load() {
		return false
	}
	n, err := s.Store.LocalAccounts(ctx)
	if err != nil {
		return false // never open the setup because the database stumbled
	}
	if n > 0 {
		s.hasAccount.Store(true)
		return false
	}
	return true
}

// needsNetwork reports whether there is nothing to relay between yet: no active event or no network.
func (s *Server) needsNetwork(ctx context.Context) bool {
	c, err := s.Store.ExportConfig(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	return err == nil && len(c.VLANs) == 0
}

// setupRow is one network line of step 2, kept as typed.
type setupRow struct{ VID, Name, CIDR, Group string }

func (r setupRow) empty() bool { return r.VID == "" && r.Name == "" && r.CIDR == "" }

// setupIface is one host interface in the trunk list.
type setupIface struct {
	netif.HostIface
	Blocked string // why it cannot be chosen (German, translated in the template), "" = selectable
	Note    string // a remark on a selectable interface
}

type setupForm struct {
	Step                             string // "konto" | "netz"
	Name                             string
	LoginCSRF                        string
	Event, Trunk, TrunkOther, Host   string
	Mode                             string
	Rows                             []setupRow
	Ifaces                           []setupIface
	Groups                           []string
	Games                            int
	HasEvent                         bool
	Err                              string
	MinPassword                      int
	ErrName, ErrPassword, ErrNetwork bool
}

// setupDefaults fills in what step 2 shows besides the typed values: the interface list, the groups, the
// suggestions for empty fields and what the finish will bring along (games, an existing event).
func (s *Server) setupDefaults(ctx context.Context, f *setupForm) {
	f.Step = "netz"
	f.Ifaces = s.trunkCandidates(ctx)
	f.Groups = slices.Clone(defaultGroups)
	f.MinPassword = setupMinPassword
	f.suggest()
	if f.Trunk == "" && f.TrunkOther == "" {
		f.Trunk = soleTrunkCandidate(f.Ifaces)
	}
	f.Games = s.setupDefaultGames(ctx)
	_, err := s.Store.ActiveEvent(ctx)
	f.HasEvent = err == nil
}

// suggest puts the suggested values into empty fields of step 2 and pads the network list to setupRowsMin rows.
func (f *setupForm) suggest() {
	f.Host = cmp.Or(f.Host, setupRelayHost)
	f.Mode = cmp.Or(f.Mode, compile.ModeObserve)
	f.Event = cmp.Or(f.Event, "LAN-Party")
	for len(f.Rows) < setupRowsMin {
		f.Rows = append(f.Rows, setupRow{Group: compile.GroupTables})
	}
}

// setupDefaultGames counts the games the setup switches on (those enabled by default; 0 when the games cannot
// be read).
func (s *Server) setupDefaultGames(ctx context.Context) int {
	games, err := s.Store.Games(ctx)
	if err != nil {
		return 0
	}
	n := 0
	for _, g := range games {
		if g.DefaultEnabled {
			n++
		}
	}
	return n
}

// setupPage shows the step that is due, or leaves when nothing is.
func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.needsAccount(ctx) {
		s.render(w, r, "setup", "Einrichten", "setup", setupForm{Step: "konto", LoginCSRF: s.loginCSRF(w, r), MinPassword: setupMinPassword})
		return
	}
	se, err := s.Store.SessionByToken(ctx, s.sessionToken(r))
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if store.RoleRank(se.User.Role) < store.RoleRank(store.RoleAdmin) || !s.needsNetwork(ctx) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	var f setupForm
	s.setupDefaults(ctx, &f)
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r.WithContext(context.WithValue(ctx, ctxKey{}, se)), "setup", "Einrichten", "setup", f)
}
