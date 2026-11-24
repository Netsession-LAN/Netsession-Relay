package web

import (
	"fmt"
	"strings"
	"testing"
)

func TestSetupCredentialsProblem(t *testing.T) {
	long := strings.Repeat("x", 201)
	for _, c := range []struct {
		name, pw, pw2   string
		errName, errPas bool
	}{
		{"admin", "geheim-genug", "geheim-genug", false, false},
		{"a", "geheim-genug", "geheim-genug", true, false},
		{"ad min", "geheim-genug", "geheim-genug", true, false},
		{"admin", "kurz", "kurz", false, true},
		{"admin", "äöüäöüäöüä", "äöüäöüäöüä", false, false}, // counted in characters, not bytes
		{"admin", long, long, false, true},
		{"admin", "geheim-genug", "geheim-anders", false, true},
	} {
		f := setupForm{Name: c.name}
		msg := f.setupCredentialsProblem(c.pw, c.pw2, fmt.Sprintf)
		if (msg != "") != (c.errName || c.errPas) || f.ErrName != c.errName || f.ErrPassword != c.errPas {
			t.Errorf("%q/%q: %q name %v password %v", c.name, c.pw, msg, f.ErrName, f.ErrPassword)
		}
	}
}
