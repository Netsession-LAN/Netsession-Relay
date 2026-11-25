package web

import (
	"context"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
)

// Resource limits of the web part: the slots of the password checks, request sizes and times, live streams per
// session. The rate limits for the login are in security_throttle.go.

// ---- password checks, request sizes and times ----

// verifyPassword runs the Argon2id check (64 MiB each) with at most cap(s.verifySem) checks at a time; the
// others wait for a free slot until ctx ends. ok=false means no slot came free.
func (s *Server) verifyPassword(ctx context.Context, password, hash string) (ok bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, verifyWait)
	defer cancel()
	select {
	case s.verifySem <- struct{}{}:
	case <-ctx.Done():
		return false, nil
	}
	defer func() { <-s.verifySem }()
	return true, auth.Verify(password, hash)
}

// hashPassword hashes a new password (Argon2id, 64 MiB like a login check) in one of the slots of
// verifyPassword. ok=false means no slot came free.
func (s *Server) hashPassword(ctx context.Context, password string) (hash string, ok bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, verifyWait)
	defer cancel()
	select {
	case s.verifySem <- struct{}{}:
	case <-ctx.Done():
		return "", false, nil
	}
	defer func() { <-s.verifySem }()
	hash, err = auth.Hash(password)
	return hash, true, err
}

const (
	verifySlots = 3 // Argon2id checks at the same time (3 x 64 MiB)
	verifyWait  = 5 * time.Second
	// MaxBody is the largest request body the web part reads. Forms only; the one upload is the event logo with
	// MaxLogoBody (logo.go).
	MaxBody = 64 << 10
	// writeTimeout ends a response nobody reads (downloads included); the live stream sets its own per event.
	writeTimeout = 2 * time.Minute
	bodyTimeout  = 20 * time.Second
)

// ---- live streams per session ----

const (
	streamsPerSession = 8
	streamsPerUser    = 32
)

type streamCount struct {
	mu      sync.Mutex
	session map[string]int
	user    map[int64]int
}

// take reserves a live stream; false when the session or the account has too many open.
func (c *streamCount) take(session string, user int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		c.session, c.user = map[string]int{}, map[int64]int{}
	}
	if c.session[session] >= streamsPerSession || c.user[user] >= streamsPerUser {
		return false
	}
	c.session[session]++
	c.user[user]++
	return true
}

func (c *streamCount) release(session string, user int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session[session]--; c.session[session] <= 0 {
		delete(c.session, session)
	}
	if c.user[user]--; c.user[user] <= 0 {
		delete(c.user, user)
	}
}
