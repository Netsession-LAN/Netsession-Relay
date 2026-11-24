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

	"github.com/Netsession2024/Netsession-Relay/internal/auth"
	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/control"
	"github.com/Netsession2024/Netsession-Relay/internal/netif"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

// First-run setup in the browser (/einrichten), so a fresh installation needs no command line:
//
//	step 1 "Konto": no account can log in yet (no local account, no single sign-on) - the first visitor
//	        creates the admin account and is logged in. There is no setup token; the page closes for good with
//	        the first account, and the log says while it is open.
//	step 2 "Netz":  an admin is logged in and there is no network yet - trunk interface, networks, mode. The
//	        configuration is compiled and checked by the engine before anything is stored.
//
// Installations that have accounts and networks never see it.

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

// trunkCandidates lists the interfaces of the host for the trunk choice. Interfaces the relay must not use
// stay in the list, greyed out with the reason; purely internal ones (loopback, the VLAN interfaces of the
// relay, container plumbing) are left out.
func (s *Server) trunkCandidates(ctx context.Context) []setupIface {
	ifs, err := s.hostIfaces()
	if err != nil {
		return nil
	}
	// with a list of allowed interfaces on the engine, the operator has decided: nothing is greyed out here
	listed := false
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	if st, err := s.Engine.Status(sctx); err == nil {
		listed = st.IfacePolicy == "list"
	}
	cancel()
	var out []setupIface
	for _, i := range ifs {
		// by name, not by kind: inside a container (LXC) the real interfaces are veth devices, too
		if i.Loopback || i.Relay || i.Kind == "vlan" || i.Kind == "tun" || i.Kind == "wireguard" ||
			strings.HasPrefix(i.Name, "docker") || strings.HasPrefix(i.Name, "br-") || strings.HasPrefix(i.Name, "veth") {
			continue
		}
		c := setupIface{HostIface: i}
		switch {
		case i.DefaultRoute && !listed:
			c.Blocked = "Verwaltung: Über diese Schnittstelle erreichst du den Server. Das Relay nimmt sie nur, wenn sie in NSRELAY_ALLOWED_IFACES der Engine steht."
		case slices.ContainsFunc(i.Addrs, func(a string) bool { p, err := netip.ParsePrefix(a); return err == nil && p.Addr().Is4() }):
			c.Note = "Hat schon eine eigene Adresse. Sie bleibt, die Netze kommen als VLANs dazu."
		}
		out = append(out, c)
	}
	return out
}

func (s *Server) setupDefaults(ctx context.Context, f *setupForm) {
	f.Step = "netz"
	f.Ifaces = s.trunkCandidates(ctx)
	f.Groups = slices.Clone(defaultGroups)
	f.MinPassword = setupMinPassword
	if f.Host == "" {
		f.Host = setupRelayHost
	}
	if f.Mode == "" {
		f.Mode = compile.ModeObserve
	}
	if f.Event == "" {
		f.Event = "LAN-Party"
	}
	for len(f.Rows) < setupRowsMin {
		f.Rows = append(f.Rows, setupRow{Group: compile.GroupTables})
	}
	if f.Trunk == "" && f.TrunkOther == "" {
		free := 0
		for _, i := range f.Ifaces {
			if i.Blocked == "" {
				free++
				f.Trunk = i.Name
			}
		}
		if free != 1 { // only preselect when there is no choice to make
			f.Trunk = ""
		}
	}
	if games, err := s.Store.Games(ctx); err == nil {
		for _, g := range games {
			if g.DefaultEnabled {
				f.Games++
			}
		}
	}
	_, err := s.Store.ActiveEvent(ctx)
	f.HasEvent = err == nil
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

// setupAccount creates the first admin account (step 1) and logs the browser in.
func (s *Server) setupAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.needsAccount(ctx) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	T := s.T(r)
	f := setupForm{Step: "konto", Name: strings.TrimSpace(r.PostFormValue("name")), MinPassword: setupMinPassword}
	reject := func(msg string) {
		f.Err = msg
		f.LoginCSRF = s.loginCSRF(w, r)
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "setup", "Einrichten", "setup", f)
	}
	ip := s.clientIP(r)
	if !s.loginLimit.allow(limitKey(ip), time.Now()) {
		reject(T(errThrottle.Error()))
		return
	}
	if c, err := r.Cookie(loginCookie); err != nil || !sameToken(r.PostFormValue("csrf"), c.Value) {
		reject(T("Die Seite war veraltet. Bitte noch einmal."))
		return
	}
	pw, pw2 := r.PostFormValue("password"), r.PostFormValue("password2")
	switch {
	case !setupName.MatchString(f.Name):
		f.ErrName = true
		reject(T("Der Name braucht 2 bis 32 Zeichen: Buchstaben, Ziffern, Punkt, Bindestrich oder Unterstrich."))
		return
	case len([]rune(pw)) < setupMinPassword:
		f.ErrPassword = true
		reject(T("Das Passwort braucht mindestens %d Zeichen.", setupMinPassword))
		return
	case len(pw) > 200:
		f.ErrPassword = true
		reject(T("Das Passwort ist zu lang (höchstens 200 Zeichen)."))
		return
	case pw != pw2:
		f.ErrPassword = true
		reject(T("Die beiden Passwörter sind nicht gleich."))
		return
	}
	// hashing takes 64 MiB like a login: it shares the slots of the password checks
	hctx, cancel := context.WithTimeout(ctx, verifyWait)
	defer cancel()
	select {
	case s.verifySem <- struct{}{}:
	case <-hctx.Done():
		reject(T(errBusy.Error()))
		return
	}
	hash, err := auth.Hash(pw)
	<-s.verifySem
	if err != nil {
		s.Log.Error("setup: password hash", "err", err)
		reject(T("Das Konto ließ sich nicht anlegen. Bitte noch einmal."))
		return
	}
	u, err := s.Store.CreateFirstAdmin(ctx, f.Name, hash)
	switch {
	case errors.Is(err, store.ErrExists): // somebody else was faster
		s.hasAccount.Store(true)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	case errors.Is(err, store.ErrInvalid):
		f.ErrName = true
		reject(T("Diesen Namen gibt es schon. Nimm einen anderen."))
		return
	case err != nil:
		s.Log.Error("setup: account", "err", err)
		reject(T("Das Konto ließ sich nicht anlegen. Bitte noch einmal."))
		return
	}
	s.hasAccount.Store(true)
	token := auth.Token(32)
	if err := s.Store.CreateSession(ctx, u, token, auth.Token(24), ip); err != nil {
		s.Log.Error("setup: session", "err", err)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.setSessionCookie(w, r, token, int(store.SessionTTL.Seconds()))
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	s.Log.Info("setup: first admin account created, the setup page for accounts is closed now", "name", f.Name, "ip", ip)
	http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
}

// setupExample is a generic LAN party: a server network, four tables, an orga network.
func setupExample(T translator) []setupRow {
	rows := []setupRow{{"10", "Server", "10.10.0.0/24", compile.GroupGameserver}}
	for n := 1; n <= 4; n++ {
		rows = append(rows, setupRow{strconv.Itoa(10 + n), T("Tisch %d", n), fmt.Sprintf("10.10.%d.0/24", n), compile.GroupTables})
	}
	return append(rows, setupRow{"99", "Orga", "10.10.99.0/24", compile.GroupOrga})
}

// setupNetwork handles step 2: the buttons that only change the form (example, more rows) and the finish.
func (s *Server) setupNetwork(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.needsNetwork(ctx) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	T := s.T(r)
	f := setupForm{Event: strings.TrimSpace(r.PostFormValue("event")), Trunk: r.PostFormValue("trunk"),
		TrunkOther: strings.TrimSpace(r.PostFormValue("trunk_other")), Host: strings.TrimSpace(r.PostFormValue("host")),
		Mode: r.PostFormValue("mode")}
	vids, names, cidrs, groups := r.PostForm["vid"], r.PostForm["name"], r.PostForm["cidr"], r.PostForm["group"]
	for i := range min(len(vids), len(names), len(cidrs), len(groups), setupRowsMax) {
		f.Rows = append(f.Rows, setupRow{strings.TrimSpace(vids[i]), strings.TrimSpace(names[i]), strings.TrimSpace(cidrs[i]), groups[i]})
	}
	show := func(code int) {
		s.setupDefaults(ctx, &f)
		w.WriteHeader(code)
		s.render(w, r, "setup", "Einrichten", "setup", f)
	}
	reject := func(msg string) {
		f.Err = msg
		show(http.StatusUnprocessableEntity)
	}
	switch r.PostFormValue("aktion") {
	case "beispiel":
		f.Rows = setupExample(T)
		show(http.StatusOK)
		return
	case "mehr":
		for range 4 {
			if len(f.Rows) < setupRowsMax {
				f.Rows = append(f.Rows, setupRow{Group: compile.GroupTables})
			}
		}
		show(http.StatusOK)
		return
	}

	trunk, host, msg := s.setupBasics(ctx, f, T)
	if msg != "" {
		reject(msg)
		return
	}
	vlans, msg := setupVLANs(f.Rows, trunk, host, T)
	if msg == "" && len(vlans) < 2 {
		f.ErrNetwork = true
		msg = T("Bitte mindestens zwei Netze eintragen. Das Relay verbindet Netze miteinander.")
	}
	if msg != "" {
		reject(msg)
		return
	}
	cfg, msg := s.setupCheck(r, f, vlans)
	if msg != "" {
		reject(msg)
		return
	}
	if err := s.Store.ImportConfig(ctx, s.actor(r), cfg); err != nil {
		reject(string(s.userError(r, "setup: store the configuration", err)))
		return
	}
	a := s.applyNow(r, "Einrichtung")
	s.Log.Info("setup: network configured", "user", s.actor(r), "trunk", trunk, "vlans", len(vlans), "mode", f.Mode, "ok", a.OK)
	if !a.OK {
		back(w, r, "/", "", errors.New(T("Die Einrichtung ist gespeichert, aber das Anwenden ist fehlgeschlagen: %s", a.Error)))
		return
	}
	msg = T("Fertig eingerichtet: %d Netze, %d Spiele. Das Relay beobachtet jetzt. Auf „Aktiv“ stellst du unter System.", len(vlans), len(cfg.Games))
	if f.Mode == compile.ModeActive {
		msg = T("Fertig eingerichtet: %d Netze, %d Spiele. Das Relay leitet jetzt weiter.", len(vlans), len(cfg.Games))
	}
	back(w, r, "/", msg, nil)
}

// setupBasics checks the fields above the list of networks: trunk interface, mode, name of the event and the
// host part of the relay address. msg is "" or what is wrong, in the language of T.
func (s *Server) setupBasics(ctx context.Context, f setupForm, T translator) (trunk string, host int, msg string) {
	// trunk: the typed name wins over the list
	trunk = f.Trunk
	if f.TrunkOther != "" {
		trunk = f.TrunkOther
	}
	if !ifName.MatchString(trunk) {
		return "", 0, T("Bitte die Schnittstelle wählen, an der die Netze als Trunk anliegen.")
	}
	if ifs := s.trunkCandidates(ctx); ifs != nil {
		i := slices.IndexFunc(ifs, func(c setupIface) bool { return c.Name == trunk })
		switch {
		case i < 0:
			return "", 0, T("Die Schnittstelle %s gibt es auf diesem Host nicht.", trunk)
		case ifs[i].Blocked != "":
			return "", 0, T("%s ist die Verwaltungsschnittstelle dieses Hosts. Das Relay braucht eine eigene Schnittstelle für die Spieler-Netze.", trunk)
		}
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
		line := func(msg string) string { return T("Netz in Zeile %d: %s", n+1, msg) }
		p, perr := netip.ParsePrefix(row.CIDR)
		relay := ""
		if perr == nil && p.Addr().Is4() && p.Bits() <= 30 {
			size := 1 << (32 - p.Bits())
			if host > size-2 {
				return nil, line(T("Die Stelle %d passt nicht in %s (höchstens %d).", host, p.Masked(), size-2))
			}
			b := p.Masked().Addr().As4()
			v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
			v += uint32(host)
			relay = netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}).String()
		}
		v, msg := vlanForm{VID: row.VID, Name: row.Name, Group: row.Group, CIDR: row.CIDR, RelayIP: relay,
			Attach: compile.AttachTrunk, Parent: trunk, Enable: true}.parse(T)
		switch {
		case msg != "":
			return nil, line(msg)
		case !slices.Contains(defaultGroups, v.Group):
			return nil, line(T("Bitte eine der Gruppen wählen."))
		case slices.ContainsFunc(vlans, func(x store.VLANConfig) bool { return x.VID == v.VID }):
			return nil, line(T("VLAN %d gibt es schon.", v.VID))
		}
		vlans = append(vlans, v)
	}
	return vlans, ""
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
