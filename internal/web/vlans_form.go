package web

import (
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The form of a network: reading it and checking its fields on their own (vlanCheck in vlans.go checks the
// network against the whole configuration).

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

// ifName is a Linux interface name (IFNAMSIZ 16 incl. NUL).
var ifName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,14}$`)

// readVLANForm reads the posted form; when editing, the VLAN ID comes from the path.
func readVLANForm(r *http.Request, isNew bool) vlanForm {
	f := vlanForm{New: isNew, VID: strings.TrimSpace(r.PostFormValue("vid")), Name: strings.TrimSpace(r.PostFormValue("name")),
		Group: r.PostFormValue("group"), CIDR: strings.TrimSpace(r.PostFormValue("cidr")),
		RelayIP: strings.TrimSpace(r.PostFormValue("relay_ip")), Attach: r.PostFormValue("attach"),
		Parent: strings.TrimSpace(r.PostFormValue("parent")), Device: strings.TrimSpace(r.PostFormValue("device")),
		Rate: strings.TrimSpace(r.PostFormValue("rate_pps")), Burst: strings.TrimSpace(r.PostFormValue("rate_burst")),
		Enable: r.PostFormValue("enable") == "1"}
	if !isNew {
		f.VID = r.PathValue("vid")
	}
	return f
}

// vlanField checks one part of the form and stores it in v; it returns "" or the error text in the language of
// T.
type vlanField func(f vlanForm, v *store.VLANConfig, T translator) string

// vlanFields are the checks of parse, in the order the form shows the fields.
var vlanFields = []vlanField{parseVLANID, parseVLANName, parseVLANCIDR, parseVLANRelayIP, parseVLANAttach, parseVLANLimits}

// parse validates the form fields on their own; errors are UI texts in the language of T.
func (f vlanForm) parse(T translator) (store.VLANConfig, string) {
	v := store.VLANConfig{Name: f.Name, Group: f.Group, Attach: f.Attach, Enabled: f.Enable}
	for _, check := range vlanFields {
		if msg := check(f, &v, T); msg != "" {
			return v, msg
		}
	}
	return v, ""
}

func parseVLANID(f vlanForm, v *store.VLANConfig, T translator) string {
	var err error
	if v.VID, err = strconv.Atoi(f.VID); err != nil || v.VID < 1 || v.VID > 4094 {
		return T("Die VLAN-ID muss eine Zahl von 1 bis 4094 sein.")
	}
	return ""
}

func parseVLANName(_ vlanForm, v *store.VLANConfig, T translator) string {
	if v.Name == "" {
		return T("Bitte einen Namen eingeben.")
	}
	if len(v.Name) > 40 {
		return T("Der Name ist zu lang (höchstens 40 Zeichen).")
	}
	return ""
}

// parseVLANCIDR stores the network with the host bits cleared.
func parseVLANCIDR(f vlanForm, v *store.VLANConfig, T translator) string {
	p, err := netip.ParsePrefix(f.CIDR)
	if err != nil || !p.Addr().Is4() {
		return T("Adressen bitte als IPv4-Netz mit Maske, z. B. 10.10.15.0/24.")
	}
	if p.Bits() > 30 {
		return T("Das Netz ist zu klein (höchstens /30).")
	}
	v.CIDR = p.Masked().String()
	return ""
}

// parseVLANRelayIP checks the relay address against the network parseVLANCIDR stored: inside it and not its
// network address.
func parseVLANRelayIP(f vlanForm, v *store.VLANConfig, T translator) string {
	network := netip.MustParsePrefix(v.CIDR)
	ip, err := netip.ParseAddr(f.RelayIP)
	if err != nil || !ip.Is4() {
		return T("Die Relay-IP ist keine IPv4-Adresse.")
	}
	if !network.Contains(ip) || ip == network.Addr() {
		return T("Die Relay-IP %s liegt nicht in %s.", ip, network)
	}
	v.RelayIP = ip.String()
	return ""
}

// parseVLANAttach checks how the network reaches the host: tagged on a trunk interface or an own interface.
func parseVLANAttach(f vlanForm, v *store.VLANConfig, T translator) string {
	switch v.Attach {
	case compile.AttachTrunk:
		if !ifName.MatchString(f.Parent) {
			return T("Bitte das Trunk-Interface angeben (z. B. ens19).")
		}
		v.Parent = f.Parent
	case compile.AttachDevice:
		if !ifName.MatchString(f.Device) {
			return T("Bitte den Namen des Interfaces angeben.")
		}
		v.Device = f.Device
	default:
		return T("Bitte wählen, wie das Netz anliegt.")
	}
	return ""
}

// parseVLANLimits reads the optional rate limit and burst (empty = the default of the engine).
func parseVLANLimits(f vlanForm, v *store.VLANConfig, T translator) string {
	for _, x := range []struct {
		s   string
		dst *int
	}{{f.Rate, &v.RatePPS}, {f.Burst, &v.Burst}} {
		n, ok := parseVLANLimit(x.s)
		if !ok {
			return T("Grenze bitte als ganze Zahl angeben (leer: Standard).")
		}
		*x.dst = n
	}
	return ""
}

// parseVLANLimit is one limit field: 0 when empty, ok=false when it is no number from 0 to 1,000,000.
func parseVLANLimit(s string) (n int, ok bool) {
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 1_000_000 {
		return 0, false
	}
	return n, true
}
