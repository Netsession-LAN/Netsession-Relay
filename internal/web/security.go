package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

func (s *Server) isAdmin(r *http.Request) bool {
	return store.RoleRank(sessionFrom(r).User.Role) >= store.RoleRank(store.RoleAdmin)
}

// ---- local redirect targets ----

// localPath reports whether p is a path on this site that is safe as a redirect target: it starts with a single
// "/", has no scheme or host, no backslash (browsers read "/\host" as "//host") and no control characters, raw
// or percent-encoded in the path.
func localPath(p string) bool {
	if !isSitePath(p) {
		return false
	}
	u, err := url.Parse(p)
	if err != nil || !pathOnly(u) {
		return false
	}
	return isSitePath(u.Path)
}

// isSitePath reports whether p starts with a single "/" and has no unsafe character; localPath checks it on the
// raw target and on the decoded path.
func isSitePath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !hasUnsafeChar(p)
}

// pathOnly reports whether u has nothing that leads to another site: no scheme, host, user or opaque part.
func pathOnly(u *url.URL) bool {
	return u.Scheme == "" && u.Host == "" && u.User == nil && u.Opaque == ""
}

func hasUnsafeChar(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return true
		}
	}
	return false
}

// ---- messages on pages without a session ----

// Texts the login page shows from ?ok= / ?fehler=. Without a session nothing else is shown, so a link cannot put
// invented text on the login page.
var (
	errLogin    = uimsg.Error("Name oder Passwort falsch.")
	errThrottle = uimsg.Error("Zu viele Anmeldeversuche. Warte kurz und versuch es noch einmal.")
	errBusy     = uimsg.Error("Gerade melden sich viele an. Bitte gleich nochmal.")
	errStale    = uimsg.Error("Die Anmeldeseite war veraltet. Bitte noch einmal.")
)

const (
	msgOIDCOff     = "Die Anmeldung mit Single Sign-on ist nicht eingerichtet."
	msgOIDCDown    = "Der Anmeldedienst ist gerade nicht erreichbar. Nimm die Notfall-Anmeldung."
	msgOIDCExpired = "Anmeldung abgelaufen. Bitte noch einmal."
	msgOIDCDenied  = "Der Anmeldedienst hat die Anmeldung abgelehnt."
	msgOIDCToken   = "Anmeldung fehlgeschlagen (Token). Bitte noch einmal." //nolint:gosec // G101: a setting or message name, not a credential
	msgOIDCNoToken = "Anmeldung fehlgeschlagen (kein ID-Token)."            //nolint:gosec // G101: a setting or message name, not a credential
	msgOIDCBad     = "Anmeldung fehlgeschlagen (Token ungültig)."
	msgOIDCNonce   = "Anmeldung fehlgeschlagen (Nonce)."
	msgOIDCGroup   = "Dein Konto ist in keiner Relay-Gruppe. Ein Admin kann dich beim Anmeldedienst in eine eintragen."
	msgOIDCFailed  = "Anmeldung fehlgeschlagen."
)

var publicMessages = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range []string{errLogin.Error(), errThrottle.Error(), errBusy.Error(), errStale.Error(), msgOIDCOff, msgOIDCDown,
		msgOIDCExpired, msgOIDCDenied, msgOIDCToken, msgOIDCNoToken, msgOIDCBad, msgOIDCNonce, msgOIDCGroup, msgOIDCFailed} {
		m[t] = true
	}
	return m
}()

func publicMessage(s string) string {
	if publicMessages[s] {
		return s
	}
	return ""
}
