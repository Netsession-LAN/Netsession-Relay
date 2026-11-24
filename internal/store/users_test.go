package store

import "testing"

func TestOIDCName(t *testing.T) {
	for _, tt := range []struct{ subject, name, email, want string }{
		{"abc", "anna", "anna@example.org", "anna"},
		{"abc", "", "anna@example.org", "anna@example.org"},
		{"0123456789abcdef", "", "", "sso-01234567"},
		{"abc", "", "", "sso-abc"},
	} {
		if got := oidcName(tt.subject, tt.name, tt.email); got != tt.want {
			t.Errorf("oidcName(%q, %q, %q) = %q, want %q", tt.subject, tt.name, tt.email, got, tt.want)
		}
	}
}
