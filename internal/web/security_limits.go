package web

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/auth"
)

// Resource limits of the web part: login rate limits, the slots of the password checks, request sizes and
// times, live streams per session.

// ---- rate limits for the login ----

// ipLimiter is a token bucket per key (client address) with a bounded number of keys.
type ipLimiter struct {
	mu    sync.Mutex
	m     map[string]*ipBucket
	burst float64
	every time.Duration // one token per every
	max   int
}

type ipBucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(burst int, every time.Duration) *ipLimiter {
	return &ipLimiter{m: map[string]*ipBucket{}, burst: float64(burst), every: every, max: 4096}
}

func (l *ipLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.m[key]
	if b == nil {
		if len(l.m) >= l.max {
			l.evict(now)
		}
		b = &ipBucket{tokens: l.burst, last: now}
		l.m[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.every))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evict drops full (idle) buckets; when all are in use it drops arbitrary ones, so memory stays bounded. A
// dropped bucket starts full again, which only helps a client that is one of thousands at that moment.
func (l *ipLimiter) evict(now time.Time) {
	for k, b := range l.m {
		if b.tokens+float64(now.Sub(b.last))/float64(l.every) >= l.burst {
			delete(l.m, k)
		}
	}
	for k := range l.m {
		if len(l.m) < l.max*3/4 {
			break
		}
		delete(l.m, k)
	}
}

// loginThrottle slows down password guessing per (client address, account name) with a growing, bounded pause.
// The account itself is never locked: somebody else guessing does not keep the owner out, and the answer is the
// same for names that exist and names that do not.
type loginThrottle struct {
	mu sync.Mutex
	m  map[string]*throttleEntry
}

type throttleEntry struct {
	fails int
	until time.Time // no attempt before this
	last  time.Time
}

const (
	throttleFree   = 3                // attempts in a row that are checked without a wait
	throttleMax    = 5 * time.Minute  // longest pause
	throttleForget = 30 * time.Minute // a quiet key starts over
	throttleKeys   = 8192
)

func throttleKey(ip, name string) string {
	name = strings.ToLower(name)
	if len(name) > 64 {
		name = name[:64]
	}
	return limitKey(ip) + "|" + name
}

// limitKey is a client address the way the login limits count it: an IPv4 address as it is, an IPv6 address as
// its /64 network. A host with IPv6 usually has a whole /64 to itself and could otherwise take a new address for
// every attempt. Anything that is not an address stays as it is.
func limitKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

// begin asks for one attempt. A key that is pausing gets the rest of its pause back and no attempt. Otherwise
// the attempt is counted as failed right away, under the same lock, and 0 is returned: the caller checks the
// password and calls reset when it was right (or cancel when no password was checked). Counting first and
// forgiving later is what keeps attempts that arrive together from all being checked before the first failure
// is recorded.
func (t *loginThrottle) begin(key string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entry(key, now)
	if d := e.until.Sub(now); d > 0 {
		return d
	}
	e.fails++
	e.last = now
	e.until = now.Add(throttlePause(e.fails))
	return 0
}

// entry gives the entry of key. A key that was quiet for throttleForget starts anew; a new entry makes room
// first when the table is full. t.mu is held.
func (t *loginThrottle) entry(key string, now time.Time) *throttleEntry {
	if t.m == nil {
		t.m = map[string]*throttleEntry{}
	}
	if e := t.m[key]; e != nil && now.Sub(e.last) <= throttleForget {
		return e
	}
	delete(t.m, key)
	if len(t.m) >= throttleKeys {
		t.evict(now)
	}
	e := &throttleEntry{}
	t.m[key] = e
	return e
}

// throttlePause is the pause after the given number of failures in a row. throttleFree attempts are free: the
// pause starts with the last of them, so the fourth attempt waits 2 s, the fifth 4 s, then 8 s … up to throttleMax.
func throttlePause(fails int) time.Duration {
	n := fails - throttleFree + 1
	switch {
	case n <= 0:
		return 0
	case n >= 10:
		return throttleMax
	}
	return min(throttleMax, time.Second<<n)
}

// evict keeps the memory bounded: expired keys go first, then arbitrary ones. The caller holds the lock.
func (t *loginThrottle) evict(now time.Time) {
	for k, x := range t.m {
		if now.Sub(x.last) > throttleForget {
			delete(t.m, k)
		}
	}
	for k := range t.m {
		if len(t.m) < throttleKeys*3/4 {
			break
		}
		delete(t.m, k)
	}
}

// cancel takes back an attempt that begin counted although no password was checked in the end (all password
// checks were busy).
func (t *loginThrottle) cancel(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[key]
	if e == nil {
		return
	}
	if e.fails--; e.fails <= 0 {
		delete(t.m, key)
		return
	}
	e.until = e.last.Add(throttlePause(e.fails))
}

func (t *loginThrottle) reset(key string) {
	t.mu.Lock()
	delete(t.m, key)
	t.mu.Unlock()
}

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
