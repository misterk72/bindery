package auth

import (
	"math"
	"sync"
	"time"
)

// requesterLimiter is a per user token bucket for the requester routes that
// spend metadata provider quota (security review item S4). Hardcover has a
// daily limit and Bindery had no general limiter, so without this one
// requester could drain the quota every other user depends on.
//
// Memory is bounded two ways: buckets idle longer than idle are dropped by a
// sweep that runs at most once per idle period, and the map never holds more
// than maxUsers entries. When it is full after a sweep, the least recently
// used bucket is evicted; that user's next call starts with a full bucket,
// which is a bounded gift, never a lockout.
type requesterLimiter struct {
	mu        sync.Mutex
	buckets   map[int64]*tokenBucket
	capacity  float64
	refill    float64 // tokens per second
	idle      time.Duration
	maxUsers  int
	lastSweep time.Time
	now       func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// Twenty searches in a burst, then one every three seconds. The Add dialog
// fires two searches per query (authors and books), so this is ten queries up
// front and a steady query every six seconds, well above anyone typing.
const (
	requesterSearchBurst    = 20
	requesterSearchRefill   = 1.0 / 3.0
	requesterLimiterIdle    = 10 * time.Minute
	requesterLimiterMaxUser = 1024
)

var defaultRequesterLimiter = newRequesterLimiter(requesterSearchBurst, requesterSearchRefill, requesterLimiterIdle, requesterLimiterMaxUser)

func newRequesterLimiter(capacity int, refillPerSecond float64, idle time.Duration, maxUsers int) *requesterLimiter {
	return &requesterLimiter{
		buckets:  make(map[int64]*tokenBucket),
		capacity: float64(capacity),
		refill:   refillPerSecond,
		idle:     idle,
		maxUsers: maxUsers,
		now:      time.Now,
	}
}

// allow spends one token for userID. When none is left it returns false and
// the whole seconds until the next token.
func (l *requesterLimiter) allow(userID int64) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)

	b, ok := l.buckets[userID]
	if !ok {
		if len(l.buckets) >= l.maxUsers {
			l.evictOldest()
		}
		b = &tokenBucket{tokens: l.capacity, last: now}
		l.buckets[userID] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.capacity, b.tokens+elapsed*l.refill)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := int(math.Ceil((1 - b.tokens) / l.refill))
	if wait < 1 {
		wait = 1
	}
	return false, wait
}

func (l *requesterLimiter) sweep(now time.Time) {
	if l.lastSweep.IsZero() {
		l.lastSweep = now
	}
	if now.Sub(l.lastSweep) < l.idle {
		return
	}
	l.lastSweep = now
	for id, b := range l.buckets {
		if now.Sub(b.last) >= l.idle {
			delete(l.buckets, id)
		}
	}
}

func (l *requesterLimiter) evictOldest() {
	var oldestID int64
	var oldest time.Time
	first := true
	for id, b := range l.buckets {
		if first || b.last.Before(oldest) {
			oldestID, oldest, first = id, b.last, false
		}
	}
	if !first {
		delete(l.buckets, oldestID)
	}
}

func (l *requesterLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
