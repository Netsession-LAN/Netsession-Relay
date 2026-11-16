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

// Parameters (OWASP 2024 recommendation for Argon2id: m=64 MiB, t=3, p=2 is well above the minimum).
const (
	memory  = 64 * 1024
	time    = 3
	threads = 2
	keyLen  = 32
	saltLen = 16
)

var b64 = base64.RawStdEncoding

// Hash returns "$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>".
func Hash(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, time, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, time, threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// ErrMismatch is returned for a wrong password.
var ErrMismatch = errors.New("auth: password mismatch")

// Verify checks a password against an encoded hash in constant time.
func Verify(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("auth: unsupported hash format")
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return errors.New("auth: unsupported argon2 version")
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return errors.New("auth: bad parameters")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return errors.New("auth: bad salt")
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return errors.New("auth: bad hash")
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
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

// Password returns a random password that is easy to type (no ambiguous characters), in groups of 4.
func Password() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return sb.String()
}
