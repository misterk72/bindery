package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/indexer/newznab"
	"github.com/vavallee/bindery/internal/models"
)

func TestParseRetryHint(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want time.Duration
		ok   bool
	}{
		// The description that prompted #1934, verbatim from NZB.life.
		{"minutes, real-world", "indexer error 500: Request limit reached. Retry in 485 minutes.", 485 * time.Minute, true},
		{"singular unit", "Request limit reached. Retry in 1 hour", time.Hour, true},
		{"seconds", "Too many requests, retry in 30 seconds", 30 * time.Second, true},
		{"days", "Grab limit reached. Retry in 1 day.", 24 * time.Hour, true},
		{"case insensitive", "RETRY IN 5 MINUTES", 5 * time.Minute, true},
		{"space-free unit", "retry in 12minutes", 12 * time.Minute, true},
		// No hint at all is the common case: the caller falls back to the
		// default rather than treating it as "retry immediately".
		{"no hint", "Request limit reached", 0, false},
		{"unit we don't understand", "Retry in 3 fortnights", 0, false},
		{"number missing", "Retry in a while", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryHint(tc.desc)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCooldownClampsHint pins the two ends of the clamp: an indexer cannot
// bench itself for a month, and a zero-length hint cannot produce a cooldown
// that has already expired by the time it is stored.
func TestCooldownClampsHint(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		desc string
		want time.Duration
	}{
		{"absurdly long hint is capped", "Request limit reached. Retry in 30 days.", maxRateLimitCooldown},
		{"zero hint gets the floor", "Request limit reached. Retry in 0 minutes.", minRateLimitCooldown},
		{"no hint gets the default", "Request limit reached.", defaultRateLimitCooldown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &indexerCooldowns{now: func() time.Time { return now }}
			if !c.note(models.Indexer{ID: 1, Name: "idx"}, &newznab.IndexerError{Code: 500, Description: tc.desc}) {
				t.Fatal("note did not record a cooldown for a 500")
			}
			c.mu.Lock()
			got := c.entries[1].until.Sub(now)
			c.mu.Unlock()
			if got != tc.want {
				t.Errorf("cooldown = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCooldownExpires holds the deadline to the indexer's OWN hint: still held
// one minute before it, released one minute after. The hint (485 minutes) is
// deliberately far from defaultRateLimitCooldown, so a build that ignores the
// hint and always applies the default fails this rather than coincidentally
// passing it.
func TestCooldownExpires(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	c := &indexerCooldowns{now: func() time.Time { return now }}
	idx := models.Indexer{ID: 7, Name: "NZB.life"}
	c.note(idx, &newznab.IndexerError{Code: 500, Description: "Request limit reached. Retry in 485 minutes."})

	if _, held := c.active(idx); !held {
		t.Fatal("indexer is not in cooldown immediately after a 500")
	}
	now = now.Add(484 * time.Minute)
	reason, held := c.active(idx)
	if !held {
		t.Error("cooldown released a minute before the indexer said to come back")
	}
	if !strings.Contains(reason, "rate limited") {
		t.Errorf("reason = %q, want it to name the rate limit", reason)
	}
	now = now.Add(2 * time.Minute)
	if _, held := c.active(idx); held {
		t.Error("cooldown outlived the deadline the indexer gave")
	}
}

// TestCooldownClearedByIndexerEdit covers the recovery path: changing the
// indexer row (new API key, different account, re-enable) means the user wants
// it retried now, and must not be made to wait out a lockout that belonged to
// the old configuration.
func TestCooldownClearedByIndexerEdit(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	c := &indexerCooldowns{now: func() time.Time { return now }}
	idx := models.Indexer{ID: 7, Name: "NZB.life", UpdatedAt: now.Add(-time.Hour)}
	c.note(idx, &newznab.IndexerError{Code: 500, Description: "Request limit reached. Retry in 485 minutes."})

	if _, held := c.active(idx); !held {
		t.Fatal("indexer is not in cooldown after a 500")
	}
	edited := idx
	edited.UpdatedAt = now.Add(time.Minute)
	if _, held := c.active(edited); held {
		t.Error("an edited indexer is still held; the user's change should retry immediately")
	}
	// The entry is gone, not merely bypassed, so the un-edited copy is clear too.
	if _, held := c.active(idx); held {
		t.Error("the cooldown entry survived the edit")
	}
}

// TestCooldownIgnoresNonRateLimitErrors is the deliberate scope line of #1934:
// a suspended account (101) or a network failure must not bench the indexer on
// a timer. Auth failures need a human, and one who fixes their API key must see
// it work immediately (#1935).
func TestCooldownIgnoresNonRateLimitErrors(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	idx := models.Indexer{ID: 3, Name: "NZB Finder"}
	for _, err := range []error{
		&newznab.IndexerError{Code: 101, Description: "Account suspended"},
		&newznab.IndexerError{Code: 100, Description: "Incorrect user credentials"},
		context.DeadlineExceeded,
	} {
		c := &indexerCooldowns{now: func() time.Time { return now }}
		if c.note(idx, err) {
			t.Errorf("%v recorded a cooldown", err)
		}
		if _, held := c.active(idx); held {
			t.Errorf("%v put the indexer in cooldown", err)
		}
	}
}

// TestCooldownIgnoresUnidentifiedIndexers guards the map key: an indexer with
// no id has nothing stable to key on, and tracking them all under 0 would let
// one rate-limited indexer bench every other unsaved one.
func TestCooldownIgnoresUnidentifiedIndexers(t *testing.T) {
	c := &indexerCooldowns{}
	idx := models.Indexer{Name: "unsaved"}
	if c.note(idx, &newznab.IndexerError{Code: 500, Description: "Request limit reached."}) {
		t.Error("recorded a cooldown against indexer id 0")
	}
	if _, held := c.active(idx); held {
		t.Error("an id-less indexer is in cooldown")
	}
}

// rateLimitedIndexer serves the Newznab 500 that NZB.life sends and counts how
// many times it was asked.
func rateLimitedIndexer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<error code="500" description="Request limit reached. Retry in 485 minutes."/>`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestSearchBookStopsQueryingARateLimitedIndexer is the behavioural pin for
// #1934: the second search must not reach an indexer that already said to come
// back in 485 minutes.
func TestSearchBookStopsQueryingARateLimitedIndexer(t *testing.T) {
	srv, hits := rateLimitedIndexer(t)
	s := newTestSearcher()
	idxs := []models.Indexer{{ID: 1, Name: "NZB.life", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	crit := MatchCriteria{Title: "Redshirts", Author: "John Scalzi", MediaType: models.MediaTypeEbook}

	s.SearchBook(context.Background(), idxs, crit)
	first := hits.Load()
	if first == 0 {
		t.Fatal("the first search never reached the indexer")
	}

	s.SearchBook(context.Background(), idxs, crit)
	if got := hits.Load(); got != first {
		t.Errorf("the indexer was queried %d more time(s) while in cooldown", got-first)
	}
}

// TestSearchBookWithDebugReportsCooldown pins the visibility half: the panel
// that showed the original error must also show why the indexer is no longer
// being queried, rather than silently dropping it from the list.
func TestSearchBookWithDebugReportsCooldown(t *testing.T) {
	srv, hits := rateLimitedIndexer(t)
	s := newTestSearcher()
	idxs := []models.Indexer{{ID: 1, Name: "NZB.life", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	crit := MatchCriteria{Title: "Redshirts", Author: "John Scalzi", MediaType: models.MediaTypeEbook}

	_, dbg := s.SearchBookWithDebug(context.Background(), idxs, crit)
	if len(dbg.Indexers) != 1 || dbg.Indexers[0].Error == "" {
		t.Fatalf("first search did not record the indexer error: %+v", dbg.Indexers)
	}
	first := hits.Load()

	_, dbg = s.SearchBookWithDebug(context.Background(), idxs, crit)
	if got := hits.Load(); got != first {
		t.Errorf("the indexer was queried %d more time(s) while in cooldown", got-first)
	}
	if len(dbg.Indexers) != 1 {
		t.Fatalf("the held indexer vanished from the debug output: %+v", dbg.Indexers)
	}
	entry := dbg.Indexers[0]
	if !entry.Skipped {
		t.Error("the held indexer is not marked skipped")
	}
	if !strings.Contains(entry.SkipReason, "rate limited") {
		t.Errorf("skipReason = %q, want it to explain the rate limit", entry.SkipReason)
	}
}

// TestCooldownIsSharedAcrossSearchPaths matters because one *Searcher is shared
// by the scheduler's auto-grab and the API's interactive search
// (cmd/bindery/main.go). A limit hit by one must be respected by the other, or
// the 12-hour wanted scan keeps hammering an indexer the user can see is held.
func TestCooldownIsSharedAcrossSearchPaths(t *testing.T) {
	srv, hits := rateLimitedIndexer(t)
	s := newTestSearcher()
	idxs := []models.Indexer{{ID: 1, Name: "NZB.life", URL: srv.URL, Enabled: true, Categories: []int{7020}}}

	s.SearchBookWithDebug(context.Background(), idxs, MatchCriteria{Title: "Redshirts", Author: "John Scalzi", MediaType: models.MediaTypeEbook})
	first := hits.Load()
	if first == 0 {
		t.Fatal("the interactive search never reached the indexer")
	}

	s.SearchBook(context.Background(), idxs, MatchCriteria{Title: "Lock In", Author: "John Scalzi", MediaType: models.MediaTypeEbook})
	s.SearchQuery(context.Background(), idxs, "scalzi")
	if got := hits.Load(); got != first {
		t.Errorf("auto-grab and freeform search sent %d more request(s) to a held indexer", got-first)
	}
}

// cloudflareBlockedIndexer serves the HTTP 429 that Cloudflare answers with
// when a tracker's rate limit trips (error code 1015): a text body, no
// Newznab <error> element. It counts how many times it was asked.
func cloudflareBlockedIndexer(t *testing.T, retryAfter string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("error code: 1015"))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestSearchBookStopsQueryingAnHTTP429Indexer is #2635's pin: a rate limit
// applied by the host in front of the indexer must bench it the same way a
// Newznab <error code="500"> does. Before the fix the 429 was an untyped
// error, no cooldown was recorded, and every search in a sweep of thousands
// of books sent the indexer another request it had already refused.
func TestSearchBookStopsQueryingAnHTTP429Indexer(t *testing.T) {
	srv, hits := cloudflareBlockedIndexer(t, "")
	s := newTestSearcher()
	idxs := []models.Indexer{{ID: 1, Name: "NZB.life", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	crit := MatchCriteria{Title: "Redshirts", Author: "John Scalzi", MediaType: models.MediaTypeEbook}

	s.SearchBook(context.Background(), idxs, crit)
	if got := hits.Load(); got != 1 {
		t.Fatalf("the first search sent %d requests, want 1: a 429 must also stop tier fall-through", got)
	}
	for range 5 {
		s.SearchBook(context.Background(), idxs, crit)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("the indexer was queried %d more time(s) while in cooldown", got-1)
	}
	reason, held := s.cooldowns.active(idxs[0])
	if !held {
		t.Fatal("no cooldown recorded for the 429")
	}
	if !strings.Contains(reason, "HTTP 429") {
		t.Errorf("cooldown reason = %q, want it to carry the HTTP status", reason)
	}
}

// TestCooldownHonoursRetryAfter: a Retry-After header is the server's own
// deadline and outranks the default hour, within the usual clamp.
func TestCooldownHonoursRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	idx := models.Indexer{ID: 7, Name: "abNZB"}
	cases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"seconds", &newznab.HTTPStatusError{Status: 429, RetryAfter: 20 * time.Minute}, 20 * time.Minute},
		{"absent falls back to the default", &newznab.HTTPStatusError{Status: 429}, defaultRateLimitCooldown},
		{"clamped to the maximum", &newznab.HTTPStatusError{Status: 429, RetryAfter: 72 * time.Hour}, maxRateLimitCooldown},
		{"503 with Retry-After", &newznab.HTTPStatusError{Status: 503, RetryAfter: 5 * time.Minute}, 5 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &indexerCooldowns{now: func() time.Time { return now }}
			if !c.note(idx, tc.err) {
				t.Fatalf("%v did not record a cooldown", tc.err)
			}
			if got := c.entries[idx.ID].until.Sub(now); got != tc.want {
				t.Errorf("cooldown = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestCooldownIgnoresAPlain503: a 503 with no Retry-After is maintenance or
// a proxy hiccup, not an instruction to stay away, so the next search may
// try again.
func TestCooldownIgnoresAPlain503(t *testing.T) {
	c := &indexerCooldowns{}
	idx := models.Indexer{ID: 3, Name: "NZB Finder"}
	if c.note(idx, &newznab.HTTPStatusError{Status: 503, Snippet: "maintenance"}) {
		t.Error("a plain 503 recorded a cooldown")
	}
}

// TestCooldownEscalatesOnRepeatedRateLimits: an indexer that keeps refusing
// is left alone for longer each time, up the same ladder Sonarr and Radarr
// climb, and stays at the top rather than wrapping.
func TestCooldownEscalatesOnRepeatedRateLimits(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := &indexerCooldowns{now: func() time.Time { return now }}
	idx := models.Indexer{ID: 1, Name: "NZB.life"}
	err := &newznab.HTTPStatusError{Status: 429, Snippet: "error code: 1015"}

	want := []time.Duration{time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, d := range want {
		if !c.note(idx, err) {
			t.Fatalf("limit %d: no cooldown recorded", i+1)
		}
		if got := c.entries[idx.ID].until.Sub(now); got != d {
			t.Errorf("limit %d: cooldown = %s, want %s", i+1, got, d)
		}
		// Wait it out, so the next limit is the first request after it.
		now = c.entries[idx.ID].until.Add(time.Second)
		if _, held := c.active(idx); held {
			t.Fatalf("limit %d: still held after the cooldown expired", i+1)
		}
	}
}

// TestCooldownRelaxesOnSuccess: a search the indexer answers steps the ladder
// down one rung, so a limit that clears and comes back a week later costs an
// hour again rather than a day.
func TestCooldownRelaxesOnSuccess(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := &indexerCooldowns{now: func() time.Time { return now }}
	idx := models.Indexer{ID: 1, Name: "NZB.life"}
	err := &newznab.IndexerError{Code: 500, Description: "Request limit reached."}

	c.note(idx, err) // 1h, level now 1
	c.note(idx, err) // 3h, level now 2
	c.relax(idx)     // level 1
	c.note(idx, err)
	if got := c.entries[idx.ID].until.Sub(now); got != 3*time.Hour {
		t.Errorf("after one success the next limit = %s, want 3h", got)
	}
	c.relax(idx)
	c.relax(idx)
	c.relax(idx) // cannot go below the first rung
	c.note(idx, err)
	if got := c.entries[idx.ID].until.Sub(now); got != time.Hour {
		t.Errorf("after several successes the next limit = %s, want 1h", got)
	}
}

// TestCooldownHintOutranksLadder: the indexer's own deadline is what it
// asked for, whichever rung the ladder is on.
func TestCooldownHintOutranksLadder(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := &indexerCooldowns{now: func() time.Time { return now }}
	idx := models.Indexer{ID: 1, Name: "NZB.life"}
	for range 3 {
		c.note(idx, &newznab.HTTPStatusError{Status: 429})
	}
	c.note(idx, &newznab.HTTPStatusError{Status: 429, RetryAfter: 20 * time.Minute})
	if got := c.entries[idx.ID].until.Sub(now); got != 20*time.Minute {
		t.Errorf("Retry-After on rung 4 gave %s, want 20m", got)
	}
	c.note(idx, &newznab.IndexerError{Code: 500, Description: "Request limit reached. Retry in 485 minutes."})
	if got := c.entries[idx.ID].until.Sub(now); got != 485*time.Minute {
		t.Errorf("text hint on rung 5 gave %s, want 485m", got)
	}
}

// TestCooldownEditResetsLadder: editing the indexer forgets the rung along
// with the entry; a new key or account starts over at an hour.
func TestCooldownEditResetsLadder(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := &indexerCooldowns{now: func() time.Time { return now }}
	idx := models.Indexer{ID: 1, Name: "NZB.life"}
	err := &newznab.HTTPStatusError{Status: 429}
	c.note(idx, err)
	c.note(idx, err)
	idx.UpdatedAt = now.Add(time.Minute)
	if _, held := c.active(idx); held {
		t.Fatal("still held after the indexer was edited")
	}
	c.note(idx, err)
	if got := c.entries[idx.ID].until.Sub(now); got != time.Hour {
		t.Errorf("first limit after an edit = %s, want 1h", got)
	}
}

// TestSearcherReportsCooldownForTheIndexersTab: the exported view the API
// fills into the indexer list.
func TestSearcherReportsCooldownForTheIndexersTab(t *testing.T) {
	srv, _ := cloudflareBlockedIndexer(t, "")
	s := newTestSearcher()
	idxs := []models.Indexer{{ID: 1, Name: "NZB.life", URL: srv.URL, Enabled: true, Categories: []int{7020}}}
	if _, _, held := s.Cooldown(idxs[0]); held {
		t.Fatal("held before any search")
	}
	s.SearchBook(context.Background(), idxs, MatchCriteria{Title: "Redshirts", Author: "John Scalzi", MediaType: models.MediaTypeEbook})
	until, reason, held := s.Cooldown(idxs[0])
	if !held {
		t.Fatal("not held after a 429")
	}
	if d := time.Until(until); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("until is %s away, want about an hour", d)
	}
	if !strings.Contains(reason, "HTTP 429") {
		t.Errorf("reason = %q, want the indexer's message", reason)
	}
}
