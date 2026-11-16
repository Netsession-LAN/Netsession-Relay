package auth

import (
	"strings"
	"testing"
)

func TestHashVerify(t *testing.T) {
	h, err := Hash("korrekt pferd batterie")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("format %q", h)
	}
	if err := Verify("korrekt pferd batterie", h); err != nil {
		t.Errorf("correct password rejected: %v", err)
	}
	if err := Verify("falsch", h); err != ErrMismatch {
		t.Errorf("wrong password: %v", err)
	}
	h2, _ := Hash("korrekt pferd batterie")
	if h == h2 {
		t.Error("same hash twice (salt not random)")
	}
	for _, bad := range []string{"", "$bcrypt$x", "$argon2id$v=19$m=1,t=1,p=1$!!$!!"} {
		if Verify("x", bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTokensAndPasswords(t *testing.T) {
	if Token(32) == Token(32) || len(Token(32)) != 43 {
		t.Error("token not random or wrong length")
	}
	p := Password()
	if len(p) != 19 || strings.Count(p, "-") != 3 || strings.ContainsAny(p, "0O1lI") {
		t.Errorf("password %q", p)
	}
}
