package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Second review, item 1: three authors that always fail at the front of the
// queue must not starve the rest. The reviewer's probe: authors 1 to 3 fail
// with the provider unavailable, 4 to 6 are healthy, several hourly ticks.
func TestDiscoveryTick_BrokenAuthorsAtTheHeadDoNotStarveTheQueue(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A", "OL3A", "OL4A", "OL5A", "OL6A")
	f.job.batchSize = func(int, time.Duration) int { return 3 }
	for _, id := range []string{"OL1A", "OL2A", "OL3A"} {
		f.disc.outcomes[id] = DiscoveryOutcome{Err: errors.New("HTTP 503"), Unavailable: true}
	}

	first := f.now
	for tick := 0; tick < 5; tick++ {
		f.job.tick(context.Background())
		f.now = f.now.Add(time.Hour)
	}

	calls := map[string]int{}
	for _, c := range f.disc.calls {
		calls[c]++
	}
	for _, id := range []string{"OL4A", "OL5A", "OL6A"} {
		if calls[id] == 0 {
			t.Errorf("healthy author %s was never checked in 5 ticks (calls %v)", id, f.disc.calls)
		}
	}
	// The broken authors are due again after discoveryRetryAfter, not a week.
	want := first.Add(discoveryRetryAfter - 168*time.Hour)
	for _, id := range []string{"OL1A", "OL2A", "OL3A"} {
		got := f.cursor(t, id)
		if got == nil || !got.Equal(want) {
			t.Errorf("%s cursor = %v, want %v (due again %s after the tripped pass)", id, got, want, discoveryRetryAfter)
		}
		if calls[id] != 1 {
			t.Errorf("%s checked %d times in 5 hours, want 1 (retry after %s)", id, calls[id], discoveryRetryAfter)
		}
	}

	// Once the retry offset passes, they are checked again.
	f.now = first.Add(discoveryRetryAfter + time.Minute)
	f.disc.calls = nil
	f.job.tick(context.Background())
	if len(f.disc.calls) == 0 || f.disc.calls[0] != "OL1A" {
		t.Errorf("after %s the broken authors should be due again, calls %v", discoveryRetryAfter, f.disc.calls)
	}
}

// An error about the author (a not found, a record that does not parse) is
// stamped as usual and does not count toward the breaker.
func TestDiscoveryTick_AuthorSpecificErrorsStampAndResetTheStreak(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A", "OL3A", "OL4A")
	f.job.batchSize = func(int, time.Duration) int { return 4 }
	f.disc.outcomes["OL1A"] = DiscoveryOutcome{Err: errors.New("HTTP 503"), Unavailable: true}
	f.disc.outcomes["OL2A"] = DiscoveryOutcome{Err: errors.New("HTTP 503"), Unavailable: true}
	f.disc.outcomes["OL3A"] = DiscoveryOutcome{Err: errors.New("not found")}
	f.disc.outcomes["OL4A"] = DiscoveryOutcome{Err: errors.New("HTTP 503"), Unavailable: true}

	res := f.job.tick(context.Background())
	if res.Breaker || res.Checked != 4 {
		t.Fatalf("tick = %+v, want 4 checked and no breaker (the not found reset the streak)", res)
	}
	for _, id := range []string{"OL1A", "OL2A", "OL3A", "OL4A"} {
		if got := f.cursor(t, id); got == nil || !got.Equal(f.now) {
			t.Errorf("%s cursor = %v, want stamped now", id, got)
		}
	}

	g := newDiscoveryJobFixture(t, "OL1A", "OL2A", "OL3A", "OL4A")
	g.job.batchSize = func(int, time.Duration) int { return 4 }
	for _, id := range []string{"OL1A", "OL2A", "OL3A", "OL4A"} {
		g.disc.outcomes[id] = DiscoveryOutcome{Err: errors.New("openlibrary: not found")}
	}
	if res := g.job.tick(context.Background()); res.Breaker || res.Checked != 4 {
		t.Fatalf("four not founds: tick = %+v, want all checked and no breaker", res)
	}
	for _, id := range []string{"OL1A", "OL2A", "OL3A", "OL4A"} {
		if got := g.cursor(t, id); got == nil || !got.Equal(g.now) {
			t.Errorf("%s cursor = %v, want a 404 style error stamped normally", id, got)
		}
	}
}
