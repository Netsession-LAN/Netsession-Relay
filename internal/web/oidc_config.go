package web

import (
	"cmp"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The settings of the login with OpenID Connect: issuer, client and the groups that give a role.

// OIDCConfig comes from the environment (NSRELAY_OIDC_*); empty Issuer = OIDC off.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// Name is what the login button calls the identity provider ("Sign in with <Name>"); default "SSO".
	Name string
	// GroupAdmin, GroupOperator and GroupViewer are the groups of the identity provider that give the three
	// roles; defaults relay-admin, relay-operator, relay-viewer.
	GroupAdmin, GroupOperator, GroupViewer string
}

// Defaults of the optional OIDC settings.
const (
	DefaultOIDCName          = "SSO"
	DefaultOIDCGroupAdmin    = "relay-admin"
	DefaultOIDCGroupOperator = "relay-operator"
	DefaultOIDCGroupViewer   = "relay-viewer"
)

// Enabled reports whether the configuration is complete.
func (c OIDCConfig) Enabled() bool {
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != "" && c.RedirectURL != ""
}

// withDefaults fills the optional settings.
func (c OIDCConfig) withDefaults() OIDCConfig {
	c.Name = cmp.Or(strings.TrimSpace(c.Name), DefaultOIDCName)
	c.GroupAdmin = cmp.Or(strings.TrimSpace(c.GroupAdmin), DefaultOIDCGroupAdmin)
	c.GroupOperator = cmp.Or(strings.TrimSpace(c.GroupOperator), DefaultOIDCGroupOperator)
	c.GroupViewer = cmp.Or(strings.TrimSpace(c.GroupViewer), DefaultOIDCGroupViewer)
	return c
}

// role maps the groups of an account to a role: the highest one a group gives, "" when no group gives one.
// Group names are compared without regard to case.
func (c OIDCConfig) role(groups []string) string {
	best := ""
	for _, g := range groups {
		if r := c.groupRole(strings.TrimSpace(g)); store.RoleRank(r) > store.RoleRank(best) {
			best = r
		}
	}
	return best
}

// groupRole is the role one group gives, "" for any other group.
func (c OIDCConfig) groupRole(g string) string {
	switch {
	case strings.EqualFold(g, c.GroupAdmin):
		return store.RoleAdmin
	case strings.EqualFold(g, c.GroupOperator):
		return store.RoleOperator
	case strings.EqualFold(g, c.GroupViewer):
		return store.RoleViewer
	}
	return ""
}
