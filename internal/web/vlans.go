package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// vlanForm is the create/edit form of a network. Values stay strings so a rejected form is shown again
// exactly as typed.
type vlanForm struct {
	New                                 bool
	VID, Name, Group, CIDR, RelayIP     string
	Attach, Parent, Device, Rate, Burst string
	Enable                              bool
	Groups                              []string
	Err                                 string
}

// defaultGroups are offered when no VLAN exists yet (same short names as the presets).
var defaultGroups = []string{compile.GroupTables, compile.GroupGameserver, compile.GroupWiFi, compile.GroupOrga}

// ifName is a Linux interface name (IFNAMSIZ 16 incl. NUL).
var ifName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,14}$`)

func (s *Server) vlanGroups(c store.Config) []string {
	var gs []string
	for _, v := range c.VLANs {
		if !slices.Contains(gs, v.Group) {
			gs = append(gs, v.Group)
		}
	}
	if len(gs) == 0 {
		gs = slices.Clone(defaultGroups)
	}
	return gs
}

func (s *Server) vlanNewForm(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.ExportConfig(r.Context())
	if err != nil {
		s.failInternal(w, r, "network form: configuration", err)
		return
	}
	f := vlanForm{New: true, Attach: compile.AttachTrunk, Enable: true, Groups: s.vlanGroups(c)}
	// The trunk is the same for all networks in practice: take it from an existing one.
	for _, v := range c.VLANs {
		if v.Attach == compile.AttachTrunk && v.Parent != "" {
			f.Parent = v.Parent
			break
		}
	}
	if slices.Contains(f.Groups, compile.GroupTables) {
		f.Group = compile.GroupTables
	}
	s.render(w, r, "network-edit", "Netz anlegen", "network", f)
}

func (s *Server) vlanEditForm(w http.ResponseWriter, r *http.Request) {
	vid, _ := strconv.Atoi(r.PathValue("vid"))
	v, err := s.Store.VLAN(r.Context(), vid)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Dieses Netz gibt es nicht.")
		return
	}
	c, err := s.Store.ExportConfig(r.Context())
	s.degraded("network form: groups", err)
	f := vlanForm{VID: strconv.Itoa(v.VID), Name: v.Name, Group: v.Group, CIDR: v.CIDR, RelayIP: v.RelayIP,
		Attach: v.Attach, Parent: v.Parent, Device: v.Device, Enable: v.Enabled, Groups: s.vlanGroups(c)}
	if v.RatePPS > 0 {
		f.Rate = strconv.Itoa(v.RatePPS)
	}
	if v.Burst > 0 {
		f.Burst = strconv.Itoa(v.Burst)
	}
	s.render(w, r, "network-edit", s.T(r)("%s bearbeiten", v.Name), "network", f)
}

func (s *Server) vlanCreate(w http.ResponseWriter, r *http.Request) { s.vlanSave(w, r, true) }
func (s *Server) vlanUpdate(w http.ResponseWriter, r *http.Request) { s.vlanSave(w, r, false) }

func (s *Server) vlanSave(w http.ResponseWriter, r *http.Request, isNew bool) {
	ctx := r.Context()
	T := s.T(r)
	f := vlanForm{New: isNew, VID: strings.TrimSpace(r.PostFormValue("vid")), Name: strings.TrimSpace(r.PostFormValue("name")),
		Group: r.PostFormValue("group"), CIDR: strings.TrimSpace(r.PostFormValue("cidr")),
		RelayIP: strings.TrimSpace(r.PostFormValue("relay_ip")), Attach: r.PostFormValue("attach"),
		Parent: strings.TrimSpace(r.PostFormValue("parent")), Device: strings.TrimSpace(r.PostFormValue("device")),
		Rate: strings.TrimSpace(r.PostFormValue("rate_pps")), Burst: strings.TrimSpace(r.PostFormValue("rate_burst")),
		Enable: r.PostFormValue("enable") == "1"}
	if !isNew {
		f.VID = r.PathValue("vid")
	}
	c, err := s.Store.ExportConfig(ctx)
	if err != nil {
		s.failInternal(w, r, "network: configuration", err)
		return
	}
	f.Groups = s.vlanGroups(c)
	title := "Netz anlegen"
	if !isNew {
		title = T("%s bearbeiten", f.Name)
	}
	reject := func(msg string) {
		f.Err = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "network-edit", title, "network", f)
	}

	v, msg := f.parse(T)
	if msg != "" {
		reject(msg)
		return
	}
	exists := slices.ContainsFunc(c.VLANs, func(x store.VLANConfig) bool { return x.VID == v.VID })
	switch {
	case isNew && exists:
		reject(T("VLAN %d gibt es schon.", v.VID))
		return
	case !isNew && !exists:
		s.fail(w, r, http.StatusNotFound, "Dieses Netz gibt es nicht.")
		return
	}
	if !slices.Contains(f.Groups, v.Group) {
		reject(T("Bitte eine der Gruppen wählen."))
		return
	}
	// Check against the whole configuration before anything is stored: overlapping networks, a relay IP
	// outside the network and the like are rejected here, not by a failing apply afterwards.
	if msg := s.vlanCheck(r, v); msg != "" {
		reject(msg)
		return
	}
	if isNew {
		err = s.Store.AddVLAN(ctx, s.actor(r), v)
	} else {
		err = s.Store.UpdateVLAN(ctx, s.actor(r), v)
	}
	if err != nil {
		reject(string(s.userError(r, "network: store", err)))
		return
	}
	verb := "geändert"
	if isNew {
		verb = "angelegt"
	}
	a := s.applyNow(r, fmt.Sprintf("VLAN %d %s", v.VID, verb))
	if a.OK {
		a.Message = T("%s (VLAN %d) %s.", v.Name, v.VID, T(verb))
	} else if a.Error != "" {
		a.Error = T("%s (VLAN %d) %s, Anwenden fehlgeschlagen: %s", v.Name, v.VID, T(verb), a.Error)
	}
	s.answer(w, r, "/netz", a)
}

// parse validates the form fields on their own; errors are UI texts in the language of T.
func (f vlanForm) parse(T translator) (store.VLANConfig, string) {
	v := store.VLANConfig{Name: f.Name, Group: f.Group, Attach: f.Attach, Enabled: f.Enable}
	var err error
	if v.VID, err = strconv.Atoi(f.VID); err != nil || v.VID < 1 || v.VID > 4094 {
		return v, T("Die VLAN-ID muss eine Zahl von 1 bis 4094 sein.")
	}
	if v.Name == "" {
		return v, T("Bitte einen Namen eingeben.")
	}
	if len(v.Name) > 40 {
		return v, T("Der Name ist zu lang (höchstens 40 Zeichen).")
	}
	p, err := netip.ParsePrefix(f.CIDR)
	if err != nil || !p.Addr().Is4() {
		return v, T("Adressen bitte als IPv4-Netz mit Maske, z. B. 10.10.15.0/24.")
	}
	if p.Bits() > 30 {
		return v, T("Das Netz ist zu klein (höchstens /30).")
	}
	v.CIDR = p.Masked().String()
	ip, err := netip.ParseAddr(f.RelayIP)
	if err != nil || !ip.Is4() {
		return v, T("Die Relay-IP ist keine IPv4-Adresse.")
	}
	if !p.Masked().Contains(ip) || ip == p.Masked().Addr() {
		return v, T("Die Relay-IP %s liegt nicht in %s.", ip, p.Masked())
	}
	v.RelayIP = ip.String()
	switch v.Attach {
	case compile.AttachTrunk:
		if !ifName.MatchString(f.Parent) {
			return v, T("Bitte das Trunk-Interface angeben (z. B. ens19).")
		}
		v.Parent = f.Parent
	case compile.AttachDevice:
		if !ifName.MatchString(f.Device) {
			return v, T("Bitte den Namen des Interfaces angeben.")
		}
		v.Device = f.Device
	default:
		return v, T("Bitte wählen, wie das Netz anliegt.")
	}
	for _, x := range []struct {
		s   string
		dst *int
	}{{f.Rate, &v.RatePPS}, {f.Burst, &v.Burst}} {
		if x.s == "" {
			continue
		}
		n, err := strconv.Atoi(x.s)
		if err != nil || n < 0 || n > 1_000_000 {
			return v, T("Grenze bitte als ganze Zahl angeben (leer: Standard).")
		}
		*x.dst = n
	}
	return v, ""
}

// vlanCheck compiles the configuration with v added or replaced (and switched on, so all address
// checks run) and returns an error text in the language of r or "".
func (s *Server) vlanCheck(r *http.Request, v store.VLANConfig) string {
	T := s.T(r)
	in, err := s.Store.BuildInput(r.Context())
	if err != nil {
		return string(s.internal(r, "network: read the configuration", err))
	}
	cv := compile.VLAN{VID: v.VID, Name: v.Name, Group: v.Group, CIDR: netip.MustParsePrefix(v.CIDR),
		RelayIP: netip.MustParseAddr(v.RelayIP), Attach: v.Attach, Parent: v.Parent, Device: v.Device,
		Enabled: true, RatePPS: v.RatePPS, Burst: v.Burst}
	if i := slices.IndexFunc(in.VLANs, func(x compile.VLAN) bool { return x.VID == v.VID }); i >= 0 {
		in.VLANs[i] = cv
	} else {
		in.VLANs = append(in.VLANs, cv)
	}
	// Other VLANs keep their state; overlaps with switched-off networks matter too, because they can be
	// switched on later.
	for i := range in.VLANs {
		in.VLANs[i].Enabled = true
	}
	if _, _, err := compile.Compile(in); err != nil {
		if errors.Is(err, compile.ErrOverlap) {
			return T("Das Netz überschneidet sich mit einem anderen Netz: %s", err.Error())
		}
		return T("Das Netz passt nicht zur Konfiguration: %s", err.Error())
	}
	return ""
}

func (s *Server) vlanDelete(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	vid, err := strconv.Atoi(r.PathValue("vid"))
	if err != nil {
		s.answer(w, r, "/netz", applyAnswer{Error: T("VLAN-ID ungültig.")})
		return
	}
	v, err := s.Store.VLAN(r.Context(), vid)
	if err != nil {
		s.answer(w, r, "/netz", applyAnswer{Error: T("Dieses Netz gibt es nicht.")})
		return
	}
	if err := s.Store.DeleteVLAN(r.Context(), s.actor(r), vid); err != nil {
		s.answer(w, r, "/netz", applyAnswer{Error: string(s.userError(r, "network: delete", err))})
		return
	}
	a := s.applyNow(r, fmt.Sprintf("VLAN %d gelöscht", vid))
	if a.OK {
		a.Message = T("%s (VLAN %d) gelöscht.", v.Name, vid)
	}
	s.answer(w, r, "/netz", a)
}
