// Package auth hashes and verifies passwords (Argon2id, PHC string format) and creates random tokens.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the cost parameters of Argon2id. They are stored in every hash, so hashes made with older
// parameters keep working after a change of the defaults.
type Params struct {
	Memory  uint32 // KiB
	Passes  uint32 // number of passes over the memory ("t")
	Threads uint8
}

// DefaultParams is what Hash uses: 64 MiB, 3 passes, 2 threads (well above the OWASP minimum for Argon2id).
var DefaultParams = Params{Memory: 64 * 1024, Passes: 3, Threads: 2}

// Bounds for the parameters of a stored hash. Verify computes whatever a hash asks for, so a damaged or planted
// hash must not be able to ask for gigabytes of memory or hours of work, and one that asks for next to nothing
// is not a password hash worth accepting.
const (
	MinMemory  = 8 * 1024   // KiB
	MaxMemory  = 256 * 1024 // KiB
	maxPasses  = 16
	maxThreads = 16
	minKeyLen  = 16
	maxKeyLen  = 64
	minSaltLen = 8
	maxSaltLen = 64

	keyLen  = 32
	saltLen = 16
)

func (p Params) valid() bool {
	return p.Memory >= MinMemory && p.Memory <= MaxMemory && p.Passes >= 1 && p.Passes <= maxPasses &&
		p.Threads >= 1 && p.Threads <= maxThreads
}

var b64 = base64.RawStdEncoding

// Hash returns "$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>".
func Hash(password string) (string, error) { return HashWith(password, DefaultParams) }

// HashWith is Hash with other cost parameters (tests use cheap ones; they must be inside the bounds Verify
// accepts).
func HashWith(password string, p Params) (string, error) {
	if !p.valid() {
		return "", ErrParams
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Passes, p.Memory, p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Passes, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Errors of Verify.
var (
	// ErrMismatch is returned for a wrong password.
	ErrMismatch = errors.New("auth: password mismatch")
	// ErrFormat is returned for a string that is not an Argon2id hash in the PHC format.
	ErrFormat = errors.New("auth: unsupported hash format")
	// ErrParams is returned for a hash whose cost parameters, salt or key length are outside the bounds.
	ErrParams = errors.New("auth: hash parameters out of bounds")
)

// Verify checks a password against an encoded hash in constant time. The parameters of the hash are checked
// against fixed bounds before anything is computed.
func Verify(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return ErrFormat
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return fmt.Errorf("%w: argon2 version", ErrFormat)
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Passes, &p.Threads); err != nil {
		return fmt.Errorf("%w: parameters", ErrFormat)
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("%w: salt", ErrFormat)
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("%w: key", ErrFormat)
	}
	if !p.valid() || len(salt) < minSaltLen || len(salt) > maxSaltLen || len(want) < minKeyLen || len(want) > maxKeyLen {
		return ErrParams
	}
	got := argon2.IDKey([]byte(password), salt, p.Passes, p.Memory, p.Threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// Token returns n random bytes, URL-safe base64 encoded.
func Token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// passwordAlphabet has no characters that are easy to mix up (0/O, 1/l/I).
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

// Password returns a random password that is easy to type (no ambiguous characters), in groups of 4.
func Password() string {
	var sb strings.Builder
	for i := range 16 {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(passwordAlphabet[randIndex(len(passwordAlphabet))])
	}
	return sb.String()
}

// randIndex returns a uniformly distributed index below n (n <= 256). A random byte modulo n would prefer the
// low indexes whenever 256 is not a multiple of n, so bytes at or above the largest multiple are thrown away
// and drawn again.
func randIndex(n int) int {
	limit := 256 - 256%n
	var b [1]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			panic(err) // crypto/rand never fails on supported platforms
		}
		if int(b[0]) < limit {
			return int(b[0]) % n
		}
	}
}
