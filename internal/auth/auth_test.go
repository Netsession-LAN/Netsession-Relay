package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// cheap are the smallest parameters Verify accepts; the tests that do not look at the defaults use them.
var cheap = Params{Memory: MinMemory, Passes: 1, Threads: 1}

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
	if err := Verify("falsch", h); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong password: %v", err)
	}
	h2, _ := HashWith("korrekt pferd batterie", DefaultParams)
	if h == h2 {
		t.Error("same hash twice (salt not random)")
	}
	for _, bad := range []string{"", "$bcrypt$x", "$argon2id$v=19$m=1,t=1,p=1$!!$!!", "x$argon2id$v=19$m=8192,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5a2V5"} {
		if err := Verify("x", bad); !errors.Is(err, ErrFormat) {
			t.Errorf("Verify(%q) = %v, want ErrFormat", bad, err)
		}
	}
}

// A hash made with other (cheaper) parameters verifies: the parameters travel with the hash.
func TestHashWith(t *testing.T) {
	h, err := HashWith("pw", cheap)
	if err != nil || !strings.HasPrefix(h, fmt.Sprintf("$argon2id$v=19$m=%d,t=1,p=1$", MinMemory)) {
		t.Fatalf("hash %q, %v", h, err)
	}
	if err := Verify("pw", h); err != nil {
		t.Errorf("correct password rejected: %v", err)
	}
	if err := Verify("other", h); !errors.Is(err, ErrMismatch) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := HashWith("pw", Params{Memory: 16, Passes: 1, Threads: 1}); !errors.Is(err, ErrParams) {
		t.Errorf("parameters below the bounds: %v, want ErrParams", err)
	}
}

// Verify computes what the stored hash asks for. A hash that asks for too much (memory, passes, threads, key
// length) or for next to nothing is refused before any work is done.
func TestVerifyRefusesParametersOutOfBounds(t *testing.T) {
	const salt, key = "c2FsdHNhbHRzYWx0", "a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5" // 12 and 24 bytes
	long := strings.Repeat("QUFB", 30)                                       // 90 bytes
	for name, h := range map[string]string{
		"no passes":       "$argon2id$v=19$m=65536,t=0,p=2$" + salt + "$" + key,
		"no threads":      "$argon2id$v=19$m=65536,t=3,p=0$" + salt + "$" + key,
		"too many passes": "$argon2id$v=19$m=65536,t=4000000000,p=2$" + salt + "$" + key,
		"too many thread": "$argon2id$v=19$m=65536,t=3,p=200$" + salt + "$" + key,
		"4 GiB of memory": "$argon2id$v=19$m=4194304,t=3,p=2$" + salt + "$" + key,
		"tiny memory":     "$argon2id$v=19$m=8,t=1,p=1$" + salt + "$" + key,
		"short key":       "$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$a2V5",
		"long key":        "$argon2id$v=19$m=65536,t=3,p=2$" + salt + "$" + long,
		"short salt":      "$argon2id$v=19$m=65536,t=3,p=2$c2E$" + key,
		"long salt":       "$argon2id$v=19$m=65536,t=3,p=2$" + long + "$" + key,
	} {
		if err := Verify("x", h); !errors.Is(err, ErrParams) {
			t.Errorf("%s: %v, want ErrParams", name, err)
		}
	}
}

func TestTokensAndPasswords(t *testing.T) {
	if a, b := Token(32), Token(32); a == b || len(a) != 43 {
		t.Error("token not random or wrong length")
	}
	p := Password()
	if len(p) != 19 || strings.Count(p, "-") != 3 || strings.ContainsAny(p, "0O1lI") {
		t.Errorf("password %q", p)
	}
	if Password() == p {
		t.Error("same password twice")
	}
}

// Every index is drawn, none above the range, and roughly equally often (a plain modulo would make the first
// 256 % 54 = 40 characters of the alphabet a quarter more likely than the rest).
func TestRandIndexIsUniform(t *testing.T) {
	const n, draws = len(passwordAlphabet), 54 * 4000
	var seen [n]int
	for range draws {
		i := randIndex(n)
		if i < 0 || i >= n {
			t.Fatalf("index %d outside 0..%d", i, n-1)
		}
		seen[i]++
	}
	var low, high int // the indexes a modulo would prefer, and the others
	for i, c := range seen {
		if c == 0 {
			t.Fatalf("index %d never drawn", i)
		}
		if i < 256%n {
			low += c
		} else {
			high += c
		}
	}
	perLow, perHigh := float64(low)/float64(256%n), float64(high)/float64(n-256%n)
	// with the bias the ratio would be 1.25; uniform draws stay within a few percent of 1 at this sample size
	if r := perLow / perHigh; r < 0.93 || r > 1.07 {
		t.Errorf("low indexes are drawn %.3f times as often as high ones, want about 1", r)
	}
}
